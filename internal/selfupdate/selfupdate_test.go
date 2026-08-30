package selfupdate

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHappyPath(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{\"tag_name\": \"testVersion\"}")
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		HTTPClient: server.Client(),
	}

	tag, err := latestReleaseTag(config)
	if err != nil {
		t.Fatalf("latestReleaseTag should work with default config")
	}

	if tag != "testVersion" {
		t.Fatalf("latestReleaseTag should fetch tag from testserver, got %s", tag)
	}
}

func TestNon200(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not found", 404)
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		HTTPClient: server.Client(),
		Repo:       "test/repo",
		APIBaseURL: "https://api.test.com",
	}

	_, err := latestReleaseTag(config)
	if err == nil {
		t.Fatalf("latestReleaseTag should return error on non-200 response")
	}

	if !(strings.Contains(err.Error(), "https://api.test.com") || strings.Contains(err.Error(), "test/repo")) {
		t.Fatalf("error should specify url(%s) and repo(%s), but only said: %v", config.APIBaseURL, config.Repo, err)
	}

	if !strings.Contains(err.Error(), "200") {
		t.Fatalf("error should mention valid response code, but only said %v", err)
	}
}

func TestMissingTag(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{\"missing\": \"tag\"}")
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		HTTPClient: server.Client(),
		Repo:       "test/repo",
		APIBaseURL: "https://api.test.com",
	}

	_, err := latestReleaseTag(config)
	if err == nil {
		t.Fatalf("latestReleaseTag should return error on missing tag_name in response")
	}

	if !(strings.Contains(err.Error(), "https://api.test.com") || strings.Contains(err.Error(), "test/repo")) {
		t.Fatalf("error should specify url(%s) and repo(%s), but only said: %v", config.APIBaseURL, config.Repo, err)
	}

	if !strings.Contains(err.Error(), "tag_name") {
		t.Fatalf("error should mention tag_name as error, but only said %v", err)
	}
}

func TestEmptyTag(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{\"tag_name\": \"\"}")
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		HTTPClient: server.Client(),
		Repo:       "test/repo",
		APIBaseURL: "https://api.test.com",
	}

	_, err := latestReleaseTag(config)
	if err == nil {
		t.Fatalf("latestReleaseTag should return error on missing tag_name in response")
	}

	if !(strings.Contains(err.Error(), "https://api.test.com") || strings.Contains(err.Error(), "test/repo")) {
		t.Fatalf("error should specify url(%s) and repo(%s), but only said: %v", config.APIBaseURL, config.Repo, err)
	}

	if !strings.Contains(err.Error(), "tag_name") {
		t.Fatalf("error should mention tag_name as error, but only said %v", err)
	}
}

func TestAuthHeaderSetWhenConfigured(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer BearerToken" {
			t.Fatalf("Authorization header set correct, got %s", r.Header.Get("Authorization"))
		}
		io.WriteString(w, "{\"tag_name\": \"\"}")
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		HTTPClient: server.Client(),
		Repo:       "test/repo",
		APIBaseURL: "https://api.test.com",
		Token:      "BearerToken",
	}

	_, err := latestReleaseTag(config)
	if err == nil {
		t.Fatalf("latestReleaseTag should return error for this test")
	}
}

func TestNoAuthHeaderSetWhenNotConfigured(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Get("Authorization")) > 0 {
			t.Fatalf("Authorization header should not be set, got %s", r.Header.Get("Authorization"))
		}
		io.WriteString(w, "{\"tag_name\": \"\"}")
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		HTTPClient: server.Client(),
		Repo:       "test/repo",
		APIBaseURL: "https://api.test.com",
	}

	_, err := latestReleaseTag(config)
	if err == nil {
		t.Fatalf("latestReleaseTag should return error for this test")
	}
}
