package gitlab

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab-search-go/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestClient(t *testing.T, srvURL string, tweak func(*config.Config)) *Client {
	t.Helper()
	proto, host, err := config.ParseDomain(srvURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Protocol: proto, Domain: host, Token: "t",
		Concurrency: 4, RetryCount: 5,
	}
	if tweak != nil {
		tweak(cfg)
	}
	c, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	c.RetryLadder = []time.Duration{time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond}
	return c
}

func TestRetryLadderEscalates(t *testing.T) {
	c := &Client{}
	steps := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 45 * time.Second, time.Minute, time.Minute}
	for attempt, want := range steps {
		d := c.backoff(attempt)
		if d < want || d > time.Duration(float64(want)*1.2) {
			t.Errorf("backoff(%d) = %v, want in [%v, %v]", attempt, d, want, time.Duration(float64(want)*1.2))
		}
	}
	// a flat server hint below the ladder top (GitLab sends Retry-After: 60) must not flatten the waits
	if c.ladderCap() != time.Minute {
		t.Errorf("ladderCap() = %v, want 1m", c.ladderCap())
	}
}

func TestGetRetries429WithRetryAfter(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	body, _, err := c.Get(context.Background(), c.BaseURL+"/x")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" || reqs != 2 {
		t.Fatalf("body=%q reqs=%d", body, reqs)
	}
	if r, ok := parseRetryAfter("5"); !ok || r != 5*time.Second {
		t.Fatalf("Retry-After seconds parse: %v %v", r, ok)
	}
}

func TestGetDoesNotRetry401(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, _, err := c.Get(context.Background(), c.BaseURL+"/x")
	if reqs != 1 {
		t.Fatalf("expected 1 request, got %d", reqs)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 401 {
		t.Fatalf("want StatusError 401, got %v", err)
	}
}

func TestGetExhaustsRetryCount(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := func(c *config.Config) { c.RetryCount = 2 }
	c := newTestClient(t, srv.URL, cfg)
	_, _, err := c.Get(context.Background(), c.BaseURL+"/x")
	if reqs != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", reqs)
	}
	var ee *ExhaustedError
	if !errors.As(err, &ee) {
		t.Fatalf("want ExhaustedError, got %v", err)
	}
}

func TestRateLimitThrottles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, func(cfg *config.Config) { cfg.RateLimit = 20 })
	start := time.Now()
	for i := 0; i < 6; i++ {
		if _, _, err := c.Get(context.Background(), c.BaseURL+"/x"); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("rate limit not applied, 6 requests at 20/s took %v", elapsed)
	}
}

func TestTLSOptions(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	ctx := context.Background()

	// default: verification fails against self-signed server
	if _, _, err := newTestClient(t, srv.URL, nil).Get(ctx, srv.URL+"/x"); err == nil {
		t.Fatal("expected TLS verification error by default")
	}

	// ignore_ssl: passes
	c := newTestClient(t, srv.URL, func(cfg *config.Config) { cfg.IgnoreSSL = true })
	if _, _, err := c.Get(ctx, srv.URL+"/x"); err != nil {
		t.Fatalf("ignore_ssl should pass: %v", err)
	}

	// ca_file with server cert: passes with verification enabled
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(certPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c = newTestClient(t, srv.URL, func(cfg *config.Config) { cfg.CAFile = certPath })
	if _, _, err := c.Get(ctx, srv.URL+"/x"); err != nil {
		t.Fatalf("ca_file should pass: %v", err)
	}

	// broken ca_file: fatal configuration error
	badPath := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(badPath, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(&config.Config{Protocol: "https", Domain: "x", Token: "t", Concurrency: 1, CAFile: badPath}, discardLogger()); err == nil {
		t.Fatal("expected error for invalid CA file")
	}
}
