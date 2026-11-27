package crypt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
)

func headerFile(root string, gen int) string {
	return filepath.Join(root, "worlds", keyWorld, headerName(int64(gen)))
}

func TestDeletedKeyHeaderIsNoticedAndCanBePutBack(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a := newComputer(t, localdir.New(root))
	if err := a.m.Enable(ctx, pass, pass, true, true); err != nil {
		t.Fatal(err)
	}
	wa := world(t, "v1")
	if _, err := a.e.Push(ctx, wa); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(headerFile(root, 1))
	os.Remove(headerFile(root, 1)) // the cloud loses the key header

	*a.clock = a.clock.Add(2 * headerRecheck)
	if _, err := a.m.ListWorlds(ctx); !errors.Is(err, ErrKeyRollback) {
		t.Fatalf("a missing header must be noticed: %v", err)
	}
	// Sticky: nothing carries on in the meantime, and syncing says why.
	if _, err := a.e.Push(ctx, wa); !errors.Is(err, ErrKeyRollback) {
		t.Fatalf("push: %v", err)
	}
	if p := sync.Explain(ErrKeyRollback); p.Kind != sync.ProblemKey || p.Level != sync.LevelWarn || p.Fix != "/encryption" {
		t.Fatalf("problem = %+v", p)
	}
	in := a.m.Info(ctx)
	if !in.KeyRollback || in.Error != "" {
		t.Fatalf("info: %+v", in)
	}
	if err := a.m.Unlock(ctx, pass, false); !errors.Is(err, ErrKeyRollback) {
		t.Fatalf("unlock must not carry on: %v", err)
	}
	if _, err := os.Stat(headerFile(root, 1)); err == nil {
		t.Fatal("nothing may be restored by itself")
	}

	// The person chooses to put the key header back.
	if err := a.m.RestoreKeyHeader(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(headerFile(root, 1)); string(got) != string(saved) {
		t.Fatalf("the restored header differs from the original:\n%s\n%s", got, saved)
	}
	if in := a.m.Info(ctx); in.KeyRollback || !in.Unlocked {
		t.Fatalf("after restoring: %+v", in)
	}
	if st, err := a.e.Status(ctx, wa); err != nil || st != sync.StatusSynced {
		t.Fatalf("status after restoring: %s %v", st, err)
	}
}

func TestOlderKeyHeaderAfterAPassphraseChange(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a := newComputer(t, localdir.New(root))
	if err := a.m.Enable(ctx, pass, pass, true, true); err != nil {
		t.Fatal(err)
	}
	if err := a.m.StartChangePassphrase(ctx, pass, newPass, newPass, true, true); err != nil {
		t.Fatal(err)
	}
	a.wait(t)
	os.Remove(headerFile(root, 2)) // rolled back to the previous key

	*a.clock = a.clock.Add(2 * headerRecheck)
	if _, err := a.m.ListWorlds(ctx); !errors.Is(err, ErrKeyRollback) {
		t.Fatalf("an older header must be noticed: %v", err)
	}
	// Choosing to go by the cloud: the newer key is forgotten everywhere here.
	if err := a.m.AcceptKeyRollback(ctx); err != nil {
		t.Fatal(err)
	}
	if len(a.keys) != 0 {
		t.Fatalf("the newer key must leave the keychain: %d", len(a.keys))
	}
	in := a.m.Info(ctx)
	if in.KeyRollback || in.Unlocked || !in.Enabled {
		t.Fatalf("after accepting: %+v", in)
	}
	if err := a.m.Unlock(ctx, newPass, false); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("the newer passphrase doesn't fit the older header: %v", err)
	}
	if err := a.m.Unlock(ctx, pass, false); err != nil {
		t.Fatal(err)
	}
	if h, _ := ReadHeader(ctx, localdir.New(root)); h.Generation() != 1 {
		t.Fatal("the cloud's header must not have been touched")
	}
}

func TestAcceptingAMissingHeaderTurnsEncryptionOffHere(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a := newComputer(t, localdir.New(root))
	if err := a.m.Enable(ctx, pass, pass, true, true); err != nil {
		t.Fatal(err)
	}
	os.Remove(headerFile(root, 1))
	*a.clock = a.clock.Add(2 * headerRecheck)
	if _, err := a.m.ListWorlds(ctx); !errors.Is(err, ErrKeyRollback) {
		t.Fatal(err)
	}
	if err := a.m.AcceptKeyRollback(ctx); err != nil {
		t.Fatal(err)
	}
	if in := a.m.Info(ctx); in.Enabled || in.KeyRollback || len(a.keys) != 0 {
		t.Fatalf("after accepting a missing header: %+v keys=%d", in, len(a.keys))
	}
	// A fresh key can be made.
	if err := a.m.Enable(ctx, newPass, newPass, true, false); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackIsNoticedAfterARestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	shared := localdir.New(root)
	a := newComputer(t, shared)
	if err := a.m.Enable(ctx, pass, pass, true, true); err != nil {
		t.Fatal(err)
	}
	os.Remove(headerFile(root, 1))
	// Restart: the key comes from the keychain, but the header is gone.
	m2, _ := NewManager(shared, a.dir, a.keys, func() bool { return true })
	if _, err := m2.ListWorlds(ctx); !errors.Is(err, ErrKeyRollback) {
		t.Fatalf("after a restart: %v", err)
	}
	if err := m2.RestoreKeyHeader(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.ListWorlds(ctx); err != nil {
		t.Fatalf("after restoring: %v", err)
	}
}

func TestSettingsFromAnEarlierVersionAreNotFlagged(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a := newComputer(t, localdir.New(root))
	if err := a.m.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	a.m.cfg.KeyGen, a.m.cfg.Header = 0, nil // as saved before this was tracked
	os.Remove(headerFile(root, 1))
	*a.clock = a.clock.Add(2 * headerRecheck)
	if _, err := a.m.ListWorlds(ctx); errors.Is(err, ErrKeyRollback) {
		t.Fatal("nothing to compare against, so nothing to flag")
	}
	if err := a.m.RestoreKeyHeader(ctx); !errors.Is(err, ErrNoSavedHeader) {
		t.Fatalf("restore without a saved header: %v", err)
	}
}
