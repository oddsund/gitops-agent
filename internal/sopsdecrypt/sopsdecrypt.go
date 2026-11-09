// Package sopsdecrypt decrypts sops-encrypted secrets.enc.env files; see
// docs/secrets.md for why this is a library and not the sops CLI.
package sopsdecrypt

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/getsops/sops/v3/decrypt"
)

// sshPrivateKeyFileEnv is the environment variable sops' age keysource
// checks for an SSH-native age identity file (see
// github.com/getsops/sops/v3/age.SopsAgeSshPrivateKeyFileEnv).
const sshPrivateKeyFileEnv = "SOPS_AGE_SSH_PRIVATE_KEY_FILE"

// decryptFile is decrypt.File; tests replace it to inject sops errors.
var decryptFile = decrypt.File

// useKey points sops at the SSH private key. The decrypt package has no
// functional parameter for the key; sops only accepts it via this env var
// (or a default path).
func useKey(sshKeyPath string) error {
	if err := os.Setenv(sshPrivateKeyFileEnv, sshKeyPath); err != nil {
		return fmt.Errorf("setting %s: %w", sshPrivateKeyFileEnv, err)
	}
	return nil
}

// DecryptFile decrypts a sops-encrypted dotenv file at encPath, using the
// SSH private key at sshKeyPath as the decryption identity. The returned
// error carries sops' own text, which can quote file content and MACs: it
// is not safe to show. Callers that surface errors must use
// DecryptServiceSecrets.
func DecryptFile(encPath, sshKeyPath string) ([]byte, error) {
	if err := useKey(sshKeyPath); err != nil {
		return nil, err
	}
	out, err := decryptFile(encPath, "dotenv")
	if err != nil {
		return nil, fmt.Errorf("decrypting %s: %w", encPath, err)
	}
	return out, nil
}

func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
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
// <secretsBaseDir>/<serviceName>/secrets.env. If the file does not exist,
// any previously decrypted secrets.env for the service is removed (see
// RemoveServiceSecrets); a service with neither is a no-op. After a nil
// return, secrets.env exists if and only if secrets.enc.env does.
// serviceName must match the service's `name` in services.toml -- that's
// also what the service's compose.yml env_file entry must point at, see
// docs/secrets.md.
func DecryptServiceSecrets(serviceDir, serviceName, sshKeyPath, secretsBaseDir string, log *slog.Logger) error {
	l := logger(log).With("service", serviceName)
	encPath := filepath.Join(serviceDir, "secrets.enc.env")
	if _, err := os.Stat(encPath); errors.Is(err, os.ErrNotExist) {
		// The encrypted source is gone, so a plaintext left by an earlier
		// decrypt is stale and must not keep feeding the service.
		if err := RemoveServiceSecrets(serviceName, secretsBaseDir, log); err != nil {
			return fmt.Errorf("removing stale secrets for %s: %w", serviceName, err)
		}
		l.Debug("no encrypted secrets for service", "path", serviceDir)
		return nil
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", encPath, err)
	}

	// A decrypt.File failure becomes a *redact.Error (fixed summary, scrubbed
	// journal detail). The other errors in this function carry a path and an
	// errno, never file content, and stay verbatim so that "permission
	// denied writing /run/gitops-agent/x/secrets.env" remains actionable.
	if err := useKey(sshKeyPath); err != nil {
		return err
	}
	plaintext, err := decryptFile(encPath, "dotenv")
	if err != nil {
		return wrapDecryptErr(err, secretsBaseDir)
	}

	outDir := filepath.Join(secretsBaseDir, serviceName)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}
	outPath := filepath.Join(outDir, "secrets.env")
	if err := os.WriteFile(outPath, plaintext, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	l.Info("wrote decrypted secrets", "path", outPath)
	return nil
}

// RemoveServiceSecrets deletes <secretsBaseDir>/<serviceName>/secrets.env and
// then the service's directory under secretsBaseDir. A file or directory
// that is already gone is success. The directory is removed non-recursively,
// so a directory holding anything else is left alone: it is not ours to
// delete. The service name is validated here, independently of
// internal/config, because this function deletes files and must not be
// steerable out of secretsBaseDir by a hostile name.
func RemoveServiceSecrets(serviceName, secretsBaseDir string, log *slog.Logger) error {
	l := logger(log).With("service", serviceName)
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
		l.Info("removed decrypted secrets", "path", file)
	}
	return nil
}
