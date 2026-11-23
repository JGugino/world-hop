package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// ErrDestExists: the folder a download would create is already there. Nothing
// is ever overwritten by a download.
var ErrDestExists = errors.New("a folder with that name already exists there")

// ErrNotInStore: the store has no world with that id.
var ErrNotInStore = errors.New("that world is not in the cloud")

// RemoteWorld describes the newest stored version of a world.
type RemoteWorld struct {
	ID        string
	DeviceID  string // device that wrote the newest version
	UpdatedAt time.Time
	Size      int64 // total bytes of the world's files
	Files     int
	Diverged  bool // several heads: two computers pushed without syncing
}

// RemoteIDs lists the ids of every world in the store.
func (e *Engine) RemoteIDs(ctx context.Context) ([]string, error) {
	return e.Store.ListWorlds(ctx)
}

// Remote describes a stored world. ErrNotInStore if it has no manifests.
func (e *Engine) Remote(ctx context.Context, id string) (RemoteWorld, error) {
	hs, err := e.heads(ctx, id)
	if err != nil {
		return RemoteWorld{}, err
	}
	if len(hs) == 0 {
		return RemoteWorld{}, ErrNotInStore
	}
	m := hs[0].M
	rw := RemoteWorld{ID: id, DeviceID: m.DeviceID, UpdatedAt: m.CreatedAt, Files: len(m.Files), Diverged: len(hs) > 1}
	for _, f := range m.Files {
		rw.Size += f.Size
	}
	return rw, nil
}

// HasState reports whether this device has ever synced the world. A world
// with state that is now missing locally was deleted or moved on purpose, so
// it must not be fetched again without being asked.
func (e *Engine) HasState(id string) bool {
	_, err := os.Stat(e.statePath(id))
	return err == nil
}

// Download creates the stored world id as a new folder inside savesDir. The
// world is built in a staging folder, every hash is verified, and only then is
// it renamed into place. It never touches an existing folder: if the target
// name is taken it returns ErrDestExists. If the world has diverged (several
// heads) the newest is used and the next sync reports the conflict.
func (e *Engine) Download(ctx context.Context, id, savesDir string) (PullResult, error) {
	if !manifest.ValidID(id) {
		return PullResult{}, fmt.Errorf("bad world id %q", id)
	}
	st, err := os.Stat(savesDir)
	if err != nil || !st.IsDir() {
		return PullResult{}, fmt.Errorf("%s is not a folder", savesDir)
	}
	dest := filepath.Join(savesDir, id)
	if _, err := os.Lstat(dest); err == nil {
		return PullResult{}, fmt.Errorf("%w: %s", ErrDestExists, dest)
	}
	hs, err := e.heads(ctx, id)
	if err != nil {
		return PullResult{}, err
	}
	if len(hs) == 0 {
		return PullResult{}, ErrNotInStore
	}
	return e.apply(ctx, worlds.World{ID: id, Name: id, Path: dest}, hs[0], "download")
}
