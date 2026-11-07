package notify

import (
	"bytes"
	"io"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Host: "mail.example.org", Port: 587, TLS: "starttls",
		From:     "gitops-agent@example.org",
		To:       []string{"you@example.org", "Ops <ops@example.org>"},
		Hostname: "box1",
	}
}

var msgTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func failEv(name, stage, err string) Event {
	return Event{Key: Key{KindService, name}, Stage: stage, Err: err}
}

func recEv(name string) Event { return Event{Key: Key{KindService, name}, Recovered: true} }

func build(t *testing.T, c Config, obs Observation, evs ...Event) (*message, *mail.Message, string) {
	t.Helper()
	m, err := buildMessage(c, obs, evs, msgTime)
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	pm, err := mail.ReadMessage(bytes.NewReader(m.data))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	b, _ := io.ReadAll(pm.Body)
	return m, pm, string(b)
}

func TestBuildMessage_Structure(t *testing.T) {
	m, pm, body := build(t, testConfig(), Observation{Commit: "abcdef123456"}, failEv("web", "deploy", "boom"))
	if m.from != "gitops-agent@example.org" || len(m.to) != 2 || m.to[1] != "ops@example.org" {
		t.Errorf("envelope = %q %q", m.from, m.to)
	}
	if got := pm.Header.Get("From"); got != "<gitops-agent@example.org>" {
		t.Errorf("From = %q", got)
	}
	if got := pm.Header.Get("To"); !strings.Contains(got, "ops@example.org") {
		t.Errorf("To = %q", got)
	}
	if _, err := pm.Header.Date(); err != nil {
		t.Errorf("Date: %v", err)
	}
	if !regexp.MustCompile(`^<[0-9]+\.[0-9a-f]{8}@example\.org>$`).MatchString(pm.Header.Get("Message-ID")) {
		t.Errorf("Message-ID = %q", pm.Header.Get("Message-ID"))
	}
	if pm.Header.Get("MIME-Version") != "1.0" || pm.Header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		pm.Header.Get("Auto-Submitted") != "auto-generated" {
		t.Errorf("headers = %v", pm.Header)
	}
	for _, want := range []string{"FAILING", "  web  (stage: deploy)", "Commit:  abcdef123456", "Time:    2026-03-04T05:06:07Z", "journalctl -u gitops-agent"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Status:") || strings.Contains(body, "RECOVERED") {
		t.Errorf("unexpected sections:\n%s", body)
	}
}

func TestBuildMessage_HeaderInjection(t *testing.T) {
	c := testConfig()
	c.IncludeError = true
	m, pm, body := build(t, c, Observation{},
		failEv("evil\r\nBcc: attacker@example.net\r\nX-Pwn: 1", "deploy", "boom\r\nBcc: z@example.net\nsecret=hunter2"),
		failEv("x\nSubject: forged", "de\r\nploy", ""),
		recEv("y\r\nBcc: q@example.net"))
	want := map[string]bool{"From": true, "To": true, "Subject": true, "Date": true, "Message-Id": true, "Mime-Version": true,
		"Content-Type": true, "Content-Transfer-Encoding": true, "Auto-Submitted": true}
	if len(pm.Header) != len(want) {
		t.Errorf("header set = %v", pm.Header)
	}
	for k := range pm.Header {
		if !want[k] {
			t.Errorf("unexpected header %q", k)
		}
	}
	if len(pm.Header["Subject"]) != 1 || strings.ContainsAny(pm.Header.Get("Subject"), "\r\n") {
		t.Errorf("Subject = %q", pm.Header["Subject"])
	}
	head, _, _ := bytes.Cut(m.data, []byte("\r\n\r\n"))
	if n := bytes.Count(head, []byte("\r\n")) + 1; n != 9 {
		t.Errorf("header lines = %d, want 9:\n%s", n, head)
	}
	if strings.Contains(body, "\nBcc:") || strings.Contains(body, "\nX-Pwn") || strings.Contains(body, "\nSubject:") {
		t.Errorf("forged line in body:\n%s", body)
	}
	if !strings.Contains(body, "  evil Bcc: attacker@example.net X-Pwn: 1  (stage: deploy)") {
		t.Errorf("cleaned name not on one line:\n%s", body)
	}
	if strings.Contains(body, "hunter2") {
		t.Errorf("second error line leaked:\n%s", body)
	}
	if !strings.Contains(body, "      error: boom") {
		t.Errorf("first error line missing:\n%s", body)
	}
}

func TestHeader_RejectsLineBreaks(t *testing.T) {
	if _, err := header("Subject", "a\r\nb"); err == nil {
		t.Error("CRLF accepted")
	}
	if _, err := header("X", "a\nb"); err == nil {
		t.Error("LF accepted")
	}
	if _, err := header("X", "a\rb"); err == nil {
		t.Error("CR accepted")
	}
	if got, err := header("X", "ok"); err != nil || got != "X: ok\r\n" {
		t.Errorf("header = %q, %v", got, err)
	}
}

func TestBuildMessage_NonASCIISubject(t *testing.T) {
	_, pm, _ := build(t, testConfig(), Observation{}, failEv("tjeneste-æøå", "deploy", ""))
	raw := pm.Header.Get("Subject")
	if !strings.HasPrefix(raw, "=?utf-8?q?") {
		t.Errorf("Subject not encoded: %q", raw)
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(raw)
	if err != nil || dec != "[gitops-agent] box1: tjeneste-æøå is failing" {
		t.Errorf("decoded = %q, %v", dec, err)
	}
}

func TestBuildMessage_IncludeError(t *testing.T) {
	long := strings.Repeat("x", 500)
	_, _, body := build(t, testConfig(), Observation{}, failEv("web", "deploy", "SECRET-ERR "+long))
	if strings.Contains(body, "SECRET-ERR") || strings.Contains(body, "error:") {
		t.Errorf("error text present with include_error off:\n%s", body)
	}
	c := testConfig()
	c.IncludeError = true
	_, _, body = build(t, c, Observation{}, failEv("web", "deploy", "first "+long+"\nsecond"))
	var line string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "      error: ") {
			line = strings.TrimPrefix(l, "      error: ")
		}
	}
	if len([]rune(line)) != 200 || !strings.HasSuffix(line, "...") || strings.Contains(body, "second") {
		t.Errorf("error line = %q (%d runes)", line, len([]rune(line)))
	}
}

func TestSubject(t *testing.T) {
	c := testConfig()
	sync := Event{Key: Key{Kind: KindSync}, Stage: "sync"}
	tests := []struct {
		name string
		evs  []Event
		want string
	}{
		{"one failing", []Event{failEv("web", "", "")}, "[gitops-agent] box1: web is failing"},
		{"one recovered", []Event{recEv("web")}, "[gitops-agent] box1: web recovered"},
		{"sync", []Event{sync}, "[gitops-agent] box1: git sync is failing"},
		{"mixed", []Event{failEv("a", "", ""), failEv("b", "", ""), recEv("c")}, "[gitops-agent] box1: 2 failing, 1 recovered"},
		{"only failing many", []Event{failEv("a", "", ""), failEv("b", "", "")}, "[gitops-agent] box1: 2 failing"},
		{"unnamed", []Event{failEv("\r\n", "", "")}, "[gitops-agent] box1: (unnamed) is failing"},
	}
	for _, tc := range tests {
		if got := subject(c, sortedEvents(tc.evs)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuildMessage_LongNameTruncated(t *testing.T) {
	name := strings.Repeat("n", 10000)
	_, pm, body := build(t, testConfig(), Observation{}, failEv(name, "deploy", ""))
	subj, _ := new(mime.WordDecoder).DecodeHeader(pm.Header.Get("Subject"))
	want := strings.Repeat("n", 61) + "..."
	if !strings.Contains(subj, want) || strings.Contains(subj, strings.Repeat("n", 62)) {
		t.Errorf("Subject = %q", subj)
	}
	if !strings.Contains(body, "  "+want+"  (stage") {
		t.Errorf("body name not truncated:\n%s", body)
	}
}

func TestBuildMessage_StatusURLAndNoPassword(t *testing.T) {
	_, _, body := build(t, testConfig(), Observation{}, failEv("web", "", ""))
	if strings.Contains(body, "Status:") {
		t.Error("Status line without status_url")
	}
	c := testConfig()
	c.StatusURL = "https://status.example.org/"
	c.PasswordFile = "/etc/pw"
	_, _, body = build(t, c, Observation{}, failEv("web", "", ""))
	if !strings.Contains(body, "Status:  https://status.example.org/") {
		t.Errorf("status url missing:\n%s", body)
	}
	if strings.Contains(body, "/etc/pw") {
		t.Error("password file path in body")
	}
}

func TestBuildMessage_SectionsAndOrder(t *testing.T) {
	sync := Event{Key: Key{Kind: KindSync}, Stage: "sync"}
	_, _, body := build(t, testConfig(), Observation{}, failEv("zz", "deploy", ""), recEv("aa"), sync, failEv("bb", "", ""))
	i, j, k := strings.Index(body, "git sync  (stage: sync)"), strings.Index(body, "  bb"), strings.Index(body, "  zz")
	if !(i >= 0 && i < j && j < k) {
		t.Errorf("order wrong:\n%s", body)
	}
	if strings.Contains(body, "  bb  (stage") {
		t.Errorf("empty stage printed:\n%s", body)
	}
	if !strings.Contains(body, "RECOVERED\n  aa\n") {
		t.Errorf("recovered section missing:\n%s", body)
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText("a\u2028b\x00c\x7fd\te  f", 64); got != "a b c d e f" {
		t.Errorf("got %q", got)
	}
	if got := cleanText("bad\xffutf8", 64); got != "bad\uFFFDutf8" {
		t.Errorf("got %q", got)
	}
}
