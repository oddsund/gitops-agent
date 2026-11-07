package notify

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func bufLogger() (*slog.Logger, *syncBuf) {
	var b syncBuf
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), &b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func obsOf(states ...State) Observation { return Observation{Commit: "abcdef123456", States: states} }

func TestService_EndToEnd(t *testing.T) {
	// testTLS is called once per fake and once per service; both certs are
	// valid for 127.0.0.1 but the service trusts only its own, so share one.
	srv, cli := testTLS(t)
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg = srv })
	c := &clock{t0}
	cfg := testConfig()
	cfg.Host, cfg.Port, cfg.MinInterval = "127.0.0.1", f.port(), time.Hour
	m := newMailer(cfg, "pw")
	m.username, m.tlsConfig = "user", cli
	log, buf := bufLogger()
	s := newService(cfg, log, c.now, m, m.redact)
	defer s.Close(2 * time.Second)

	s.Observe(obsOf(svc("a", true)))
	waitFor(t, "first mail", func() bool { return len(f.messages()) == 1 })
	if !strings.Contains(f.messages()[0].data, "Subject: [gitops-agent] box1: a is failing") {
		t.Errorf("data = %q", f.messages()[0].data)
	}
	s.Observe(obsOf(svc("a", true)))
	s.Observe(obsOf(svc("a", true)))
	s.Observe(obsOf(svc("a", false)))
	waitFor(t, "recovery mail", func() bool { return len(f.messages()) == 2 })
	if !strings.Contains(f.messages()[1].data, "a recovered") {
		t.Errorf("data = %q", f.messages()[1].data)
	}
	// Three flips in one cycle are one mail.
	s.Observe(obsOf(svc("x", true), svc("y", true), svc("z", true)))
	waitFor(t, "batched mail", func() bool { return len(f.messages()) == 3 })
	time.Sleep(50 * time.Millisecond)
	if n := len(f.messages()); n != 3 {
		t.Errorf("messages = %d, want 3", n)
	}
	if !strings.Contains(f.messages()[2].data, "3 failing") {
		t.Errorf("data = %q", f.messages()[2].data)
	}
	if !strings.Contains(buf.String(), "notification sent") {
		t.Errorf("log = %s", buf.String())
	}
}

func TestService_RecoveryNeedsPriorFailureMail(t *testing.T) {
	f := newFakeSMTP(t, nil)
	cfg := testConfig()
	cfg.Host, cfg.Port, cfg.TLS, cfg.MinInterval = "127.0.0.1", f.port(), "none", time.Hour
	m := newMailer(cfg, "")
	log, _ := bufLogger()
	s := newService(cfg, log, (&clock{t0}).now, m, m.redact)
	s.Observe(obsOf(svc("a", false)))
	s.Observe(obsOf(svc("a", false)))
	s.Close(2 * time.Second)
	if n := len(f.messages()); n != 0 {
		t.Errorf("messages = %d, want 0", n)
	}
}

func TestService_PasswordAndServerTextNeverLogged(t *testing.T) {
	srv, cli := testTLS(t)
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg, f.rejectAuth = srv, true })
	cfg := testConfig()
	cfg.Host, cfg.Port, cfg.MinInterval = "127.0.0.1", f.port(), time.Hour
	m := newMailer(cfg, "pw-distinct-123")
	m.username, m.tlsConfig = "user-distinct", cli
	log, buf := bufLogger()
	s := newService(cfg, log, (&clock{t0}).now, m, m.redact)
	s.Observe(obsOf(svc("a", true)))
	s.Close(3 * time.Second)

	out := buf.String()
	if !strings.Contains(out, "notification not sent") || !strings.Contains(out, "stage=auth") || !strings.Contains(out, `"server replied 535"`) {
		t.Fatalf("log = %s", out)
	}
	for _, bad := range []string{"pw-distinct-123", "user-distinct", "credentials rejected", "5.7.8", "example.org"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q:\n%s", bad, out)
		}
	}
}

// blockingSender blocks every Send until released.
type blockingSender struct {
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (b *blockingSender) Send(ctx context.Context, from string, to []string, msg []byte) error {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

func TestService_ObserveNeverBlocksAndQueueIsBounded(t *testing.T) {
	bs := &blockingSender{release: make(chan struct{})}
	log, buf := bufLogger()
	cfg := testConfig()
	s := newService(cfg, log, (&clock{t0}).now, bs, func(s string) string { return s })
	defer s.Close(time.Second)
	defer close(bs.release)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			s.Observe(obsOf(svc("svc"+string(rune('a'+i)), true)))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Observe blocked on a stuck sender")
	}
	if !strings.Contains(buf.String(), "send queue full") {
		t.Errorf("no queue-full warning:\n%s", buf.String())
	}
}

type panicSender struct {
	mu    sync.Mutex
	calls int
	ok    chan struct{}
}

func (p *panicSender) Send(ctx context.Context, from string, to []string, msg []byte) error {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		panic("boom in sender")
	}
	close(p.ok)
	return nil
}

func TestService_WorkerSurvivesPanic(t *testing.T) {
	ps := &panicSender{ok: make(chan struct{})}
	log, buf := bufLogger()
	s := newService(testConfig(), log, (&clock{t0}).now, ps, func(s string) string { return s })
	defer s.Close(time.Second)
	s.Observe(obsOf(svc("a", true)))
	s.Observe(obsOf(svc("b", true)))
	select {
	case <-ps.ok:
	case <-time.After(2 * time.Second):
		t.Fatal("worker died after a panic; second mail never sent")
	}
	if !strings.Contains(buf.String(), "panic while sending") {
		t.Errorf("log = %s", buf.String())
	}
}

func TestService_CloseFlushesQueuedMail(t *testing.T) {
	f := newFakeSMTP(t, nil)
	cfg := testConfig()
	cfg.Host, cfg.Port, cfg.TLS, cfg.MinInterval = "127.0.0.1", f.port(), "none", time.Hour
	m := newMailer(cfg, "")
	log, _ := bufLogger()
	s := newService(cfg, log, (&clock{t0}).now, m, m.redact)
	s.Observe(obsOf(svc("a", true)))
	s.Close(3 * time.Second)
	if n := len(f.messages()); n != 1 {
		t.Errorf("messages after Close = %d, want 1", n)
	}
	s.Close(time.Second) // idempotent
	s.Observe(obsOf(svc("b", true)))
}

func TestService_CloseWithHungServerIsBounded(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.hangAt = "greeting" })
	cfg := testConfig()
	cfg.Host, cfg.Port, cfg.TLS, cfg.MinInterval = "127.0.0.1", f.port(), "none", time.Hour
	m := newMailer(cfg, "")
	m.totalTimeout = 30 * time.Second
	log, _ := bufLogger()
	s := newService(cfg, log, (&clock{t0}).now, m, m.redact)
	s.Observe(obsOf(svc("a", true)))
	<-f.accepted
	start := time.Now()
	s.Close(200 * time.Millisecond)
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("Close took %v", d)
	}
}

func TestNew_BadPasswordFile(t *testing.T) {
	cfg := testConfig()
	cfg.Username = "u"
	cfg.PasswordFile = writePW(t, "pw", 0o644)
	if s, err := New(cfg, nil, nil); err == nil || s != nil {
		t.Fatalf("New = %v, %v", s, err)
	}
	cfg.PasswordFile = writePW(t, "pw\n", 0o600)
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close(time.Second)
}
