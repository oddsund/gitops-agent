// Package deploy applies a service's compose.yml via the docker CLI.
package deploy

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
)

// Deploy runs `docker compose -f <serviceDir>/compose.yml up -d
// --remove-orphans` for the given service directory.
func Deploy(serviceDir string) error {
	composeFile := filepath.Join(serviceDir, "compose.yml")
	args := []string{"compose", "-f", composeFile, "up", "-d", "--remove-orphans"}
	log.Printf("deploy: running docker %s", strings.Join(args, " "))

	cmd := exec.Command("docker", args...)
	cmd.Dir = serviceDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up (%s): %w: %s", composeFile, err, stderr.String())
	}

	// docker compose up -d writes its "Container X Started" progress to
	// stderr, not stdout -- it's meant for a terminal, not a pipe. Log
	// whatever came back on either, since seeing what actually happened
	// beats guessing at 2am.
	if out := strings.TrimSpace(stdout.String()); out != "" {
		log.Printf("deploy: docker compose stdout:\n%s", out)
	}
	if out := strings.TrimSpace(stderr.String()); out != "" {
		log.Printf("deploy: docker compose stderr:\n%s", out)
	}
	return nil
}
