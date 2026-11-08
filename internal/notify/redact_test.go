package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oddsund/gitops-agent/internal/deploy"
)

const redactCanary = "s3cr3t-Canary-9f2a1b"

// TestService_ComposeErrorTextIsSafeInMail closes the email leg of the
// chain: a deploy error built by the real deploy.Runner, with the canary on
// docker's stderr, must reach the SMTP server only as the fixed summary.
func TestService_ComposeErrorTextIsSafeInMail(t *testing.T) {
	secrets := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(filepath.Join(secrets, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "demo", "secrets.env"), []byte("API_KEY="+redactCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serviceDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubDir := t.TempDir()
	script := "#!/bin/sh\necho 'error parsing env_file: unexpected \"API_KEY=" + redactCanary + "\"' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+":/usr/bin:/bin")

	derr := deploy.Runner{SecretsDir: secrets}.Deploy(serviceDir)
	if derr == nil {
		t.Fatal("want deploy error")
	}

	srv, cli := testTLS(t)
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.tlsCfg = srv })
	c := &clock{t0}
	cfg := testConfig()
	cfg.Host, cfg.Port, cfg.MinInterval = "127.0.0.1", f.port(), time.Hour
	cfg.IncludeError = true
	m := newMailer(cfg, "pw")
	m.username, m.tlsConfig = "user", cli
	log, _ := bufLogger()
	s := newService(cfg, log, c.now, m, m.redact)
	defer s.Close(2 * time.Second)

	s.Observe(obsOf(State{Key: Key{KindService, "demo"}, Failing: true, Stage: "deploy", Err: derr.Error()}))
	waitFor(t, "failure mail", func() bool { return len(f.messages()) == 1 })

	data := f.messages()[0].data
	if strings.Contains(data, redactCanary) {
		t.Errorf("mail leaks the canary:\n%s", data)
	}
	if want := "docker compose up failed with exit code 1; see the journal"; !strings.Contains(data, want) {
		t.Errorf("mail lacks %q:\n%s", want, data)
	}
}
