package notify

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const maxPasswordFile = 4096

// LoadPassword reads the SMTP password from path. The file must be a regular
// file that only its owner can access: config.toml is world-readable, so
// this file is the only place the secret lives. A file that group or others
// can read is refused rather than warned about, the way ssh treats a private
// key; a warning on a credential is read by nobody.
//
// Error strings carry the path, never the contents.
//
// The file must also be owned by the agent's effective uid.
func LoadPassword(path string) (string, error) { return loadPassword(path, os.Geteuid) }

// loadPassword takes the uid lookup as a parameter so tests can fake it.
func loadPassword(path string, euid func() int) (string, error) {
	// Stat the open descriptor, not the path, so the file checked is the
	// file read. Symlinks are followed; the check applies to the target.
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("password_file %s: cannot open: %w", path, unwrapPath(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("password_file %s: cannot stat: %w", path, unwrapPath(err))
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("password_file %s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("password_file %s is accessible to group or others (mode %04o); run chmod 600", path, fi.Mode().Perm())
	}
	if uid, ok := ownerUID(fi); ok && uid != euid() {
		return "", fmt.Errorf("password_file %s is owned by uid %d, not the agent's uid %d; run chown", path, uid, euid())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPasswordFile+1))
	if err != nil {
		return "", fmt.Errorf("password_file %s: cannot read: %w", path, unwrapPath(err))
	}
	if len(data) > maxPasswordFile {
		return "", fmt.Errorf("password_file %s is larger than %d bytes", path, maxPasswordFile)
	}
	pw := strings.TrimRight(string(data), "\r\n")
	if pw == "" || strings.ContainsAny(pw, "\r\n\x00") {
		return "", fmt.Errorf("password_file %s must contain one non-empty line", path)
	}
	return pw, nil
}

// unwrapPath drops the *PathError wrapper so the path is not printed twice.
func unwrapPath(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err
	}
	return err
}
