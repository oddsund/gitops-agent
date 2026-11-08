// Package redact keeps secret values out of text that is shown or logged.
//
// It removes whole values only. If a tool quotes just a fragment of a value
// (for example the first 20 characters near a parse error), the fragment is
// not found. The scrub is therefore a second barrier, not a security
// boundary: the first barrier is Error, whose message never carries tool
// output. Values are read from <dir>/*/secrets.env only; a secret that
// reaches a tool by another route (a literal in compose.yml, the host
// environment) is not known to the agent. Partially overlapping values (not
// nested) are matched left to right, so the later one is not masked in
// full: "abcdefg" and "defghij" on "abcdefghij" give "***hij".
package redact

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// MinLen is the shortest secret value that is scrubbed. Shorter values
// ("1", "true", "8080", "yes") would also hit ordinary words and numbers in
// compose output and make the errors unreadable, and a value that short
// protects almost nothing anyway.
const MinLen = 6

// Mask replaces every scrubbed occurrence.
const Mask = "***"

// maxFile caps how much of one env file is read.
const maxFile = 1 << 20

// Scrubber replaces known secret values in text. The zero value and a nil
// *Scrubber both leave text unchanged.
type Scrubber struct{ r *strings.Replacer }

// NewScrubber builds a Scrubber from values. Values shorter than MinLen
// are dropped, duplicates are collapsed, and longer values are tried before
// shorter ones, so a value that contains another is masked whole.
func NewScrubber(values ...string) *Scrubber {
	seen := make(map[string]bool, len(values))
	var vs []string
	for _, v := range values {
		if len(v) < MinLen || seen[v] {
			continue
		}
		seen[v] = true
		vs = append(vs, v)
	}
	if len(vs) == 0 {
		return &Scrubber{}
	}
	sort.SliceStable(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	pairs := make([]string, 0, 2*len(vs))
	for _, v := range vs {
		pairs = append(pairs, v, Mask)
	}
	return &Scrubber{r: strings.NewReplacer(pairs...)}
}

// Scrub returns text with every occurrence of a known value replaced by Mask.
func (s *Scrubber) Scrub(text string) string {
	if s == nil || s.r == nil {
		return text
	}
	return s.r.Replace(text)
}

// EnvValues returns the strings that compose might print for each value in
// dotenv text r. It never returns an error and never logs; malformed lines
// are skipped. Per line it yields: the text after the first "=", the same
// without one pair of surrounding quotes, the unquoted text before " #"
// (compose strips inline comments), the quote-stripped form with "\n"
// decoded to a newline (sops writes newlines escaped), and the Go-quoted
// form of that without its surrounding quotes. Candidates shorter than
// MinLen are left out.
func EnvValues(r io.Reader) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if len(v) >= MinLen && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxFile)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		_, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		add(val)
		unq := val
		quoted := false
		if n := len(val); n >= 2 && (val[0] == '\'' || val[0] == '"') && val[n-1] == val[0] {
			unq = val[1 : n-1]
			quoted = true
		}
		add(unq)
		if !quoted {
			if i := strings.Index(val, " #"); i >= 0 {
				add(strings.TrimRight(val[:i], " \t"))
			}
		}
		dec := strings.ReplaceAll(unq, `\n`, "\n")
		add(dec)
		if q := strconv.Quote(dec); len(q) >= 2 {
			add(q[1 : len(q)-1])
		}
	}
	return out
}

// LoadEnvDir reads <dir>/*/secrets.env (regular files only, at most 1 MiB
// each) and returns a Scrubber for all values found. A missing dir, an
// unreadable file or no values give a Scrubber that changes nothing. It
// returns no error on purpose: an error text could be built from file
// content, and the caller can do nothing useful with it.
func LoadEnvDir(dir string) *Scrubber {
	if dir == "" {
		return &Scrubber{}
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*", "secrets.env"))
	if err != nil {
		return &Scrubber{}
	}
	var values []string
	for _, p := range paths {
		// No Lstat-then-open: the check and the open would be two steps. Open
		// refusing symlinks and FIFOs, then check the opened file.
		f, err := os.OpenFile(p, openFlags, 0)
		if err != nil {
			continue
		}
		if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
			values = append(values, EnvValues(io.LimitReader(f, maxFile))...)
		}
		_ = f.Close()
	}
	return NewScrubber(values...)
}
