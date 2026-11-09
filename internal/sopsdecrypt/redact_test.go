package sopsdecrypt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/oddsund/gitops-agent/internal/redact"
)

var longHex = regexp.MustCompile(`[0-9a-fA-F]{32,}`)

// runSeam runs DecryptServiceSecrets on an enc file with decryptFile
// replaced by a function that returns err.
func runSeam(t *testing.T, base string, err error) error {
	t.Helper()
	old := decryptFile
	decryptFile = func(string, string) ([]byte, error) { return nil, err }
	t.Cleanup(func() { decryptFile = old })
	dir := t.TempDir()
	if werr := os.WriteFile(filepath.Join(dir, "secrets.enc.env"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	return DecryptServiceSecrets(dir, "svc", "testdata/test_key", base, nil)
}

func asRedact(t *testing.T, err error) {
	t.Helper()
	var re *redact.Error
	if !errors.As(err, &re) {
		t.Fatalf("error %T %v is not a *redact.Error", err, err)
	}
}

func TestDecryptServiceSecrets_IntegrityErrorSafe(t *testing.T) {
	mac := strings.Repeat("ab12cd34", 16)
	err := runSeam(t, t.TempDir(), fmt.Errorf("Failed to verify data integrity. expected mac %q, got %q", mac, mac))
	asRedact(t, err)
	if err.Error() != summaryIntegrity {
		t.Errorf("Error() = %q, want %q", err.Error(), summaryIntegrity)
	}
	d := redact.Detail(err)
	if d != detailIntegrity || longHex.MatchString(d) || strings.Contains(d, mac) {
		t.Errorf("detail = %q", d)
	}
}

func TestDecryptServiceSecrets_ParseErrorSafe(t *testing.T) {
	err := runSeam(t, t.TempDir(), errors.New("invalid dotenv input line: PASSWORD-canary-9f2a"))
	asRedact(t, err)
	if err.Error() != summaryGeneric {
		t.Errorf("Error() = %q", err.Error())
	}
	if d := redact.Detail(err); d != detailParse {
		t.Errorf("detail = %q", d)
	}
}

func TestDecryptServiceSecrets_ReadError(t *testing.T) {
	pe := &fs.PathError{Op: "open", Path: "/x/secrets.enc.env", Err: fs.ErrPermission}
	err := runSeam(t, t.TempDir(), fmt.Errorf("Failed to read %q: %w", pe.Path, pe))
	asRedact(t, err)
	if err.Error() != summaryRead {
		t.Errorf("Error() = %q, want %q", err.Error(), summaryRead)
	}
}

// dataKeyErr mimics sops' getDataKeyError: a short Error() and the per-key
// reasons in UserError().
type dataKeyErr struct{ user string }

func (e dataKeyErr) Error() string {
	return "Error getting data key: 0 successful groups required, got 0"
}
func (e dataKeyErr) UserError() string { return e.user }

func TestDecryptServiceSecrets_DataKeyErrorScrubbedAndMasked(t *testing.T) {
	base := t.TempDir()
	const value = "hunter2-canary-value"
	if err := os.MkdirAll(filepath.Join(base, "svc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "svc", "secrets.env"), []byte("K="+value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hex := strings.Repeat("0f", 32)
	err := runSeam(t, base, dataKeyErr{user: "no identity matched age1abc " + value + " " + hex})
	asRedact(t, err)
	if err.Error() != summaryGeneric {
		t.Errorf("Error() = %q", err.Error())
	}
	d := redact.Detail(err)
	if strings.Contains(d, value) || strings.Contains(d, hex) || strings.Count(d, redact.Mask) != 2 {
		t.Errorf("detail = %q", d)
	}
	if !strings.Contains(d, "age1abc") {
		t.Errorf("key diagnostic lost: %q", d)
	}
}

// Messages that sops builds from values must never reach the journal, and
// neither may an error nobody has classified yet.
func TestDecryptServiceSecrets_ValueQuotingErrorsNotLogged(t *testing.T) {
	const c = "PLAINTEXT-canary-7731"
	for _, msg := range []string{
		"Could not decrypt value: Input string " + c + " does not match sops' data format",
		"Could not convert " + c + " to bytes: boom",
		"Tree contains a non-string key (type int): " + c + ". Only string keys are supported",
		"some future sops message quoting " + c,
	} {
		err := runSeam(t, t.TempDir(), errors.New(msg))
		asRedact(t, err)
		d := redact.Detail(err)
		if err.Error() != summaryGeneric || strings.Contains(err.Error(), c) || strings.Contains(d, c) {
			t.Errorf("%q: Error() = %q, detail = %q", msg, err.Error(), d)
		}
		if !strings.HasPrefix(d, detailUnclassified) || !strings.Contains(d, "*errors.errorString") {
			t.Errorf("%q: detail = %q", msg, d)
		}
	}
}

func TestSanitize_CapKeepsTailAndMasksStraddlingValue(t *testing.T) {
	const value = "straddling-secret-value"
	scrub := redact.NewScrubber(value)
	// The value sits across the 2 KiB cut point of the unscrubbed text.
	msg := strings.Repeat("x", 100) + value + strings.Repeat("y", maxDetail-len(value)/2)
	d := sanitize(msg, scrub)
	if strings.Contains(d, "secret-value") || strings.Contains(d, "straddling") {
		t.Errorf("value fragment survived: %q", d[:60])
	}
	if !strings.HasSuffix(d, "yyyy") || !strings.HasPrefix(d, "...") || len(d) > maxDetail+3 {
		t.Errorf("cap wrong: len %d", len(d))
	}
}

func TestDecryptServiceSecrets_RealSopsParseError(t *testing.T) {
	const c = "NOT-A-DOTENV-LINE-canary"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.enc.env"), []byte(c+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := DecryptServiceSecrets(dir, "svc", "testdata/test_key", t.TempDir(), nil)
	asRedact(t, err)
	if err.Error() != summaryGeneric || strings.Contains(err.Error(), c) {
		t.Errorf("Error() = %q", err.Error())
	}
	if d := redact.Detail(err); d != detailParse {
		t.Errorf("detail = %q (real sops message shape changed?)", d)
	}
}

func TestDecryptServiceSecrets_WrongKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	key := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v: %s", err, out)
	}
	enc, err := os.ReadFile("testdata/secrets.enc.env")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.enc.env"), enc, 0o600); err != nil {
		t.Fatal(err)
	}
	err = DecryptServiceSecrets(dir, "svc", key, t.TempDir(), nil)
	asRedact(t, err)
	if err.Error() != summaryGeneric {
		t.Errorf("Error() = %q", err.Error())
	}
	d := redact.Detail(err)
	if longHex.MatchString(d) {
		t.Errorf("detail has a long hex run: %q", d)
	}
	if !strings.Contains(d, "Failed to get the data key") {
		t.Errorf("data-key diagnostics lost: %q", d)
	}
}

// Errors from the agent's own file operations stay verbatim: a path and an
// errno, no file content.
func TestDecryptServiceSecrets_WriteErrorVerbatim(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
	enc, err := os.ReadFile("testdata/secrets.enc.env")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.enc.env"), enc, 0o600); err != nil {
		t.Fatal(err)
	}
	err = DecryptServiceSecrets(dir, "svc", "testdata/test_key", base, nil)
	if err == nil {
		t.Fatal("want error")
	}
	var re *redact.Error
	if errors.As(err, &re) {
		t.Fatalf("own error wrapped in redact.Error: %v", err)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("want *fs.PathError, got %T", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(wantPlaintext(t)), "\n") {
		if _, v, ok := strings.Cut(line, "="); ok && len(v) >= redact.MinLen && strings.Contains(err.Error(), v) {
			t.Errorf("error contains plaintext value %q: %v", v, err)
		}
	}
}
