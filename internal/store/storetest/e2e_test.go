package storetest_test

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/gdrive"
	"github.com/JGugino/world-hopper/internal/store/storetest"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// Two devices share one cloud store; the second pulls later ("laptop was off").
func twoDevice(t *testing.T, mk func() store.Store, packs func() []string) {
	ctx := context.Background()
	rnd := make([]byte, 3<<20)
	rand.New(rand.NewSource(1)).Read(rnd)
	mkDev := func() (*sync.Engine, worlds.World) {
		base := t.TempDir()
		e, err := sync.New(mk(), filepath.Join(base, "state"))
		if err != nil {
			t.Fatal(err)
		}
		return e, worlds.World{ID: "Survival", Name: "Survival", Path: filepath.Join(base, "saves", "Survival")}
	}
	ea, wa := mkDev()
	eb, wb := mkDev()
	os.MkdirAll(filepath.Join(wa.Path, "region"), 0o755)
	os.WriteFile(filepath.Join(wa.Path, "level.dat"), []byte("level"), 0o644)
	os.WriteFile(filepath.Join(wa.Path, "session.lock"), []byte("STALE"), 0o644)
	os.WriteFile(filepath.Join(wa.Path, "region", "r.0.0.mca"), rnd, 0o644)

	if _, err := ea.Push(ctx, wa); err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, err := eb.Pull(ctx, wb); err != nil {
		t.Fatalf("pull: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(wb.Path, "region", "r.0.0.mca"))
	if err != nil || !bytes.Equal(got, rnd) {
		t.Fatalf("region file differs after pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wb.Path, "session.lock")); err == nil {
		t.Fatal("session.lock was synced")
	}
	for _, p := range packs() {
		if filepath.Base(p) == "session.lock" {
			t.Fatal("lock in store")
		}
	}
}

func TestEngineOverFakeDrive(t *testing.T) {
	httpx.BaseDelay = time.Millisecond
	fake := storetest.NewFakeDrive()
	defer fake.Close()
	fake.Fail503 = 7
	twoDevice(t, func() store.Store {
		s := gdrive.New(&storetest.Auth{Fresh: "tok"}, fake.Client())
		s.APIBase = fake.URL
		return s
	}, fake.Names)
}
