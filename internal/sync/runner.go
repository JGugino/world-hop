package sync

import (
	"context"
	"errors"
	"fmt"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// RunnerStatus is the status shape served to the UI (/api/status).
type RunnerStatus struct {
	State      string     `json:"state"` // "idle" | "syncing"
	LastSync   *time.Time `json:"lastSync"`
	LastResult string     `json:"lastResult"`
	// Warnings lists worlds that need attention, e.g. a newer version exists
	// but the world is open in Minecraft.
	Warnings []string `json:"warnings"`
	Version  string   `json:"version"`
}

// Runner drives the Engine for the tray and local API: it tracks sync state
// for the UI and runs one sync pass at a time over the discovered worlds.
type Runner struct {
	engine *Engine
	list   func() []worlds.World

	// Limited, if set, reports whether the license keeps a world from syncing
	// (without claiming the free slot). It only affects what the UI shows;
	// the Engine's Authorize hook is what actually enforces the limit.
	Limited func(worldID string) bool

	pass   stdsync.Mutex // one sync pass at a time
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

// ForceSync runs a sync pass over every world now. If one is already running
// it returns the current status without starting another.
func (r *Runner) ForceSync(ctx context.Context) RunnerStatus {
	if !r.pass.TryLock() {
		return r.Status()
	}
	defer r.pass.Unlock()
	return r.runPass(ctx, r.listWorlds())
}

// SyncWorlds syncs just the given worlds, waiting for any pass already in
// progress to finish first (so a world that just closed is never dropped).
func (r *Runner) SyncWorlds(ctx context.Context, ws []worlds.World) RunnerStatus {
	r.pass.Lock()
	defer r.pass.Unlock()
	return r.runPass(ctx, ws)
}

// SyncAll is ForceSync but waits for a running pass instead of skipping.
func (r *Runner) SyncAll(ctx context.Context) RunnerStatus {
	r.pass.Lock()
	defer r.pass.Unlock()
	return r.runPass(ctx, r.listWorlds())
}

func (r *Runner) listWorlds() []worlds.World {
	if r.list == nil {
		return nil
	}
	return r.list()
}

func (r *Runner) runPass(ctx context.Context, ws []worlds.World) RunnerStatus {
	r.mu.Lock()
	r.status.State = "syncing"
	r.mu.Unlock()

	result, warnings := r.run(ctx, ws)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.status.State = "idle"
	r.status.LastSync = &now
	r.status.LastResult = result
	r.status.Warnings = warnings
	return r.status
}

func (r *Runner) run(ctx context.Context, ws []worlds.World) (string, []string) {
	if r.engine == nil {
		return "No sync store configured yet.", nil
	}
	if len(ws) == 0 {
		return "No worlds found.", nil
	}
	var pushed, pulled, synced, open, conflicts, failed, limited int
	var firstErr error
	var warnings []string
	for _, w := range ws {
		if ctx.Err() != nil {
			return "Sync cancelled.", warnings
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
				} else if errors.Is(err, ErrWorldOpen) {
					warnings = append(warnings, fmt.Sprintf("%s has a newer version in the cloud but is open in Minecraft. Close it to update.", w.Name))
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
		case errors.Is(err, license.ErrWorldLimit):
			limited++
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
	if limited > 0 {
		msg += fmt.Sprintf(", %d not synced (free version: 1 world)", limited)
	}
	if conflicts > 0 {
		msg += fmt.Sprintf(", %d in conflict", conflicts)
	}
	if failed > 0 {
		msg += fmt.Sprintf(", %d failed (%v)", failed, firstErr)
	}
	return msg + ".", warnings
}
