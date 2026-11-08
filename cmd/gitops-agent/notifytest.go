package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/notify"
)

func init() { subcommands["notify-test"] = runNotifyTest }

func runNotifyTest(args []string) error {
	err := notifyTest(args, os.Stdout, os.Stderr, notifyTestDeps{
		load:     config.Load,
		send:     notify.SendTest,
		hostname: os.Hostname,
		euid:     os.Geteuid,
	})
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

type notifyTestDeps struct {
	load     func(path string) (*config.AgentConfig, error)
	send     func(ctx context.Context, cfg notify.Config, now func() time.Time) error
	hostname func() (string, error)
	euid     func() int
}

// ownerErrorFragment is the text in notify.LoadPassword's owner error that
// the hint below keys on. A test in internal/notify pins it.
const ownerErrorFragment = " is owned by uid "

// The service user is whatever `gitops-agent install` wrote as User= into
// the unit (--user or $SUDO_USER), so no name is hard-coded here. Reading it
// from systemd also honours drop-in overrides.
const notifyTestCommand = `sudo -u "$(systemctl show -p User --value gitops-agent)" gitops-agent notify-test`

const notifyTestUserHint = `run this command as the agent's service user, for example: ` + notifyTestCommand

func notifyTest(args []string, stdout, stderr io.Writer, d notifyTestDeps) error {
	flagSet := flag.NewFlagSet("notify-test", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	flagSet.Usage = func() {}
	path := flagSet.String("config", "/etc/gitops-agent/config.toml", "path to config.toml")

	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stderr, `usage: gitops-agent notify-test [-config PATH]

Send one test email through the [notify] settings in config.toml and report
whether the mail server accepted it. Nothing is deployed.

Run it as the user the agent runs as (the unit's User=), for example:
  `+notifyTestCommand+`
The password file must belong to that user and have mode 0600, the same
rules the agent applies at startup. The agent never runs as root, so
notify-test refuses to run as root.
`)
			flagSet.SetOutput(stderr)
			flagSet.PrintDefaults()
		}
		return err
	}
	if flagSet.NArg() > 0 {
		return errors.New("notify-test takes no arguments")
	}

	// The daemon never runs as root, so a root run proves nothing about it.
	if d.euid() == 0 {
		return errors.New("notify-test must not run as root; " + notifyTestUserHint)
	}

	cfg, err := d.load(*path)
	if err != nil {
		return err
	}
	if cfg.Notify.SMTPHost == "" {
		return fmt.Errorf("no notifications configured: [notify] smtp_host is empty in %s", *path)
	}
	host, err := d.hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	nc := notifyConfig(cfg.Notify, host)

	// Printed before connecting, so a hang can be attributed. Recipient
	// addresses are deliberately not printed.
	fmt.Fprintf(stdout, "notify-test: sending to %d recipient(s) via %s:%d (tls=%s)\n",
		len(nc.To), nc.Host, nc.Port, nc.TLS)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := d.send(ctx, nc, time.Now); err != nil {
		msg := fmt.Sprintf("test email not sent: %v", err)
		if nc.PasswordFile != "" && strings.Contains(err.Error(), ownerErrorFragment) {
			msg += "; " + notifyTestUserHint
		}
		return errors.New(msg)
	}
	fmt.Fprintln(stdout, "notify-test: OK, the mail server accepted the test message")
	return nil
}
