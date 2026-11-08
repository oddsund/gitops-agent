// Package notify sends plain email when a service starts failing or
// recovers. It is deliberately small and standard-library only.
//
// Two rules shape everything here. First, a mail problem must never cost a
// deploy: Observe does no network I/O, sending runs on one background
// worker, and panics are contained. Second, the git repository is the trust
// boundary: service names and error text come from it (or from docker), so
// they are cleaned before they reach any header, and the destination, the
// credentials and the server come only from the host's config.toml.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"sync"
	"time"
)

// Kind says what a Key refers to.
type Kind int

const (
	KindSync    Kind = iota // the git sync step, as a whole
	KindService             // one service
)

// Key identifies something that can fail. A struct rather than strings so
// a service literally named "git sync" cannot collide with the sync step.
type Key struct {
	Kind Kind
	Name string // service name; empty for KindSync
}

// State is one key's health as of the cycle that just finished.
type State struct {
	Key     Key
	Failing bool
	Stage   string // sync, decrypt, deploy, teardown; may be empty
	Err     string // raw error text; only used when IncludeError is set
}

// Observation is everything Service needs after one cycle.
type Observation struct {
	Commit string  // 12-char short commit, may be empty
	States []State // sync first, then services that have been attempted
}

// Config is what the daemon passes in. Plain values: notify does not import
// internal/config.
type Config struct {
	Host         string
	Port         int
	TLS          string // "starttls", "implicit", "none"
	From         string
	To           []string
	Username     string
	PasswordFile string
	MinInterval  time.Duration
	IncludeError bool
	StatusURL    string
	Hostname     string // this machine, for Subject, Message-ID and EHLO
}

// String and LogValue keep an accidental "cfg", cfg out of the log.
func (Config) String() string         { return "notify.Config{redacted}" }
func (c Config) LogValue() slog.Value { return slog.StringValue(c.String()) }

// sender is what Service needs from Mailer; tests substitute their own.
type sender interface {
	Send(ctx context.Context, from string, to []string, msg []byte) error
}

const queueSize = 8

// Service is the notifier the daemon uses.
type Service struct {
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	sender sender
	redact func(string) string

	mu     sync.Mutex // guards det and closed, and serialises sends on queue
	det    *Detector
	closed bool
	queue  chan *message

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (s *Service) String() string { return "notify.Service{redacted}" }

// New builds a Service and starts its worker. An error means notifications
// cannot work (unreadable or too-open password file, bad address); the
// caller decides what to do about it.
func New(cfg Config, log *slog.Logger, now func() time.Time) (*Service, error) {
	m, err := mailerFor(cfg)
	if err != nil {
		return nil, err
	}
	return newService(cfg, log, now, m, m.redact), nil
}

// mailerFor applies the checks every sender must pass (addresses, then the
// password file) and builds the Mailer. New and SendTest both call it, so
// the notify-test command cannot report success for a setup the daemon
// would refuse.
func mailerFor(cfg Config) (*Mailer, error) {
	for _, a := range append([]string{cfg.From}, cfg.To...) {
		if _, err := mail.ParseAddress(a); err != nil {
			return nil, fmt.Errorf("notify: invalid address in configuration")
		}
	}
	password := ""
	if cfg.PasswordFile != "" {
		var err error
		if password, err = LoadPassword(cfg.PasswordFile); err != nil {
			return nil, err
		}
	}
	return newMailer(cfg, password), nil
}

func newService(cfg Config, log *slog.Logger, now func() time.Time, snd sender, redact func(string) string) *Service {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		cfg:    cfg,
		log:    log,
		now:    now,
		sender: snd,
		redact: redact,
		det:    NewDetector(cfg.MinInterval, now),
		queue:  make(chan *message, queueSize),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go s.worker()
	return s
}

// Observe records the outcome of one cycle and queues a mail if something
// changed. It never blocks on the network and never panics outward.
func (s *Service) Observe(obs Observation) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("notify: panic in Observe", "panic", fmt.Sprint(r))
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	events, held := s.det.Step(obs.States)
	if held > 0 {
		s.log.Debug("failure notification held back by min_interval", "held", held)
	}
	if len(events) == 0 {
		return
	}
	msg, err := buildMessage(s.cfg, obs, events, s.now())
	if err != nil {
		s.log.Warn("notification not built", "reason", err.Error())
		return
	}
	// Non-blocking: a slow mail server must never stall the reconcile loop.
	// The queue is held under mu, so Close cannot close it mid-send.
	select {
	case s.queue <- msg:
	default:
		s.log.Warn("notification dropped: send queue full", "queued", cap(s.queue))
	}
}

func (s *Service) worker() {
	defer close(s.done)
	for msg := range s.queue {
		s.sendOne(msg)
	}
}

// sendOne wraps a single iteration, not the loop, so a panic loses one mail
// and the worker keeps running.
func (s *Service) sendOne(msg *message) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("notify: panic while sending", "panic", s.redact(fmt.Sprint(r)))
		}
	}()
	err := s.sender.Send(s.ctx, msg.from, msg.to, msg.data)
	if err != nil {
		stage, reason := "send", "failed"
		if se, ok := err.(*sendError); ok {
			stage, reason = se.stage, se.reason
		}
		s.log.Warn("notification not sent", "stage", stage, "reason", s.redact(reason),
			"host", s.cfg.Host, "port", s.cfg.Port, "events", msg.events)
		return
	}
	s.log.Info("notification sent", "events", msg.events, "recipients", len(msg.to))
}

// Close stops accepting observations, gives queued mail up to wait to go
// out, then aborts anything still in flight. It is idempotent.
func (s *Service) Close(wait time.Duration) {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.queue)
		s.mu.Unlock()

		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-s.done:
			s.cancel()
			return
		case <-t.C:
		}
		s.cancel()
		t2 := time.NewTimer(time.Second)
		defer t2.Stop()
		select {
		case <-s.done:
		case <-t2.C:
		}
	})
}
