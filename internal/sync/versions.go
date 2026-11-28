// versions.go: the versions of a world the cloud still holds, and restoring one of
// them. Every push is kept as a version until cloud cleanup removes it (see
// cleanup.go), so these are the "cloud snapshots": the person can go back to any of
// them, not only to the newest.

package sync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// ErrNoSuchVersion: the cloud does not hold a version with that name.
var ErrNoSuchVersion = errors.New("that version is no longer in the cloud")

// ErrCurrentVersion: the version asked for is already the newest one, so there is
// nothing to restore.
var ErrCurrentVersion = errors.New("that is already the current version")

// CloudVersion is one stored version of a world, as the UI lists it.
type CloudVersion struct {
	Name       string    // manifest name, the id used to restore it
	Generation int64     // how many versions came before it, in the longest chain
	CreatedAt  time.Time // when it was pushed
	Mine       bool      // pushed from this computer (device ids are anonymous)
	Current    bool      // a head: the version other computers would pull
	Files      int
	Size       int64 // total bytes of the world's files
}

// CloudVersions lists the versions the cloud holds for a world, newest first. It
// returns a *LostError if the cloud lost versions this computer had seen.
func (e *Engine) CloudVersions(ctx context.Context, id string) ([]CloudVersion, error) {
	names, err := e.Store.ListManifests(ctx, id)
	if err != nil {
		return nil, err
	}
	hs, err := e.heads(ctx, id)
	if err != nil {
		return nil, err
	}
	isHead := map[string]bool{}
	for _, h := range hs {
		isHead[h.Name] = true
	}
	ms, err := e.loadListed(ctx, id, names)
	if err != nil {
		return nil, err
	}
	out := make([]CloudVersion, 0, len(names))
	for i, n := range names {
		m := ms[i]
		v := CloudVersion{Name: n, Generation: m.Generation, CreatedAt: m.CreatedAt,
			Mine: m.DeviceID == e.DeviceID, Current: isHead[n], Files: len(m.Files)}
		for _, f := range m.Files {
			v.Size += f.Size
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { // newest first, as heads orders them
		if out[i].Generation != out[j].Generation {
			return out[i].Generation > out[j].Generation
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// RestoreCloud puts an older cloud version on this computer as the world. The
// current world is kept as a local snapshot first ("cloud-restore"), so this can be
// undone. Nothing is written to the cloud here: the world then differs from the
// newest version, so the next push uploads the restored copy as a new version on top
// of it (nothing in the cloud is rewritten). It refuses while the world is in a
// conflict, because "newest" is not yet decided.
func (e *Engine) RestoreCloud(ctx context.Context, w worlds.World, name string) (PullResult, error) {
	if err := e.check(w); err != nil {
		return PullResult{}, err
	}
	if _, _, ok := manifest.ParseName(name); !ok {
		return PullResult{}, fmt.Errorf("bad version name")
	}
	hs, err := e.heads(ctx, w.ID)
	if err != nil {
		return PullResult{}, err
	}
	switch {
	case len(hs) == 0:
		return PullResult{}, ErrNotInStore
	case len(hs) > 1:
		return PullResult{}, &ConflictError{w.ID, headNames(hs)}
	case hs[0].Name == name:
		return PullResult{}, ErrCurrentVersion
	}
	names, err := e.Store.ListManifests(ctx, w.ID)
	if err != nil {
		return PullResult{}, err
	}
	found := false
	for _, n := range names {
		found = found || n == name
	}
	if !found {
		return PullResult{}, ErrNoSuchVersion
	}
	m, err := e.loadManifest(ctx, w.ID, name)
	if err != nil {
		return PullResult{}, err
	}
	return e.applyAs(ctx, w, head{name, m}, "cloud-restore", hs[0])
}
