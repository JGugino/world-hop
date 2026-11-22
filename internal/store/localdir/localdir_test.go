package localdir

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/storetest"
)

func TestWriteOnceManifestsAndPacks(t *testing.T) {
	ctx := context.Background()
	d := New(t.TempDir())
	name := "manifest-000000000001-abc.json"
	if err := d.PutManifest(ctx, "w", name, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := d.PutManifest(ctx, "w", name, []byte("2")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if b, _ := d.GetManifest(ctx, "w", name); string(b) != "1" {
		t.Fatal("manifest was overwritten")
	}
	if l, _ := d.ListManifests(ctx, "w"); len(l) != 1 {
		t.Fatalf("list %v (temp files leaked?)", l)
	}
	if l, err := d.ListManifests(ctx, "none"); err != nil || len(l) != 0 {
		t.Fatal(l, err)
	}
	pack := strings.Repeat("a", 64)
	if err := d.PutPack(ctx, pack, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := d.PutPack(ctx, pack, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetPack(ctx, strings.Repeat("b", 64)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := d.PutPack(ctx, "../../etc", nil); err == nil {
		t.Fatal("accepted bad pack name")
	}
}

func TestContract(t *testing.T) {
	storetest.Contract(t, New(t.TempDir()))
}
