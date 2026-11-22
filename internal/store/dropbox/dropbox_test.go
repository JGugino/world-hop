package dropbox

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

func setup(t *testing.T) (*Store, *storetest.FakeDropbox, *storetest.Auth) {
	t.Helper()
	httpx.BaseDelay = time.Millisecond
	fake := storetest.NewFakeDropbox()
	t.Cleanup(fake.Close)
	auth := &storetest.Auth{Fresh: "tok"}
	s := New(auth, fake.Client())
	s.APIBase, s.ContentBase = fake.URL, fake.URL
	return s, fake, auth
}

func TestContract(t *testing.T) {
	s, _, _ := setup(t)
	storetest.Contract(t, s)
}

func TestContractWithThrottling(t *testing.T) {
	s, fake, _ := setup(t)
	fake.Fail429 = 3
	storetest.Contract(t, s)
}

func TestLayout(t *testing.T) {
	s, fake, _ := setup(t)
	ctx := context.Background()
	name := chunker.Hash([]byte("x"))
	s.PutPack(ctx, name, []byte("x"))
	s.PutManifest(ctx, "My.World", "manifest-000000000001-a.json", []byte("m"))
	got := fake.Paths()
	want := []string{"/packs/" + name, "/worlds/My.World/manifest-000000000001-a.json"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestExpiredTokenIsRefreshedOnce(t *testing.T) {
	s, _, auth := setup(t)
	auth.Stale = "old"
	if _, err := s.ListManifests(context.Background(), "W"); err != nil {
		t.Fatal(err)
	}
	if n := auth.Refreshes.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
}

func TestPersistentAuthFailureSurfaces(t *testing.T) {
	s, fake, _ := setup(t)
	fake.Token = "other"
	_, err := s.GetPack(context.Background(), chunker.Hash([]byte("x")))
	var se *httpx.StatusError
	if !errors.As(err, &se) || se.Code != 401 || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
