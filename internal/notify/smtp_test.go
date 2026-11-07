package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

const testMsg = "Subject: hi\r\n\r\nbody line\n.hidden\nlast\n"

func testMailer(f *fakeSMTP, mode string) *Mailer {
	return &Mailer{
		host: "127.0.0.1", hostname: "box1", port: f.port(), mode: mode,
		dialTimeout: 2 * time.Second, totalTimeout: 5 * time.Second,
	}
}

func send(m *Mailer) error {
	return m.Send(context.Background(), "from@example.org", []string{"a@example.org", "b@example.org"}, []byte(testMsg))
}

func wantSendError(t *testing.T, err error, stage, reason string) {
	t.Helper()
	var se *sendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *sendError", err)
	}
	if se.stage != stage || se.reason != reason {
		t.Errorf("got stage=%q reason=%q, want stage=%q reason=%q", se.stage, se.reason, stage, reason)
	}
}

func cmdIndex(cmds []string, verb string) int {
	for i, c := range cmds {
		if c == verb {
			return i
		}
	}
	return -1
}

func checkDelivered(t *testing.T, f *fakeSMTP) {
	t.Helper()
	msgs := f.messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.from != "from@example.org" || len(m.rcpts) != 2 || m.rcpts[0] != "a@example.org" || m.rcpts[1] != "b@example.org" {
		t.Errorf("envelope = %q %q", m.from, m.rcpts)
	}
	want := "Subject: hi\r\n\r\nbody line\r\n.hidden\r\nlast\r\n"
	if m.data != want {
		t.Errorf("data = %q, want %q", m.data, want)
	}
}

func TestSend_STARTTLSWithAuth(t *testing.T) {
	srv, cli := testTLS(t)
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg = srv })
	m := testMailer(f, "starttls")
	m.tlsConfig = cli
	m.username, m.password = "user", "pass"
	if err := send(m); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, f)
	if len(f.authLines) != 1 || f.authLines[0] != "\x00user\x00pass" {
		t.Errorf("auth = %q", f.authLines)
	}
	cmds := f.commands()
	if s, a := cmdIndex(cmds, "STARTTLS"), cmdIndex(cmds, "AUTH"); s < 0 || a < s {
		t.Errorf("commands = %v", cmds)
	}
}

func TestSend_ImplicitTLSWithAuth(t *testing.T) {
	srv, cli := testTLS(t)
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg, f.implicit = srv, true })
	m := testMailer(f, "implicit")
	m.tlsConfig = cli
	m.username, m.password = "user", "pass"
	if err := send(m); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, f)
	if f.hasCmd("STARTTLS") || len(f.authLines) != 1 {
		t.Errorf("commands = %v auth = %q", f.commands(), f.authLines)
	}
}

func TestSend_NoneOnLoopbackWithoutLogin(t *testing.T) {
	f := newFakeSMTP(t, nil)
	if err := send(testMailer(f, "none")); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, f)
}

func TestSend_NeverDowngradesFromSTARTTLS(t *testing.T) {
	f := newFakeSMTP(t, nil) // does not offer STARTTLS
	m := testMailer(f, "starttls")
	m.username, m.password = "user", "pass"
	wantSendError(t, send(m), "starttls", "server does not offer STARTTLS")
	if f.hasCmd("AUTH") || f.hasCmd("MAIL") {
		t.Errorf("commands = %v", f.commands())
	}
}

func TestSend_RefusesAuthWithoutTLS(t *testing.T) {
	f := newFakeSMTP(t, nil)
	// Built directly, bypassing config validation.
	m := testMailer(f, "none")
	m.username, m.password = "u", "p"
	wantSendError(t, send(m), "auth", "refusing to authenticate without TLS")
	if f.hasCmd("AUTH") {
		t.Errorf("AUTH sent in clear text: %v", f.commands())
	}
}

func TestSend_UntrustedCertificate(t *testing.T) {
	srv, _ := testTLS(t)
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg = srv })
	err := send(testMailer(f, "starttls")) // no RootCAs
	wantSendError(t, err, "starttls", "certificate verification failed")
	if len(f.messages()) != 0 {
		t.Error("message delivered over an unverified connection")
	}
}

func TestSend_Timeouts(t *testing.T) {
	for _, hang := range []string{"greeting", "ehlo", "data"} {
		t.Run(hang, func(t *testing.T) {
			f := newFakeSMTP(t, func(f *fakeSMTP) { f.hangAt = hang })
			m := testMailer(f, "none")
			m.dialTimeout, m.totalTimeout = 300*time.Millisecond, 300*time.Millisecond
			done := make(chan error, 1)
			go func() { done <- send(m) }()
			select {
			case err := <-done:
				wantSendError(t, err, wantStageFor(hang), "timeout")
			case <-time.After(2 * time.Second):
				t.Fatal("Send did not honour its deadline")
			}
		})
	}
}

func wantStageFor(hang string) string {
	switch hang {
	case "greeting":
		return "connect"
	case "ehlo":
		return "connect"
	}
	return "data"
}

func TestSend_ConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	m := &Mailer{host: "127.0.0.1", port: port, mode: "none", dialTimeout: time.Second, totalTimeout: time.Second}
	wantSendError(t, send(m), "connect", "connection refused")
}

func TestSend_ContextCancelAbortsHungSend(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.hangAt = "greeting" })
	m := testMailer(f, "none")
	m.totalTimeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := m.Send(ctx, "from@example.org", []string{"a@example.org"}, []byte(testMsg))
	wantSendError(t, err, "connect", "cancelled (shutting down)")
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestSend_RcptRejectedDropsServerText(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.rcptCode = "550 no such user a@example.org" })
	err := send(testMailer(f, "none"))
	wantSendError(t, err, "rcpt", "server replied 550")
	if strings.Contains(err.Error(), "no such user") || strings.Contains(err.Error(), "example.org") {
		t.Errorf("server text in error: %v", err)
	}
}

func TestMailer_PasswordNeverFormatted(t *testing.T) {
	m := &Mailer{password: "hunter2-distinct", username: "u"}
	svc := &Service{}
	cfg := testConfig()
	cfg.PasswordFile = "/x"
	for _, v := range []any{m, cfg, svc} {
		out := fmt.Sprintf("%v %+v %#v", v, v, v)
		if strings.Contains(out, "hunter2-distinct") {
			t.Errorf("%T leaked the password: %s", v, out)
		}
	}
}
