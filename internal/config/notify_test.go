package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const notifyBase = `
[git]
repo_url = "git@example.com:o/r.git"
branch = "main"
clone_path = "/c"
pull_interval_seconds = 300
[sops]
ssh_key_path = "/k"
`

func loadInline(t *testing.T, extra string) (*AgentConfig, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(notifyBase+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestLoad_NotifyAbsentOrEmptyIsOff(t *testing.T) {
	for name, extra := range map[string]string{
		"absent":      "",
		"empty host":  "[notify]\nsmtp_host = \"\"\nto = [\"garbage\"]\n",
		"only to set": "[notify]\nto = [\"not an address\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadInline(t, extra)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			n := cfg.Notify
			if n.SMTPHost != "" || n.TLS != "" || n.SMTPPort != 0 || n.MinIntervalSeconds != 0 {
				t.Errorf("defaults applied to a disabled section: %+v", n)
			}
		})
	}
}

func TestLoad_NotifyDefaults(t *testing.T) {
	tests := []struct {
		name, host, extra string
		tls               string
		port              int
	}{
		{"minimal", "mail.example.org", "", "starttls", 587},
		{"implicit", "mail.example.org", "tls = \"implicit\"\n", "implicit", 465},
		{"none on localhost", "localhost", "tls = \"none\"\n", "none", 25},
		{"explicit port", "mail.example.org", "smtp_port = 2525\n", "starttls", 2525},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadInline(t, "[notify]\nsmtp_host = \""+tc.host+"\"\nfrom = \"a@example.org\"\nto = [\"b@example.org\"]\n"+tc.extra)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			n := cfg.Notify
			if n.TLS != tc.tls || n.SMTPPort != tc.port || n.MinIntervalSeconds != 3600 || n.IncludeError {
				t.Errorf("got %+v", n)
			}
		})
	}
}

func TestLoad_NotifyInvalid(t *testing.T) {
	hdr := "[notify]\nfrom = \"a@example.org\"\nto = [\"b@example.org\"]\n"
	tests := []struct {
		name, toml, want string
	}{
		{"bad tls", hdr + "smtp_host = \"h.example.org\"\ntls = \"ssl\"\n", "notify.tls"},
		{"port too big", hdr + "smtp_host = \"h.example.org\"\nsmtp_port = 70000\n", "notify.smtp_port"},
		{"port in host", hdr + "smtp_host = \"mail.example.org:587\"\n", "bare host name"},
		{"url in host", hdr + "smtp_host = \"smtp://h.example.org\"\n", "bare host name"},
		{"none on remote", hdr + "smtp_host = \"mail.example.org\"\ntls = \"none\"\n", "loopback"},
		{"username with none", hdr + "smtp_host = \"127.0.0.1\"\ntls = \"none\"\nusername = \"u\"\npassword_file = \"/p\"\n", "never sends a password in clear text"},
		{"username only", hdr + "smtp_host = \"h.example.org\"\nusername = \"u\"\n", "set together"},
		{"password_file only", hdr + "smtp_host = \"h.example.org\"\npassword_file = \"/p\"\n", "set together"},
		{"relative password_file", hdr + "smtp_host = \"h.example.org\"\nusername = \"u\"\npassword_file = \"p\"\n", "absolute"},
		{"bad from", "[notify]\nsmtp_host = \"h.example.org\"\nfrom = \"nope\"\nto = [\"b@example.org\"]\n", "notify.from"},
		{"empty to", "[notify]\nsmtp_host = \"h.example.org\"\nfrom = \"a@example.org\"\nto = []\n", "at least one"},
		{"too many to", "[notify]\nsmtp_host = \"h.example.org\"\nfrom = \"a@example.org\"\nto = [" + strings.Repeat("\"x@example.org\",", 11) + "]\n", "limit"},
		{"crlf in to", "[notify]\nsmtp_host = \"h.example.org\"\nfrom = \"a@example.org\"\nto = [\"ok@example.org\", \"a@b\\r\\nBcc: x@y\"]\n", "notify.to[1]"},
		{"negative interval", hdr + "smtp_host = \"h.example.org\"\nmin_interval_seconds = -1\n", "negative"},
		{"ftp status url", hdr + "smtp_host = \"h.example.org\"\nstatus_url = \"ftp://x\"\n", "status_url"},
		{"credentials in status url", hdr + "smtp_host = \"h.example.org\"\nstatus_url = \"https://u:p@h/\"\n", "user name or password"},
		{"javascript status url", hdr + "smtp_host = \"h.example.org\"\nstatus_url = \"javascript:1\"\n", "status_url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadInline(t, tc.toml)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestLoad_NotifyValidFull(t *testing.T) {
	_, err := loadInline(t, `[notify]
smtp_host = "::1"
tls = "none"
from = "GitOps <a@example.org>"
to = ["b@example.org"]
status_url = "https://status.example.org/"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}
