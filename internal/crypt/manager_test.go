package crypt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
)

type memKeys map[string][]byte

func (m memKeys) Get(id string) ([]byte, error) { return m[id], nil }
func (m memKeys) Set(id string, k []byte) error { m[id] = k; return nil }
func (m memKeys) Delete(id string) error        { delete(m, id); return nil }

func TestEnableRules(t *testing.T) {
	ctx := context.Background()
	inner := localdir.New(t.TempDir())
	pro := false
	m, _ := NewManager(inner, t.TempDir(), nil, func() bool { return pro })
	m.Params = fast

	if err := m.Enable(ctx, pass, pass, true, false); !errors.Is(err, ErrProRequired) {
		t.Fatalf("free user: %v", err)
	}
	pro = true
	if err := m.Enable(ctx, pass, "typo", true, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("mismatched confirm: %v", err)
	}
	if err := m.Enable(ctx, pass, pass, false, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("no acknowledgement: %v", err)
	}
	if err := m.Enable(ctx, "short", "short", true, false); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("weak: %v", err)
	}
	if in := m.Info(ctx); in.Enabled || in.KeyInStore == nil || *in.KeyInStore {
		t.Fatalf("nothing should be set up yet: %+v", in)
	}
	if err := m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	if in := m.Info(ctx); !in.Enabled || !in.Unlocked {
		t.Fatalf("after enable: %+v", in)
	}
	// Pro lapsing must not strand already-encrypted data.
	pro = false
	if _, err := m.ListWorlds(ctx); err != nil {
		t.Fatalf("encrypted store unreadable after Pro lapsed: %v", err)
	}
}

func TestLockUnlockAndKeychain(t *testing.T) {
	ctx := context.Background()
	inner := localdir.New(t.TempDir())
	dir := t.TempDir()
	keys := memKeys{}
	pro := func() bool { return true }
	m, _ := NewManager(inner, dir, keys, pro)
	m.Params = fast
	if err := m.Enable(ctx, pass, pass, true, true); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatal("remember should store the key in the keychain")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "encryption.json")); len(b) == 0 || strings.Contains(string(b), pass) {
		t.Fatalf("settings file must not hold the passphrase: %s", b)
	}

	// Restart with a remembered key: unlocked with no prompt.
	m2, _ := NewManager(inner, dir, keys, pro)
	if _, err := m2.ListWorlds(ctx); err != nil {
		t.Fatalf("remembered key not used: %v", err)
	}

	// Lock: forgets everywhere; syncing is refused, never falls back to plain.
	if err := m2.Lock(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatal("lock must clear the keychain")
	}
	if err := m2.PutPack(ctx, strings.Repeat("ab", 32), []byte("x")); !errors.Is(err, ErrLocked) {
		t.Fatal("locked store accepted a write")
	}
	if _, err := m2.ListWorlds(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v", err)
	}
	// Restart without a remembered key stays locked; wrong passphrase is caught.
	m3, _ := NewManager(inner, dir, keys, pro)
	if _, err := m3.ListWorlds(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("after restart: %v", err)
	}
	if err := m3.Unlock(ctx, "wrong passphrase!", false); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("unlock wrong: %v", err)
	}
	if err := m3.Unlock(ctx, pass, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m3.ListWorlds(ctx); err != nil {
		t.Fatal(err)
	}
}

// Existing unencrypted data: it stays where it is, the encrypted layout starts
// beside it, and turning encryption on does not confuse the "last synced"
// records (no phantom conflicts, no old data pulled over newer).
func TestExistingPlainDataIsMigrated(t *testing.T) {
	ctx := context.Background()
	inner := localdir.New(t.TempDir())
	m, _ := NewManager(inner, t.TempDir(), nil, func() bool { return true })
	m.Params = fast
	state := t.TempDir()
	e, _ := sync.New(m, state)
	e.StateScope = m.Scope
	w := world(t, "v1")

	if _, err := e.Push(ctx, w); err != nil { // plain
		t.Fatal(err)
	}
	if err := m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx, w); st != sync.StatusLocalAhead {
		t.Fatalf("after enabling, world should be waiting for its first encrypted upload, got %s", st)
	}
	if _, err := e.Push(ctx, w); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx, w); st != sync.StatusSynced {
		t.Fatalf("after encrypted push: %s", st)
	}
	// Local edit, then turn encryption off again: plain layout is older but the
	// world must be treated as local-ahead, not pulled over.
	os.WriteFile(filepath.Join(w.Path, "level.dat"), []byte("v2-edited"), 0o644)
	if err := m.Disable(); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx, w); st != sync.StatusLocalAhead {
		t.Fatalf("after disabling: %s", st)
	}
	// Plain listing hides the encryption bookkeeping.
	ids, _ := m.ListWorlds(ctx)
	for _, id := range ids {
		if IsReserved(id) {
			t.Fatalf("reserved id leaked into world list: %v", ids)
		}
	}
}

// Turning encryption on leaves the unencrypted copy behind; RemovePlaintext deletes
// it once the encrypted layout holds the world, and not before.
func TestPlaintextCopyIsRemovedOnlyOnceReplaced(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inner := localdir.New(root)
	m, _ := NewManager(inner, t.TempDir(), nil, func() bool { return true })
	m.Params = fast
	e, _ := sync.New(m, t.TempDir())
	e.StateScope = m.Scope
	w := world(t, "v1")
	if _, err := e.Push(ctx, w); err != nil { // plaintext
		t.Fatal(err)
	}
	// A second world only exists in the clear in the cloud: no encrypted copy, so
	// it must survive every run below.
	w2 := world(t, "other")
	w2.ID, w2.Name = "W2", "W2"
	if _, err := e.Push(ctx, w2); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	plainManifests := func() int {
		ents, _ := os.ReadDir(filepath.Join(root, "worlds", w.ID))
		return len(ents)
	}
	packCount := func() int { ents, _ := os.ReadDir(filepath.Join(root, "packs")); return len(ents) }
	plainPacks := packCount()

	// Not yet encrypted: nothing may be removed.
	if n, err := m.RemovePlaintext(ctx); err != nil || n != 0 || plainManifests() != 1 || packCount() != plainPacks {
		t.Fatalf("removed before replaced: %d %v", n, err)
	}
	if _, err := e.Push(ctx, w); err != nil { // encrypted
		t.Fatal(err)
	}
	encPacks := packCount() - plainPacks
	w2Packs := len(packsOf(t, inner, "W2"))
	if encPacks == 0 {
		t.Fatal("test setup: encrypted push stored nothing")
	}
	n, err := m.RemovePlaintext(ctx)
	if err != nil || n != 1 {
		t.Fatalf("remove: %d %v", n, err)
	}
	if plainManifests() != 0 || packCount() != encPacks+w2Packs {
		t.Fatalf("plaintext left: %d manifests, %d packs (encrypted %d, kept %d)", plainManifests(), packCount(), encPacks, w2Packs)
	}
	// The encrypted copy is untouched and still syncs.
	if st, err := e.Status(ctx, w); err != nil || st != sync.StatusSynced {
		t.Fatalf("after removal: %s %v", st, err)
	}
	b, _ := sync.New(m, t.TempDir())
	b.StateScope = m.Scope
	dl := filepath.Join(t.TempDir())
	if _, err := b.Download(ctx, w.ID, dl); err != nil {
		t.Fatalf("download of the encrypted copy: %v", err)
	}
	// Again: nothing more to do, and the world with no encrypted copy is intact.
	if n, err := m.RemovePlaintext(ctx); err != nil || n != 0 {
		t.Fatalf("second run: %d %v", n, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "worlds", "W2")); len(ents) != 1 {
		t.Fatal("a world with no encrypted copy lost its plaintext copy")
	}
	if _, err := inner.GetPack(ctx, mustPack(t, inner, "W2")); err != nil {
		t.Fatalf("W2's pack was removed: %v", err)
	}
	// While locked it does nothing.
	m.Lock()
	if n, err := m.RemovePlaintext(ctx); err != nil || n != 0 {
		t.Fatalf("locked: %d %v", n, err)
	}
}

// packsOf lists the packs a plaintext world's manifests point at.
func packsOf(t *testing.T, st *localdir.Dir, id string) []string {
	t.Helper()
	ctx := context.Background()
	names, _ := st.ListManifests(ctx, id)
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		b, err := st.GetManifest(ctx, id, n)
		if err != nil {
			t.Fatal(err)
		}
		mf, err := manifest.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range mf.Chunks {
			if !seen[c.Pack] {
				seen[c.Pack] = true
				out = append(out, c.Pack)
			}
		}
	}
	return out
}

// mustPack returns one pack of a plaintext world.
func mustPack(t *testing.T, st *localdir.Dir, id string) string {
	t.Helper()
	p := packsOf(t, st, id)
	if len(p) == 0 {
		t.Fatalf("%s has no packs", id)
	}
	return p[0]
}
