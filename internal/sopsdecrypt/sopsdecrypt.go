// Package sopsdecrypt decrypts sops-encrypted secrets.enc.env files; see
// docs/secrets.md for why this is a library and not the sops CLI.
package sopsdecrypt

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/getsops/sops/v3/decrypt"
)

// sshPrivateKeyFileEnv is the environment variable sops' age keysource
// checks for an SSH-native age identity file (see
// github.com/getsops/sops/v3/age.SopsAgeSshPrivateKeyFileEnv).
const sshPrivateKeyFileEnv = "SOPS_AGE_SSH_PRIVATE_KEY_FILE"

// DecryptFile decrypts a sops-encrypted dotenv file at encPath, using the
// SSH private key at sshKeyPath as the decryption identity.
func DecryptFile(encPath, sshKeyPath string) ([]byte, error) {
	// decrypt package has no functional parameter for the key; sops only
	// accepts it via this env var (or a default path).
	if err := os.Setenv(sshPrivateKeyFileEnv, sshKeyPath); err != nil {
		return nil, fmt.Errorf("setting %s: %w", sshPrivateKeyFileEnv, err)
	}

	out, err := decrypt.File(encPath, "dotenv")
	if err != nil {
		return nil, fmt.Errorf("decrypting %s: %w", encPath, err)
	}
	return out, nil
}

// DefaultSecretsBaseDir is where DecryptServiceSecrets writes plaintext by
// default: tmpfs (backed by the gitops-agent.service unit's
// RuntimeDirectory=gitops-agent), not the git worktree. That means a
// secret is gone on reboot or `systemctl stop`, and is never one `git add
// -A` away from being committed (.gitignore already covers secrets.env, so
// this is defence in depth on top of that, not instead of it).
const DefaultSecretsBaseDir = "/run/gitops-agent"

// DecryptServiceSecrets decrypts <serviceDir>/secrets.enc.env, if it
// exists, and writes the plaintext to
// <secretsBaseDir>/<serviceName>/secrets.env. It's a no-op for services
// that don't have any encrypted secrets. serviceName must match the
// service's `name` in services.toml -- that's also what the service's
// compose.yml env_file entry must point at, see docs/secrets.md.
func DecryptServiceSecrets(serviceDir, serviceName, sshKeyPath, secretsBaseDir string) error {
	encPath := filepath.Join(serviceDir, "secrets.enc.env")
	if _, err := os.Stat(encPath); errors.Is(err, os.ErrNotExist) {
		log.Printf("sopsdecrypt: no secrets.enc.env in %s, nothing to decrypt (not an error, just a quiet service)", serviceDir)
		return nil
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", encPath, err)
	}

	plaintext, err := DecryptFile(encPath, sshKeyPath)
	if err != nil {
		return err
	}

	outDir := filepath.Join(secretsBaseDir, serviceName)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}
	outPath := filepath.Join(outDir, "secrets.env")
	if err := os.WriteFile(outPath, plaintext, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	log.Printf("sopsdecrypt: wrote %s", outPath)
	return nil
}

// RemoveServiceSecrets deletes <secretsBaseDir>/<serviceName>/secrets.env and
// then the service's directory under secretsBaseDir. A file or directory
// that is already gone is success. The directory is removed non-recursively,
// so a directory holding anything else is left alone: it is not ours to
// delete. The service name is validated here, independently of
// internal/config, because this function deletes files and must not be
// steerable out of secretsBaseDir by a hostile name.
func RemoveServiceSecrets(serviceName, secretsBaseDir string) error {
	if serviceName == "" || serviceName == "." || serviceName == ".." ||
		strings.ContainsAny(serviceName, "/\\\x00") ||
		!filepath.IsLocal(serviceName) ||
		serviceName != filepath.Base(serviceName) {
		return fmt.Errorf("refusing to remove secrets for invalid service name %q", serviceName)
	}
	// An empty base would make Join yield a path relative to the CWD.
	if secretsBaseDir == "" {
		return errors.New("refusing to remove secrets: empty secrets base directory")
	}

	base := filepath.Clean(secretsBaseDir)
	dir := filepath.Join(base, serviceName)
	file := filepath.Join(dir, "secrets.env")

	// Last line of defence against anything the name check missed; the
	// filesystem is not touched before this passes.
	rel, err := filepath.Rel(base, dir)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return fmt.Errorf("secrets path %s escapes %s", dir, base)
	}

	// Lexical checks cannot see a symlinked service directory, through which
	// os.Remove would delete a file outside the base. Refuse anything that
	// is not a real directory before removing anything.
	switch fi, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("checking %s: %w", dir, err)
	case !fi.IsDir():
		return fmt.Errorf("refusing to remove secrets: %s is not a directory", dir)
	}

	removed := false
	switch err := os.Remove(file); {
	case err == nil:
		removed = true
	case errors.Is(err, fs.ErrNotExist):
		// Carry on: an empty directory left by an earlier run should go too.
	default:
		return fmt.Errorf("removing %s: %w", file, err)
	}

	// The stdlib maps both ENOTEMPTY and EEXIST to fs.ErrExist.
	if err := os.Remove(dir); err != nil &&
		!errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("removing %s: %w", dir, err)
	}

	// Silent when the file was already absent so the per-cycle sweep for
	// disabled services logs nothing in the steady state.
	if removed {
		log.Printf("sopsdecrypt: removed %s", file)
	}
	return nil
}
