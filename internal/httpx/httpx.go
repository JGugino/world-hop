// Package httpx holds the HTTP plumbing shared by the cloud stores: retry with
// backoff on throttling and server errors, and bearer auth with one refresh
// retry on 401.
package httpx

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// Auth supplies bearer tokens. Invalidate forces the next Token call to
// refresh, and is called after the provider rejects a token with 401.
type Auth interface {
	Token(ctx context.Context) (string, error)
	Invalidate()
}

// StatusError is a non-success HTTP response.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("http %d: %s", e.Code, e.Body) }

// Tunables, variables so tests can run fast.
var (
	MaxAttempts = 5
	BaseDelay   = 500 * time.Millisecond
	MaxDelay    = 30 * time.Second
)

func retryable(code int) bool {
	return code == 429 || code == 500 || code == 502 || code == 503 || code == 504
}

func delay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if s := resp.Header.Get("Retry-After"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				return min(time.Duration(n)*time.Second, MaxDelay)
			}
		}
	}
	d := BaseDelay << attempt
	d += time.Duration(rand.Int63n(int64(BaseDelay) + 1))
	return min(d, MaxDelay)
}

// Do sends the request built by build, retrying throttled and failed attempts
// with backoff. build is called once per attempt so bodies can be replayed.
// The caller closes the returned body.
func Do(ctx context.Context, c *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	if c == nil {
		c = http.DefaultClient
	}
	var lastErr error
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req.WithContext(ctx))
		var wait time.Duration
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			wait = delay(attempt, nil)
		case retryable(resp.StatusCode):
			lastErr = &StatusError{Code: resp.StatusCode, Body: ReadErrBody(resp)}
			wait = delay(attempt, resp)
			resp.Body.Close()
		default:
			return resp, nil
		}
		if attempt == MaxAttempts-1 {
			break
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// ReadErrBody reads a bounded prefix of an error response body.
func ReadErrBody(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b)
}

// Authed is an HTTP client that signs requests with a bearer token.
type Authed struct {
	HTTP *http.Client
	Auth Auth
}

// Do signs and sends the request from build, with retry. On 401 it refreshes
// the token once and tries again. Statuses other than 2xx are returned as-is
// so callers can interpret provider-specific errors; call Check for the generic case.
func (a *Authed) Do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	for try := 0; ; try++ {
		tok, err := a.Auth.Token(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := Do(ctx, a.HTTP, func() (*http.Request, error) {
			req, err := build()
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			return req, nil
		})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && try == 0 {
			resp.Body.Close()
			a.Auth.Invalidate()
			continue
		}
		return resp, nil
	}
}

// Check turns a non-2xx response into a *StatusError (closing the body).
func Check(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	return &StatusError{Code: resp.StatusCode, Body: ReadErrBody(resp)}
}
