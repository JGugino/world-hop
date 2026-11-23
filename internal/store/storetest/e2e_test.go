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
	"github.com/JGugino/world-hopper/internal/store/s3"
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

// A world uploaded to Drive by one computer shows up as cloud-only on another
// with an empty saves folder, and downloads intact.
func TestCloudOnlyDownloadOverFakeDrive(t *testing.T) {
	httpx.BaseDelay = time.Millisecond
	fake := storetest.NewFakeDrive()
	defer fake.Close()
	mk := func() *sync.Engine {
		s := gdrive.New(&storetest.Auth{Fresh: "tok"}, fake.Client())
		s.APIBase = fake.URL
		e, err := sync.New(s, filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	ctx := context.Background()
	data := make([]byte, 2<<20)
	rand.New(rand.NewSource(7)).Read(data)

	ea, eb := mk(), mk()
	wa := filepath.Join(t.TempDir(), "saves", "Creative")
	os.MkdirAll(filepath.Join(wa, "region"), 0o755)
	os.WriteFile(filepath.Join(wa, "level.dat"), []byte("level"), 0o644)
	os.WriteFile(filepath.Join(wa, "region", "r.0.0.mca"), data, 0o644)
	if _, err := ea.Push(ctx, worlds.World{ID: "Creative", Name: "Creative", Path: wa}); err != nil {
		t.Fatal(err)
	}

	ids, err := eb.RemoteIDs(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "Creative" {
		t.Fatalf("RemoteIDs = %v %v", ids, err)
	}
	rw, err := eb.Remote(ctx, "Creative")
	if err != nil || rw.Files != 2 || rw.Size != int64(len(data))+5 {
		t.Fatalf("Remote = %+v %v", rw, err)
	}
	saves := filepath.Join(t.TempDir(), "saves")
	os.MkdirAll(saves, 0o755)
	if _, err := eb.Download(ctx, "Creative", saves); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(saves, "Creative", "region", "r.0.0.mca"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded region differs: %v", err)
	}
}

func TestEngineOverFakeS3(t *testing.T) {
	httpx.BaseDelay = time.Millisecond
	fake := storetest.NewFakeS3("bkt", "AK", "SK", "us-east-1")
	defer fake.Close()
	fake.Fail503 = 4
	fake.PageSize = 3
	twoDevice(t, func() store.Store {
		s, err := s3.New(s3.Config{Endpoint: fake.URL, Bucket: "bkt", Prefix: "wh", PathStyle: true,
			AccessKey: "AK", SecretKey: "SK"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, fake.Keys)
}
