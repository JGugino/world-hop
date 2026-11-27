// snapshot.go: local snapshots. Before a pull, a conflict resolution or a restore
// replaces a world, the old version is kept under StateDir/snapshots so the user can
// always go back. Snapshots are local only; nothing here talks to the store.

package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/JGugino/world-hopper/internal/worlds"
)

// SnapshotInfo is one saved snapshot: its id (a UTC timestamp plus the reason it
// was taken, such as "pull" or "conflict") and the folder it lives in.
type SnapshotInfo struct {
	ID   string
	Path string
}

// snapDir is the folder holding a world's snapshots.
func (e *Engine) snapDir(id string) string { return filepath.Join(e.StateDir, "snapshots", id) }

// Snapshot copies the current world into the local snapshot area and prunes
// old ones. It returns the snapshot id. The copy is made under a ".partial" name
// and renamed into place so a crash never leaves a half-copied snapshot that looks
// complete.
func (e *Engine) Snapshot(w worlds.World, reason string) (string, error) {
	id := e.now().Format("20060102T150405.000000Z") + "-" + reason
	dst := filepath.Join(e.snapDir(w.ID), id)
	tmp := dst + ".partial"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	if err := copyTree(w.Path, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	e.pruneSnapshots(w.ID)
	return id, nil
}

// pruneSnapshots deletes a world's oldest snapshots beyond SnapshotsKeep (5 by
// default). Ids start with a timestamp, so sorting by id is sorting by age.
func (e *Engine) pruneSnapshots(worldID string) {
	keep := e.SnapshotsKeep
	if keep <= 0 {
		keep = 5
	}
	if all, err := e.Snapshots(worldID); err == nil && len(all) > keep {
		for _, s := range all[:len(all)-keep] {
			os.RemoveAll(s.Path)
		}
	}
}

// canMoveSnapshot reports whether a world folder can be renamed into the
// snapshot area (same volume), by trying it with an empty folder. A rename is
// instant and uses no extra disk space, so it is preferred over copying a world
// that may be gigabytes.
func (e *Engine) canMoveSnapshot(w worlds.World) bool {
	src := filepath.Join(filepath.Dir(w.Path), ".worldhopper-probe-"+filepath.Base(w.Path))
	dst := filepath.Join(e.snapDir(w.ID), ".probe.partial")
	os.RemoveAll(src)
	os.RemoveAll(dst)
	if os.MkdirAll(src, 0o755) != nil || os.MkdirAll(filepath.Dir(dst), 0o755) != nil {
		os.RemoveAll(src)
		return false
	}
	err := os.Rename(src, dst)
	os.RemoveAll(src)
	os.RemoveAll(dst)
	return err == nil
}

// keepLeftover files a world left at its aside path as a snapshot. That
// happens when the app stopped between swapping a new version in and filing
// the replaced one as a snapshot. Recover would delete it, so it is kept first.
func (e *Engine) keepLeftover(w worlds.World) {
	old := oldPath(w.Path)
	if _, err := os.Stat(old); err != nil || !worlds.IsWorld(w.Path) {
		return // nothing left over, or the world is missing and Recover puts it back
	}
	if os.MkdirAll(e.snapDir(w.ID), 0o755) != nil {
		return
	}
	id := e.now().Format("20060102T150405.000000Z") + "-recovered"
	if rename(old, filepath.Join(e.snapDir(w.ID), id)) == nil {
		e.pruneSnapshots(w.ID)
	}
}

// swapWithSnapshot replaces the world at w.Path with staging and keeps the
// replaced world as a snapshot, returning its id. When the snapshot area is on
// the same volume the old folder is simply renamed into it, so no data is
// copied: the old world exists under one name or the other at every instant.
// Otherwise it is copied first, as Snapshot does, before anything is replaced.
func (e *Engine) swapWithSnapshot(w worlds.World, staging, reason string) (string, error) {
	if !e.canMoveSnapshot(w) {
		id, err := e.Snapshot(w, reason)
		if err != nil {
			os.RemoveAll(staging)
			return "", fmt.Errorf("snapshot before overwrite: %w", err)
		}
		if err := swapIn(w.Path, staging); err != nil {
			os.RemoveAll(staging)
			return "", err
		}
		return id, nil
	}
	if _, err := swapKeepOld(w.Path, staging); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	id := e.now().Format("20060102T150405.000000Z") + "-" + reason
	if err := rename(oldPath(w.Path), filepath.Join(e.snapDir(w.ID), id)); err != nil {
		// The world is already replaced; the old one stays at its aside path.
		return "", fmt.Errorf("the world was updated but the previous version could not be saved as a snapshot (it is still at %s): %w", oldPath(w.Path), err)
	}
	e.pruneSnapshots(w.ID)
	return id, nil
}

// Snapshots lists a world's snapshots, oldest first. A missing snapshot folder is
// just an empty list.
func (e *Engine) Snapshots(worldID string) ([]SnapshotInfo, error) {
	ents, err := os.ReadDir(e.snapDir(worldID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SnapshotInfo
	for _, d := range ents {
		if d.IsDir() && filepath.Ext(d.Name()) != ".partial" {
			out = append(out, SnapshotInfo{d.Name(), filepath.Join(e.snapDir(worldID), d.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Restore swaps a snapshot back in. The current world is snapshotted first,
// so a restore can itself be undone. The next push uploads the restored
// world as a new version. The snapshot id is validated (no path separators) so a
// caller can't point it outside the snapshot folder.
func (e *Engine) Restore(w worlds.World, snapshotID string) error {
	if err := e.checkOpen(w); err != nil {
		return err
	}
	if snapshotID == "" || snapshotID != filepath.Base(snapshotID) {
		return fmt.Errorf("bad snapshot id")
	}
	src := filepath.Join(e.snapDir(w.ID), snapshotID)
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return fmt.Errorf("snapshot %q not found", snapshotID)
	}
	staging := stagingPath(w.Path)
	os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := copyTree(src, staging); err != nil {
		os.RemoveAll(staging)
		return err
	}
	if worlds.IsWorld(w.Path) {
		if _, err := e.Snapshot(w, "pre-restore"); err != nil {
			os.RemoveAll(staging)
			return err
		}
	}
	if err := swapIn(w.Path, staging); err != nil {
		os.RemoveAll(staging)
		return err
	}
	return nil
}
