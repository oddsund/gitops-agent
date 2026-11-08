package notify

import (
	"bytes"
	"context"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time { return msgTime }

func TestSendTestWith_Delivers(t *testing.T) {
	f := newFakeSMTP(t, nil)
	cfg := testConfig()
	cfg.From = "from@example.org"
	cfg.To = []string{"a@example.org", "b@example.org"}
	cfg.StatusURL = "http://box1:8080/"
	m := testMailer(f, "none")
	m.password = "SECRET-PW"
	if err := sendTestWith(context.Background(), cfg, m, fixedNow); err != nil {
		t.Fatal(err)
	}
	msgs := f.messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	got := msgs[0]
	if got.from != "from@example.org" || len(got.rcpts) != 2 || got.rcpts[0] != "a@example.org" || got.rcpts[1] != "b@example.org" {
		t.Errorf("envelope = %q %q", got.from, got.rcpts)
	}
	pm, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	if s := pm.Header.Get("Subject"); s != "[gitops-agent] box1: test notification" {
		t.Errorf("Subject = %q", s)
	}
	if pm.Header.Get("Auto-Submitted") != "auto-generated" {
		t.Errorf("Auto-Submitted = %q", pm.Header.Get("Auto-Submitted"))
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(pm.Body)
	body := buf.String()
	for _, want := range []string{"This is a test message from gitops-agent on box1.", "Status:  http://box1:8080/", "Time:    2026-03-04T05:06:07Z"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Commit:") {
		t.Errorf("body has a Commit line:\n%s", body)
	}
	if strings.Contains(got.data, "SECRET-PW") {
		t.Error("password in message")
	}
}

func TestSendTestWith_FailureVocabulary(t *testing.T) {
	t.Run("auth", func(t *testing.T) {
		srv, cli := testTLS(t)
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg, f.rejectAuth = srv, true })
		m := testMailer(f, "starttls")
		m.tlsConfig = cli
		m.username, m.password = "user", "pass"
		wantSendError(t, sendTestWith(context.Background(), testConfig(), m, fixedNow), "auth", "server replied 535")
	})
	t.Run("rcpt", func(t *testing.T) {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.rcptCode = "550 no such user" })
		wantSendError(t, sendTestWith(context.Background(), testConfig(), testMailer(f, "none"), fixedNow), "rcpt", "server replied 550")
	})
	t.Run("connect", func(t *testing.T) {
		f := newFakeSMTP(t, nil)
		m := testMailer(f, "none")
		f.ln.Close()
		wantSendError(t, sendTestWith(context.Background(), testConfig(), m, fixedNow), "connect", "connection refused")
	})
	t.Run("starttls", func(t *testing.T) {
		f := newFakeSMTP(t, nil)
		wantSendError(t, sendTestWith(context.Background(), testConfig(), testMailer(f, "starttls"), fixedNow), "starttls", "server does not offer STARTTLS")
	})
	t.Run("timeout", func(t *testing.T) {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.hangAt = "greeting" })
		// The Mailer's own deadline fires first in production (30s < 45s).
		m := testMailer(f, "none")
		m.dialTimeout, m.totalTimeout = 300*time.Millisecond, 300*time.Millisecond
		wantSendError(t, sendTestWith(context.Background(), testConfig(), m, fixedNow), "connect", "timeout")
	})
}

func TestSendTestWith_ErrorHasNoPassword(t *testing.T) {
	srv, cli := testTLS(t)
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg, f.rejectAuth = srv, true })
	m := testMailer(f, "starttls")
	m.tlsConfig = cli
	m.username, m.password = "user", "SECRET-PW"
	err := sendTestWith(context.Background(), testConfig(), m, fixedNow)
	wantSendError(t, err, "auth", "server replied 535")
	// The fake echoes the credentials in its 535 line, like a careless server.
	if strings.Contains(err.Error(), "SECRET-PW") {
		t.Errorf("password in error: %v", err)
	}
}

func TestSendTestWith_ErrorHasNoServerText(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.rcptCode = "550 secret-user@example.org not allowed" })
	err := sendTestWith(context.Background(), testConfig(), testMailer(f, "none"), fixedNow)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, bad := range []string{"secret-user", "not allowed"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("server text %q in error: %v", bad, err)
		}
	}
}

// noDialConfig returns a config whose server fails the test if anything
// connects to it.
func noDialConfig(t *testing.T) Config {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		if _, err := ln.Accept(); err == nil {
			t.Error("SendTest dialed despite a refused configuration")
		}
	}()
	cfg := testConfig()
	cfg.Host, cfg.Port, cfg.TLS = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port, "none"
	return cfg
}

func TestSendTest_PasswordRules(t *testing.T) {
	cfg := noDialConfig(t)

	cfg.PasswordFile = writePW(t, "pw\n", 0o644)
	if err := SendTest(context.Background(), cfg, fixedNow); err == nil || !strings.Contains(err.Error(), "accessible to group or others") {
		t.Errorf("open mode: %v", err)
	}
	cfg.PasswordFile += ".missing"
	if err := SendTest(context.Background(), cfg, fixedNow); err == nil || !strings.Contains(err.Error(), "cannot open") {
		t.Errorf("missing file: %v", err)
	}
	// Let a wrongly dialing SendTest be caught by the accept goroutine.
	time.Sleep(100 * time.Millisecond)
}

func TestSendTest_InvalidAddress(t *testing.T) {
	cfg := noDialConfig(t)
	cfg.From = "not an address"
	err := SendTest(context.Background(), cfg, fixedNow)
	if err == nil || err.Error() != "notify: invalid address in configuration" {
		t.Errorf("err = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
}

func TestBuildTestMessage_HostileHostname(t *testing.T) {
	c := testConfig()
	c.Hostname = "box\r\nBcc: evil@example.org"
	m, err := buildTestMessage(c, msgTime)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(m.data, []byte("\r\nBcc:")) {
		t.Errorf("injected header:\n%s", m.data)
	}
	pm, err := mail.ReadMessage(bytes.NewReader(m.data))
	if err != nil {
		t.Fatal(err)
	}
	if s := pm.Header.Get("Subject"); strings.ContainsAny(s, "\r\n") || s != "[gitops-agent] box Bcc: evil@example.org: test notification" {
		t.Errorf("Subject = %q", s)
	}
}
