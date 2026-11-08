package redact

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestScrub(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		in     string
		want   string
	}{
		{"once", []string{"hunter22x"}, "bad hunter22x here", "bad *** here"},
		{"twice", []string{"hunter22x"}, "hunter22x and hunter22x", "*** and ***"},
		{"adjacent", []string{"hunter22x"}, "hunter22xhunter22x", "******"},
		{"too short", []string{"abc", "true"}, "abc true", "abc true"},
		{"exactly MinLen", []string{"abcdef"}, "x abcdef y", "x *** y"},
		{"one below MinLen", []string{"abcde"}, "x abcde y", "x abcde y"},
		{"contains another", []string{"secret", "secret123456"}, "a secret123456 b", "a *** b"},
		{"contains another reversed", []string{"secret123456", "secret"}, "a secret123456 b secret", "a *** b ***"},
		{"duplicates", []string{"hunter22x", "hunter22x"}, "hunter22x", "***"},
		{"no values", nil, "text", "text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewScrubber(tc.values...).Scrub(tc.in); got != tc.want {
				t.Errorf("Scrub(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestScrub_NilAndZero(t *testing.T) {
	var s *Scrubber
	if got := s.Scrub("abc"); got != "abc" {
		t.Errorf("nil: %q", got)
	}
	if got := (&Scrubber{}).Scrub("abc"); got != "abc" {
		t.Errorf("zero: %q", got)
	}
}

func TestScrub_OutputNotRescanned(t *testing.T) {
	// A value made of mask characters must not match the mask that an
	// earlier replacement wrote.
	if got := NewScrubber("abcdef", "******").Scrub("abcdef"); got != "***" {
		t.Errorf("got %q", got)
	}
}

func TestEnvValues(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "K=plainvalue\n", []string{"plainvalue"}},
		{"double quoted", `K="quoted value"` + "\n", []string{`"quoted value"`, "quoted value"}},
		{"single quoted", "K='single1'\n", []string{"'single1'", "single1"}},
		{"export", "export K=exported1\n", []string{"exported1"}},
		{"inline comment", "K=abcdefg #comment\n", []string{"abcdefg #comment", "abcdefg"}},
		{"escaped newline", `K="abcd\nefgh"` + "\n", []string{`"abcd\nefgh"`, `abcd\nefgh`, "abcd\nefgh"}},
		{"embedded quote", `K=ab"cd"ef` + "\n", []string{`ab"cd"ef`, `ab\"cd\"ef`}},
		{"non ascii stays as is", "K=cafécafé\n", []string{"cafécafé"}},
		{"control char", "K=ab\tcdef\n", []string{"ab\tcdef", `ab\tcdef`}},
		{"comments blanks no equals", "# K=commentvalue\n\n   \nnoequalsline\n", nil},
		{"crlf", "K=crlfvalue\r\nL=other-value\r\n", []string{"crlfvalue", "other-value"}},
		{"value with equals", "K=a=b=c=d=e\n", []string{"a=b=c=d=e"}},
		{"literal backslash-n with quotes", `K='say "hi" \nbye'` + "\n",
			[]string{`'say "hi" \nbye'`, `say "hi" \nbye`, "say \"hi\" \nbye", `say \"hi\" \nbye`}},
		{"short dropped", "K=abc\n", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sorted(EnvValues(strings.NewReader(tc.in)))
			want := sorted(tc.want)
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
				t.Errorf("EnvValues = %q, want %q", got, want)
			}
		})
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func writeEnv(t *testing.T, dir, svc, content string) string {
	t.Helper()
	d := filepath.Join(dir, svc)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "secrets.env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadEnvDir_TwoServices(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "a", "K=value-from-a\n")
	writeEnv(t, dir, "b", "K=value-from-b\n")
	if got := LoadEnvDir(dir).Scrub("value-from-a value-from-b"); got != "*** ***" {
		t.Errorf("got %q", got)
	}
}

func TestLoadEnvDir_Missing(t *testing.T) {
	if got := LoadEnvDir(filepath.Join(t.TempDir(), "nope")).Scrub("abcdefgh"); got != "abcdefgh" {
		t.Errorf("got %q", got)
	}
	if got := LoadEnvDir("").Scrub("abcdefgh"); got != "abcdefgh" {
		t.Errorf("got %q", got)
	}
}

func TestLoadEnvDir_Unreadable(t *testing.T) {
	dir := t.TempDir()
	p := writeEnv(t, dir, "a", "K=unreadable-val\n")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if f, err := os.Open(p); err == nil {
		_ = f.Close()
		t.Skip("mode 0 file is still readable (running as root?)")
	}
	if got := LoadEnvDir(dir).Scrub("unreadable-val"); got != "unreadable-val" {
		t.Errorf("got %q", got)
	}
}

func TestLoadEnvDir_SymlinkSkipped(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.env")
	if err := os.WriteFile(target, []byte("K=linked-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "a", "secrets.env")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if got := LoadEnvDir(dir).Scrub("linked-secret"); got != "linked-secret" {
		t.Errorf("symlink was followed: %q", got)
	}
}

func TestLoadEnvDir_FIFOSkippedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "a", "secrets.env"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	writeEnv(t, dir, "b", "K=value-from-b\n")
	done := make(chan *Scrubber, 1)
	go func() { done <- LoadEnvDir(dir) }()
	select {
	case s := <-done:
		if got := s.Scrub("value-from-b"); got != "***" {
			t.Errorf("regular file after the FIFO was not read: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadEnvDir blocked on a FIFO")
	}
}

func TestError_GoStringHidesDetail(t *testing.T) {
	e := &Error{Summary: "sum", Detail: "journal-only"}
	for _, got := range []string{fmt.Sprintf("%v", e), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e)} {
		if got != "sum" {
			t.Errorf("formatted = %q", got)
		}
	}
}

func TestLoadEnvDir_SizeCap(t *testing.T) {
	dir := t.TempDir()
	filler := strings.Repeat("# filler line to take up space\n", (1<<20)/31+10)
	writeEnv(t, dir, "a", "K=early-secret\n"+filler+"L=late-secret\n")
	s := LoadEnvDir(dir)
	if got := s.Scrub("early-secret"); got != "***" {
		t.Errorf("early value not read: %q", got)
	}
	if got := s.Scrub("late-secret"); got != "late-secret" {
		t.Errorf("value past the cap was read: %q", got)
	}
}

func TestDetail(t *testing.T) {
	e := &Error{Summary: "sum", Detail: "det"}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("x"), ""},
		{"direct", e, "det"},
		{"wrapped", fmt.Errorf("deploying x: %w", e), "det"},
		{"joined", errors.Join(errors.New("a"), fmt.Errorf("w: %w", e)), "det"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detail(tc.err); got != tc.want {
				t.Errorf("Detail = %q, want %q", got, tc.want)
			}
		})
	}
	if e.Error() != "sum" {
		t.Errorf("Error() = %q", e.Error())
	}
}
