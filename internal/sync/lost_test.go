package sync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/worlds"
)

func init() { packRecheckDelay = 0 } // the fake store is consistent

// twoVersions: A pushes v1 then v2, and B pulls v2, so B has seen both.
func twoVersions(t *testing.T) (a, b dev, shared string) {
	t.Helper()
	a, b, shared = twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	write(t, a.w.Path, "level.dat", []byte("version two"))
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	if _, err := b.e.Pull(ctx, b.w); err != nil {
		t.Fatal(err)
	}
	return a, b, shared
}

func manifestFiles(t *testing.T, shared, world string) []string {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(shared, "worlds", world))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func newest(t *testing.T, shared string) string {
	t.Helper()
	names := manifestFiles(t, shared, "Survival")
	var best string
	var g int64
	for _, n := range names {
		if gen, _, ok := manifest.ParseName(n); ok && gen >= g {
			best, g = n, gen
		}
	}
	return best
}

func snapshotOf(t *testing.T, root string) map[string]string {
	out := map[string]string{}
	for k, v := range readAll(t, root) {
		out[k] = string(v)
	}
	return out
}

func sameFiles(t *testing.T, a, b map[string]string) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("world changed: %d files before, %d after", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("world changed: %s differs", k)
		}
	}
}

func TestRolledBackCloudIsNotSilentlyPulledOver(t *testing.T) {
	_, b, shared := twoVersions(t)
	before := snapshotOf(t, b.w.Path)
	// The cloud "forgets" the newest version: B is now looking at an older state.
	if err := os.Remove(filepath.Join(shared, "worlds", "Survival", newest(t, shared))); err != nil {
		t.Fatal(err)
	}

	if st, err := b.e.Status(ctx, b.w); err != nil || st != StatusCloudLost {
		t.Fatalf("status = %s, %v", st, err)
	}
	var le *LostError
	if res, err := b.e.Sync(ctx, b.w); !errors.As(err, &le) || le.Kind != LostVersions || len(le.Versions) != 1 || le.Remaining != 1 || res.Pulled || res.Pushed {
		t.Fatalf("sync = %+v, %v", res, err)
	}
	if _, err := b.e.Pull(ctx, b.w); !errors.As(err, &le) {
		t.Fatalf("pull: %v", err)
	}
	if _, err := b.e.Push(ctx, b.w); !errors.As(err, &le) {
		t.Fatalf("push: %v", err)
	}
	if _, err := b.e.Resolve(ctx, b.w, KeepRemote); !errors.As(err, &le) {
		t.Fatalf("resolve: %v", err)
	}
	sameFiles(t, before, snapshotOf(t, b.w.Path))
	if p := Explain(le); p.Kind != ProblemCloudLost || p.Level != LevelWarn {
		t.Fatalf("problem = %+v", p)
	}
}

func TestWorldDeletedFromCloudIsNotQuietlyReuploaded(t *testing.T) {
	_, b, shared := twoVersions(t)
	if err := os.RemoveAll(filepath.Join(shared, "worlds", "Survival")); err != nil {
		t.Fatal(err)
	}
	var le *LostError
	if _, err := b.e.Sync(ctx, b.w); !errors.As(err, &le) || le.Kind != LostWorld {
		t.Fatalf("sync: %v", err)
	}
	if got := manifestFiles(t, shared, "Survival"); len(got) != 0 {
		t.Fatalf("WorldHop re-uploaded by itself: %v", got)
	}
}

func TestAcceptTheCloudAsItIsDoesNotPullOverTheWorld(t *testing.T) {
	_, b, shared := twoVersions(t)
	before := snapshotOf(t, b.w.Path)
	os.Remove(filepath.Join(shared, "worlds", "Survival", newest(t, shared)))

	res, err := b.e.ResolveLost(ctx, b.w, LostAcceptCloud)
	if err != nil || res.Action != LostAcceptCloud {
		t.Fatalf("accept: %+v %v", res, err)
	}
	// The missing history is forgotten; the world is judged like any other. B's
	// copy differs from the older cloud version, so it is a conflict for the
	// person to settle, not a quiet revert.
	if st, _ := b.e.Status(ctx, b.w); st != StatusConflict {
		t.Fatalf("after accepting: %s", st)
	}
	if r, err := b.e.Sync(ctx, b.w); err != nil || r.Pulled || r.Pushed {
		t.Fatalf("a sync after accepting must not act: %+v %v", r, err)
	}
	sameFiles(t, before, snapshotOf(t, b.w.Path))
	// Now the usual choice is theirs: keep this computer's version.
	if _, err := b.e.Resolve(ctx, b.w, KeepLocal); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatalf("after keep-local: %s", st)
	}
	if _, err := b.e.ResolveLost(ctx, b.w, LostAcceptCloud); !errors.Is(err, ErrNotLost) {
		t.Fatalf("nothing left to accept: %v", err)
	}
}

func TestReuploadRebuildsWhatTheCloudLostAndOtherComputersCanFollow(t *testing.T) {
	a, b, shared := twoVersions(t)
	os.RemoveAll(shared) // the whole store is wiped
	os.MkdirAll(shared, 0o755)

	res, err := b.e.ResolveLost(ctx, b.w, LostReupload)
	if err != nil || res.Manifest == "" {
		t.Fatalf("reupload: %+v %v", res, err)
	}
	if gen, _, _ := manifest.ParseName(res.Manifest); gen <= 2 {
		t.Fatalf("the new version must come after every generation seen, got %s", res.Manifest)
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatalf("B after reupload: %s", st)
	}
	// A saw the lost versions too, so it is stopped as well, until its own person chooses.
	if st, _ := a.e.Status(ctx, a.w); st != StatusCloudLost {
		t.Fatalf("A: %s", st)
	}
	if _, err := a.e.ResolveLost(ctx, a.w, LostAcceptCloud); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.e.Status(ctx, a.w); st != StatusSynced { // same contents as what B uploaded
		t.Fatalf("A after accepting: %s", st)
	}
	sameWorld(t, a.w.Path, b.w.Path)
}

func removePacks(t *testing.T, shared string) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(shared, "packs"))
	if err != nil || len(ents) == 0 {
		t.Fatalf("no packs to remove: %v", err)
	}
	for _, e := range ents {
		os.Remove(filepath.Join(shared, "packs", e.Name()))
	}
}

func TestMissingPacksStopAPushInsteadOfBuildingOnThem(t *testing.T) {
	_, b, shared := twoVersions(t)
	removePacks(t, shared)
	write(t, b.w.Path, "level.dat", []byte("edited on B"))
	before := manifestFiles(t, shared, "Survival")

	var le *LostError
	if _, err := b.e.Push(ctx, b.w); !errors.As(err, &le) || le.Kind != LostData || le.Packs == 0 {
		t.Fatalf("push: %v", err)
	}
	if got := manifestFiles(t, shared, "Survival"); len(got) != len(before) {
		t.Fatalf("a version pointing at missing data was written: %v", got)
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusCloudLost {
		t.Fatalf("status = %s", st)
	}
	if _, err := b.e.ResolveLost(ctx, b.w, LostAcceptCloud); !errors.Is(err, ErrCannotAccept) {
		t.Fatalf("accepting lost data: %v", err)
	}

	// The person chooses to rebuild from this computer.
	res, err := b.e.ResolveLost(ctx, b.w, LostReupload)
	if err != nil || res.Manifest == "" {
		t.Fatalf("reupload: %+v %v", res, err)
	}
	if st, _ := b.e.Status(ctx, b.w); st != StatusSynced {
		t.Fatalf("after reupload: %s", st)
	}
	// A computer that never had the world can now download it.
	c := twoDevicesPeer(t, shared)
	if _, err := c.e.Pull(ctx, c.w); err != nil {
		t.Fatalf("download after the rebuild: %v", err)
	}
	if string(readAll(t, c.w.Path)["level.dat"]) != "edited on B" {
		t.Fatal("downloaded the wrong version")
	}
}

func twoDevicesPeer(t *testing.T, shared string) dev {
	t.Helper()
	base := t.TempDir()
	e, err := New(storeAt(shared), filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return dev{e, worlds.World{ID: "Survival", Name: "Survival", Path: filepath.Join(base, "saves", "Survival")}}
}

func TestMissingPacksStopADownloadAndNothingIsPatchedUp(t *testing.T) {
	_, _, shared := twoVersions(t)
	removePacks(t, shared)
	c := twoDevicesPeer(t, shared)
	var le *LostError
	if _, err := c.e.Pull(ctx, c.w); !errors.As(err, &le) || le.Kind != LostData {
		t.Fatalf("pull: %v", err)
	}
	if worlds.IsWorld(c.w.Path) {
		t.Fatal("a half-built world was left behind")
	}
	if st, _ := c.e.Status(ctx, c.w); st != StatusCloudLost {
		t.Fatalf("status = %s", st)
	}
	if _, err := c.e.ResolveLost(ctx, c.w, LostReupload); !errors.Is(err, ErrNoLocalCopy) {
		t.Fatalf("reupload with no local copy: %v", err)
	}
}

func TestResolveLostRules(t *testing.T) {
	_, b, shared := twoVersions(t)
	if _, err := b.e.ResolveLost(ctx, b.w, LostAcceptCloud); !errors.Is(err, ErrNotLost) {
		t.Fatalf("nothing lost: %v", err)
	}
	if _, err := b.e.ResolveLost(ctx, b.w, "heal"); !errors.Is(err, ErrBadLostAction) {
		t.Fatalf("bad action: %v", err)
	}
	os.Remove(filepath.Join(shared, "worlds", "Survival", newest(t, shared)))
	b.e.IsOpen = func(string) bool { return true }
	for _, a := range []LostAction{LostAcceptCloud, LostReupload} {
		if _, err := b.e.ResolveLost(ctx, b.w, a); !errors.Is(err, ErrWorldOpen) {
			t.Fatalf("%s while the world is open: %v", a, err)
		}
	}
}

func TestRunnerShowsWhatTheCloudLost(t *testing.T) {
	_, b, shared := twoVersions(t)
	os.Remove(filepath.Join(shared, "worlds", "Survival", newest(t, shared)))
	r := NewRunner("t", b.e, func() []worlds.World { return []worlds.World{b.w} })

	ws := r.Worlds(ctx)
	if len(ws) != 1 || ws[0].Status != "cloud-lost" || ws[0].Lost == nil || ws[0].Lost.Versions != 1 || !ws[0].Lost.CanAccept || !ws[0].Lost.CanReupload {
		t.Fatalf("worlds = %+v", ws)
	}
	st := r.ForceSync(ctx)
	if st.Outcome != "attention" || len(st.Problems) != 1 || st.Problems[0].Kind != ProblemCloudLost || !strings.Contains(strings.Join(st.Problems[0].Worlds, ","), "Survival") {
		t.Fatalf("sync status = %+v", st)
	}
	if _, err := r.ResolveLost(ctx, "Survival", LostAcceptCloud); err != nil {
		t.Fatal(err)
	}
	if ws := r.Worlds(ctx); ws[0].Status != "conflict" || ws[0].Lost != nil {
		t.Fatalf("after accepting: %+v", ws[0])
	}
}

func storeAt(root string) store.Store { return localdir.New(root) }
