package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

const attestationTimeout = 2 * time.Minute

func verifyAttestation(ctx context.Context, cfg Config, path string) error {
	cfg = cfg.populateWithDefaultValues()

	if cfg.SkipAttestation {
		cfg.Logf("skipping build provenance verification (GITOPS_AGENT_SKIP_ATTESTATION=1), checksum only")
		return nil
	}

	gh, err := cfg.LookPath("gh")
	if err != nil {
		cfg.Logf("gh not found on PATH, skipping build provenance verification (checksum only)")
		return nil
	}

	if err := validateRepo(cfg.Repo); err != nil {
		return err
	}
	// An absolute path can never be parsed as an option by gh.
	if !filepath.IsAbs(path) {
		return errors.New("attestation path must be absolute")
	}

	// The token travels in the environment only, never in argv.
	var env []string
	if cfg.Token != "" {
		env = []string{"GH_TOKEN=" + cfg.Token}
	}
	// gh makes its own network calls and has no deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, attestationTimeout)
	defer cancel()
	out, err := cfg.Runner.Run(ctx, env, gh, "attestation", "verify", path, "-R", cfg.Repo)
	if err != nil {
		return fmt.Errorf("build provenance verification failed for %s: %w: %s", cfg.Repo, err, truncated(out, 1024))
	}
	cfg.Logf("build provenance verified")
	return nil
}

func truncated(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
