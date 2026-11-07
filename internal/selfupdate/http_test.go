package selfupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func lowerLimits(t *testing.T, asset, meta int64) {
	t.Helper()
	oldAsset, oldMeta := maxAssetBytes, maxMetaBytes
	maxAssetBytes, maxMetaBytes = asset, meta
	t.Cleanup(func() { maxAssetBytes, maxMetaBytes = oldAsset, oldMeta })
}

func TestInvalidTag(t *testing.T) {
	for _, tag := range []string{"testVersion", "v1.2", "v1.2.3-rc1", "v1.2.3/../x", "../../etc", "v99999999999999999999.0.0"} {
		t.Run(tag, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"tag_name": "`+tag+`"}`)
			})
			server := httptest.NewTestServer(t, handler)

			_, err := latestReleaseTag(t.Context(), Config{HTTPClient: server.Client()})
			if err == nil || !strings.Contains(err.Error(), "invalid release tag") {
				t.Fatalf("err = %v, want invalid release tag", err)
			}
		})
	}
}

func TestNon200IncludesURLAndTruncatedBody(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited: "+strings.Repeat("x", 10<<10), 403)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	_, err := latestReleaseTag(t.Context(), Config{
		HTTPClient: server.Client(),
		Repo:       "test/repo",
		APIBaseURL: server.URL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{server.URL + "/repos/test/repo/releases/latest", "200", "403", "rate limited"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if len(err.Error()) >= 600 {
		t.Errorf("error is %d bytes, body should be truncated", len(err.Error()))
	}
}

func TestResponseTooLarge(t *testing.T) {
	lowerLimits(t, maxAssetBytes, 64)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tag_name": "v1.2.3", "pad": "`+strings.Repeat("x", 100)+`"}`)
	})
	server := httptest.NewTestServer(t, handler)

	_, err := latestReleaseTag(t.Context(), Config{HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendsTokenAndAccept(t *testing.T) {
	var auth, accept string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		io.WriteString(w, `{"tag_name": "v1.2.3"}`)
	})
	server := httptest.NewTestServer(t, handler)

	if _, err := latestReleaseTag(t.Context(), Config{HTTPClient: server.Client(), Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" || accept != "application/vnd.github+json" {
		t.Errorf("auth = %q, accept = %q", auth, accept)
	}

	if _, err := latestReleaseTag(t.Context(), Config{HTTPClient: server.Client()}); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("auth = %q, want none without a token", auth)
	}
}

func TestTokenStrippedOnCrossHostRedirect(t *testing.T) {
	var authB string
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authB = r.Header.Get("Authorization")
		io.WriteString(w, `{"tag_name": "v1.2.3"}`)
	}))
	defer b.Close()

	var authA string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authA = r.Header.Get("Authorization")
		http.Redirect(w, r, b.URL+r.URL.Path, http.StatusFound)
	}))
	defer a.Close()

	tag, err := latestReleaseTag(t.Context(), Config{HTTPClient: a.Client(), APIBaseURL: a.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.2.3" {
		t.Errorf("tag = %q", tag)
	}
	if authA != "Bearer tok" {
		t.Errorf("first hop auth = %q, want the token", authA)
	}
	if authB != "" {
		t.Errorf("redirect target (other port, same hostname) saw Authorization %q", authB)
	}
}

func TestRedirectDowngradeRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tag_name": "v1.2.3"}`)
	}))
	defer plain.Close()

	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer tls.Close()

	_, err := latestReleaseTag(t.Context(), Config{HTTPClient: tls.Client(), APIBaseURL: tls.URL})
	if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallerCheckRedirectStillRuns(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tag_name": "v1.2.3"}`)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()

	client := *srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("caller says no") }

	_, err := latestReleaseTag(t.Context(), Config{HTTPClient: &client, APIBaseURL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "caller says no") {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCanceled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := latestReleaseTag(ctx, Config{HTTPClient: server.Client(), APIBaseURL: server.URL})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestInvalidRepo(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	_, err := latestReleaseTag(t.Context(), Config{HTTPClient: server.Client(), APIBaseURL: server.URL, Repo: "a/b/../c"})
	if err == nil {
		t.Fatal("expected error")
	}
	if hits.Load() != 0 {
		t.Errorf("hits = %d, want 0", hits.Load())
	}
}
