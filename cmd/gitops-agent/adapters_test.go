package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/notify"
)

func TestNotifyConfig(t *testing.T) {
	got := notifyConfig(config.NotifyConfig{
		SMTPHost: "mail.example.org", SMTPPort: 587, TLS: "starttls",
		From: "a@example.org", To: []string{"b@example.org"},
		Username: "u", PasswordFile: "/pw", MinIntervalSeconds: 90,
		IncludeError: true, StatusURL: "https://s.example.org/",
	}, "box1")
	want := notify.Config{
		Host: "mail.example.org", Port: 587, TLS: "starttls",
		From: "a@example.org", To: []string{"b@example.org"},
		Username: "u", PasswordFile: "/pw", MinInterval: 90 * time.Second,
		IncludeError: true, StatusURL: "https://s.example.org/", Hostname: "box1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}
