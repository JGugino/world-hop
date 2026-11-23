package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/worlds"
)

type schedFixture struct {
	t      *testing.T
	a, b   *Engine
	wa, wb worlds.World
	open   map[string]bool
	ra     *Runner
	sa     *Scheduler
	now    time.Time
}

func newSchedFixture(t *testing.T) *schedFixture {
	t.Helper()
	shared := filepath.Join(t.TempDir(), "store")
	mk := func() (*Engine, worlds.World) {
		base := t.TempDir()
		e, err := New(localdir.New(shared), filepath.Join(base, "state"))
		if err != nil {
			t.Fatal(err)
		}
		return e, worlds.World{ID: "Survival", Name: "Survival", Path: filepath.Join(base, "saves", "Survival")}
	}
	f := &schedFixture{t: t, open: map[string]bool{}, now: time.Unix(1_000_000, 0)}
	f.a, f.wa = mk()
	f.b, f.wb = mk()
	f.a.IsOpen = func(p string) bool { return f.open[p] }
	f.ra = NewRunner("t", f.a, func() []worlds.World { return []worlds.World{f.wa} })
	f.sa = NewScheduler(f.ra, f.ra.listWorlds, func(p string) bool { return f.open[p] },
		SchedulerConfig{PollInterval: time.Second, SyncInterval: time.Hour, Settle: 3 * time.Second})
	f.sa.now = func() time.Time { return f.now }
	return f
}

func smallWorld(t *testing.T, root string, seed int64) {
	write(t, root, "level.dat", rnd(seed, 2000))
	write(t, root, "session.lock", []byte("STALE-LOCK"))
	write(t, root, "region/r.0.0.mca", rnd(seed+1, 200<<10))
}

// seedBoth makes both devices hold the same pushed world.
func (f *schedFixture) seedBoth() {
	smallWorld(f.t, f.wa.Path, 1)
	if _, err := f.a.Push(ctx, f.wa); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.b.Pull(ctx, f.wb); err != nil {
		f.t.Fatal(err)
	}
}

func (f *schedFixture) advance(d time.Duration) { f.now = f.now.Add(d) }

func TestSchedulerStartupPullsRemote(t *testing.T) {
	f := newSchedFixture(t)
	f.seedBoth()
	write(t, f.wb.Path, "level.dat", rnd(7, 3000))
	if _, err := f.b.Push(ctx, f.wb); err != nil {
		t.Fatal(err)
	}
	f.sa.Poll(ctx) // startup pass
	if got, _ := os.ReadFile(filepath.Join(f.wa.Path, "level.dat")); string(got) != string(rnd(7, 3000)) {
		t.Fatalf("world not pulled at startup: %s", f.ra.Status().LastResult)
	}
	if !strings.Contains(f.ra.Status().LastResult, "1 downloaded") {
		t.Fatalf("result: %s", f.ra.Status().LastResult)
	}
}

func TestSchedulerPushesOnCloseAfterSettle(t *testing.T) {
	f := newSchedFixture(t)
	smallWorld(t, f.wa.Path, 1)
	f.open[f.wa.Path] = true
	f.sa.Poll(ctx) // startup pass: world open, must be skipped
	if len(f.ra.Status().Problems) != 1 || f.ra.Status().Problems[0].Kind != ProblemWorldOpen {
		t.Fatalf("open world not skipped: %s", f.ra.Status().LastResult)
	}
	if st, _ := f.b.Status(ctx, f.wb); st != StatusNoRemote {
		t.Fatalf("open world was uploaded: %s", st)
	}

	f.advance(time.Second)
	f.sa.Poll(ctx) // still open: nothing

	f.open[f.wa.Path] = false // closes
	f.advance(time.Second)
	f.sa.Poll(ctx) // closed but not settled
	if st, _ := f.b.Status(ctx, f.wb); st != StatusNoRemote {
		t.Fatalf("pushed before settling: %s", st)
	}
	f.advance(4 * time.Second)
	f.sa.Poll(ctx) // settled: push
	if st, _ := f.b.Status(ctx, f.wb); st != StatusNoLocal {
		t.Fatalf("not pushed after close: %s (%s)", st, f.ra.Status().LastResult)
	}
	// The stale lock must never reach the store.
	if _, err := f.b.Pull(ctx, f.wb); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.wb.Path, "session.lock")); err == nil {
		t.Fatal("session.lock was uploaded")
	}
}

func TestSchedulerReopenBeforeSettleCancelsPush(t *testing.T) {
	f := newSchedFixture(t)
	smallWorld(t, f.wa.Path, 1)
	f.open[f.wa.Path] = true
	f.sa.Poll(ctx)
	f.open[f.wa.Path] = false
	f.advance(time.Second)
	f.sa.Poll(ctx)
	f.open[f.wa.Path] = true // relaunch
	f.advance(time.Second)
	f.sa.Poll(ctx)
	f.open[f.wa.Path] = false
	f.advance(time.Second)
	f.sa.Poll(ctx) // closed again, settle restarts
	if st, _ := f.b.Status(ctx, f.wb); st != StatusNoRemote {
		t.Fatalf("pushed too early: %s", st)
	}
}

func TestSchedulerTimerPullAndOpenWarning(t *testing.T) {
	f := newSchedFixture(t)
	f.seedBoth()
	f.sa.Poll(ctx) // startup: in sync
	write(t, f.wb.Path, "level.dat", rnd(8, 3000))
	if _, err := f.b.Push(ctx, f.wb); err != nil {
		t.Fatal(err)
	}
	level := func() string { b, _ := os.ReadFile(filepath.Join(f.wa.Path, "level.dat")); return string(b) }
	f.advance(time.Minute)
	f.sa.Poll(ctx) // before SyncInterval: no pull
	if level() == string(rnd(8, 3000)) {
		t.Fatal("pulled before timer")
	}
	f.advance(time.Hour)
	f.sa.Poll(ctx)
	if level() != string(rnd(8, 3000)) {
		t.Fatalf("timer pull failed: %s", f.ra.Status().LastResult)
	}

	// Newer remote while the world is open locally: warn, don't pull.
	write(t, f.wb.Path, "level.dat", rnd(9, 3000))
	if _, err := f.b.Push(ctx, f.wb); err != nil {
		t.Fatal(err)
	}
	f.open[f.wa.Path] = true
	f.advance(2 * time.Hour)
	f.sa.Poll(ctx)
	if level() != string(rnd(8, 3000)) {
		t.Fatal("open world was overwritten")
	}
	if w := f.ra.Status().Warnings; len(w) != 1 || !strings.Contains(w[0], "open in Minecraft") {
		t.Fatalf("warnings: %v", w)
	}
}

func TestForceSyncSharesLockWithScheduler(t *testing.T) {
	f := newSchedFixture(t)
	smallWorld(t, f.wa.Path, 1)
	st := f.ra.ForceSync(ctx)
	if !strings.Contains(st.LastResult, "1 uploaded") {
		t.Fatalf("force sync: %s", st.LastResult)
	}
	f.ra.pass.Lock()
	st = f.ra.ForceSync(ctx) // a pass is "running": returns without starting another
	f.ra.pass.Unlock()
	if st.State != "idle" {
		t.Fatalf("state: %+v", st)
	}
}
