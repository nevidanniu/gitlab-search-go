package gitlab

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"gitlab-search-go/internal/config"
)

type Client struct {
	BaseURL    string
	token      string
	http       *http.Client
	sem        chan struct{}
	limiter    *rate.Limiter
	retryCount int
	log        *slog.Logger

	// RetryLadder is the wait before each repeat; the last step is reused for further retries.
	RetryLadder []time.Duration
}

// DefaultRetryLadder waits 5s, 15s, 30s, 45s, 60s and then keeps 60s between repeats.
var DefaultRetryLadder = []time.Duration{
	5 * time.Second, 15 * time.Second, 30 * time.Second, 45 * time.Second, time.Minute,
}

// StatusError is a non-retryable HTTP error response.
type StatusError struct {
	URL    string
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	hint := ""
	switch e.Status {
	case http.StatusUnauthorized:
		hint = " (personal access token is invalid or lacks the read_api scope)"
	case http.StatusForbidden:
		hint = " (token does not grant access to this resource)"
	case http.StatusNotFound:
		hint = " (check the configured API domain)"
	}
	return fmt.Sprintf("GET %s: HTTP %d%s", e.URL, e.Status, hint)
}

// ExhaustedError means the request kept failing after all configured retries.
type ExhaustedError struct {
	URL      string
	Attempts int
	Last     string
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("GET %s: giving up after %d attempts, last error: %s", e.URL, e.Attempts, e.Last)
}

func New(cfg *config.Config, log *slog.Logger) (*Client, error) {
	transport := &http.Transport{
		MaxIdleConns:        cfg.Concurrency,
		MaxIdleConnsPerHost: cfg.Concurrency,
	}
	tlsCfg := &tls.Config{}
	if cfg.IgnoreSSL {
		tlsCfg.InsecureSkipVerify = true
	} else if cfg.CAFile != "" {
		pool, err := loadCAPool(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}
	transport.TLSClientConfig = tlsCfg

	c := &Client{
		BaseURL:     fmt.Sprintf("%s://%s/api/v4", cfg.Protocol, cfg.Domain),
		token:       cfg.Token,
		http:        &http.Client{Transport: transport, Timeout: 90 * time.Second},
		sem:         make(chan struct{}, cfg.Concurrency),
		retryCount:  cfg.RetryCount,
		log:         log,
		RetryLadder: DefaultRetryLadder,
	}
	if cfg.RateLimit > 0 {
		c.limiter = rate.NewLimiter(rate.Limit(cfg.RateLimit), 1)
	}
	return c, nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read CA file %s: %w", path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("CA file %s contains no valid PEM certificates", path)
	}
	return pool, nil
}

// Get performs a GET with retry/backoff and returns the full body and headers.
func (c *Client) Get(ctx context.Context, url string) ([]byte, http.Header, error) {
	var last string
	var delay time.Duration
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, delay); err != nil {
				return nil, nil, err
			}
		}
		if err := c.acquire(ctx); err != nil {
			return nil, nil, err
		}
		if c.limiter != nil {
			if err := c.limiter.Wait(ctx); err != nil {
				c.release()
				return nil, nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			c.release()
			return nil, nil, err
		}
		req.Header.Set("Private-Token", c.token)
		c.log.Debug("Requesting", "method", "GET", "url", url)

		resp, err := c.http.Do(req)
		if err != nil {
			c.release()
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			last = err.Error()
			if attempt >= c.retryCount {
				return nil, nil, &ExhaustedError{URL: url, Attempts: attempt + 1, Last: last}
			}
			delay = c.backoff(attempt)
			c.log.Warn("request failed, retrying", "url", url, "attempt", attempt+1, "max_retries", c.retryCount, "error", last, "retry_in", delay)
			continue
		}

		if retryable(resp.StatusCode) {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			c.release()
			last = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet(body))
			if attempt >= c.retryCount {
				return nil, nil, &ExhaustedError{URL: url, Attempts: attempt + 1, Last: last}
			}
			delay = c.backoff(attempt)
			if resp.StatusCode == http.StatusTooManyRequests {
				// Retry-After is honoured only when it asks for longer than the whole ladder:
				// GitLab answers every rate-limited request with a flat 60s, which would
				// otherwise replace the escalating waits with the same delay on each retry.
				if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok && d > c.ladderCap() {
					delay = d
				}
			}
			c.log.Warn("retryable status, retrying", "url", url, "status", resp.StatusCode, "attempt", attempt+1, "max_retries", c.retryCount, "retry_in", delay)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.release()
		if err != nil {
			last = err.Error()
			if attempt >= c.retryCount {
				return nil, nil, &ExhaustedError{URL: url, Attempts: attempt + 1, Last: last}
			}
			delay = c.backoff(attempt)
			c.log.Warn("body read failed, retrying", "url", url, "attempt", attempt+1, "error", last, "retry_in", delay)
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, nil, &StatusError{URL: url, Status: resp.StatusCode, Body: snippet(body)}
		}
		return body, resp.Header, nil
	}
}

func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) release() { <-c.sem }

func (c *Client) ladder() []time.Duration {
	if len(c.RetryLadder) == 0 {
		return DefaultRetryLadder
	}
	return c.RetryLadder
}

// backoff returns the wait before the retry that follows attempt already failed tries.
func (c *Client) backoff(attempt int) time.Duration {
	ladder := c.ladder()
	d := ladder[len(ladder)-1]
	if attempt < len(ladder) {
		d = ladder[attempt]
	}
	// Jitter only adds time, so a ladder step stays a lower bound and workers do not synchronize on it.
	return time.Duration(float64(d) * (1 + 0.2*rand.Float64()))
}

func (c *Client) ladderCap() time.Duration {
	ladder := c.ladder()
	return ladder[len(ladder)-1]
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func snippet(body []byte) string {
	s := string(body)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
