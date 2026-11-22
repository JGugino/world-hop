package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/JGugino/world-hopper/internal/worlds"
)

type SnapshotInfo struct {
	ID   string
	Path string
}

func (e *Engine) snapDir(id string) string { return filepath.Join(e.StateDir, "snapshots", id) }

// Snapshot copies the current world into the local snapshot area and prunes
// old ones. It returns the snapshot id.
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
	keep := e.SnapshotsKeep
	if keep <= 0 {
		keep = 5
	}
	if all, err := e.Snapshots(w.ID); err == nil && len(all) > keep {
		for _, s := range all[:len(all)-keep] {
			os.RemoveAll(s.Path)
		}
	}
	return id, nil
}

// Snapshots lists snapshots oldest first.
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
// world as a new version.
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
