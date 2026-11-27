// runner_worlds.go: what the Worlds tab needs from the Runner. Lists worlds with their
// status, runs the actions the user can choose (sync one world, resolve a conflict or
// lost data, restore a snapshot), and handles cloud-only worlds. Each action takes the
// pass lock so it never races a sync pass.

// runner_worlds.go: what the Worlds tab needs from the Runner. Lists worlds with their
// status, runs the actions the user can choose (sync one world, resolve a conflict or
// lost data, restore a snapshot), and handles cloud-only worlds. Each action takes the
// pass lock so it never races a sync pass.

package sync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/JGugino/world-hopper/internal/license"
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
	Locked bool `json:"locked,omitempty"`
	// Error is a short plain-language reason the status is unknown; Hint says
	// what to do about it and Fix is the UI page where to do it.
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
	Fix   string `json:"fix,omitempty"`
	// Lost says what the cloud lost, when Status is "cloud-lost".
	Lost *LostInfo `json:"lost,omitempty"`
}

// LostInfo is what the UI shows about data the cloud lost.
type LostInfo struct {
	Kind LostKind `json:"kind"`
	// Versions seen before and missing now; Remaining still listed.
	Versions  int `json:"versions"`
	Remaining int `json:"remaining"`
	// Packs found missing (0 when unknown).
	Packs int `json:"packs"`
	// CanAccept: the cloud can be taken as it is now (not for missing data).
	CanAccept bool `json:"canAccept"`
	// CanReupload: this computer has a copy to upload.
	CanReupload bool `json:"canReupload"`
}

// Worlds lists every world in the configured sources with its sync status.
func (r *Runner) Worlds(ctx context.Context) []WorldInfo {
	ws := r.listWorlds()
	out := make([]WorldInfo, len(ws))
	// Each world's status reads its manifests and scans its files; do a few at
	// a time so the world list opens quickly with several worlds.
	parallel(ctx, len(ws), 4, func(ctx context.Context, i int) error {
		w := ws[i]
		info := WorldInfo{ID: w.ID, Name: w.Name, Path: w.Path, Launcher: w.Launcher, Status: "unknown"}
		info.Locked = r.Limited != nil && r.Limited(w.ID)
		if r.engine == nil {
			p := Explain(ErrNoStore)
			info.Error, info.Hint, info.Fix = p.Title, p.Action, p.Fix
		} else {
			if r.engine.IsOpen != nil {
				info.Open = r.engine.IsOpen(w.Path)
			}
			if st, err := r.engine.Status(ctx, w); err != nil {
				p := Explain(err)
				info.Error, info.Hint, info.Fix = p.Title, p.Action, p.Fix
			} else {
				info.Status = string(st)
				if st == StatusCloudLost {
					if le, err := r.engine.LostDetails(ctx, w.ID); err == nil && le != nil {
						info.Lost = &LostInfo{Kind: le.Kind, Versions: len(le.Versions), Remaining: le.Remaining,
							Packs: le.Packs, CanAccept: len(le.Versions) > 0, CanReupload: worlds.IsWorld(w.Path)}
					}
				}
			}
		}
		out[i] = info
		return nil
	})
	return out
}

// find looks a world up by id among the currently configured sources.
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

// ResolveLost carries out what the person chose about data the cloud lost.
func (r *Runner) ResolveLost(ctx context.Context, id string, a LostAction) (LostResult, error) {
	if a != LostAcceptCloud && a != LostReupload {
		return LostResult{}, fmt.Errorf("%w %q", ErrBadLostAction, a)
	}
	if r.engine == nil {
		return LostResult{}, ErrNoStore
	}
	w, err := r.find(id)
	if err != nil {
		return LostResult{}, err
	}
	r.pass.Lock() // don't race a sync pass
	defer r.pass.Unlock()
	return r.engine.ResolveLost(ctx, w, a)
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

// CloudWorld is a world that is in the store but has no copy on this computer.
type CloudWorld struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	UpdatedAt *time.Time `json:"updatedAt"`
	Size      int64      `json:"size"`
	Files     int        `json:"files"`
	// Diverged: two computers uploaded without syncing. A download gets the
	// newest version and the next sync reports the conflict.
	Diverged bool `json:"diverged"`
	// Seen: this computer had the world before and it was removed from disk,
	// so it is never downloaded automatically.
	Seen  bool   `json:"seen"`
	Error string `json:"error,omitempty"`
}

// ErrBadTarget: the download folder is not one of the configured saves folders.
var ErrBadTarget = errors.New("choose one of your saves folders")

// CloudWorlds lists stored worlds with no local copy. Whether a world is local
// is decided by its id (the sanitized folder name, see worlds.IDFromName), so a
// world already here under any equivalent folder name is not listed.
func (r *Runner) CloudWorlds(ctx context.Context) ([]CloudWorld, error) {
	if r.engine == nil {
		return nil, ErrNoStore
	}
	ids, err := r.engine.RemoteIDs(ctx)
	if err != nil {
		return nil, err
	}
	local := map[string]bool{}
	for _, w := range r.listWorlds() {
		local[w.ID] = true
	}
	out := []CloudWorld{}
	for _, id := range ids {
		if local[id] {
			continue
		}
		cw := CloudWorld{ID: id, Name: id, Seen: r.engine.HasState(id)}
		rw, err := r.engine.Remote(ctx, id)
		if err != nil {
			cw.Error = Explain(err).Title
		} else {
			t := rw.UpdatedAt
			cw.UpdatedAt, cw.Size, cw.Files, cw.Diverged = &t, rw.Size, rw.Files, rw.Diverged
		}
		out = append(out, cw)
	}
	return out, nil
}

// savesFolders returns the configured saves folders (the only places a cloud world
// may be downloaded into), or none if not set.
func (r *Runner) savesFolders() []string {
	if r.SavesFolders == nil {
		return nil
	}
	return r.SavesFolders()
}

// DownloadWorld fetches a cloud-only world into one of the saves folders. It
// never overwrites: if a folder with that name exists it fails.
func (r *Runner) DownloadWorld(ctx context.Context, id, savesPath string) (PullResult, error) {
	if r.engine == nil {
		return PullResult{}, ErrNoStore
	}
	ok := false
	for _, p := range r.savesFolders() {
		if filepath.Clean(p) == filepath.Clean(savesPath) {
			ok = true
		}
	}
	if !ok {
		return PullResult{}, ErrBadTarget
	}
	r.pass.Lock() // don't race a sync pass
	defer r.pass.Unlock()
	for _, w := range r.listWorlds() {
		if w.ID == id {
			return PullResult{}, fmt.Errorf("%w: this computer already has it at %s", ErrDestExists, w.Path)
		}
	}
	return r.engine.Download(ctx, id, savesPath)
}

// canAutoDownload reports whether automatic download is on and there is exactly one
// saves folder to put new worlds in. With several folders the choice is the user's.
func (r *Runner) canAutoDownload() bool {
	return r.AutoDownload && len(r.savesFolders()) == 1
}

// autoDownload fetches cloud-only worlds this computer has never had into the
// single saves folder. Failures on one world do not stop the others.
func (r *Runner) autoDownload(ctx context.Context, local []worlds.World) (int, error) {
	if !r.canAutoDownload() {
		return 0, nil
	}
	ids, err := r.engine.RemoteIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list cloud worlds: %w", err)
	}
	have := map[string]bool{}
	for _, w := range local {
		have[w.ID] = true
	}
	dir := r.savesFolders()[0]
	var n int
	var firstErr error
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		if have[id] || r.engine.HasState(id) {
			continue
		}
		if rw, err := r.engine.Remote(ctx, id); err != nil || rw.Diverged {
			continue // leave it for the user to decide
		}
		r.setProgress(&Progress{World: id, Action: "downloading", Done: n, Total: len(ids)})
		_, err := r.engine.Download(ctx, id, dir)
		switch {
		case err == nil:
			n++
		case errors.Is(err, license.ErrWorldLimit):
			// the free version is already using its world; leave the rest in the cloud list
		case errors.Is(err, ErrDestExists):
			// a folder with that name is there but is not a recognised world
		case firstErr == nil:
			firstErr = fmt.Errorf("download %s: %w", id, err)
		}
	}
	return n, firstErr
}
