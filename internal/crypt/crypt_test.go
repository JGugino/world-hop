package crypt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/chunker"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/store/storetest"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/worlds"
)

var fast = Params{Time: 1, MemKiB: 64, Threads: 1}

const pass = "correct horse battery"

func newKey(t *testing.T, inner store.Store, p string) (*Header, *Key) {
	t.Helper()
	h, k, err := NewHeader(p, fast)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteHeader(context.Background(), inner, h); err != nil {
		t.Fatal(err)
	}
	return h, k
}

func TestContract(t *testing.T) {
	inner := localdir.New(t.TempDir())
	_, k := newKey(t, inner, pass)
	storetest.Contract(t, New(inner, k))
}

func TestHeaderAndPassphrase(t *testing.T) {
	inner := localdir.New(t.TempDir())
	ctx := context.Background()
	if _, err := ReadHeader(ctx, inner); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty store: %v", err)
	}
	if _, _, err := NewHeader("short", fast); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("weak passphrase accepted: %v", err)
	}
	h, k := newKey(t, inner, pass)
	if err := WriteHeader(ctx, inner, h); !errors.Is(err, store.ErrExists) {
		t.Fatalf("header overwritten: %v", err)
	}
	h2, err := ReadHeader(ctx, inner)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := h2.Unlock(pass) // a second computer
	if err != nil || !bytes.Equal(k.Bytes(), k2.Bytes()) {
		t.Fatalf("second computer: %v", err)
	}
	if _, err := h2.Unlock("not the passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	// A header asking for absurd cost is refused, not run.
	bad := *h2
	bad.MemKiB = 1 << 30
	if _, err := bad.Unlock(pass); !errors.Is(err, ErrBadHeader) {
		t.Fatalf("absurd KDF cost: %v", err)
	}
}

func TestNothingReadableInStore(t *testing.T) {
	dir := t.TempDir()
	inner := localdir.New(dir)
	_, k := newKey(t, inner, pass)
	s := New(inner, k)
	ctx := context.Background()
	secret := bytes.Repeat([]byte("SECRET-WORLD-DATA "), 500)
	pack := chunker.Hash(secret)
	if err := s.PutPack(ctx, pack, secret); err != nil {
		t.Fatal(err)
	}
	if err := s.PutManifest(ctx, "My-Secret-Base", "manifest-000000000001-dev1.json", []byte(`{"path":"region/r.0.0.mca"}`)); err != nil {
		t.Fatal(err)
	}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.Contains(p, pack) || strings.Contains(p, "Secret") {
			t.Errorf("plaintext name leaked: %s", p)
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte("SECRET")) || bytes.Contains(b, []byte("region/")) || bytes.Contains(b, []byte("Secret Base")) {
			t.Errorf("plaintext content leaked in %s", p)
		}
		return nil
	})
	ids, err := s.ListWorlds(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "My-Secret-Base" {
		t.Fatalf("ListWorlds = %v, %v", ids, err)
	}
}

func TestDeterministicNames(t *testing.T) {
	inner := localdir.New(t.TempDir())
	_, k := newKey(t, inner, pass)
	a, b := New(inner, k), New(inner, k)
	if a.packName("x") != b.packName("x") || a.packName("x") == a.packName("y") {
		t.Fatal("pack names must be stable per key and distinct per input")
	}
	_, k2, _ := NewHeader("another passphrase!", fast)
	if New(inner, k2).packName("x") == a.packName("x") {
		t.Fatal("different keys must give different names")
	}
}

func TestTamperDetection(t *testing.T) {
	dir := t.TempDir()
	inner := localdir.New(dir)
	_, k := newKey(t, inner, pass)
	s := New(inner, k)
	ctx := context.Background()
	data := bytes.Repeat([]byte("abc"), 1000)
	pack := chunker.Hash(data)
	s.PutPack(ctx, pack, data)
	other := bytes.Repeat([]byte("xyz"), 1000)
	otherPack := chunker.Hash(other)
	s.PutPack(ctx, otherPack, other)
	s.PutManifest(ctx, "W", "manifest-000000000001-dev1.json", []byte("one"))
	s.PutManifest(ctx, "W2", "manifest-000000000001-dev1.json", []byte("two"))

	// Flip one byte of a stored pack.
	file := findFile(t, dir, s.packName(pack))
	flip(t, file)
	if _, err := s.GetPack(ctx, pack); !errors.Is(err, ErrTampered) {
		t.Fatalf("flipped pack byte: %v", err)
	}
	// Swap two packs: each is authentic but belongs under another name.
	a, b := findFile(t, dir, s.packName(pack)), findFile(t, dir, s.packName(otherPack))
	ab, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	os.WriteFile(a, bb, 0o644)
	os.WriteFile(b, ab, 0o644)
	if _, err := s.GetPack(ctx, pack); !errors.Is(err, ErrTampered) {
		t.Fatalf("swapped packs: %v", err)
	}
	// Flip a manifest byte, and move a manifest between worlds.
	flip(t, findFile(t, dir, s.worldID("W"), "manifest-000000000001-dev1.json"))
	if _, err := s.GetManifest(ctx, "W", "manifest-000000000001-dev1.json"); !errors.Is(err, ErrTampered) {
		t.Fatalf("flipped manifest byte: %v", err)
	}
	src := findFile(t, dir, s.worldID("W2"), "manifest-000000000001-dev1.json")
	dst := findFile(t, dir, s.worldID("W"), "manifest-000000000001-dev1.json")
	b2, _ := os.ReadFile(src)
	os.WriteFile(dst, b2, 0o644)
	if _, err := s.GetManifest(ctx, "W", "manifest-000000000001-dev1.json"); !errors.Is(err, ErrTampered) {
		t.Fatalf("manifest moved between worlds: %v", err)
	}
	// A plaintext blob planted where a manifest should be is rejected.
	os.WriteFile(dst, []byte(`{"version":1}`), 0o644)
	if _, err := s.GetManifest(ctx, "W", "manifest-000000000001-dev1.json"); !errors.Is(err, ErrTampered) {
		t.Fatalf("planted plaintext manifest: %v", err)
	}
	// Wrong key can't read anything.
	_, k2, _ := NewHeader("another passphrase!", fast)
	if _, err := New(inner, k2).GetManifest(ctx, "W2", "manifest-000000000001-dev1.json"); err == nil {
		t.Fatal("wrong key read data")
	}
}

func findFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	var found string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ok := true
		for _, part := range parts {
			ok = ok && strings.Contains(p, part)
		}
		if ok {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no file matching %v under %s", parts, root)
	}
	return found
}

func flip(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 1
	os.WriteFile(path, b, 0o644)
}

// ---- end to end through the sync engine ----

func world(t *testing.T, content string) worlds.World {
	dir := filepath.Join(t.TempDir(), "W1")
	os.MkdirAll(filepath.Join(dir, "region"), 0o755)
	os.WriteFile(filepath.Join(dir, "level.dat"), []byte(content), 0o644)
	os.WriteFile(filepath.Join(dir, "region", "r.0.0.mca"), bytes.Repeat([]byte(content), 4000), 0o644)
	return worlds.World{ID: "W1", Name: "W1", Path: dir}
}

func TestRoundTripBetweenTwoComputers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	shared := localdir.New(root)
	pro := func() bool { return true }

	a, err := NewManager(shared, t.TempDir(), nil, pro)
	if err != nil {
		t.Fatal(err)
	}
	a.Params = fast
	if err := a.Enable(ctx, pass, pass, true, false); err != nil {
		t.Fatal(err)
	}
	ea, _ := sync.New(a, t.TempDir())
	wa := world(t, "hello")
	if _, err := ea.Push(ctx, wa); err != nil {
		t.Fatal(err)
	}

	// Computer B: has to supply the same passphrase; wrong one is caught early.
	b, _ := NewManager(shared, t.TempDir(), nil, pro)
	if err := b.Enable(ctx, "not the passphrase", "", false, false); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("B wrong passphrase: %v", err)
	}
	if err := b.Enable(ctx, pass, "", false, false); err != nil {
		t.Fatalf("B right passphrase: %v", err)
	}
	if ids, err := b.ListWorlds(ctx); err != nil || len(ids) != 1 || ids[0] != "W1" {
		t.Fatalf("B sees %v %v", ids, err)
	}
	eb, _ := sync.New(b, t.TempDir())
	wb := worlds.World{ID: "W1", Name: "W1", Path: filepath.Join(t.TempDir(), "W1")}
	if _, err := eb.Pull(ctx, wb); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(wb.Path, "level.dat"))
	if string(got) != "hello" {
		t.Fatalf("pulled %q", got)
	}
	// The provider's view of the shared folder contains no world data.
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if bb, _ := os.ReadFile(p); bytes.Contains(bb, []byte("hello")) || bytes.Contains(bb, []byte("level.dat")) {
				t.Errorf("plaintext in %s", p)
			}
		}
		return nil
	})
}

func TestTamperedPackFailsPull(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	shared := localdir.New(dir)
	a, _ := NewManager(shared, t.TempDir(), nil, func() bool { return true })
	a.Params = fast
	a.Enable(ctx, pass, pass, true, false)
	ea, _ := sync.New(a, t.TempDir())
	if _, err := ea.Push(ctx, world(t, "hello")); err != nil {
		t.Fatal(err)
	}
	filepath.Walk(filepath.Join(dir, "packs"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			flip(t, p)
		}
		return nil
	})
	wb := worlds.World{ID: "W1", Name: "W1", Path: filepath.Join(t.TempDir(), "W1")}
	if _, err := ea.Pull(ctx, wb); !errors.Is(err, ErrTampered) {
		t.Fatalf("pull of tampered data: %v", err)
	}
	if _, err := os.Stat(wb.Path); err == nil {
		t.Fatal("a world was created from tampered data")
	}
}
