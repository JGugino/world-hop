package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// countingStore counts calls and optionally fails pack uploads.
type countingStore struct {
	store.Store
	putPacks, getPacks, getManifests atomic.Int64
	failPutAfter                     int64 // fail PutPack once this many have been tried (0: never)
}

func (c *countingStore) PutPack(ctx context.Context, name string, data []byte) error {
	n := c.putPacks.Add(1)
	if c.failPutAfter > 0 && n > c.failPutAfter {
		return errors.New("boom")
	}
	return c.Store.PutPack(ctx, name, data)
}
func (c *countingStore) GetPack(ctx context.Context, name string) ([]byte, error) {
	c.getPacks.Add(1)
	return c.Store.GetPack(ctx, name)
}
func (c *countingStore) GetManifest(ctx context.Context, w, n string) ([]byte, error) {
	c.getManifests.Add(1)
	return c.Store.GetManifest(ctx, w, n)
}

func countedDevice(t *testing.T, shared string) (dev, *countingStore) {
	t.Helper()
	base := t.TempDir()
	cs := &countingStore{Store: localdir.New(shared)}
	e, err := New(cs, filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(base, "saves"), 0o755)
	return dev{e, worlds.World{ID: "Survival", Name: "Survival", Path: filepath.Join(base, "saves", "Survival")}}, cs
}

// age backdates every file's modification time so the stat cache trusts it
// (files modified moments ago are deliberately not cached).
func age(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chtimes(p, old, old)
		}
		return nil
	})
}

func TestScanUsesStatCacheOnlyWhenSizeAndMTimeMatch(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.dat", rnd(1, 3000))
	write(t, root, "region/r.mca", rnd(2, 2<<20))
	age(t, root)
	cache := loadStatCache(filepath.Join(t.TempDir(), "c.json"))
	first, err := scan(root, cache)
	if err != nil {
		t.Fatal(err)
	}
	cache.save()
	cache = loadStatCache(cache.path) // reload from disk

	// Same size and mtime, different bytes: only a cache that skips reading
	// can return the old hashes.
	p := filepath.Join(root, "a.dat")
	st, _ := os.Stat(p)
	os.WriteFile(p, rnd(99, 3000), 0o644)
	os.Chtimes(p, st.ModTime(), st.ModTime())
	cached, err := scan(root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TreeHash(cached.Files) != manifest.TreeHash(first.Files) {
		t.Fatal("unchanged (size, mtime) file was read again")
	}
	if full, _ := scan(root, nil); manifest.TreeHash(full.Files) == manifest.TreeHash(first.Files) {
		t.Fatal("test setup: file should differ")
	}

	// A new mtime (what the game does on every write) is noticed.
	later := time.Now().Add(-30 * time.Minute)
	os.Chtimes(p, later, later)
	fresh, err := scan(root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if full, _ := scan(root, nil); manifest.TreeHash(fresh.Files) != manifest.TreeHash(full.Files) {
		t.Fatal("changed file not re-hashed")
	}
	// So is a size change, and a removed file leaves the cache.
	write(t, root, "a.dat", rnd(5, 10))
	os.Chtimes(p, st.ModTime(), st.ModTime())
	os.Remove(filepath.Join(root, "region/r.mca"))
	res, _ := scan(root, cache)
	if len(res.Files) != 1 || res.Files[0].Size != 10 {
		t.Fatalf("%+v", res.Files)
	}
	if _, ok := cache.get("region/r.mca"); ok {
		t.Fatal("removed file stayed in the cache")
	}
}

func TestRecentlyModifiedFilesAreNotCached(t *testing.T) {
	root := t.TempDir()
	write(t, root, "new.dat", rnd(1, 100)) // modified just now
	cache := loadStatCache("")
	if _, err := scan(root, cache); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.get("new.dat"); ok {
		t.Fatal("a file written inside the racy window was cached")
	}
}

func TestSyncUnchangedWorldIsStatOnlyAndPullDownloadsOnlyNewData(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "store")
	a, ca := countedDevice(t, shared)
	b, cb := countedDevice(t, shared)
	fixtureWorld(t, a.w.Path, 1)
	age(t, a.w.Path)
	if r, err := a.e.Sync(ctx, a.w); err != nil || !r.Pushed {
		t.Fatal(r, err)
	}
	// First pull on B: no local world, downloads everything.
	if _, err := b.e.Download(ctx, "Survival", filepath.Dir(b.w.Path)); err != nil {
		t.Fatal(err)
	}
	sameWorld(t, a.w.Path, b.w.Path)
	age(t, b.w.Path)
	if r, err := b.e.Sync(ctx, b.w); err != nil || r.Status != StatusSynced || r.Pushed || r.Pulled {
		t.Fatalf("%+v %v", r, err)
	}

	// Unchanged: no pack traffic and no manifest downloads after the first pass.
	before, packs := cb.getManifests.Load(), cb.getPacks.Load()
	for i := 0; i < 3; i++ {
		if r, err := b.e.Sync(ctx, b.w); err != nil || r.Status != StatusSynced {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if cb.getManifests.Load() != before || cb.getPacks.Load() != packs {
		t.Fatal("manifests or packs were downloaded again")
	}

	// A edits one spot in a big region file and pushes.
	f := filepath.Join(a.w.Path, "region/r.0.0.mca")
	data, _ := os.ReadFile(f)
	copy(data[3<<20:], []byte("EDITED-BLOCK"))
	os.WriteFile(f, data, 0o644)
	later := time.Now().Add(-10 * time.Minute)
	os.Chtimes(f, later, later)
	packsBefore := ca.putPacks.Load()
	if r, err := a.e.Sync(ctx, a.w); err != nil || !r.Pushed {
		t.Fatal(r, err)
	}
	if n := ca.putPacks.Load() - packsBefore; n != 1 {
		t.Fatalf("uploaded %d packs for one edit", n)
	}

	// B pulls: only the pack with the new data is downloaded.
	gets := cb.getPacks.Load()
	r, err := b.e.Sync(ctx, b.w)
	if err != nil || !r.Pulled {
		t.Fatal(r, err)
	}
	if n := cb.getPacks.Load() - gets; n != 1 {
		t.Fatalf("pull downloaded %d packs, want 1", n)
	}
	sameWorld(t, a.w.Path, b.w.Path)
	if r, _ := b.e.Sync(ctx, b.w); r.Status != StatusSynced {
		t.Fatalf("after pull: %+v", r)
	}
	// The replaced world was kept as a snapshot, with nothing left aside.
	snaps, _ := b.e.Snapshots("Survival")
	if len(snaps) != 1 {
		t.Fatalf("snapshots: %v", snaps)
	}
	old, _ := os.ReadFile(filepath.Join(snaps[0].Path, "region/r.0.0.mca"))
	if bytes.Contains(old, []byte("EDITED-BLOCK")) {
		t.Fatal("snapshot holds the new version")
	}
	if _, err := os.Stat(oldPath(b.w.Path)); err == nil {
		t.Fatal("old world left beside the new one")
	}
}

func TestPullFallsBackToStoreWhenLocalChunkIsNotWhatTheCacheSays(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "store")
	a, _ := countedDevice(t, shared)
	b, _ := countedDevice(t, shared)
	fixtureWorld(t, a.w.Path, 1)
	age(t, a.w.Path)
	a.e.Sync(ctx, a.w)
	if _, err := b.e.Download(ctx, "Survival", filepath.Dir(b.w.Path)); err != nil {
		t.Fatal(err)
	}
	age(t, b.w.Path)
	b.e.Sync(ctx, b.w) // fills B's stat cache

	// B's copy of a file silently changes without a new size or mtime...
	p := filepath.Join(b.w.Path, "level.dat")
	st, _ := os.Stat(p)
	os.WriteFile(p, rnd(77, int(st.Size())), 0o644)
	os.Chtimes(p, st.ModTime(), st.ModTime())
	// ...and A changes it for real.
	write(t, a.w.Path, "level.dat", rnd(78, 2000))
	later := time.Now().Add(-5 * time.Minute)
	os.Chtimes(filepath.Join(a.w.Path, "level.dat"), later, later)
	if r, err := a.e.Sync(ctx, a.w); err != nil || !r.Pushed {
		t.Fatal(r, err)
	}
	r, err := b.e.Sync(ctx, b.w)
	if err != nil || !r.Pulled {
		t.Fatal(r, err)
	}
	sameWorld(t, a.w.Path, b.w.Path) // the result matches the store byte for byte
}

func TestPushFailureLeavesNoManifestAndReportsTheError(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "store")
	a, ca := countedDevice(t, shared)
	for i := 0; i < 8; i++ {
		write(t, a.w.Path, "region/r."+string(rune('0'+i))+".0.mca", rnd(int64(i), 20<<20))
	}
	write(t, a.w.Path, "level.dat", rnd(1, 100))
	ca.failPutAfter = 2
	if _, err := a.e.Push(ctx, a.w); err == nil {
		t.Fatal("push succeeded although uploads failed")
	}
	names, _ := localdir.New(shared).ListManifests(ctx, "Survival")
	if len(names) != 0 {
		t.Fatalf("a manifest was written after a failed push: %v", names)
	}
	// Retrying after the outage works and uploads a complete world.
	ca.failPutAfter = 0
	if r, err := a.e.Push(ctx, a.w); err != nil || !r.Pushed {
		t.Fatal(r, err)
	}
	b, _ := countedDevice(t, shared)
	if _, err := b.e.Download(ctx, "Survival", filepath.Dir(b.w.Path)); err != nil {
		t.Fatal(err)
	}
	sameWorld(t, a.w.Path, b.w.Path)
}

func TestLeftoverOldWorldBecomesSnapshotNotDeleted(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "store")
	a, _ := countedDevice(t, shared)
	fixtureWorld(t, a.w.Path, 1)
	a.e.Push(ctx, a.w)
	// Simulate a stop right after the swap: new world in place, old one aside.
	write(t, filepath.Dir(a.w.Path), filepath.Base(oldPath(a.w.Path))+"/level.dat", []byte("previous"))
	write(t, filepath.Dir(a.w.Path), filepath.Base(oldPath(a.w.Path))+"/region/x", []byte("x"))
	a.e.keepLeftover(a.w)
	if _, err := os.Stat(oldPath(a.w.Path)); err == nil {
		t.Fatal("old dir still beside the world")
	}
	snaps, _ := a.e.Snapshots("Survival")
	if len(snaps) != 1 {
		t.Fatalf("%v", snaps)
	}
	if b, _ := os.ReadFile(filepath.Join(snaps[0].Path, "level.dat")); string(b) != "previous" {
		t.Fatal("snapshot lost the old data")
	}
}

func TestParallelStopsOnFirstError(t *testing.T) {
	var started atomic.Int64
	boom := errors.New("boom")
	err := parallel(context.Background(), 1000, 4, func(ctx context.Context, i int) error {
		started.Add(1)
		if i == 3 {
			return boom
		}
		time.Sleep(time.Millisecond)
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if started.Load() > 200 {
		t.Fatalf("%d tasks started after an error", started.Load())
	}
	var mu stdsync.Mutex
	seen := map[int]bool{}
	if err := parallel(context.Background(), 50, 8, func(ctx context.Context, i int) error {
		mu.Lock()
		seen[i] = true
		mu.Unlock()
		return nil
	}); err != nil || len(seen) != 50 {
		t.Fatal(err, len(seen))
	}
}
