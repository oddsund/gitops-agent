package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Vars rather than consts so tests can lower them.
var (
	maxAssetBytes int64 = 200 << 20
	maxMetaBytes  int64 = 1 << 20
)

const errBodyBytes = 256

// clientWithSafeRedirects returns a shallow copy of c that never carries
// credentials to another host or downgrades from https. net/http compares
// hostnames without the port when deciding to forward Authorization, so a
// redirect to another port on the same host would keep the token.
func clientWithSafeRedirects(c *http.Client) *http.Client {
	cp := *c
	orig := c.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect from https to %s", req.URL.Scheme)
		}
		if orig != nil {
			return orig(req, via)
		}
		return nil
	}
	return &cp
}

func doGet(ctx context.Context, c *http.Client, url, accept, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request: %w", err)
	}
	req.Header.Set("Accept", accept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := clientWithSafeRedirects(c).Do(req)
	if err != nil {
		return nil, fmt.Errorf("error while calling %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyBytes))
		resp.Body.Close()
		msg := fmt.Sprintf("got status %d when calling %s, expected 200", resp.StatusCode, url)
		if s := strings.TrimSpace(string(snippet)); s != "" {
			msg += ": " + s
		}
		return nil, errors.New(msg)
	}
	return resp, nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response larger than %d bytes", limit)
	}
	return b, nil
}

// fetchFile deliberately sends no token: release downloads are public and
// are often redirected to a CDN.
func fetchFile(ctx context.Context, c *http.Client, url string, dst io.Writer, limit int64) error {
	resp, err := doGet(ctx, c, url, "application/octet-stream", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("error while reading file from %s: %w", url, err)
	}
	if n > limit {
		return fmt.Errorf("%s is larger than the %d byte limit", url, limit)
	}
	return nil
}
