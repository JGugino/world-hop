package s3_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/s3"
	"github.com/JGugino/world-hopper/internal/store/storetest"
)

const (
	access = "AKIAEXAMPLEEXAMPLE"
	secret = "s3cr3t/with+chars"
	bucket = "worlds-bucket"
)

func setup(t *testing.T, prefix string) (*s3.Store, *storetest.FakeS3) {
	t.Helper()
	httpx.BaseDelay = time.Millisecond
	f := storetest.NewFakeS3(bucket, access, secret, "eu-west-2")
	t.Cleanup(f.Close)
	s, err := s3.New(s3.Config{Endpoint: f.URL, Region: "eu-west-2", Bucket: bucket, Prefix: prefix,
		PathStyle: true, AccessKey: access, SecretKey: secret}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func TestContract(t *testing.T) {
	for _, prefix := range []string{"", "worldhop/team one"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			s, f := setup(t, prefix)
			f.PageSize = 2 // force pagination
			f.Fail503 = 3  // retried transparently
			storetest.Contract(t, s)
			for _, k := range f.Keys() {
				if prefix != "" && !strings.HasPrefix(k, prefix+"/") {
					t.Fatalf("key %q escaped the prefix", k)
				}
				if !strings.Contains(k, "packs/") && !strings.Contains(k, "worlds/") {
					t.Fatalf("unexpected key layout %q", k)
				}
			}
		})
	}
}

func TestLayout(t *testing.T) {
	s, f := setup(t, "")
	ctx := context.Background()
	h := strings.Repeat("ab", 32)
	if err := s.PutPack(ctx, h, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutManifest(ctx, "W", "manifest-000000000001-aaa.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	keys := f.Keys()
	if len(keys) != 2 || keys[0] != "packs/"+h || keys[1] != "worlds/W/manifest-000000000001-aaa.json" {
		t.Fatalf("keys = %v", keys)
	}
}

// The store must return exactly the bytes it was given, whatever they are
// (encrypted packs will not match their name).
func TestByteAgnostic(t *testing.T) {
	s, _ := setup(t, "")
	ctx := context.Background()
	name := strings.Repeat("0", 64) // does not match the content's hash
	data := []byte{0, 1, 2, 0xff, 0xfe, '\n', 0}
	if err := s.PutPack(ctx, name, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPack(ctx, name)
	if err != nil || string(got) != string(data) {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestPutPackSkipsExisting(t *testing.T) {
	s, f := setup(t, "")
	ctx := context.Background()
	name := strings.Repeat("cd", 32)
	s.PutPack(ctx, name, []byte("data"))
	s.PutPack(ctx, name, []byte("data"))
	if f.Puts != 1 {
		t.Fatalf("Puts = %d, want 1 (idempotent)", f.Puts)
	}
	// A wrong-sized leftover is replaced.
	f.Put("packs/"+name, []byte("junk!!"))
	if err := s.PutPack(ctx, name, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if b, _ := f.Get("packs/" + name); string(b) != "data" {
		t.Fatalf("junk not replaced: %q", b)
	}
}

func TestManifestNeverOverwritesEvenIfServiceIgnoresHeader(t *testing.T) {
	for name, set := range map[string]func(*storetest.FakeS3){
		"ignores header":  func(f *storetest.FakeS3) { f.IgnoreIfNoneMatch = true },
		"no conditionals": func(f *storetest.FakeS3) { f.NoConditional = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := setup(t, "")
			set(f)
			ctx := context.Background()
			n := "manifest-000000000001-aaa.json"
			if err := s.PutManifest(ctx, "W", n, []byte("first")); err != nil {
				t.Fatal(err)
			}
			if err := s.PutManifest(ctx, "W", n, []byte("second")); !errors.Is(err, store.ErrExists) {
				t.Fatalf("err = %v", err)
			}
			if b, _ := f.Get("worlds/W/" + n); string(b) != "first" {
				t.Fatalf("overwritten: %q", b)
			}
		})
	}
}

func TestBadCredentials(t *testing.T) {
	f := storetest.NewFakeS3(bucket, access, secret, "us-east-1")
	defer f.Close()
	mk := func(c s3.Config) error {
		c.Endpoint, c.Bucket, c.PathStyle = f.URL, bucket, true
		s, err := s3.New(c, nil)
		if err != nil {
			return err
		}
		return s.Test(context.Background())
	}
	if err := mk(s3.Config{AccessKey: access, SecretKey: secret}); err != nil {
		t.Fatalf("good creds: %v", err)
	}
	for name, c := range map[string]s3.Config{
		"secret": {AccessKey: access, SecretKey: "wrong"},
		"access": {AccessKey: "AKIAOTHER", SecretKey: secret},
		"region": {AccessKey: access, SecretKey: secret, Region: "eu-west-1"},
	} {
		err := mk(c)
		var se *s3.Error
		if !errors.As(err, &se) || se.Status != 403 {
			t.Errorf("%s: err = %v, want 403 s3.Error", name, err)
		} else if se.Error() == "" || strings.Contains(se.Error(), secret) {
			t.Errorf("%s: bad message %q", name, se)
		}
	}
	// Wrong bucket.
	s, _ := s3.New(s3.Config{Endpoint: f.URL, Bucket: "nope", PathStyle: true, AccessKey: access, SecretKey: secret}, nil)
	var se *s3.Error
	if err := s.Test(context.Background()); !errors.As(err, &se) || se.Code != "NoSuchBucket" {
		t.Errorf("bucket: %v", err)
	}
	if _, err := s.GetPack(context.Background(), strings.Repeat("0", 64)); errors.Is(err, store.ErrNotFound) {
		t.Error("a missing bucket must not look like a missing object")
	}
}

func TestRedirectIsReportedNotFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/", http.StatusMovedPermanently)
	}))
	defer srv.Close()
	s, _ := s3.New(s3.Config{Endpoint: srv.URL, Bucket: "b", PathStyle: true, AccessKey: "a", SecretKey: "b"}, nil)
	err := s.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigNormalize(t *testing.T) {
	c, err := s3.Config{Endpoint: " r2.example.com ", Bucket: "b", Prefix: "/a/b/", AccessKey: "k", SecretKey: "s"}.Normalize()
	if err != nil || c.Endpoint != "https://r2.example.com" || c.Prefix != "a/b" || c.Region != "us-east-1" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []s3.Config{
		{Bucket: "", AccessKey: "k", SecretKey: "s"},
		{Bucket: "a/b", AccessKey: "k", SecretKey: "s"},
		{Bucket: "b", AccessKey: "", SecretKey: "s"},
		{Bucket: "b", SecretKey: "s", AccessKey: "k", Endpoint: "ftp://x"},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
