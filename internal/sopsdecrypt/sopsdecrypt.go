// Package sopsdecrypt decrypts sops-encrypted secrets.enc.env files; see
// README.md for why this is a library and not the sops CLI.
package sopsdecrypt

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

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

// DecryptServiceSecrets decrypts <serviceDir>/secrets.enc.env to
// <serviceDir>/secrets.env, if secrets.enc.env exists. It's a no-op for
// services that don't have any encrypted secrets.
func DecryptServiceSecrets(serviceDir, sshKeyPath string) error {
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

	outPath := filepath.Join(serviceDir, "secrets.env")
	if err := os.WriteFile(outPath, plaintext, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	log.Printf("sopsdecrypt: wrote %s", outPath)
	return nil
}
