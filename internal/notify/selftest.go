package notify

import (
	"context"
	"time"
)

// SendTest sends one test message with the same validation and the same
// Mailer the daemon uses, and blocks until the server accepts or refuses it.
// The returned error is safe to print: it is a *sendError (fixed stage and
// reason), a LoadPassword error (path only), or the fixed address error.
func SendTest(ctx context.Context, cfg Config, now func() time.Time) error {
	m, err := mailerFor(cfg)
	if err != nil {
		return err
	}
	return sendTestWith(ctx, cfg, m, now)
}

// sendTestWith takes the sender as a parameter so tests can trust the fake
// server's certificate.
func sendTestWith(ctx context.Context, cfg Config, snd sender, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	msg, err := buildTestMessage(cfg, now())
	if err != nil {
		return err
	}
	// The Mailer has its own 10s dial and 30s total limits; this is a backstop.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Not wrapped: a *sendError already reads "smtp <stage>: <reason>".
	return snd.Send(ctx, msg.from, msg.to, msg.data)
}
