package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/notify"
)

type ntRun struct {
	loadPath string
	sent     bool
	got      notify.Config
}

func ntConfig() *config.AgentConfig {
	return &config.AgentConfig{Notify: config.NotifyConfig{
		SMTPHost: "mail.example.org", SMTPPort: 587, TLS: "starttls",
		From:         "agent@example.org",
		To:           []string{"alice@example.org", "bob@example.org"},
		Username:     "bob",
		PasswordFile: "/etc/gitops-agent/smtp-password",
	}}
}

func ntDeps(cfg *config.AgentConfig, loadErr, sendErr error, euid int) (notifyTestDeps, *ntRun) {
	r := &ntRun{}
	return notifyTestDeps{
		load: func(p string) (*config.AgentConfig, error) {
			r.loadPath = p
			return cfg, loadErr
		},
		send: func(_ context.Context, c notify.Config, _ func() time.Time) error {
			r.sent, r.got = true, c
			return sendErr
		},
		hostname: func() (string, error) { return "box1", nil },
		euid:     func() int { return euid },
	}, r
}

func TestNotifyTest_OK(t *testing.T) {
	d, r := ntDeps(ntConfig(), nil, nil, 1000)
	var out, errb bytes.Buffer
	if err := notifyTest(nil, &out, &errb, d); err != nil {
		t.Fatal(err)
	}
	want := "notify-test: sending to 2 recipient(s) via mail.example.org:587 (tls=starttls)\n" +
		"notify-test: OK, the mail server accepted the test message\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if r.loadPath != "/etc/gitops-agent/config.toml" {
		t.Errorf("loaded %q", r.loadPath)
	}
	g := r.got
	if g.Hostname != "box1" || g.Port != 587 || g.Host != "mail.example.org" || g.From != "agent@example.org" ||
		len(g.To) != 2 || g.PasswordFile != "/etc/gitops-agent/smtp-password" {
		t.Errorf("config passed to send = %+v", g)
	}
}

func TestNotifyTest_NotConfigured(t *testing.T) {
	cfg := ntConfig()
	cfg.Notify.SMTPHost = ""
	d, r := ntDeps(cfg, nil, nil, 1000)
	err := notifyTest([]string{"-config", "/x/c.toml"}, new(bytes.Buffer), new(bytes.Buffer), d)
	if err == nil || !strings.Contains(err.Error(), "smtp_host is empty") || !strings.Contains(err.Error(), "/x/c.toml") {
		t.Errorf("err = %v", err)
	}
	if r.sent {
		t.Error("send called")
	}
}

func TestNotifyTest_ConfigError(t *testing.T) {
	want := errors.New("decoding config /c: boom")
	d, r := ntDeps(nil, want, nil, 1000)
	if err := notifyTest(nil, new(bytes.Buffer), new(bytes.Buffer), d); err != want {
		t.Errorf("err = %v, want %v", err, want)
	}
	if r.sent {
		t.Error("send called")
	}
}

func TestNotifyTest_SendFailure(t *testing.T) {
	d, _ := ntDeps(ntConfig(), nil, errors.New("smtp auth: server replied 535"), 1000)
	var out bytes.Buffer
	err := notifyTest(nil, &out, new(bytes.Buffer), d)
	if err == nil || err.Error() != "test email not sent: smtp auth: server replied 535" {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(out.String(), "OK") {
		t.Errorf("stdout claims success: %q", out.String())
	}
}

// The password itself is covered in internal/notify
// (TestSendTestWith_ErrorHasNoPassword); this guards what the command prints.
func TestNotifyTest_NeverPrintsAddressesOrUsername(t *testing.T) {
	d, _ := ntDeps(ntConfig(), nil, errors.New("x"), 1000)
	var out, errb bytes.Buffer
	err := notifyTest(nil, &out, &errb, d)
	all := out.String() + errb.String() + err.Error()
	for _, bad := range []string{"alice@example.org", "bob@example.org", "agent@example.org"} {
		if strings.Contains(all, bad) {
			t.Errorf("output contains %q: %q", bad, all)
		}
	}
	// The username is not a secret by itself but is not needed either.
	if strings.Contains(all, "bob") {
		t.Errorf("output contains the username: %q", all)
	}
}

func TestNotifyTest_Flags(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		d, r := ntDeps(ntConfig(), nil, nil, 1000)
		var errb bytes.Buffer
		err := notifyTest([]string{"-h"}, new(bytes.Buffer), &errb, d)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("err = %v, want flag.ErrHelp (runNotifyTest turns it into exit 0)", err)
		}
		for _, want := range []string{"usage: gitops-agent notify-test", "-config", "the unit's User=", "refuses to run as root", "systemctl show -p User --value gitops-agent"} {
			if !strings.Contains(errb.String(), want) {
				t.Errorf("help lacks %q:\n%s", want, errb.String())
			}
		}
		if strings.Contains(errb.String(), "sudo -u gitops-agent") {
			t.Errorf("help hard-codes a user name:\n%s", errb.String())
		}
		if r.sent {
			t.Error("send called")
		}
	})
	t.Run("config path", func(t *testing.T) {
		d, r := ntDeps(ntConfig(), nil, nil, 1000)
		if err := notifyTest([]string{"--config", "/x"}, new(bytes.Buffer), new(bytes.Buffer), d); err != nil {
			t.Fatal(err)
		}
		if r.loadPath != "/x" {
			t.Errorf("loadPath = %q", r.loadPath)
		}
	})
	t.Run("positional", func(t *testing.T) {
		d, _ := ntDeps(ntConfig(), nil, nil, 1000)
		err := notifyTest([]string{"extra"}, new(bytes.Buffer), new(bytes.Buffer), d)
		if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unknown flag", func(t *testing.T) {
		d, _ := ntDeps(ntConfig(), nil, nil, 1000)
		var errb bytes.Buffer
		if err := notifyTest([]string{"-bogus"}, new(bytes.Buffer), &errb, d); err == nil {
			t.Error("want an error")
		}
		if errb.Len() != 0 {
			t.Errorf("stderr = %q, want empty (dispatch prints the error once)", errb.String())
		}
	})
}

func TestNotifyTest_RefusesRoot(t *testing.T) {
	d, r := ntDeps(ntConfig(), nil, nil, 0)
	var out bytes.Buffer
	err := notifyTest(nil, &out, new(bytes.Buffer), d)
	if err == nil || !strings.Contains(err.Error(), "must not run as root") || !strings.HasSuffix(err.Error(), notifyTestCommand) {
		t.Errorf("err = %v, want a root refusal with the command", err)
	}
	if strings.Contains(err.Error(), "sudo -u gitops-agent") {
		t.Errorf("hard-coded user name: %v", err)
	}
	if r.loadPath != "" || r.sent || out.Len() != 0 {
		t.Errorf("root run went on: load=%q sent=%v stdout=%q", r.loadPath, r.sent, out.String())
	}
}

func TestNotifyTest_OwnerHint(t *testing.T) {
	owner := errors.New("password_file /p is owned by uid 1000, not the agent's uid 1001; run chown")
	d, _ := ntDeps(ntConfig(), nil, owner, 1001)
	err := notifyTest(nil, new(bytes.Buffer), new(bytes.Buffer), d)
	if err == nil || !strings.HasSuffix(err.Error(), notifyTestUserHint) {
		t.Errorf("err = %v, want the service-user hint", err)
	}

	d, _ = ntDeps(ntConfig(), nil, errors.New("smtp auth: server replied 535"), 1001)
	err = notifyTest(nil, new(bytes.Buffer), new(bytes.Buffer), d)
	if err == nil || strings.Contains(err.Error(), "service user") {
		t.Errorf("unrelated error: err = %v, want no hint", err)
	}
}
