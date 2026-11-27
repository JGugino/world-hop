// auth.go: a fake token source for tests of code that signs requests and retries once
// after a 401.

package storetest

import (
	"context"
	"sync/atomic"
)

// Auth is a fake httpx.Auth. It hands out Stale until Invalidate is called,
// then Fresh, which models an expired access token that gets refreshed.
type Auth struct {
	Stale, Fresh string
	invalid      atomic.Bool
	Refreshes    atomic.Int32
}

// Token returns the stale token until Invalidate is called, then the fresh one.
func (a *Auth) Token(context.Context) (string, error) {
	if a.invalid.Load() || a.Stale == "" {
		return a.Fresh, nil
	}
	return a.Stale, nil
}

// Invalidate marks the stale token as expired and counts the refresh, as the real
// token source does when the server answers 401.
func (a *Auth) Invalidate() { a.invalid.Store(true); a.Refreshes.Add(1) }
