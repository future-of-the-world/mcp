// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	userAgent      = "mcp-web-search/1.0"
	defaultRetries = 2
	defaultTimeout = 10 * time.Second
	backoffBase    = 250 * time.Millisecond
	maxJitterMs    = 100
)

// FetchOptions configures an HTTP fetch request.
type FetchOptions struct {
	Method       string
	Headers      map[string]string
	Body         io.Reader
	Retries      int
	Timeout      time.Duration
	RedirectMode string
}

// isRetryableStatus reports whether the HTTP status code is a transient error
// worth retrying.
var errFetchRetriesExhausted = errors.New("fetch: exhausted retries")

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true

	default:
		return false
	}
}

// Fetch executes an HTTP request with exponential-backoff retry on transient
// failures. It applies the timeout from opts (or defaultTimeout), sets a
// User-Agent header, and retries on configured retryable status codes and
// network errors.
func Fetch(ctx context.Context, url string, opts FetchOptions) (*http.Response, error) {
	retries := opts.Retries
	if retries <= 0 {
		retries = defaultRetries
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	method := opts.Method
	if method == "" {
		method = http.MethodGet
	}

	cfg := fetchConfig{
		url:     url,
		opts:    opts,
		retries: retries,
		timeout: timeout,
		method:  method,
	}

	return fetchWithRetries(ctx, &cfg)
}

// fetchConfig holds the resolved parameters for a retry loop.
type fetchConfig struct {
	url     string
	opts    FetchOptions
	retries int
	timeout time.Duration
	method  string
}

// fetchWithRetries runs the retry loop, returning either a successful
// response or the last error encountered.
func fetchWithRetries(ctx context.Context, cfg *fetchConfig) (*http.Response, error) {
	var lastErr error

	for attempt := range cfg.retries + 1 {
		resp, err := executeAttempt(ctx, cfg, attempt)
		if err != nil {
			if !isRetryable(err, attempt, cfg.retries) {
				return nil, err
			}

			lastErr = err

			continue
		}

		return resp, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}

	return nil, errFetchRetriesExhausted
}

// executeAttempt executes a single HTTP attempt.
func executeAttempt(ctx context.Context, cfg *fetchConfig, attempt int) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, cfg.method, cfg.url, cfg.opts.Body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	for k, v := range cfg.opts.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Transport: &http.Transport{}}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("request: %w", err)}
	}

	if attempt < cfg.retries && isRetryableStatus(resp.StatusCode) {
		//nolint:errcheck // body close error is not critical
		resp.Body.Close()

		waitWithJitter(attempt)

		return nil, &retryableError{
			err: fmt.Errorf(
				"http status %d: %s",
				resp.StatusCode,
				http.StatusText(resp.StatusCode),
			),
		}
	}

	return resp, nil
}

// retryableError marks an error as eligible for retry.
type retryableError struct {
	err error
}

func (re *retryableError) Error() string { return re.err.Error() }
func (re *retryableError) Unwrap() error { return re.err }

// isRetryable reports whether an error from an attempt should trigger a retry.
func isRetryable(err error, attempt, retries int) bool {
	if attempt >= retries {
		return false
	}

	// Explicitly marked as retryable.
	var retryErr *retryableError

	if errors.As(err, &retryErr) {
		return true
	}

	// Transient network errors (timeout, DNS, connection refused, etc.).
	var netErr net.Error

	return errors.As(err, &netErr)
}

// waitWithJitter sleeps for 250ms * 2^attempt plus a random jitter of 0–100ms.
func waitWithJitter(attempt int) {
	base := backoffBase * time.Duration(1<<attempt)
	jitter := time.Duration(randByte()) * time.Millisecond

	time.Sleep(base + jitter)
}

// randByte returns a cryptographically random byte in [0, 100).
func randByte() int {
	buf := make([]byte, 1)

	_, _ = rand.Read(buf)

	return int(buf[0]) % maxJitterMs
}
