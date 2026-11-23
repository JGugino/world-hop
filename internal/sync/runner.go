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
	// SavesFolders returns the configured saves-folder sources: the only
	// places a cloud world may be downloaded into. Optional.
	SavesFolders func() []string
	// AutoDownload makes a full sync pass fetch cloud-only worlds this
	// computer has never had, when there is exactly one saves folder to put
	// them in. It only ever creates new folders.
	AutoDownload bool

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
	return r.runPass(ctx, r.listWorlds(), true)
}

// SyncWorlds syncs just the given worlds, waiting for any pass already in
// progress to finish first (so a world that just closed is never dropped).
func (r *Runner) SyncWorlds(ctx context.Context, ws []worlds.World) RunnerStatus {
	r.pass.Lock()
	defer r.pass.Unlock()
	return r.runPass(ctx, ws, false)
}

// SyncAll is ForceSync but waits for a running pass instead of skipping.
func (r *Runner) SyncAll(ctx context.Context) RunnerStatus {
	r.pass.Lock()
	defer r.pass.Unlock()
	return r.runPass(ctx, r.listWorlds(), true)
}

func (r *Runner) listWorlds() []worlds.World {
	if r.list == nil {
		return nil
	}
	return r.list()
}

// runPass runs one pass. full means a pass over everything (not a subset of
// worlds that just closed), which is when cloud-only worlds are fetched.
func (r *Runner) runPass(ctx context.Context, ws []worlds.World, full bool) RunnerStatus {
	r.mu.Lock()
	r.status.State = "syncing"
	r.mu.Unlock()

	result, warnings := r.run(ctx, ws, full)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.status.State = "idle"
	r.status.LastSync = &now
	r.status.LastResult = result
	r.status.Warnings = warnings
	return r.status
}

func (r *Runner) run(ctx context.Context, ws []worlds.World, full bool) (string, []string) {
	if r.engine == nil {
		return "No sync store configured yet.", nil
	}
	if len(ws) == 0 && !(full && r.canAutoDownload()) {
		return "No worlds found.", nil
	}
	var pushed, pulled, synced, open, conflicts, failed, limited int
	var firstErr error
	var warnings []string
	for _, w := range ws {
		if ctx.Err() != nil {
			return "Sync cancelled.", warnings
		}
		res, err := r.engine.Sync(ctx, w)
		if err == nil || errors.Is(err, ErrWorldOpen) {
			switch {
			case res.Pushed:
				pushed++
			case res.Pulled:
				pulled++
			case res.Status == StatusConflict:
				conflicts++
			case res.Status == StatusRemoteAhead && err != nil:
				warnings = append(warnings, fmt.Sprintf("%s has a newer version in the cloud but is open in Minecraft. Close it to update.", w.Name))
			case err == nil:
				synced++ // up to date, or a cloud-only world with no local copy
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
	var downloaded int
	if full && ctx.Err() == nil {
		var dlErr error
		downloaded, dlErr = r.autoDownload(ctx, ws)
		if dlErr != nil {
			failed++
			if firstErr == nil {
				firstErr = dlErr
			}
		}
	}
	msg := fmt.Sprintf("Synced %d world(s): %d pushed, %d pulled, %d up to date", len(ws), pushed, pulled, synced)
	if downloaded > 0 {
		msg += fmt.Sprintf(", %d downloaded from the cloud", downloaded)
	}
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
