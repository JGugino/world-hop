package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// newBRunner builds a second computer with an empty saves folder that shares
// the store at shared.
func newBRunner(t *testing.T, shared string) (r *Runner, e *Engine, saves string) {
	t.Helper()
	base := t.TempDir()
	saves = filepath.Join(base, "saves")
	if err := os.MkdirAll(saves, 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := New(localdir.New(shared), filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	list := func() []worlds.World {
		ws, _ := worlds.Resolve([]worlds.Source{{Kind: worlds.KindSaves, Path: saves}})
		return ws
	}
	r = NewRunner("test", e, list)
	r.SavesFolders = func() []string { return []string{saves} }
	return r, e, saves
}

func pushedFromA(t *testing.T) (a dev, shared string) {
	t.Helper()
	a, _, shared = twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	return a, shared
}

func TestCloudOnlyWorldIsListedAndDownloaded(t *testing.T) {
	a, shared := pushedFromA(t)
	rb, eb, saves := newBRunner(t, shared)

	cw, err := rb.CloudWorlds(ctx)
	if err != nil || len(cw) != 1 || cw[0].ID != "Survival" || cw[0].Size == 0 || cw[0].Files == 0 || cw[0].Seen {
		t.Fatalf("CloudWorlds = %+v, %v", cw, err)
	}
	res, err := rb.DownloadWorld(ctx, "Survival", saves)
	if err != nil || !res.Updated {
		t.Fatalf("DownloadWorld: %+v %v", res, err)
	}
	dest := filepath.Join(saves, "Survival")
	got, want := readAll(t, dest), readAll(t, a.w.Path)
	delete(want, "session.lock") // never synced
	if len(got) != len(want) {
		t.Fatalf("file count %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if !bytes.Equal(got[k], v) {
			t.Fatalf("%s differs after download", k)
		}
	}
	// No temp folders left behind, and the world now syncs as up to date.
	ents, _ := os.ReadDir(saves)
	for _, e := range ents {
		if IsInternalName(e.Name()) {
			t.Fatalf("leftover %s", e.Name())
		}
	}
	st, err := eb.Status(ctx, worlds.World{ID: "Survival", Path: dest})
	if err != nil || st != StatusSynced {
		t.Fatalf("status after download = %v %v", st, err)
	}
	if cw, _ := rb.CloudWorlds(ctx); len(cw) != 0 {
		t.Fatalf("still listed after download: %+v", cw)
	}
}

func TestDownloadNeverOverwrites(t *testing.T) {
	_, shared := pushedFromA(t)
	rb, eb, saves := newBRunner(t, shared)

	// A non-world folder with the same name: refused and untouched.
	dest := filepath.Join(saves, "Survival")
	write(t, dest, "notes.txt", []byte("mine"))
	if _, err := eb.Download(ctx, "Survival", saves); !errors.Is(err, ErrDestExists) {
		t.Fatalf("want ErrDestExists, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "notes.txt")); string(b) != "mine" {
		t.Fatal("existing folder was modified")
	}
	// Local world already present under the same id: refused via the runner.
	os.RemoveAll(dest)
	fixtureWorld(t, dest, 9)
	if _, err := rb.DownloadWorld(ctx, "Survival", saves); !errors.Is(err, ErrDestExists) {
		t.Fatalf("want ErrDestExists for local world, got %v", err)
	}
	// Only configured saves folders are valid targets.
	if _, err := rb.DownloadWorld(ctx, "Survival", t.TempDir()); !errors.Is(err, ErrBadTarget) {
		t.Fatalf("want ErrBadTarget, got %v", err)
	}
	if _, err := rb.DownloadWorld(ctx, "Nope", saves); !errors.Is(err, ErrDestExists) && err == nil {
		t.Fatal("expected an error")
	}
}

func TestDownloadUnknownAndBadID(t *testing.T) {
	_, shared := pushedFromA(t)
	_, eb, saves := newBRunner(t, shared)
	if _, err := eb.Download(ctx, "Nope", saves); !errors.Is(err, ErrNotInStore) {
		t.Fatalf("want ErrNotInStore, got %v", err)
	}
	for _, id := range []string{"../evil", "", ".."} {
		if _, err := eb.Download(ctx, id, saves); err == nil {
			t.Fatalf("id %q accepted", id)
		}
	}
	if ents, _ := os.ReadDir(filepath.Dir(saves)); len(ents) != 2 { // saves + state
		t.Fatalf("unexpected files created: %v", ents)
	}
}

func TestDownloadVerifiesHashesAndLeavesNothing(t *testing.T) {
	_, shared := pushedFromA(t)
	_, eb, saves := newBRunner(t, shared)

	// Corrupt every pack in the shared store.
	packs, _ := filepath.Glob(filepath.Join(shared, "packs", "*"))
	if len(packs) == 0 {
		t.Fatal("no packs")
	}
	for _, p := range packs {
		b, _ := os.ReadFile(p)
		b[len(b)/2] ^= 0xff
		os.WriteFile(p, b, 0o644)
	}
	if _, err := eb.Download(ctx, "Survival", saves); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("want ErrIntegrity, got %v", err)
	}
	if ents, _ := os.ReadDir(saves); len(ents) != 0 {
		t.Fatalf("a failed download left %v behind", ents)
	}
	if eb.HasState("Survival") {
		t.Fatal("state saved for a failed download")
	}
}

func TestWorldWithEquivalentLocalFolderNameIsNotCloudOnly(t *testing.T) {
	_, shared := pushedFromA(t) // store id "Survival"
	rb, _, saves := newBRunner(t, shared)
	// Same id by the sanitizing rule, different folder name.
	if worlds.IDFromName("Survival!") != "Survival" {
		t.Fatal("test premise: ids should match")
	}
	fixtureWorld(t, filepath.Join(saves, "Survival!"), 5)
	cw, err := rb.CloudWorlds(ctx)
	if err != nil || len(cw) != 0 {
		t.Fatalf("world present locally under another name was listed: %+v %v", cw, err)
	}
	if _, err := rb.DownloadWorld(ctx, "Survival", saves); !errors.Is(err, ErrDestExists) {
		t.Fatalf("want ErrDestExists, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(saves, "Survival")); err == nil {
		t.Fatal("duplicate copy created")
	}
}

func TestForceSyncAutoDownloadsNewWorldsOnce(t *testing.T) {
	_, shared := pushedFromA(t)
	rb, _, saves := newBRunner(t, shared)
	rb.AutoDownload = true

	st := rb.ForceSync(ctx)
	if !strings.Contains(st.LastResult, "1 downloaded") {
		t.Fatalf("result = %q", st.LastResult)
	}
	dest := filepath.Join(saves, "Survival")
	if !worlds.IsWorld(dest) {
		t.Fatal("world not downloaded")
	}
	// The player deletes it on purpose: it must not come back by itself...
	os.RemoveAll(dest)
	st = rb.ForceSync(ctx)
	if strings.Contains(st.LastResult, "downloaded") || worlds.IsWorld(dest) {
		t.Fatalf("deleted world was re-downloaded: %q", st.LastResult)
	}
	// ...but is still offered for a manual download.
	cw, _ := rb.CloudWorlds(ctx)
	if len(cw) != 1 || !cw[0].Seen {
		t.Fatalf("CloudWorlds = %+v", cw)
	}
	if _, err := rb.DownloadWorld(ctx, "Survival", saves); err != nil {
		t.Fatal(err)
	}
}

func TestAutoDownloadOffWithSeveralSavesFoldersOrDisabled(t *testing.T) {
	_, shared := pushedFromA(t)
	rb, _, saves := newBRunner(t, shared)
	rb.AutoDownload = true
	rb.SavesFolders = func() []string { return []string{saves, t.TempDir()} }
	if st := rb.ForceSync(ctx); strings.Contains(st.LastResult, "downloaded") || worlds.IsWorld(filepath.Join(saves, "Survival")) {
		t.Fatalf("auto-download with two saves folders: %q", st.LastResult)
	}
	rb.SavesFolders = func() []string { return []string{saves} }
	rb.AutoDownload = false
	if st := rb.ForceSync(ctx); worlds.IsWorld(filepath.Join(saves, "Survival")) {
		t.Fatalf("auto-download while disabled: %q", st.LastResult)
	}
}

func TestSyncingOneWorldDoesNotAutoDownload(t *testing.T) {
	_, shared := pushedFromA(t)
	rb, _, saves := newBRunner(t, shared)
	rb.AutoDownload = true
	fixtureWorld(t, filepath.Join(saves, "Other"), 3)
	ws := rb.listWorlds()
	rb.SyncWorlds(ctx, ws) // the scheduler's "world just closed" path
	if worlds.IsWorld(filepath.Join(saves, "Survival")) {
		t.Fatal("subset sync downloaded a world")
	}
}

// staleStore lists only the manifests it was given, as if another device's
// upload had not become visible yet.
type staleStore struct {
	store.Store
	visible []string
}

func (s staleStore) ListManifests(context.Context, string) ([]string, error) { return s.visible, nil }

func TestDivergedWorldNotAutoDownloaded(t *testing.T) {
	a, b, shared := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	v1, _ := a.e.Store.ListManifests(ctx, "Survival")
	if _, err := b.e.Pull(ctx, b.w); err != nil {
		t.Fatal(err)
	}
	write(t, b.w.Path, "extra-b.dat", []byte("b"))
	if _, err := b.e.Push(ctx, b.w); err != nil {
		t.Fatal(err)
	}
	// A pushes without seeing B's upload: two heads.
	write(t, a.w.Path, "extra-a.dat", []byte("a"))
	a.e.Store = staleStore{a.e.Store, v1}
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	rc, _, saves := newBRunner(t, shared)
	rc.AutoDownload = true
	cw, _ := rc.CloudWorlds(ctx)
	if len(cw) != 1 || !cw[0].Diverged {
		t.Fatalf("CloudWorlds = %+v", cw)
	}
	rc.ForceSync(ctx)
	if worlds.IsWorld(filepath.Join(saves, "Survival")) {
		t.Fatal("diverged world was auto-downloaded")
	}
	// A manual download is allowed; the next sync reports the conflict.
	if _, err := rc.DownloadWorld(ctx, "Survival", saves); err != nil {
		t.Fatal(err)
	}
	if st := rc.ForceSync(ctx); !strings.Contains(st.LastResult, "1 in conflict") {
		t.Fatalf("result = %q", st.LastResult)
	}
}
