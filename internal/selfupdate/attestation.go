package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const attestationTimeout = 2 * time.Minute

// releaseWorkflowPath must match the workflow file that attests the release
// binary. Renaming .github/workflows/release.yml breaks verification by
// design; update this, scripts/install.sh and the docs together.
const releaseWorkflowPath = ".github/workflows/release.yml"

// minGHVersion is the first gh release with --signer-workflow,
// --source-ref and --deny-self-hosted-runners (--source-ref came last, in
// 2.68.0). An older gh fails closed; there is no fallback to a weaker check.
const minGHVersion = "2.68.0"

// verifyAttestation requires that releaseWorkflowPath in cfg.Repo built the
// file at path, from refs/tags/<tag>, on a GitHub-hosted runner.
func verifyAttestation(ctx context.Context, cfg Config, path, tag string) error {
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
	// The skip paths above stay unconditional; the tag only matters once
	// it is about to reach argv.
	if err := validateTag(tag); err != nil {
		return err
	}

	// The token travels in the environment only, never in argv.
	var env []string
	if cfg.Token != "" {
		env = []string{"GH_TOKEN=" + cfg.Token}
	}
	// gh makes its own network calls and has no deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, attestationTimeout)
	defer cancel()
	// release.yml runs only on tag push, so the attested ref is always the
	// tag. Pinning it stops an attestation from one release being used for
	// another tag's asset.
	args := []string{
		"attestation", "verify", path,
		"-R", cfg.Repo,
		"--signer-workflow", cfg.Repo + "/" + releaseWorkflowPath,
		"--source-ref", "refs/tags/" + tag,
		"--deny-self-hosted-runners",
	}
	out, err := cfg.Runner.Run(ctx, env, gh, args...)
	if err != nil {
		if strings.Contains(string(out), "unknown flag") {
			return fmt.Errorf("build provenance verification failed for %s: %w: %s (this gh is too old to check the signer workflow; upgrade to gh >= %s or set GITOPS_AGENT_SKIP_ATTESTATION=1)", cfg.Repo, err, truncated(out, 1024), minGHVersion)
		}
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
