package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sendError is the only error Send returns. It carries a step and a reason
// from a fixed vocabulary and never copies library or server text: an SMTP
// reply can echo the user name or a recipient, and nothing here may put
// those in a log line.
type sendError struct{ stage, reason string }

func (e *sendError) Error() string { return "smtp " + e.stage + ": " + e.reason }

func classify(ctx context.Context, err error) string {
	var (
		tpErr   *textproto.Error
		netErr  net.Error
		unknown x509.UnknownAuthorityError
		hostErr x509.HostnameError
		invalid x509.CertificateInvalidError
		verify  *tls.CertificateVerificationError
	)
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled):
		return "cancelled (shutting down)"
	case errors.As(err, &tpErr):
		return fmt.Sprintf("server replied %d", tpErr.Code)
	case errors.As(err, &unknown), errors.As(err, &hostErr), errors.As(err, &invalid), errors.As(err, &verify):
		return "certificate verification failed"
	case errors.Is(err, context.Canceled):
		return "cancelled (shutting down)"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed by server"
	}
	return "failed"
}

// Mailer sends one message per Send over a fresh SMTP connection.
type Mailer struct {
	host, hostname string
	port           int
	mode           string // starttls | implicit | none
	username       string
	password       string // never logged, never in an error
	dialTimeout    time.Duration
	totalTimeout   time.Duration
	tlsConfig      *tls.Config // nil in production; tests set RootCAs
}

func newMailer(cfg Config, password string) *Mailer {
	return &Mailer{
		host:         cfg.Host,
		hostname:     cfg.Hostname,
		port:         cfg.Port,
		mode:         cfg.TLS,
		username:     cfg.Username,
		password:     password,
		dialTimeout:  10 * time.Second,
		totalTimeout: 30 * time.Second,
	}
}

// String, GoString and LogValue keep the password out of an accidental
// "%v", "%+v", "%#v" or slog attribute.
func (m *Mailer) String() string   { return "notify.Mailer{redacted}" }
func (m *Mailer) GoString() string { return m.String() }

func (m *Mailer) tlsCfg() *tls.Config {
	var c *tls.Config
	if m.tlsConfig != nil {
		c = m.tlsConfig.Clone()
	} else {
		c = &tls.Config{}
	}
	// No InsecureSkipVerify anywhere: certificate verification cannot be
	// turned off.
	c.ServerName = m.host
	c.MinVersion = tls.VersionTLS12
	return c
}

func (m *Mailer) redact(s string) string {
	if m.password == "" {
		return s
	}
	return strings.ReplaceAll(s, m.password, "[redacted]")
}

func heloName(hostname string) string {
	if n := hostChars(hostname); n != "" {
		return n
	}
	return "localhost"
}

// Send delivers msg. net/smtp has no deadlines or context, so the whole
// conversation runs under one absolute deadline set on the raw connection
// before the client reads the greeting. TLS wrappers sit on top of that
// socket, so the deadline keeps applying through the handshake.
func (m *Mailer) Send(ctx context.Context, from string, to []string, msg []byte) error {
	fail := func(stage string, err error) error {
		return &sendError{stage, classify(ctx, err)}
	}
	d := net.Dialer{Timeout: m.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(m.host, strconv.Itoa(m.port)))
	if err != nil {
		return fail("connect", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(m.totalTimeout)); err != nil {
		return fail("connect", err)
	}
	// Shutdown aborts a stuck read by closing the socket.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	var c *smtp.Client
	if m.mode == "implicit" {
		tc := tls.Client(conn, m.tlsCfg())
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail("tls", err)
		}
		c, err = smtp.NewClient(tc, m.host)
	} else {
		c, err = smtp.NewClient(conn, m.host)
	}
	if err != nil {
		return fail("connect", err)
	}
	defer c.Close()

	// Hello first, so net/smtp does not send its default "EHLO localhost".
	if err := c.Hello(heloName(m.hostname)); err != nil {
		return fail("connect", err)
	}
	if m.mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			// Never downgrade to clear text.
			return &sendError{"starttls", "server does not offer STARTTLS"}
		}
		if err := c.StartTLS(m.tlsCfg()); err != nil {
			return fail("starttls", err)
		}
	}
	if m.username != "" {
		// Defence in depth: config validation already forbids this, and
		// smtp.PlainAuth refuses non-TLS to anything but localhost.
		if _, active := c.TLSConnectionState(); !active {
			return &sendError{"auth", "refusing to authenticate without TLS"}
		}
		if err := c.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return fail("auth", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fail("mail", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fail("rcpt", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fail("data", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fail("data", err)
	}
	if err := w.Close(); err != nil {
		return fail("data", err)
	}
	// The message is accepted once the data terminator is acknowledged.
	_ = c.Quit()
	return nil
}
