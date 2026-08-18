package statusserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleHealthz_OK(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	tr.CycleComplete(nil)

	rr := httptest.NewRecorder()
	tr.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "ok") {
		t.Errorf("body = %q, want it to contain ok", rr.Body.String())
	}
}

func TestHandleHealthz_Unhealthy(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	tr.CycleComplete(errors.New("deploy failed"))

	rr := httptest.NewRecorder()
	tr.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "deploy failed") {
		t.Errorf("body = %q, want it to contain the error", rr.Body.String())
	}
}

func TestHandleHealthz_NeverRunYetIsHealthy(t *testing.T) {
	tr := NewTracker("dev", time.Now())

	rr := httptest.NewRecorder()
	tr.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 before the first cycle completes", rr.Code)
	}
}

func TestHandleStatusJSON(t *testing.T) {
	tr := NewTracker("v1.2.3", time.Now())
	tr.SeenService("demoapp", "services/demoapp", true)
	tr.SyncResult("deadbeef", nil)

	rr := httptest.NewRecorder()
	tr.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status.json", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var s Status
	if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
		t.Fatalf("body did not parse as JSON: %v", err)
	}
	if s.Version != "v1.2.3" {
		t.Errorf("Version = %q", s.Version)
	}
	if s.CommitHash != "deadbeef" {
		t.Errorf("CommitHash = %q", s.CommitHash)
	}
	if len(s.Services) != 1 || s.Services[0].Name != "demoapp" {
		t.Errorf("Services = %+v", s.Services)
	}
}

func TestHandleIndex_RendersWithoutLinks(t *testing.T) {
	tr := NewTracker("v1.2.3", time.Now())
	tr.SeenService("demoapp", "services/demoapp", true)
	tr.ServiceAttempt("demoapp")
	tr.ServiceResult("demoapp", errors.New("compose up failed: exit 1"))
	tr.CycleComplete(errors.New("compose up failed: exit 1"))

	rr := httptest.NewRecorder()
	tr.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(body, "demoapp") {
		t.Error("index page doesn't mention the service")
	}
	if !strings.Contains(body, "compose up failed") {
		t.Error("index page doesn't surface the error")
	}
	// The page is proxied behind a path-stripping handle_path, so it must
	// not emit any internal links -- see the Handler doc comment.
	if strings.Contains(body, "<a ") || strings.Contains(body, "href=") {
		t.Error("index page must not contain links, it's served behind a prefix-stripping proxy")
	}
	if strings.Contains(body, "<script") {
		t.Error("index page must not contain JavaScript")
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	if got := ago(time.Time{}, now); got != "never" {
		t.Errorf("ago(zero) = %q, want never", got)
	}
	if got := ago(now.Add(-30*time.Second), now); got != "just now" {
		t.Errorf("ago(30s ago) = %q, want just now", got)
	}
	if got := ago(now.Add(-90*time.Minute), now); !strings.HasSuffix(got, "ago") {
		t.Errorf("ago(90m ago) = %q, want it to end in 'ago'", got)
	}
}

func TestUntil(t *testing.T) {
	now := time.Now()
	if got := until(time.Time{}, now); got != "unknown" {
		t.Errorf("until(zero) = %q, want unknown", got)
	}
	if got := until(now.Add(-time.Minute), now); got != "any moment now" {
		t.Errorf("until(past) = %q, want 'any moment now'", got)
	}
}
