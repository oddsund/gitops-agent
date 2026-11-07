package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const restartTimeout = time.Minute

// staleTempAge is far beyond the HTTP and attestation timeouts, so a temp
// file younger than this may belong to a concurrent run and is kept.
const staleTempAge = time.Hour

// Update installs the latest release over cfg.BinPath. The restart targets
// the agent's own unit, not the unit running this process, so it cannot kill
// the updater. Concurrent runs are not locked: two runs end in two atomic
// renames of verified files.
func Update(ctx context.Context, cfg Config) error {
	cfg = cfg.populateWithDefaultValues()

	if cfg.EUID() != 0 {
		return errors.New("update must run as root")
	}
	if !filepath.IsAbs(cfg.BinPath) {
		return errors.New("BinPath must be absolute")
	}
	if !filepath.IsAbs(cfg.VersionFilePath) {
		return errors.New("VersionFilePath must be absolute")
	}
	if err := validateServiceName(cfg.ServiceName); err != nil {
		return err
	}
	if err := validateRepo(cfg.Repo); err != nil {
		return err
	}
	// Checked up front so a late failure cannot follow the binary swap.
	versionDir := filepath.Dir(cfg.VersionFilePath)
	if fi, err := os.Stat(versionDir); err != nil {
		return fmt.Errorf("version file directory %s: %w", versionDir, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("version file directory %s: not a directory", versionDir)
	}

	removeStaleTempFiles(filepath.Dir(cfg.BinPath), cfg.Logf)

	tag, err := latestReleaseTag(ctx, cfg)
	if err != nil {
		return err
	}
	// An unparsable latest tag must fail here; only the installed version
	// may be unparsable, and that means "update".
	if _, err := parseVersion(tag); err != nil {
		return err
	}

	installed, err := readInstalledVersion(cfg.VersionFilePath)
	if err != nil {
		return err
	}

	cmpResult, cmpErr := compareVersions(installed, tag)
	switch {
	case cmpErr != nil:
		if installed == "" {
			cfg.Logf("no installed version recorded, installing %s", tag)
		} else {
			cfg.Logf("installed version %q is not a release version, updating to %s", installed, tag)
		}
	case cmpResult == 0 && !cfg.Force:
		cfg.Logf("already on %s, nothing to do", tag)
		return nil
	case cmpResult > 0 && !cfg.Force:
		cfg.Logf("WARNING: installed version %s is newer than latest release %s; not downgrading (use --force to install %s anyway)", installed, tag, tag)
		return nil
	case cmpResult < 0:
		cfg.Logf("installed: %s, latest: %s, updating", installed, tag)
	default:
		cfg.Logf("--force: installing %s over %s", tag, installed)
	}

	asset, err := assetNameFor(cfg.AssetName, runtime.GOARCH)
	if err != nil {
		return err
	}
	cfg.Logf("downloading and verifying %s from %s", asset, tag)
	// Same directory as the target, so the rename below is atomic.
	tmp, err := fetchVerified(ctx, cfg, tag, filepath.Dir(cfg.BinPath))
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	if err := verifyAttestation(ctx, cfg, tmp); err != nil {
		return err
	}

	if err := os.Rename(tmp, cfg.BinPath); err != nil {
		return fmt.Errorf("installing %s: %w", cfg.BinPath, err)
	}
	syncDir(filepath.Dir(cfg.BinPath))

	if err := writeFileAtomic(cfg.VersionFilePath, []byte(tag+"\n"), 0o644); err != nil {
		return fmt.Errorf("binary %s was replaced but the version file could not be written (next run will repeat the update): %w", cfg.BinPath, err)
	}

	cfg.Logf("restarting %s", cfg.ServiceName)
	// Detached from ctx: a signal after the swap must not leave the old code
	// running, because later runs would report "already on" and never retry.
	restartCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartTimeout)
	defer cancel()
	if out, err := cfg.Runner.Run(restartCtx, nil, "systemctl", "restart", cfg.ServiceName); err != nil {
		msg := fmt.Errorf("restarting %s failed; new binary %s is installed and %s records %s, run \"systemctl restart %s\" manually (rerunning update will not retry it): %w: %s",
			cfg.ServiceName, cfg.BinPath, cfg.VersionFilePath, tag, cfg.ServiceName, err, truncated(out, 1024))
		cfg.Logf("ERROR: %v", msg)
		return msg
	}
	cfg.Logf("updated to %s", tag)
	return nil
}

// removeStaleTempFiles sweeps downloads left by a killed run. Only regular
// files with the exact prefix are touched; Type() does not follow symlinks.
func removeStaleTempFiles(dir string, logf func(string, ...any)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < staleTempAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			logf("removed stale temp file %s", e.Name())
		}
	}
}

func readInstalledVersion(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading version file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// syncDir is best effort: a lost directory entry only repeats the update.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	d.Sync()
	d.Close()
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".installed-version-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	ok = true
	return nil
}
