package gdrive

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/chunker"
	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/storetest"
)

func setup(t *testing.T) (*Store, *storetest.FakeDrive, *storetest.Auth) {
	t.Helper()
	httpx.BaseDelay = time.Millisecond
	fake := storetest.NewFakeDrive()
	t.Cleanup(fake.Close)
	auth := &storetest.Auth{Fresh: "tok"}
	s := New(auth, fake.Client())
	s.APIBase = fake.URL
	return s, fake, auth
}

func TestContract(t *testing.T) {
	s, _, _ := setup(t)
	storetest.Contract(t, s)
}

func TestContractWithFlakyServer(t *testing.T) {
	s, fake, _ := setup(t)
	fake.Fail503 = 3
	storetest.Contract(t, s)
}

func TestExpiredTokenIsRefreshedOnce(t *testing.T) {
	s, _, auth := setup(t)
	auth.Stale = "old"
	if err := s.PutManifest(context.Background(), "W", "manifest-000000000001-a.json", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if n := auth.Refreshes.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
}

func TestLargePackUsesResumableUpload(t *testing.T) {
	s, fake, _ := setup(t)
	data := make([]byte, 6<<20)
	for i := range data {
		data[i] = byte(i * 7)
	}
	name := chunker.Hash(data)
	ctx := context.Background()
	if err := s.PutPack(ctx, name, data); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPack(ctx, name, data); err != nil { // already there: no second upload
		t.Fatal(err)
	}
	if fake.Uploads != 1 {
		t.Fatalf("uploads = %d, want 1", fake.Uploads)
	}
	got, err := s.GetPack(ctx, name)
	if err != nil || len(got) != len(data) || chunker.Hash(got) != name {
		t.Fatalf("roundtrip failed: %v", err)
	}
}

func TestPackWithWrongSizeIsReplaced(t *testing.T) {
	s, fake, _ := setup(t)
	ctx := context.Background()
	name := chunker.Hash([]byte("good"))
	fake.Seed("packs/"+name, "pack", "", []byte("junk-partial"))
	if err := s.PutPack(ctx, name, []byte("good")); err != nil {
		t.Fatal(err)
	}
	b, err := s.GetPack(ctx, name)
	if err != nil || string(b) != "good" {
		t.Fatalf("got %q %v", b, err)
	}
}

func TestRejectedAuthSurfaces(t *testing.T) {
	s, fake, _ := setup(t)
	fake.Token = "something-else"
	_, err := s.ListManifests(context.Background(), "W")
	var se *httpx.StatusError
	if !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("err = %v", err)
	}
	_ = store.ErrNotFound
}
