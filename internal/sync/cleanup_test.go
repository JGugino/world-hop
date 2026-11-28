package sync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
)

// pushVersions pushes n versions of the dev's world, each with a different 300 KiB
// file so every version stores a pack of its own.
func pushVersions(t *testing.T, d dev, n int, seed int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		write(t, d.w.Path, "region/r.9.9.mca", rnd(seed+int64(i), 300<<10))
		if _, err := d.e.Push(ctx, d.w); err != nil {
			t.Fatal(err)
		}
	}
}

func countDir(t *testing.T, dir string) int {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	return len(ents)
}

// later makes the engine's clock run days ahead, so versions count as old.
func later(e *Engine, days int) {
	e.Now = func() time.Time { return time.Now().Add(time.Duration(days) * 24 * time.Hour) }
}

func TestCleanupRemovesOldVersionsAndTheirPacks(t *testing.T) {
	a, b, shared := twoDevices(t)
	a.e.CloudKeep = func() int { return 10 }
	fixtureWorld(t, a.w.Path, 1)
	pushVersions(t, a, 15, 100)
	before := countDir(t, filepath.Join(shared, "packs"))

	// Young versions are all kept, however many there are.
	if res, err := a.e.Cleanup(ctx); err != nil || res.Manifests != 0 || res.Packs != 0 {
		t.Fatalf("fresh history must be kept: %+v %v", res, err)
	}
	later(a.e, 30)
	res, err := a.e.Cleanup(ctx)
	if err != nil || res.Manifests != 5 || res.Worlds != 1 || res.Packs != 4 {
		t.Fatalf("cleanup = %+v %v (want 5 manifests, 4 packs)", res, err)
	}
	if n := len(manifestFiles(t, shared, "Survival")); n != 10 {
		t.Fatalf("%d manifests left, want 10", n)
	}
	if got := countDir(t, filepath.Join(shared, "packs")); got != before-4 {
		t.Fatalf("%d packs left, was %d", got, before)
	}
	// Nothing the newest version needs is gone: a fresh computer can pull it.
	if _, err := b.e.Pull(ctx, b.w); err != nil {
		t.Fatalf("pull after cleanup: %v", err)
	}
	sameWorld(t, a.w.Path, b.w.Path)
	// Running again finds nothing to do.
	if res, err := a.e.Cleanup(ctx); err != nil || res.Manifests != 0 || res.Packs != 0 {
		t.Fatalf("second run: %+v %v", res, err)
	}
}

func TestCleanupKeepsPacksAnotherWorldStillUses(t *testing.T) {
	a, _, shared := twoDevices(t)
	a.e.CloudKeep = func() int { return 10 }
	fixtureWorld(t, a.w.Path, 1)
	pushVersions(t, a, 12, 100)
	// A second world holding only what Survival's second version added: its pack is
	// byte for byte the one that version stored, so the two worlds share it.
	other := a
	other.w.ID, other.w.Name = "Copy", "Copy"
	other.w.Path = filepath.Join(filepath.Dir(a.w.Path), "Copy")
	write(t, other.w.Path, "level.dat", nil) // empty: adds no chunk of its own
	write(t, other.w.Path, "region/r.9.9.mca", rnd(101, 300<<10))
	packs := countDir(t, filepath.Join(shared, "packs"))
	if _, err := other.e.Push(ctx, other.w); err != nil {
		t.Fatal(err)
	}
	if countDir(t, filepath.Join(shared, "packs")) != packs {
		t.Fatal("test setup: the copy was meant to reuse a pack Survival already stored")
	}
	later(a.e, 30)
	if res, err := a.e.Cleanup(ctx); err != nil || res.Manifests != 2 {
		t.Fatalf("cleanup: %+v %v (Survival's two oldest versions should go)", res, err)
	}
	// Survival's second version is gone, but the pack it shares with the copy stays.
	dest := filepath.Join(t.TempDir(), "Copy")
	if _, err := a.e.Download(ctx, "Copy", filepath.Dir(dest)); err != nil {
		t.Fatalf("copy damaged by cleanup: %v", err)
	}
}

func TestOtherComputersAreNotToldTheCloudLostPrunedHistory(t *testing.T) {
	a, b, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	pushVersions(t, a, 1, 100)
	if _, err := b.e.Pull(ctx, b.w); err != nil { // B has seen only version 1
		t.Fatal(err)
	}
	pushVersions(t, a, 14, 200)
	later(a.e, 30)
	if res, err := a.e.Cleanup(ctx); err != nil || res.Manifests == 0 {
		t.Fatalf("cleanup: %+v %v", res, err)
	}
	if st, err := b.e.Status(ctx, b.w); err != nil || st != StatusRemoteAhead {
		t.Fatalf("B sees %s, %v; its version was only pruned, not lost", st, err)
	}
	if _, err := b.e.Pull(ctx, b.w); err != nil {
		t.Fatal(err)
	}
	sameWorld(t, a.w.Path, b.w.Path)
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatalf("B after pull: %s", st)
	}
}

func TestCleanupNeverTouchesConflictingHeads(t *testing.T) {
	a, b := conflicted(t)
	later(a.e, 60)
	later(b.e, 60)
	if res, err := a.e.Cleanup(ctx); err != nil || res.Manifests != 0 {
		t.Fatalf("both heads must stay: %+v %v", res, err)
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusConflict {
		t.Fatalf("conflict gone after cleanup: %s", st)
	}
}

func TestCleanupStopsWhenAManifestCannotBeRead(t *testing.T) {
	a, _, shared := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	pushVersions(t, a, 14, 100)
	later(a.e, 30)
	files := manifestFiles(t, shared, "Survival")
	if err := os.WriteFile(filepath.Join(shared, "worlds", "Survival", files[0]), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(a.e.StateDir, "manifests"))
	packs := countDir(t, filepath.Join(shared, "packs"))
	if _, err := a.e.Cleanup(ctx); err == nil {
		t.Fatal("an unreadable manifest must stop cleanup")
	}
	if countDir(t, filepath.Join(shared, "packs")) != packs || len(manifestFiles(t, shared, "Survival")) != 14 {
		t.Fatal("cleanup deleted something although it could not read everything")
	}
}

func TestWipedAndReuploadedCloudIsStillFlaggedAfterCleanupExists(t *testing.T) {
	// Covered by TestReuploadRebuilds...: pruning tolerance must not hide a wipe.
	a, b, shared := twoVersions(t)
	os.RemoveAll(shared)
	os.MkdirAll(shared, 0o755)
	if _, err := b.e.ResolveLost(ctx, b.w, LostReupload); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.e.Status(ctx, a.w); st != StatusCloudLost {
		t.Fatalf("A: %s", st)
	}
}

func TestCleanupNeedsAStoreThatCanDelete(t *testing.T) {
	a, _, shared := twoDevices(t)
	a.e.Store = struct{ store.Store }{localdir.New(shared)} // hides the Remover methods
	if _, err := a.e.Cleanup(ctx); !errors.Is(err, store.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestCleanupDueOncePerDay(t *testing.T) {
	a, _, _ := twoDevices(t)
	if !a.e.CleanupDue() {
		t.Fatal("never run: should be due")
	}
	a.e.MarkCleaned()
	if a.e.CleanupDue() {
		t.Fatal("just ran: should not be due")
	}
	later(a.e, 2)
	if !a.e.CleanupDue() {
		t.Fatal("two days later: should be due")
	}
}

func TestCleanupKeepsTheConfiguredNumberOfVersions(t *testing.T) {
	a, _, shared := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	pushVersions(t, a, 8, 100)
	later(a.e, 30)
	// No setting: the default keeps the newest 5.
	if _, err := a.e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(manifestFiles(t, shared, "Survival")); n != 5 {
		t.Fatalf("default kept %d versions, want 5", n)
	}
	// The setting can go down to 2, and never below (a bad value falls back to 5).
	a.e.CloudKeep = func() int { return 2 }
	if _, err := a.e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(manifestFiles(t, shared, "Survival")); n != 2 {
		t.Fatalf("kept %d versions, want 2", n)
	}
	a.e.CloudKeep = func() int { return 0 }
	if got := a.e.cloudKeep(); got != 5 {
		t.Fatalf("a value below the minimum gave %d, want 5", got)
	}
}

// readWorldFile reads one file of a world.
func readWorldFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRestoreCloudVersionBringsBackAnOlderVersion(t *testing.T) {
	a, _, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	write(t, a.w.Path, "region/r.9.9.mca", rnd(100, 300<<10))
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	old := readWorldFile(t, a.w.Path, "region/r.9.9.mca")
	pushVersions(t, a, 2, 200)

	vs, err := a.e.CloudVersions(ctx, a.w.ID)
	if err != nil || len(vs) != 3 || !vs[0].Current || vs[1].Current || !vs[0].Mine {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if _, err := a.e.RestoreCloud(ctx, a.w, vs[0].Name); !errors.Is(err, ErrCurrentVersion) {
		t.Fatalf("restoring the current version: %v", err)
	}
	if _, err := a.e.RestoreCloud(ctx, a.w, "manifest-000000000099-abc.json"); !errors.Is(err, ErrNoSuchVersion) {
		t.Fatalf("restoring a version that is not there: %v", err)
	}
	if _, err := a.e.RestoreCloud(ctx, a.w, vs[2].Name); err != nil {
		t.Fatal(err)
	}
	if got := readWorldFile(t, a.w.Path, "region/r.9.9.mca"); string(got) != string(old) {
		t.Fatal("the world was not put back to the older version")
	}
	if snaps, _ := a.e.Snapshots(a.w.ID); len(snaps) != 1 {
		t.Fatalf("the replaced world was not kept as a snapshot: %v", snaps)
	}
	// The restored world is a change to push, not a reason to pull the newest back.
	if st, _ := a.e.Status(ctx, a.w); st != StatusLocalAhead {
		t.Fatalf("status after restore = %s, want local-ahead", st)
	}
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	if vs, _ := a.e.CloudVersions(ctx, a.w.ID); len(vs) != 4 || !vs[0].Current {
		t.Fatalf("the restore should be a new version on top: %+v", vs)
	}
}
