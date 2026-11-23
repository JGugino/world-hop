// Package storetest holds a contract test suite for store.Store backends and
// in-process fake Google Drive server to run it against.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/JGugino/world-hopper/internal/chunker"
	"github.com/JGugino/world-hopper/internal/store"
)

// Contract checks the behaviour the sync engine relies on.
func Contract(t *testing.T, s store.Store) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("pack-data-"), 1000)
	pack := chunker.Hash(data)

	if _, err := s.GetPack(ctx, pack); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPack missing: %v", err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := s.PutPack(ctx, pack, data); err != nil {
			t.Fatalf("PutPack #%d: %v", i, err)
		}
	}
	got, err := s.GetPack(ctx, pack)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("GetPack roundtrip: %v", err)
	}
	if err := s.PutPack(ctx, "../evil", data); err == nil {
		t.Fatal("bad pack name accepted")
	}

	if l, err := s.ListManifests(ctx, "W1"); err != nil || len(l) != 0 {
		t.Fatalf("empty list: %v %v", l, err)
	}
	n1, n2, n3 := "manifest-000000000002-bbb.json", "manifest-000000000001-aaa.json", "manifest-000000000003-ccc.json"
	for _, n := range []string{n1, n2, n3} {
		if err := s.PutManifest(ctx, "W1", n, []byte("m-"+n)); err != nil {
			t.Fatalf("PutManifest %s: %v", n, err)
		}
	}
	if err := s.PutManifest(ctx, "W1", n1, []byte("other")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("overwrite must be ErrExists, got %v", err)
	}
	if err := s.PutManifest(ctx, "W2", n1, []byte("w2")); err != nil {
		t.Fatalf("same name in other world: %v", err)
	}
	b, err := s.GetManifest(ctx, "W1", n1)
	if err != nil || string(b) != "m-"+n1 {
		t.Fatalf("GetManifest: %q %v (must keep the first write)", b, err)
	}
	if _, err := s.GetManifest(ctx, "W1", "manifest-000000000009-zzz.json"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetManifest missing: %v", err)
	}
	l, err := s.ListManifests(ctx, "W1")
	want := []string{n2, n1, n3}
	if err != nil || len(l) != 3 || l[0] != want[0] || l[1] != want[1] || l[2] != want[2] {
		t.Fatalf("ListManifests = %v %v, want %v", l, err, want)
	}
	if l, _ := s.ListManifests(ctx, "W2"); len(l) != 1 {
		t.Fatalf("worlds must be isolated: %v", l)
	}
	if l, err := s.ListWorlds(ctx); err != nil || len(l) != 2 || l[0] != "W1" || l[1] != "W2" {
		t.Fatalf("ListWorlds = %v %v, want [W1 W2]", l, err)
	}
}
