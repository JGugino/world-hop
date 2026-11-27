package crypt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/worlds"
)

const newPass = "a different long passphrase"

// gated holds PutPack until released, so a test can look at a change that is
// still running.
type gated struct {
	store.Store
	mu      stdsync.Mutex
	gate    chan struct{}
	entered chan struct{}
}

func (g *gated) PutPack(ctx context.Context, name string, data []byte) error {
	g.mu.Lock()
	gate, entered := g.gate, g.entered
	g.mu.Unlock()
	if gate != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.Store.PutPack(ctx, name, data)
}

func (g *gated) hold() (release func(), entered chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate, g.entered = make(chan struct{}), make(chan struct{}, 8)
	gate := g.gate
	var once stdsync.Once
	return func() { once.Do(func() { close(gate) }) }, g.entered
}

func (g *gated) open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate = nil
}

type computer struct {
	m     *Manager
	e     *sync.Engine
	dir   string
	state string
	keys  memKeys
	clock *time.Time
}

func newComputer(t *testing.T, shared store.Store) *computer {
	t.Helper()
	c := &computer{dir: t.TempDir(), state: t.TempDir(), keys: memKeys{}}
	now := time.Now()
	c.clock = &now
	m, err := NewManager(shared, c.dir, c.keys, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	m.Params = fast
	m.Now = func() time.Time { return *c.clock }
	e, err := sync.New(m, c.state)
	if err != nil {
		t.Fatal(err)
	}
	e.StateScope = m.Scope
	c.m, c.e = m, e
	return c
}

func (c *computer) wait(t *testing.T) Rekey {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c.m.WaitChangePassphrase(ctx)
	in := c.m.Info(context.Background())
	if in.Rekey == nil {
		t.Fatal("no rekey state")
	}
	return *in.Rekey
}

func writeLevel(t *testing.T, w worlds.World, s string) {
	t.Helper()
	os.WriteFile(filepath.Join(w.Path, "level.dat"), []byte(s), 0o644)
	os.WriteFile(filepath.Join(w.Path, "region", "r.0.0.mca"), bytes.Repeat([]byte(s), 4000), 0o644)
}

func TestChangePassphraseEndToEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	shared := localdir.New(root)
	a, b := newComputer(t, shared), newComputer(t, shared)

	if err := a.m.Enable(ctx, pass, pass, true, true); err != nil {
		t.Fatal(err)
	}
	wa := world(t, "v1")
	if _, err := a.e.Push(ctx, wa); err != nil {
		t.Fatal(err)
	}
	writeLevel(t, wa, "v2-longer")
	if _, err := a.e.Push(ctx, wa); err != nil { // two versions: a history
		t.Fatal(err)
	}
	if err := b.m.Enable(ctx, pass, "", false, false); err != nil {
		t.Fatal(err)
	}
	wb := worlds.World{ID: "W1", Name: "W1", Path: filepath.Join(t.TempDir(), "W1")}
	if _, err := b.e.Pull(ctx, wb); err != nil {
		t.Fatal(err)
	}

	// Computer A changes the passphrase.
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, true); err != nil {
		t.Fatal(err)
	}
	if r := a.wait(t); r.State != RekeyDone || r.Total == 0 || r.Done != r.Total {
		t.Fatalf("rekey = %+v", r)
	}
	h, _ := ReadHeader(ctx, shared)
	if h.Generation() != 2 {
		t.Fatalf("header generation = %d", h.Generation())
	}
	if names, _ := shared.ListManifests(ctx, keyWorld); len(names) != 2 {
		t.Fatalf("both headers must remain: %v", names)
	}
	if len(a.keys) != 1 {
		t.Fatalf("keychain should hold only the new key: %d entries", len(a.keys))
	}

	// No phantom conflict: the sync record is still valid under the new key.
	if st, err := a.e.Status(ctx, wa); err != nil || st != sync.StatusSynced {
		t.Fatalf("A after change: %s %v", st, err)
	}
	writeLevel(t, wa, "v3-after-change")
	if _, err := a.e.Push(ctx, wa); err != nil {
		t.Fatalf("A cannot push under the new key: %v", err)
	}

	// Nothing readable by the old passphrase's keys in the new layout, and
	// nothing in plaintext anywhere.
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if bb, _ := os.ReadFile(p); bytes.Contains(bb, []byte("v3-after-change")) || bytes.Contains(bb, []byte("level.dat")) {
				t.Errorf("plaintext in %s", p)
			}
		}
		return nil
	})

	// Computer B still holds the old key. After the recheck interval it notices.
	*b.clock = b.clock.Add(2 * headerRecheck)
	if _, err := b.m.ListWorlds(ctx); !errors.Is(err, ErrPassphraseChanged) {
		t.Fatalf("B should notice the change, got %v", err)
	}
	if in := b.m.Info(ctx); in.Unlocked || !in.KeyChanged {
		t.Fatalf("B info: %+v", in)
	}
	if err := b.m.Unlock(ctx, pass, false); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("old passphrase must stop working: %v", err)
	}
	if err := b.m.Unlock(ctx, newPass, false); err != nil {
		t.Fatal(err)
	}
	if in := b.m.Info(ctx); !in.Unlocked || in.KeyChanged {
		t.Fatalf("B after unlock: %+v", in)
	}
	if st, _ := b.e.Status(ctx, wb); st != sync.StatusRemoteAhead {
		t.Fatalf("B should see A's v3 as newer, got %s", st)
	}
	if _, err := b.e.Pull(ctx, wb); err != nil {
		t.Fatalf("B pull after change: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(wb.Path, "level.dat")); string(got) != "v3-after-change" {
		t.Fatalf("B pulled %q", got)
	}
}

func TestOtherComputerNoticesAfterRestart(t *testing.T) {
	ctx := context.Background()
	shared := localdir.New(t.TempDir())
	a, b := newComputer(t, shared), newComputer(t, shared)
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	if err := b.m.Enable(ctx, pass, "", false, true); err != nil { // B remembers the old key
		t.Fatal(err)
	}
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
	a.wait(t)

	// B restarts: its keychain holds the old key, which no longer matches.
	b2, _ := NewManager(shared, b.dir, b.keys, func() bool { return true })
	if _, err := b2.ListWorlds(ctx); !errors.Is(err, ErrPassphraseChanged) {
		t.Fatalf("after restart: %v", err)
	}
	if len(b.keys) != 0 {
		t.Fatal("the stale key should be dropped from the keychain")
	}
	if in := b2.Info(ctx); !in.KeyChanged {
		t.Fatalf("info: %+v", in)
	}
}

func TestChangePassphraseRules(t *testing.T) {
	ctx := context.Background()
	shared := localdir.New(t.TempDir())
	a := newComputer(t, shared)
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("not enabled: %v", err)
	}
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		cur, next, confirm string
		ack                bool
		want               error
	}{
		"wrong current": {"not the passphrase", newPass, newPass, true, ErrWrongPassphrase},
		"mismatch":      {pass, newPass, "typo", true, ErrNotConfirmed},
		"no ack":        {pass, newPass, newPass, false, ErrNotConfirmed},
		"same":          {pass, pass, pass, true, ErrSamePassphrase},
		"weak":          {pass, "short", "short", true, ErrWeakPassphrase},
	} {
		if err := a.m.StartChangePassphrase(ctx, tc.cur, tc.next, tc.confirm, tc.ack, false); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if in := a.m.Info(ctx); in.Rekey != nil {
		t.Fatalf("a refused request must not start anything: %+v", in.Rekey)
	}
	if h, _ := ReadHeader(ctx, shared); h.Generation() != 1 {
		t.Fatal("a refused request changed the key")
	}
}

func TestChangeWorksFromLockedAndWithoutPro(t *testing.T) {
	ctx := context.Background()
	shared := localdir.New(t.TempDir())
	a := newComputer(t, shared)
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	wa := world(t, "v1")
	if _, err := a.e.Push(ctx, wa); err != nil {
		t.Fatal(err)
	}
	a.m.Lock()
	a.m.Pro = func() bool { return false } // a lapsed license must not strand the data
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
	if r := a.wait(t); r.State != RekeyDone {
		t.Fatalf("rekey = %+v", r)
	}
	if in := a.m.Info(ctx); !in.Unlocked {
		t.Fatalf("changing the passphrase leaves this computer unlocked: %+v", in)
	}
	if st, _ := a.e.Status(ctx, wa); st != sync.StatusSynced {
		t.Fatalf("status %s", st)
	}
}

func TestRunningChangePausesSyncAndCanBeCancelledAndResumed(t *testing.T) {
	ctx := context.Background()
	inner := localdir.New(t.TempDir())
	g := &gated{Store: inner}
	a := newComputer(t, g)
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	wa := world(t, "v1")
	if _, err := a.e.Push(ctx, wa); err != nil {
		t.Fatal(err)
	}

	release, entered := g.hold()
	defer release()
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the copy never started")
	}
	in := a.m.Info(ctx)
	if in.Rekey == nil || in.Rekey.State != RekeyRunning || in.Rekey.Total == 0 || in.Rekey.Worlds != 1 {
		t.Fatalf("running state: %+v", in.Rekey)
	}
	// While it runs: syncing waits, and nothing that would disturb the key works.
	if _, err := a.e.Push(ctx, wa); !errors.Is(err, ErrRekeying) {
		t.Fatalf("push during a change: %v", err)
	}
	if err := a.m.Lock(); !errors.Is(err, ErrRekeying) {
		t.Fatalf("lock during a change: %v", err)
	}
	if err := a.m.Disable(); !errors.Is(err, ErrRekeying) {
		t.Fatalf("disable during a change: %v", err)
	}
	if err := a.m.StartChangePassphrase(ctx, pass, "yet another passphrase", "yet another passphrase", true, false); !errors.Is(err, ErrRekeying) {
		t.Fatalf("second change: %v", err)
	}
	if p := sync.Explain(ErrRekeying); p.Kind != sync.ProblemBusy || p.Level != sync.LevelInfo {
		t.Fatalf("problem = %+v", p)
	}

	// Cancel: nothing was committed, the old key keeps working.
	a.m.CancelChangePassphrase()
	if r := a.wait(t); r.State != RekeyCancelled {
		t.Fatalf("after cancel: %+v", r)
	}
	if h, _ := ReadHeader(ctx, inner); h.Generation() != 1 {
		t.Fatal("a cancelled change must not commit")
	}
	if _, err := os.Stat(filepath.Join(a.dir, "rekey.json")); err != nil {
		t.Fatal("the unfinished change should be remembered so a retry reuses its salt")
	}
	pend := a.m.loadPending()
	g.open()
	if _, err := a.e.Push(ctx, wa); err != nil {
		t.Fatalf("sync after cancel: %v", err)
	}

	// Retry with the same new passphrase: same salt, so the copied data counts.
	a.m.DismissRekey()
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
	if r := a.wait(t); r.State != RekeyDone {
		t.Fatalf("retry: %+v", r)
	}
	if h, _ := ReadHeader(ctx, inner); h.Generation() != 2 || h.Salt != pend.Header.Salt {
		t.Fatalf("retry should reuse the earlier salt (gen %d)", h.Generation())
	}
	if _, err := os.Stat(filepath.Join(a.dir, "rekey.json")); err == nil {
		t.Fatal("a finished change must clear the pending record")
	}
}

func TestVersionsPushedDuringTheCopyAreCarriedOver(t *testing.T) {
	ctx := context.Background()
	inner := localdir.New(t.TempDir())
	g := &gated{Store: inner}
	a := newComputer(t, g)
	b := newComputer(t, inner) // another computer, not paused by A's change
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	if err := b.m.Enable(ctx, pass, "", false, false); err != nil {
		t.Fatal(err)
	}
	wa := world(t, "v1")
	if _, err := a.e.Push(ctx, wa); err != nil {
		t.Fatal(err)
	}
	wb := worlds.World{ID: "W1", Name: "W1", Path: filepath.Join(t.TempDir(), "W1")}
	if _, err := b.e.Pull(ctx, wb); err != nil {
		t.Fatal(err)
	}

	release, entered := g.hold()
	defer release()
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
	<-entered
	writeLevel(t, wb, "v2-from-b-during-the-copy")
	if _, err := b.e.Push(ctx, wb); err != nil { // lands in the old layout, mid-copy
		t.Fatal(err)
	}
	g.open()
	release()
	if r := a.wait(t); r.State != RekeyDone {
		t.Fatalf("rekey: %+v", r)
	}
	// A, now on the new key, sees B's version: it was carried over.
	if st, _ := a.e.Status(ctx, wa); st != sync.StatusRemoteAhead {
		t.Fatalf("A should see B's version as newer, got %s", st)
	}
	if _, err := a.e.Pull(ctx, wa); err != nil {
		t.Fatalf("pull of the carried-over version: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(wa.Path, "level.dat")); string(got) != "v2-from-b-during-the-copy" {
		t.Fatalf("A pulled %q", got)
	}
}

func TestConcurrentChangeOnAnotherComputerWins(t *testing.T) {
	ctx := context.Background()
	inner := localdir.New(t.TempDir())
	g := &gated{Store: inner}
	a := newComputer(t, g)
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.e.Push(ctx, world(t, "v1")); err != nil {
		t.Fatal(err)
	}
	release, entered := g.hold()
	defer release()
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
	<-entered
	// Another computer commits its own generation 2 while A is still copying.
	h1, _ := ReadHeader(ctx, inner)
	h2, _, _ := NewHeaderAfter(h1, "the other computers choice", fast)
	if err := WriteHeader(ctx, inner, h2); err != nil {
		t.Fatal(err)
	}
	release()
	r := a.wait(t)
	if r.State != RekeyFailed || !strings.Contains(r.Error, "another computer") {
		t.Fatalf("rekey = %+v", r)
	}
	if got, _ := ReadHeader(ctx, inner); got.Salt != h2.Salt {
		t.Fatal("the other computer's key must stand")
	}
}

func TestTamperedDataStopsTheChange(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	shared := localdir.New(root)
	a := newComputer(t, shared)
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.e.Push(ctx, world(t, "v1")); err != nil {
		t.Fatal(err)
	}
	filepath.Walk(filepath.Join(root, "packs"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			flip(t, p)
		}
		return nil
	})
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
	if r := a.wait(t); r.State != RekeyFailed {
		t.Fatalf("a damaged pack must stop the change: %+v", r)
	}
	if h, _ := ReadHeader(ctx, shared); h.Generation() != 1 {
		t.Fatal("a failed change committed")
	}
}
