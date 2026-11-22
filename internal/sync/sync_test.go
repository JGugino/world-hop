package sync

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/worlds"
)

var ctx = context.Background()

func rnd(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func write(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureWorld builds a small world including a stale session.lock.
func fixtureWorld(t *testing.T, root string, seed int64) {
	write(t, root, "level.dat", rnd(seed, 2000))
	write(t, root, "session.lock", []byte("STALE-LOCK"))
	write(t, root, "region/r.0.0.mca", rnd(seed+1, 6<<20))
	write(t, root, "region/r.0.1.mca", rnd(seed+2, 1<<20))
	write(t, root, "playerdata/abc.dat", rnd(seed+3, 500))
	write(t, root, "data/empty.dat", nil)
}

type dev struct {
	e *Engine
	w worlds.World
}

// twoDevices returns two devices sharing one store, each with its own saves dir.
func twoDevices(t *testing.T) (a, b dev, shared string) {
	t.Helper()
	shared = filepath.Join(t.TempDir(), "store")
	mk := func(name string) dev {
		base := t.TempDir()
		e, err := New(localdir.New(shared), filepath.Join(base, "state"))
		if err != nil {
			t.Fatal(err)
		}
		return dev{e, worlds.World{ID: "Survival", Name: "Survival", Path: filepath.Join(base, "saves", "Survival")}}
	}
	return mk("a"), mk("b"), shared
}

func readAll(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			b, _ := os.ReadFile(p)
			out[filepath.ToSlash(rel)] = b
		}
		return nil
	})
	return out
}

func sameWorld(t *testing.T, a, b string) {
	t.Helper()
	x, y := readAll(t, a), readAll(t, b)
	delete(x, "session.lock")
	delete(y, "session.lock")
	if len(x) != len(y) {
		t.Fatalf("file count %d vs %d", len(x), len(y))
	}
	for k, v := range x {
		if !bytes.Equal(v, y[k]) {
			t.Fatalf("%s differs", k)
		}
	}
}

func TestPushPullRoundTripNeverSyncsLock(t *testing.T) {
	a, b, shared := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	r, err := a.e.Push(ctx, a.w)
	if err != nil || !r.Pushed {
		t.Fatal(r, err)
	}
	// "Laptop was off": B pulls later.
	p, err := b.e.Pull(ctx, b.w)
	if err != nil || !p.Updated {
		t.Fatal(p, err)
	}
	sameWorld(t, a.w.Path, b.w.Path)
	if _, err := os.Stat(filepath.Join(b.w.Path, "session.lock")); err == nil {
		t.Fatal("session.lock was synced")
	}
	filepath.WalkDir(shared, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			if bs, _ := os.ReadFile(p); bytes.Contains(bs, []byte("STALE-LOCK")) {
				t.Fatalf("lock content found in store file %s", p)
			}
		}
		return nil
	})
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatal(st)
	}
	// Second pull and push are no-ops.
	if p, _ := b.e.Pull(ctx, b.w); p.Updated {
		t.Fatal("pull should be a no-op")
	}
	if r, _ := b.e.Push(ctx, b.w); r.Pushed {
		t.Fatal("push should be a no-op")
	}
}

func TestIncrementalPushUploadsOnlyChanges(t *testing.T) {
	a, _, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	first, _ := a.e.Push(ctx, a.w)
	f := filepath.Join(a.w.Path, "region/r.0.0.mca")
	data, _ := os.ReadFile(f)
	copy(data[3<<20:], []byte("EDITED-BLOCK"))
	os.WriteFile(f, data, 0o644)
	second, err := a.e.Push(ctx, a.w)
	if err != nil || !second.Pushed {
		t.Fatal(second, err)
	}
	if second.NewBytes > 4<<20+4096 || second.NewBytes >= first.NewBytes {
		t.Fatalf("uploaded %d bytes of %d after tiny edit", second.NewBytes, first.NewBytes)
	}
}

func TestOpenWorldIsNotTouched(t *testing.T) {
	a, b, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	a.e.IsOpen = func(string) bool { return true }
	if _, err := a.e.Push(ctx, a.w); !errors.Is(err, ErrWorldOpen) {
		t.Fatal(err)
	}
	a.e.IsOpen = nil
	a.e.Push(ctx, a.w)
	b.e.Pull(ctx, b.w)
	write(t, a.w.Path, "level.dat", []byte("new"))
	a.e.Push(ctx, a.w)
	before := readAll(t, b.w.Path)
	b.e.IsOpen = func(string) bool { return true }
	if _, err := b.e.Pull(ctx, b.w); !errors.Is(err, ErrWorldOpen) {
		t.Fatal(err)
	}
	if !bytes.Equal(before["level.dat"], readAll(t, b.w.Path)["level.dat"]) {
		t.Fatal("open world was modified")
	}
}

func TestConflictMatrix(t *testing.T) {
	setup := func(t *testing.T) (dev, dev) {
		a, b, _ := twoDevices(t)
		fixtureWorld(t, a.w.Path, 1)
		a.e.Push(ctx, a.w)
		b.e.Pull(ctx, b.w)
		return a, b
	}
	t.Run("A only", func(t *testing.T) {
		a, b := setup(t)
		write(t, a.w.Path, "level.dat", []byte("A"))
		if _, err := a.e.Push(ctx, a.w); err != nil {
			t.Fatal(err)
		}
		if st, _ := b.e.Status(ctx, b.w); st != StatusRemoteAhead {
			t.Fatal(st)
		}
		if _, err := b.e.Push(ctx, b.w); !errors.Is(err, ErrBehind) { // nothing local changed
			t.Fatalf("want ErrBehind, got %v", err)
		}
		if _, err := b.e.Pull(ctx, b.w); err != nil {
			t.Fatal(err)
		}
		sameWorld(t, a.w.Path, b.w.Path)
	})
	t.Run("B only", func(t *testing.T) {
		a, b := setup(t)
		write(t, b.w.Path, "level.dat", []byte("B"))
		if _, err := b.e.Push(ctx, b.w); err != nil {
			t.Fatal(err)
		}
		if _, err := a.e.Pull(ctx, a.w); err != nil {
			t.Fatal(err)
		}
		sameWorld(t, a.w.Path, b.w.Path)
	})
	t.Run("both changed", func(t *testing.T) {
		a, b := setup(t)
		write(t, a.w.Path, "level.dat", []byte("A"))
		write(t, b.w.Path, "level.dat", []byte("B"))
		if _, err := a.e.Push(ctx, a.w); err != nil {
			t.Fatal(err)
		}
		var ce *ConflictError
		if _, err := b.e.Push(ctx, b.w); !errors.As(err, &ce) {
			t.Fatalf("push: want conflict, got %v", err)
		}
		if _, err := b.e.Pull(ctx, b.w); !errors.As(err, &ce) {
			t.Fatalf("pull: want conflict, got %v", err)
		}
		if got := readAll(t, b.w.Path)["level.dat"]; string(got) != "B" {
			t.Fatal("local world was overwritten by a conflicting pull")
		}
		if st, _ := b.e.Status(ctx, b.w); st != StatusConflict {
			t.Fatal(st)
		}
	})
}

func conflicted(t *testing.T) (dev, dev) {
	a, b, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	a.e.Push(ctx, a.w)
	b.e.Pull(ctx, b.w)
	write(t, a.w.Path, "level.dat", []byte("A"))
	write(t, b.w.Path, "level.dat", []byte("B"))
	a.e.Push(ctx, a.w)
	return a, b
}

func TestResolveKeepLocal(t *testing.T) {
	a, b := conflicted(t)
	if _, err := b.e.Resolve(ctx, b.w, KeepLocal); err != nil {
		t.Fatal(err)
	}
	if _, err := a.e.Pull(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	if string(readAll(t, a.w.Path)["level.dat"]) != "B" {
		t.Fatal("B's version should have won")
	}
}

func TestResolveKeepRemoteSnapshotsLocal(t *testing.T) {
	_, b := conflicted(t)
	r, err := b.e.Resolve(ctx, b.w, KeepRemote)
	if err != nil {
		t.Fatal(err)
	}
	if string(readAll(t, b.w.Path)["level.dat"]) != "A" {
		t.Fatal("remote should have won")
	}
	snaps, _ := b.e.Snapshots(b.w.ID)
	if len(snaps) != 1 || string(readAll(t, snaps[0].Path)["level.dat"]) != "B" {
		t.Fatalf("local version not preserved in snapshot (%v, %v)", r, snaps)
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatal(st)
	}
}

func TestResolveKeepBothAndNewest(t *testing.T) {
	_, b := conflicted(t)
	r, err := b.e.Resolve(ctx, b.w, KeepBoth)
	if err != nil {
		t.Fatal(err)
	}
	if string(readAll(t, r.Copy)["level.dat"]) != "B" || string(readAll(t, b.w.Path)["level.dat"]) != "A" {
		t.Fatal("keep-both did not preserve both")
	}
	if worlds.IsWorld(r.Copy) && strings.HasPrefix(filepath.Base(r.Copy), ".") {
		t.Fatal("copy must be a normal folder")
	}

	a2, b2 := conflicted(t)
	_ = a2
	future := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(b2.w.Path, "level.dat"), future, future)
	rr, err := b2.e.Resolve(ctx, b2.w, KeepNewest)
	if err != nil || rr.Used != KeepLocal {
		t.Fatal(rr, err)
	}
}

// failingStore fails PutManifest, simulating a push killed after the packs
// were uploaded but before the manifest.
type failingStore struct {
	store.Store
	failManifest bool
}

func (f *failingStore) PutManifest(c context.Context, w, n string, d []byte) error {
	if f.failManifest {
		return errors.New("simulated crash")
	}
	return f.Store.PutManifest(c, w, n, d)
}

func TestInterruptedPushLeavesStoreConsistent(t *testing.T) {
	a, b, shared := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	a.e.Push(ctx, a.w)
	b.e.Pull(ctx, b.w)

	write(t, a.w.Path, "region/r.0.1.mca", rnd(99, 2<<20))
	fs := &failingStore{Store: localdir.New(shared), failManifest: true}
	a.e.Store = fs
	if _, err := a.e.Push(ctx, a.w); err == nil {
		t.Fatal("expected failure")
	}
	// Others see no change; the orphaned packs are harmless.
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatal(st)
	}
	// Retry succeeds and B gets the data.
	fs.failManifest = false
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	if _, err := b.e.Pull(ctx, b.w); err != nil {
		t.Fatal(err)
	}
	sameWorld(t, a.w.Path, b.w.Path)
}

func TestCorruptDownloadRejectedWorldIntact(t *testing.T) {
	a, b, shared := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	a.e.Push(ctx, a.w)
	b.e.Pull(ctx, b.w)
	write(t, a.w.Path, "level.dat", rnd(7, 3000))
	a.e.Push(ctx, a.w)

	// Corrupt every pack in the store (flip a byte, keep the size).
	packs, _ := os.ReadDir(filepath.Join(shared, "packs"))
	for _, p := range packs {
		path := filepath.Join(shared, "packs", p.Name())
		bs, _ := os.ReadFile(path)
		bs[len(bs)/2] ^= 0xff
		os.WriteFile(path, bs, 0o644)
	}
	before := readAll(t, b.w.Path)
	if _, err := b.e.Pull(ctx, b.w); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("want ErrIntegrity, got %v", err)
	}
	after := readAll(t, b.w.Path)
	if len(before) != len(after) || !bytes.Equal(before["level.dat"], after["level.dat"]) {
		t.Fatal("world changed after a failed pull")
	}
	if _, err := os.Stat(stagingPath(b.w.Path)); err == nil {
		t.Fatal("staging dir left behind")
	}
}

func TestRecoverAfterCrashMidSwap(t *testing.T) {
	_, b, _ := twoDevices(t)
	fixtureWorld(t, b.w.Path, 1)
	// Crash after the old world was moved aside but before the new one landed.
	if err := os.Rename(b.w.Path, oldPath(b.w.Path)); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(stagingPath(b.w.Path), 0o755)
	write(t, stagingPath(b.w.Path), "level.dat", []byte("half-built"))
	if err := Recover(b.w.Path); err != nil {
		t.Fatal(err)
	}
	if !worlds.IsWorld(b.w.Path) || len(readAll(t, b.w.Path)) != 6 {
		t.Fatal("old world not restored")
	}
	for _, p := range []string{stagingPath(b.w.Path), oldPath(b.w.Path)} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s left behind", p)
		}
	}
}

func TestSnapshotsPruneAndRestore(t *testing.T) {
	a, b, _ := twoDevices(t)
	b.e.SnapshotsKeep = 2
	fixtureWorld(t, a.w.Path, 1)
	a.e.Push(ctx, a.w)
	b.e.Pull(ctx, b.w) // fresh world: no snapshot
	if s, _ := b.e.Snapshots(b.w.ID); len(s) != 0 {
		t.Fatal("snapshot taken for a brand new world")
	}
	for i := 0; i < 4; i++ {
		write(t, a.w.Path, "level.dat", []byte{byte('0' + i)})
		a.e.Push(ctx, a.w)
		b.e.Pull(ctx, b.w)
	}
	snaps, _ := b.e.Snapshots(b.w.ID)
	if len(snaps) != 2 {
		t.Fatalf("kept %d snapshots, want 2", len(snaps))
	}
	newest := snaps[len(snaps)-1]
	if err := b.e.Restore(b.w, newest.ID); err != nil {
		t.Fatal(err)
	}
	if string(readAll(t, b.w.Path)["level.dat"]) != "2" {
		t.Fatal("restore did not bring back previous version")
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusLocalAhead {
		t.Fatal(st)
	}
	if err := b.e.Restore(b.w, "../x"); err == nil {
		t.Fatal("accepted bad snapshot id")
	}
}

func TestNewDeviceWithDifferentLocalWorldConflicts(t *testing.T) {
	a, b, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	a.e.Push(ctx, a.w)
	fixtureWorld(t, b.w.Path, 50) // unrelated world with the same name
	var ce *ConflictError
	if _, err := b.e.Pull(ctx, b.w); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	if _, err := b.e.Push(ctx, b.w); !errors.As(err, &ce) {
		t.Fatal(err)
	}
}

// Two heads (a true concurrent push) are merged by a resolution.
func TestDivergedHeadsMergedByResolve(t *testing.T) {
	a, b, shared := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	a.e.Push(ctx, a.w)
	b.e.Pull(ctx, b.w)
	write(t, a.w.Path, "level.dat", []byte("A"))
	a.e.Push(ctx, a.w)
	// Forge a sibling of A's head from another device, as a racing push would.
	hs, _ := a.e.heads(ctx, a.w.ID)
	sib := *hs[0].M
	sib.DeviceID = "ffffff"
	sib.CreatedAt = sib.CreatedAt.Add(-time.Minute)
	data, _ := sib.Marshal()
	if err := localdir.New(shared).PutManifest(ctx, a.w.ID, sib.Name(), data); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusConflict {
		t.Fatalf("two heads must be a conflict, got %s", st)
	}
	if _, err := b.e.Resolve(ctx, b.w, KeepRemote); err != nil {
		t.Fatal(err)
	}
	if hs, _ := a.e.heads(ctx, a.w.ID); len(hs) != 1 {
		t.Fatalf("heads after merge = %d", len(hs))
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatal(st)
	}
}
