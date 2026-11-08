package notify

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type message struct {
	from   string
	to     []string
	data   []byte
	events int // how many events the mail reports, for the log
}

const (
	maxNameRunes    = 64
	maxStageRunes   = 16
	maxHostRunes    = 63
	maxErrRunes     = 200
	maxSubjectRunes = 120
	maxURLRunes     = 2048
)

// cleanText makes s safe to put on one line of a mail, header or body. Service
// names come from the repository and error text from docker, so both are
// attacker-influenced. Control characters (CR and LF included) and the Unicode
// line and paragraph separators become spaces, space runs collapse, invalid
// UTF-8 becomes U+FFFD, and the result is cut to at most max runes, ending in
// "..." when cut.
func cleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max <= 3 {
		return string([]rune(s)[:max])
	}
	return string([]rune(s)[:max-3]) + "..."
}

// header formats one header line. It is the last line of defence against
// header injection: a CR or LF in value is an error, so a bug in the
// cleaning above fails closed (no mail) instead of letting a service name
// add a Bcc header.
func header(name, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("header %s contains a line break", name)
	}
	return name + ": " + value + "\r\n", nil
}

func displayName(k Key) string {
	if k.Kind == KindSync {
		return "git sync"
	}
	n := cleanText(k.Name, maxNameRunes)
	if n == "" {
		return "(unnamed)"
	}
	return n
}

func sortedEvents(evs []Event) []Event {
	out := append([]Event(nil), evs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Key, out[j].Key
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return out
}

func subject(c Config, evs []Event) string {
	host := cleanText(c.Hostname, maxHostRunes)
	var failing, recovered int
	var first Event
	for i, e := range evs {
		if i == 0 {
			first = e
		}
		if e.Recovered {
			recovered++
		} else {
			failing++
		}
	}
	var what string
	switch {
	case len(evs) == 1 && first.Recovered:
		what = displayName(first.Key) + " recovered"
	case len(evs) == 1:
		what = displayName(first.Key) + " is failing"
	default:
		var parts []string
		if failing > 0 {
			parts = append(parts, fmt.Sprintf("%d failing", failing))
		}
		if recovered > 0 {
			parts = append(parts, fmt.Sprintf("%d recovered", recovered))
		}
		what = strings.Join(parts, ", ")
	}
	subj := "[gitops-agent] "
	if host != "" {
		subj += host + ": "
	}
	return cleanText(subj+what, maxSubjectRunes)
}

func body(c Config, obs Observation, evs []Event, now time.Time) string {
	var b strings.Builder
	host := cleanText(c.Hostname, maxHostRunes)
	fmt.Fprintf(&b, "gitops-agent on %s reports a change.\n", host)

	section := func(title string, recovered bool) {
		started := false
		for _, e := range evs {
			if e.Recovered != recovered {
				continue
			}
			if !started {
				fmt.Fprintf(&b, "\n%s\n", title)
				started = true
			}
			b.WriteString("  " + displayName(e.Key))
			if !recovered {
				if st := cleanText(e.Stage, maxStageRunes); st != "" {
					fmt.Fprintf(&b, "  (stage: %s)", st)
				}
			}
			b.WriteString("\n")
			if !recovered && c.IncludeError {
				line, _, _ := strings.Cut(e.Err, "\n")
				line, _, _ = strings.Cut(line, "\r")
				if line = cleanText(line, maxErrRunes); line != "" {
					fmt.Fprintf(&b, "      error: %s\n", line)
				}
			}
		}
	}
	section("FAILING", false)
	section("RECOVERED", true)

	b.WriteString("\n")
	if obs.Commit != "" {
		fmt.Fprintf(&b, "Commit:  %s\n", cleanText(obs.Commit, 12))
	}
	fmt.Fprintf(&b, "Time:    %s\n", now.UTC().Format(time.RFC3339))
	if c.StatusURL != "" {
		fmt.Fprintf(&b, "Status:  %s\n", cleanText(c.StatusURL, maxURLRunes))
	}
	b.WriteString("\nYou get one message when a service starts failing and one when it\n" +
		"recovers. Nothing is sent while it stays broken. Logs:\n" +
		"  journalctl -u gitops-agent\n")
	return b.String()
}

// hostChars keeps only characters that are legal in a host name.
func hostChars(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return -1
	}, s)
}

func messageID(c Config, now time.Time) string {
	domain := ""
	if i := strings.LastIndex(c.From, "@"); i >= 0 {
		domain = hostChars(strings.TrimRight(c.From[i+1:], ">"))
	}
	if domain == "" {
		domain = hostChars(c.Hostname)
	}
	if domain == "" {
		domain = "localhost"
	}
	id := fmt.Sprintf("%d", now.UnixNano())
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err == nil {
		id += "." + hex.EncodeToString(rnd[:])
	}
	return "<" + id + "@" + domain + ">"
}

// buildMessage renders one mail for the events of a cycle. It is pure apart
// from the random Message-ID.
func buildMessage(c Config, obs Observation, evs []Event, now time.Time) (*message, error) {
	evs = sortedEvents(evs)
	return render(c, subject(c, evs), body(c, obs, evs, now), now, len(evs))
}

// buildTestMessage renders the mail that notify-test sends.
func buildTestMessage(c Config, now time.Time) (*message, error) {
	host := cleanText(c.Hostname, maxHostRunes)
	subj := "[gitops-agent] "
	if host != "" {
		subj += host + ": "
	}
	subj = cleanText(subj+"test notification", maxSubjectRunes)

	var b strings.Builder
	fmt.Fprintf(&b, "This is a test message from gitops-agent on %s.\n\n", host)
	fmt.Fprintf(&b, "If you can read this, the [notify] settings in config.toml work:\n")
	fmt.Fprintf(&b, "the server accepted the message from %s. No service is failing.\n\n", host)
	fmt.Fprintf(&b, "Time:    %s\n", now.UTC().Format(time.RFC3339))
	if c.StatusURL != "" {
		fmt.Fprintf(&b, "Status:  %s\n", cleanText(c.StatusURL, maxURLRunes))
	}
	b.WriteString("\nYou asked for this message with: gitops-agent notify-test\n")
	return render(c, subj, b.String(), now, 0)
}

// render parses the addresses and writes the headers shared by every mail
// this package sends, so the header block exists once.
func render(c Config, subj, text string, now time.Time, events int) (*message, error) {
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return nil, fmt.Errorf("from address: %w", err)
	}
	var tos []string
	var bare []string
	for _, t := range c.To {
		a, err := mail.ParseAddress(t)
		if err != nil {
			return nil, fmt.Errorf("to address: %w", err)
		}
		tos = append(tos, a.String())
		bare = append(bare, a.Address)
	}

	var b strings.Builder
	for _, h := range [][2]string{
		{"From", from.String()},
		{"To", strings.Join(tos, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", subj)},
		{"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", messageID(c, now)},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "8bit"},
		{"Auto-Submitted", "auto-generated"},
	} {
		line, err := header(h[0], h[1])
		if err != nil {
			return nil, err
		}
		b.WriteString(line)
	}
	b.WriteString("\r\n")
	b.WriteString(text)
	return &message{from: from.Address, to: bare, data: []byte(b.String()), events: events}, nil
}
