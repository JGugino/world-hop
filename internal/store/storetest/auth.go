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

func (a *Auth) Token(context.Context) (string, error) {
	if a.invalid.Load() || a.Stale == "" {
		return a.Fresh, nil
	}
	return a.Stale, nil
}

func (a *Auth) Invalidate() { a.invalid.Store(true); a.Refreshes.Add(1) }
