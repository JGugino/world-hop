package link

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/storetest"
)

const (
	tAccess = "AKIATESTTESTTEST"
	tSecret = "very-secret-value"
)

type s3env struct {
	m    *Manager
	sec  MemorySecrets
	fake *storetest.FakeS3
	dir  string
	pro  *bool
}

func newS3Env(t *testing.T, pro bool) *s3env {
	t.Helper()
	httpx.BaseDelay = time.Millisecond
	e := &s3env{sec: MemorySecrets{}, dir: t.TempDir(), pro: &pro}
	e.fake = storetest.NewFakeS3("bkt", tAccess, tSecret, "us-east-1")
	t.Cleanup(e.fake.Close)
	e.m = New(Config{}, &oauth.MemoryStore{}, e.dir)
	e.m.EnableS3(e.sec, proFunc(func() bool { return *e.pro }))
	return e
}

func (e *s3env) input() S3Input {
	return S3Input{
		S3Settings: S3Settings{Endpoint: e.fake.URL, Bucket: "bkt", Prefix: "wh", PathStyle: true},
		AccessKey:  tAccess, SecretKey: tSecret,
	}
}

func TestS3SaveStoresSecretOnlyInKeychain(t *testing.T) {
	e := newS3Env(t, true)
	if err := e.m.SaveS3(e.input()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.sec[S3], tSecret) {
		t.Fatal("secret not in the secret store")
	}
	entries, _ := os.ReadDir(e.dir)
	for _, ent := range entries {
		b, _ := os.ReadFile(filepath.Join(e.dir, ent.Name()))
		if strings.Contains(string(b), tSecret) || strings.Contains(string(b), tAccess) {
			t.Fatalf("credential written to plain file %s: %s", ent.Name(), b)
		}
	}
	st := e.m.Status()
	if st.Chosen != S3 || st.State != StateLinked {
		t.Fatalf("status %+v", st)
	}
	c := e.m.S3Config()
	if c.Bucket != "bkt" || c.AccessKey != tAccess || !c.HasSecret {
		t.Fatalf("config %+v", c)
	}
}

func TestS3SyncsThroughManager(t *testing.T) {
	e := newS3Env(t, true)
	if err := e.m.SaveS3(e.input()); err != nil {
		t.Fatal(err)
	}
	storetest.Contract(t, e.m)
	for _, k := range e.fake.Keys() {
		if !strings.HasPrefix(k, "wh/") {
			t.Fatalf("key %q outside prefix", k)
		}
	}
}

func TestS3BlankSecretKeepsStoredOne(t *testing.T) {
	e := newS3Env(t, true)
	e.m.SaveS3(e.input())
	in := e.input()
	in.SecretKey, in.Prefix = "", "other"
	if err := e.m.TestS3(context.Background(), in); err != nil {
		t.Fatalf("test with stored secret: %v", err)
	}
	if err := e.m.SaveS3(in); err != nil {
		t.Fatal(err)
	}
	if e.m.S3Config().Prefix != "other" || !strings.Contains(e.sec[S3], tSecret) {
		t.Fatal("secret lost on resave")
	}
	// A different access key without a new secret must not reuse the old secret.
	in.AccessKey = "AKIAOTHER"
	if err := e.m.SaveS3(in); err == nil {
		t.Fatal("saved a new access key with no secret")
	}
}

func TestS3TestConnection(t *testing.T) {
	e := newS3Env(t, true)
	if err := e.m.TestS3(context.Background(), e.input()); err != nil {
		t.Fatal(err)
	}
	if len(e.sec) != 0 {
		t.Fatal("Test must not save")
	}
	bad := e.input()
	bad.SecretKey = "nope"
	err := e.m.TestS3(context.Background(), bad)
	if err == nil || strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	bad = e.input()
	bad.Bucket = ""
	if err := e.m.TestS3(context.Background(), bad); err == nil {
		t.Fatal("empty bucket accepted")
	}
}

func TestS3NeedsPro(t *testing.T) {
	e := newS3Env(t, false)
	ctx := context.Background()
	if err := e.m.SaveS3(e.input()); !errors.Is(err, license.ErrProRequired) {
		t.Fatalf("save: %v", err)
	}
	if err := e.m.TestS3(ctx, e.input()); !errors.Is(err, license.ErrProRequired) {
		t.Fatalf("test: %v", err)
	}
	if err := e.m.Choose(S3); !errors.Is(err, license.ErrProRequired) {
		t.Fatalf("choose: %v", err)
	}
	var ps ProviderStatus
	for _, p := range e.m.Status().Providers {
		if p.ID == S3 {
			ps = p
		}
	}
	if !ps.Pro || !ps.Locked {
		t.Fatalf("provider %+v", ps)
	}
	if len(e.sec) != 0 || len(e.fake.Keys()) != 0 {
		t.Fatal("something was stored without Pro")
	}
}

// A licence that lapses stops an already-built S3 store.
func TestS3StopsWhenProLapses(t *testing.T) {
	e := newS3Env(t, true)
	e.m.SaveS3(e.input())
	ctx := context.Background()
	if _, err := e.m.ListWorlds(ctx); err != nil {
		t.Fatal(err)
	}
	*e.pro = false
	if _, err := e.m.ListWorlds(ctx); !errors.Is(err, license.ErrProRequired) {
		t.Fatalf("err = %v", err)
	}
	*e.pro = true
	if _, err := e.m.ListWorlds(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestS3UnlinkForgetsEverything(t *testing.T) {
	e := newS3Env(t, true)
	e.m.SaveS3(e.input())
	e.m.PutPack(context.Background(), strings.Repeat("ab", 32), []byte("x"))
	if err := e.m.Unlink(S3); err != nil {
		t.Fatal(err)
	}
	if len(e.sec) != 0 || e.m.S3Config().Bucket != "" || e.m.Status().State != StateUnlinked {
		t.Fatalf("not forgotten: %+v", e.m.Status())
	}
	if _, err := e.m.GetPack(context.Background(), strings.Repeat("ab", 32)); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v", err)
	}
	if len(e.fake.Keys()) != 1 {
		t.Fatal("cloud data must be left alone")
	}
}

func TestS3ChoiceSurvivesRestartAndStartLinkRefused(t *testing.T) {
	e := newS3Env(t, true)
	e.m.SaveS3(e.input())
	m2 := New(Config{}, &oauth.MemoryStore{}, e.dir)
	m2.EnableS3(e.sec, proFunc(func() bool { return true }))
	if m2.Status().Chosen != S3 {
		t.Fatalf("chosen = %q", m2.Status().Chosen)
	}
	if _, err := m2.StartLink(S3); !errors.Is(err, ErrManualSetup) {
		t.Fatalf("err = %v", err)
	}
	if _, err := m2.GetPack(context.Background(), strings.Repeat("0", 64)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

type proFunc func() bool

func (f proFunc) Pro() bool { return f() }
