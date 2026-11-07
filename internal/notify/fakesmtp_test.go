package notify

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type received struct {
	from  string
	rcpts []string
	data  string
}

// fakeSMTP is a loopback-only SMTP server for tests. It advertises AUTH
// only after TLS, like real servers.
type fakeSMTP struct {
	ln         net.Listener
	tlsCfg     *tls.Config // nil: no STARTTLS offered
	implicit   bool        // wrap accepted conns in TLS immediately
	hangAt     string      // "", "greeting", "ehlo", "data"
	rejectAuth bool
	rcptCode   string // e.g. "550 no such user"; empty accepts

	mu        sync.Mutex
	got       []received
	authLines []string
	cmds      []string
	accepted  chan struct{}
	conns     []net.Conn
}

// testTLS returns a server config and a client config that trusts it. The
// certificate comes from httptest and is valid for 127.0.0.1.
func testTLS(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	ts := httptest.NewUnstartedServer(http.NotFoundHandler())
	ts.StartTLS()
	t.Cleanup(ts.Close)
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	return &tls.Config{Certificates: ts.TLS.Certificates}, &tls.Config{RootCAs: pool}
}

func newFakeSMTP(t *testing.T, configure func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, accepted: make(chan struct{}, 64)}
	if configure != nil {
		configure(f)
	}
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		for _, c := range f.conns {
			c.Close()
		}
		f.mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			f.accepted <- struct{}{}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) record(verb string) {
	f.mu.Lock()
	f.cmds = append(f.cmds, verb)
	f.mu.Unlock()
}

func (f *fakeSMTP) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeSMTP) messages() []received {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received(nil), f.got...)
}

func (f *fakeSMTP) hasCmd(verb string) bool {
	for _, c := range f.commands() {
		if c == verb {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve(raw net.Conn) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	var conn net.Conn = raw
	upgraded := false
	if f.implicit {
		conn = tls.Server(raw, f.tlsCfg)
		upgraded = true
	}
	if f.hangAt == "greeting" {
		time.Sleep(3 * time.Second)
		return
	}
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	var cur received
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		f.record(verb)
		switch verb {
		case "EHLO", "HELO":
			if f.hangAt == "ehlo" {
				time.Sleep(3 * time.Second)
				return
			}
			say("250-fake")
			if f.tlsCfg != nil && !upgraded {
				say("250-STARTTLS")
			}
			if upgraded {
				say("250 AUTH PLAIN")
			} else {
				say("250 SIZE 1000000")
			}
		case "STARTTLS":
			say("220 go ahead")
			conn = tls.Server(raw, f.tlsCfg)
			r = bufio.NewReader(conn)
			say = func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
			upgraded = true
		case "AUTH":
			parts := strings.Fields(line)
			dec := ""
			if len(parts) == 3 {
				b, _ := base64.StdEncoding.DecodeString(parts[2])
				dec = string(b)
			}
			f.mu.Lock()
			f.authLines = append(f.authLines, dec)
			f.mu.Unlock()
			if f.rejectAuth {
				// Echo the credentials the way a careless server might.
				say("535 5.7.8 credentials rejected for " + strings.ReplaceAll(dec, "\x00", " "))
			} else {
				say("235 ok")
			}
		case "MAIL":
			cur = received{from: between(line)}
			say("250 ok")
		case "RCPT":
			if f.rcptCode != "" {
				say(f.rcptCode)
				continue
			}
			cur.rcpts = append(cur.rcpts, between(line))
			say("250 ok")
		case "DATA":
			say("354 go")
			if f.hangAt == "data" {
				time.Sleep(3 * time.Second)
				return
			}
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				l = strings.TrimPrefix(l, ".") // un-dot-stuff
				b.WriteString(l)
			}
			cur.data = b.String()
			f.mu.Lock()
			f.got = append(f.got, cur)
			f.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 unknown")
		}
	}
}

func between(s string) string {
	i, j := strings.Index(s, "<"), strings.LastIndex(s, ">")
	if i < 0 || j < i {
		return ""
	}
	return s[i+1 : j]
}
