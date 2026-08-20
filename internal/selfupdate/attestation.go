package selfupdate

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

// verifyAttestation verifies path's build provenance against repo with
// `gh attestation verify`, mirroring
// scripts/lib/github-release.bash's github_release_verify_attestation.
// This repo's whole pitch is a single static binary with few dependencies,
// so a missing gh only skips the check (logged), rather than failing the
// update -- the checksum check already ran either way. token, if
// non-empty, is passed as GH_TOKEN (needed for a private repo, and avoids
// the stricter unauthenticated API rate limit).
func verifyAttestation(repo, token, path string) error {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		log.Printf("gh not found -- skipping build provenance verification (checksum only)")
		return nil
	}

	cmd := exec.Command(ghPath, "attestation", "verify", path, "-R", repo)
	if token != "" {
		cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
	}

	log.Printf("verifying build provenance for %s...", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build provenance verification failed for %s -- leaving the currently installed binary untouched: %w\n%s", path, err, out)
	}
	log.Printf("build provenance verified for %s", path)
	return nil
}
