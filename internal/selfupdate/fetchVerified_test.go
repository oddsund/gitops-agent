package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFetchVerified_HappyPath(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sha256") {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("0177b9a9f8f856d74996c8e894388e35fa406f2972003326de5711a7cfd5b2a1 test-asset"))
		} else {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("Testfil"))
		}
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		APIBaseURL:      "https://api.test.com",
		AssetName:       "test-asset",
		DownloadBaseURL: "https://test.com",
		Repo:            "test/repo",
		HTTPClient:      server.Client(),
	}

	tmpDir := t.TempDir()

	res, err := fetchVerified(config, "", tmpDir)

	if err != nil {
		t.Fatalf("Expected no error for fetching file, got %v", err)
	}

	content, err := os.ReadFile(res)

	if err != nil {
		t.Fatalf("Couldn't read file created by fetchVerified: %v", err)
	}

	if string(content[:]) != "Testfil" {
		t.Fatalf("Expected content to be \"Testfil\", but got %s", string(content[:100]))
	}

}

func TestFetchVerified_ShasumMismatch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sha256") {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b856 test-asset"))
		} else {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("Testfil"))
		}
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		APIBaseURL:      "https://api.test.com",
		AssetName:       "test-asset",
		DownloadBaseURL: "https://test.com",
		Repo:            "test/repo",
		HTTPClient:      server.Client(),
	}

	tmpDir := t.TempDir()

	_, err := fetchVerified(config, "", tmpDir)

	if err == nil {
		t.Fatalf("Couldn't read file created by fetchVerified: %v", err)
	}

	if !(strings.Contains(err.Error(), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b856")) {
		t.Fatalf("Expected error to contain sha256 from upstream")
	}

	if !(strings.Contains(err.Error(), "0177b9a9f8f856d74996c8e894388e35fa406f2972003326de5711a7cfd5b2a1")) {
		t.Fatalf("Expected error to contain computed sha256 of assetfile from upstream")
	}

	verifyNoFilesInDir(t, tmpDir)

}

func TestFetchVerified_MissingShasum(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sha256") {
			http.Error(w, "Not found", 404)
		} else {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("Testfil"))
		}
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		APIBaseURL:      "https://api.test.com",
		AssetName:       "test-asset",
		DownloadBaseURL: "https://test.com",
		Repo:            "test/repo",
		HTTPClient:      server.Client(),
	}

	tmpDir := t.TempDir()

	_, err := fetchVerified(config, "", tmpDir)

	if err == nil {
		t.Fatalf("Couldn't read file created by fetchVerified: %v", err)
	}

	if !(strings.Contains(err.Error(), "error while fetching test-asset.sha256 file for test-asset, refusing to install unverified binary")) {
		t.Fatalf("Expected error to say that shasum was missing, but said %v", err)
	}

	verifyNoFilesInDir(t, tmpDir)

}

func TestFetchVerified_MissingAsset(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not found", 404)
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		APIBaseURL:      "https://api.test.com",
		AssetName:       "test-asset",
		DownloadBaseURL: "https://test.com",
		Repo:            "test/repo",
		HTTPClient:      server.Client(),
	}

	tmpDir := t.TempDir()

	_, err := fetchVerified(config, "", tmpDir)

	if err == nil {
		t.Fatalf("Couldn't read file created by fetchVerified: %v", err)
	}

	if !(strings.Contains(err.Error(), "error while fetching asset file")) {
		t.Fatalf("Expected error to say that shasum was missing, but said %v", err)
	}

	verifyNoFilesInDir(t, tmpDir)

}

func TestFetchVerified_InvalidChecksumfile(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sha256") {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85test-asset"))
		} else {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("Testfil"))
		}
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		APIBaseURL:      "https://api.test.com",
		AssetName:       "test-asset",
		DownloadBaseURL: "https://test.com",
		Repo:            "test/repo",
		HTTPClient:      server.Client(),
	}

	tmpDir := t.TempDir()

	_, err := fetchVerified(config, "", tmpDir)

	if err == nil {
		t.Fatalf("Couldn't read file created by fetchVerified: %v", err)
	}

	if !(strings.Contains(err.Error(), "unparsable shafile for asset - wrong content format")) {
		t.Fatalf("Expected error to say \"unparsable shafile for asset - wrong content format\", said %v", err)
	}

	verifyNoFilesInDir(t, tmpDir)

}

func TestFetchVerified_ChecksumTooLong(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sha256") {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("0177b9a9f8f856d74996c8e894388e35fa406f2972003326de5711a7cfd5b2a1t test-asset"))
		} else {
			w.Header().Add("content-type", "application/octet-stream")
			w.Write([]byte("Testfil"))
		}
	})
	server := httptest.NewTestServer(t, handler)

	config := Config{
		APIBaseURL:      "https://api.test.com",
		AssetName:       "test-asset",
		DownloadBaseURL: "https://test.com",
		Repo:            "test/repo",
		HTTPClient:      server.Client(),
	}

	tmpDir := t.TempDir()

	_, err := fetchVerified(config, "", tmpDir)

	if err == nil {
		t.Fatalf("Couldn't read file created by fetchVerified: %v", err)
	}

	if !(strings.Contains(err.Error(), "unparsable shafile for asset - digest looks wrong")) {
		t.Fatalf("Expected error to say \"unparsable shafile for asset - digest looks wrong\", said %v", err)
	}

	verifyNoFilesInDir(t, tmpDir)
}

func verifyNoFilesInDir(t *testing.T, dir string) {
	entries, err := os.ReadDir(dir)

	if err != nil {
		t.Fatalf("Should be able to read tmp dir for test")
	}

	if len(entries) != 0 {
		t.Fatalf("Dir should be empty as nothing should be installed on mismatched sha256")
	}
}
