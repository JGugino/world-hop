package sync

import (
	"context"
	"errors"
	"fmt"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/worlds"
)

// RunnerStatus is the status shape served to the UI (/api/status).
type RunnerStatus struct {
	State      string     `json:"state"` // "idle" | "syncing"
	LastSync   *time.Time `json:"lastSync"`
	LastResult string     `json:"lastResult"`
	Version    string     `json:"version"`
}

// Runner drives the Engine for the tray and local API: it tracks sync state
// for the UI and runs one sync pass at a time over the discovered worlds.
type Runner struct {
	engine *Engine
	list   func() []worlds.World

	mu     stdsync.Mutex
	status RunnerStatus
}

// NewRunner returns a Runner. engine may be nil (no store configured yet), in
// which case a sync only reports that nothing is configured. list returns the
// worlds to sync and may be nil.
func NewRunner(version string, engine *Engine, list func() []worlds.World) *Runner {
	msg := "No sync has run yet."
	if engine == nil {
		msg = "No sync store configured yet."
	}
	return &Runner{
		engine: engine,
		list:   list,
		status: RunnerStatus{State: "idle", LastResult: msg, Version: version},
	}
}

// Status returns a snapshot of the current state.
func (r *Runner) Status() RunnerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// ForceSync runs a sync pass now. If one is already running it returns the
// current status without starting another.
func (r *Runner) ForceSync(ctx context.Context) RunnerStatus {
	r.mu.Lock()
	if r.status.State == "syncing" {
		st := r.status
		r.mu.Unlock()
		return st
	}
	r.status.State = "syncing"
	r.mu.Unlock()

	result := r.run(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.status.State = "idle"
	r.status.LastSync = &now
	r.status.LastResult = result
	return r.status
}

func (r *Runner) run(ctx context.Context) string {
	if r.engine == nil {
		return "No sync store configured yet."
	}
	var ws []worlds.World
	if r.list != nil {
		ws = r.list()
	}
	if len(ws) == 0 {
		return "No worlds found."
	}
	var pushed, pulled, synced, open, conflicts, failed int
	var firstErr error
	for _, w := range ws {
		if ctx.Err() != nil {
			return "Sync cancelled."
		}
		st, err := r.engine.Status(ctx, w)
		if err == nil {
			switch st {
			case StatusLocalAhead, StatusNoRemote:
				var res PushResult
				if res, err = r.engine.Push(ctx, w); err == nil && res.Pushed {
					pushed++
				} else if err == nil {
					synced++
				}
			case StatusRemoteAhead:
				if _, err = r.engine.Pull(ctx, w); err == nil {
					pulled++
				}
			case StatusConflict:
				conflicts++
			default: // synced, or remote-only world with no local copy
				synced++
			}
		}
		var ce *ConflictError
		switch {
		case err == nil:
		case errors.Is(err, ErrWorldOpen):
			open++
		case errors.As(err, &ce):
			conflicts++
		default:
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", w.Name, err)
			}
		}
	}
	msg := fmt.Sprintf("Synced %d world(s): %d pushed, %d pulled, %d up to date", len(ws), pushed, pulled, synced)
	if open > 0 {
		msg += fmt.Sprintf(", %d skipped (open in Minecraft)", open)
	}
	if conflicts > 0 {
		msg += fmt.Sprintf(", %d in conflict", conflicts)
	}
	if failed > 0 {
		msg += fmt.Sprintf(", %d failed (%v)", failed, firstErr)
	}
	return msg + "."
}
