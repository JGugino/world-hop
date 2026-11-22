package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/JGugino/world-hopper/internal/worlds"
)

// ErrNoStore: no sync store is linked yet.
var ErrNoStore = errors.New("no sync store configured yet")

// ErrUnknownWorld: no world with that id is in the configured sources.
var ErrUnknownWorld = errors.New("unknown world")

// ErrBadStrategy: not one of the conflict strategies.
var ErrBadStrategy = errors.New("unknown conflict strategy")

// WorldInfo is one world's state for the UI.
type WorldInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Launcher string `json:"launcher,omitempty"`
	// Status is an Engine Status value, or "unknown" when it could not be
	// determined (no store linked, or the store was unreachable; see Error).
	Status string `json:"status"`
	Open   bool   `json:"open"`
	// Locked: the free version is already syncing a different world.
	Locked bool   `json:"locked,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Worlds lists every world in the configured sources with its sync status.
func (r *Runner) Worlds(ctx context.Context) []WorldInfo {
	ws := r.listWorlds()
	out := make([]WorldInfo, 0, len(ws))
	for _, w := range ws {
		info := WorldInfo{ID: w.ID, Name: w.Name, Path: w.Path, Launcher: w.Launcher, Status: "unknown"}
		info.Locked = r.Limited != nil && r.Limited(w.ID)
		if r.engine == nil {
			info.Error = ErrNoStore.Error()
		} else {
			if r.engine.IsOpen != nil {
				info.Open = r.engine.IsOpen(w.Path)
			}
			if st, err := r.engine.Status(ctx, w); err != nil {
				info.Error = err.Error()
			} else {
				info.Status = string(st)
			}
		}
		out = append(out, info)
	}
	return out
}

func (r *Runner) find(id string) (worlds.World, error) {
	for _, w := range r.listWorlds() {
		if w.ID == id {
			return w, nil
		}
	}
	return worlds.World{}, ErrUnknownWorld
}

// SyncWorld syncs a single world now.
func (r *Runner) SyncWorld(ctx context.Context, id string) (RunnerStatus, error) {
	w, err := r.find(id)
	if err != nil {
		return RunnerStatus{}, err
	}
	return r.SyncWorlds(ctx, []worlds.World{w}), nil
}

// Resolve settles a conflict on one world with the chosen strategy.
func (r *Runner) Resolve(ctx context.Context, id string, s Strategy) (ResolveResult, error) {
	switch s {
	case KeepLocal, KeepRemote, KeepNewest, KeepBoth:
	default:
		return ResolveResult{}, fmt.Errorf("%w %q", ErrBadStrategy, s)
	}
	if r.engine == nil {
		return ResolveResult{}, ErrNoStore
	}
	w, err := r.find(id)
	if err != nil {
		return ResolveResult{}, err
	}
	r.pass.Lock() // don't race a sync pass
	defer r.pass.Unlock()
	return r.engine.Resolve(ctx, w, s)
}

// Snapshots lists a world's local snapshots, oldest first.
func (r *Runner) Snapshots(id string) ([]SnapshotInfo, error) {
	if r.engine == nil {
		return nil, ErrNoStore
	}
	if _, err := r.find(id); err != nil {
		return nil, err
	}
	return r.engine.Snapshots(id)
}

// Restore swaps a snapshot back in for the world.
func (r *Runner) Restore(id, snapshotID string) error {
	if r.engine == nil {
		return ErrNoStore
	}
	w, err := r.find(id)
	if err != nil {
		return err
	}
	r.pass.Lock()
	defer r.pass.Unlock()
	return r.engine.Restore(w, snapshotID)
}

// FindWorld looks a world up by id among the configured sources.
func (r *Runner) FindWorld(id string) (worlds.World, error) { return r.find(id) }
