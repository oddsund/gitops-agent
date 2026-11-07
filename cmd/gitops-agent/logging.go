package main

import (
	"io"
	"log/slog"
	"os"
)

// newLogger builds the daemon's logger. journal reports whether stderr is
// the systemd journal (JOURNAL_STREAM set).
//
// What must never reach a log line, as an attr or inside an error string:
// decrypted content or anything derived from it; an environment, a compose
// environment: block or os.Environ(); a URL with a password (pass repository
// URLs through gitsync.RedactURL); an SMTP password; a %+v of a struct that
// holds config or secret fields.
func newLogger(w io.Writer, level slog.Level, journal bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if journal {
		// journald timestamps every line itself; a second time=
		// attr is noise and can disagree by a few ms.
		opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// parseLevel turns the -log-level flag value into a slog.Level.
func parseLevel(s string) (slog.Level, error) {
	var lvl slog.Level
	err := lvl.UnmarshalText([]byte(s))
	return lvl, err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
