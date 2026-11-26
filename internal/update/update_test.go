package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func server(t *testing.T, code int, body string) (*httptest.Server, *int) {
	hits := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

const rel = `{"tag_name":"v0.2.0","html_url":"https://github.com/JGugino/world-hop/releases/tag/v0.2.0"}`

func TestNewerReleaseIsReported(t *testing.T) {
	s, _ := server(t, 200, rel)
	c := New("v0.1.0", t.TempDir())
	c.URL = s.URL
	var got Info
	c.OnChange = func(i Info) { got = i }
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Latest != "v0.2.0" || got.URL == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestSameOrOlderIsNotAnUpdate(t *testing.T) {
	for _, cur := range []string{"v0.2.0", "0.3.1", "dev"} {
		s, _ := server(t, 200, rel)
		c := New(cur, t.TempDir())
		c.URL = s.URL
		c.Check(context.Background())
		if c.Info().Available {
			t.Errorf("%s: should not be available", cur)
		}
	}
}

func TestFailuresAreQuiet(t *testing.T) {
	for name, tc := range map[string]struct {
		code int
		body string
	}{
		"private repo": {404, `{"message":"Not Found"}`},
		"rate limited": {403, `{}`},
		"garbage":      {200, `<html>`},
		"foreign link": {200, `{"tag_name":"v9.0.0","html_url":"https://evil.example/x"}`},
		"prerelease":   {200, `{"tag_name":"v9.0.0","prerelease":true,"html_url":"https://github.com/JGugino/world-hop/x"}`},
		"bad tag":      {200, `{"tag_name":"nightly","html_url":"https://github.com/JGugino/world-hop/x"}`},
	} {
		s, _ := server(t, tc.code, tc.body)
		c := New("v0.1.0", t.TempDir())
		c.URL = s.URL
		c.Check(context.Background())
		if c.Info().Available {
			t.Errorf("%s: offered an update", name)
		}
	}
}

func TestReleaseLinkPrefixes(t *testing.T) {
	for url, want := range map[string]bool{
		"https://github.com/JGugino/world-hop/releases/tag/v1":      true,
		"https://github.com/JGugino/world-hopper/releases/tag/v1":   true, // saved by an earlier build
		"https://github.com/JGugino/world-hop-evil/releases/tag/v1": false,
		"https://github.com/someone-else/world-hop/releases/tag/v1": false,
		"https://evil.example/JGugino/world-hop/":                   false,
	} {
		if got := safeURL(url); got != want {
			t.Errorf("safeURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestOfflineDoesNotPanic(t *testing.T) {
	c := New("v0.1.0", t.TempDir())
	c.URL = "http://127.0.0.1:1/"
	if err := c.Check(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if c.Info().Available {
		t.Fatal("no update while offline")
	}
}

func TestResultIsRememberedAndRechecksDaily(t *testing.T) {
	dir := t.TempDir()
	s, hits := server(t, 200, rel)
	c := New("v0.1.0", dir)
	c.URL = s.URL
	c.Check(context.Background())
	if _, err := os.Stat(filepath.Join(dir, stateFile)); err != nil {
		t.Fatal(err)
	}
	// A restart knows about the update without asking again.
	c2 := New("v0.1.0", dir)
	c2.URL = s.URL
	if !c2.Info().Available {
		t.Fatal("saved result not used")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c2.Run(ctx)
	if *hits != 1 {
		t.Fatalf("hits = %d, want 1 (checked less than a day ago)", *hits)
	}
	// Once a day has passed, a check is due.
	c2.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	c2.Run(ctx2)
	if *hits != 2 {
		t.Fatalf("hits = %d, want 2", *hits)
	}
}

func TestParse(t *testing.T) {
	for v, ok := range map[string]bool{"v1.2.3": true, "1.2.3": true, "v1.2.3-rc1": true, "dev": false, "0.0": false, "v1.x.3": false} {
		if _, got := parse(v); got != ok {
			t.Errorf("parse(%q) = %v", v, got)
		}
	}
	a, _ := parse("v0.10.0")
	b, _ := parse("v0.9.9")
	if !newer(a, b) {
		t.Error("0.10.0 should be newer than 0.9.9")
	}
}
