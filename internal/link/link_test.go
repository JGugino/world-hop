package link

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/dropbox"
	"github.com/JGugino/world-hopper/internal/store/gdrive"
	"github.com/JGugino/world-hopper/internal/store/storetest"
)

// env wires a Manager to a fake identity provider and fake Dropbox/Drive APIs.
type env struct {
	m       *Manager
	tokens  *oauth.MemoryStore
	idp     *httptest.Server
	drive   *storetest.FakeDrive
	dbx     *storetest.FakeDropbox
	revoked bool
	state   string
	redir   string
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	httpx.BaseDelay = time.Millisecond
	e := &env{tokens: &oauth.MemoryStore{}, drive: storetest.NewFakeDrive(), dbx: storetest.NewFakeDropbox()}
	t.Cleanup(e.drive.Close)
	t.Cleanup(e.dbx.Close)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		e.state, e.redir = q.Get("state"), q.Get("redirect_uri")
		http.Redirect(w, r, e.redir+"?code=c&state="+url.QueryEscape(e.state), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" && e.revoked {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		// Expires immediately, so every store call goes through a refresh.
		w.Write([]byte(`{"access_token":"tok","refresh_token":"r","expires_in":1}`))
	})
	e.idp = httptest.NewServer(mux)
	t.Cleanup(e.idp.Close)

	e.m = New(cfg, e.tokens, t.TempDir())
	e.m.HTTP = e.idp.Client()
	g, d := e.m.providers[GoogleDrive], e.m.providers[Dropbox]
	g.oauth.AuthURL, g.oauth.TokenURL = e.idp.URL+"/auth", e.idp.URL+"/token"
	d.oauth.AuthURL, d.oauth.TokenURL = e.idp.URL+"/auth", e.idp.URL+"/token"
	g.newStore = func(a httpx.Auth, hc *http.Client) store.Store {
		s := gdrive.New(a, e.drive.Client())
		s.APIBase = e.drive.URL
		return s
	}
	d.newStore = func(a httpx.Auth, hc *http.Client) store.Store {
		s := dropbox.New(a, e.dbx.Client())
		s.APIBase, s.ContentBase = e.dbx.URL, e.dbx.URL
		return s
	}
	e.m.OpenURL = func(u string) error { // the "browser"
		go func() {
			if resp, err := http.Get(u); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	return e
}

func (e *env) waitState(t *testing.T, want string) Status {
	t.Helper()
	for i := 0; i < 200; i++ {
		if st := e.m.Status(); st.State == want {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state never became %q: %+v", want, e.m.Status())
	return Status{}
}

var both = Config{GoogleClientID: "gid", DropboxAppKey: "dkey"}

func TestLinkAndSyncThroughBothProviders(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		id    string
		count func(e *env) int
	}{
		{GoogleDrive, func(e *env) int { return len(e.drive.Names()) }},
		{Dropbox, func(e *env) int { return len(e.dbx.Paths()) }},
	} {
		t.Run(tc.id, func(t *testing.T) {
			e := newEnv(t, both)
			if st := e.m.Status(); st.State != StateNone || len(st.Providers) != 2 {
				t.Fatalf("initial status = %+v", st)
			}
			if err := e.m.PutPack(ctx, "x", nil); !errors.Is(err, ErrNotLinked) {
				t.Fatalf("unlinked store call: %v", err)
			}
			if err := e.m.Choose(tc.id); err != nil {
				t.Fatal(err)
			}
			if st := e.m.Status(); st.State != StateUnlinked {
				t.Fatalf("chosen but unlinked: %+v", st)
			}
			u, err := e.m.StartLink(tc.id)
			if err != nil || u == "" {
				t.Fatalf("StartLink: %q %v", u, err)
			}
			st := e.waitState(t, StateLinked)
			if !st.Providers[0].Linked && !st.Providers[1].Linked {
				t.Fatalf("no provider shows linked: %+v", st)
			}
			if _, err := e.tokens.Get(tc.id); err != nil {
				t.Fatalf("token not stored: %v", err)
			}
			if err := e.m.PutManifest(ctx, "W", "manifest-000000000001-a.json", []byte("m")); err != nil {
				t.Fatalf("PutManifest: %v", err)
			}
			if tc.count(e) != 1 {
				t.Fatal("manifest did not reach the provider")
			}
			l, err := e.m.ListManifests(ctx, "W")
			if err != nil || len(l) != 1 {
				t.Fatalf("list: %v %v", l, err)
			}

			if err := e.m.Unlink(tc.id); err != nil {
				t.Fatal(err)
			}
			if st := e.m.Status(); st.State != StateUnlinked {
				t.Fatalf("after unlink: %+v", st)
			}
			if _, err := e.m.ListManifests(ctx, "W"); !errors.Is(err, ErrNotLinked) {
				t.Fatalf("after unlink: %v", err)
			}
			if tc.count(e) != 1 {
				t.Fatal("unlink must not touch cloud data")
			}
		})
	}
}

func TestRevokedAccessAsksToReconnectThenRelinks(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, both)
	e.m.StartLink(GoogleDrive)
	e.waitState(t, StateLinked)
	e.revoked = true
	if _, err := e.m.ListManifests(ctx, "W"); !errors.Is(err, oauth.ErrReauth) {
		t.Fatalf("err = %v", err)
	}
	if st := e.m.Status(); st.State != StateReconnect {
		t.Fatalf("status = %+v", st)
	}
	e.revoked = false
	e.m.StartLink(GoogleDrive)
	e.waitState(t, StateLinked)
	if _, err := e.m.ListManifests(ctx, "W"); err != nil {
		t.Fatal(err)
	}
}

func TestChoiceSurvivesRestart(t *testing.T) {
	e := newEnv(t, both)
	if err := e.m.Choose(Dropbox); err != nil {
		t.Fatal(err)
	}
	m2 := New(both, e.tokens, e.m.dir)
	if m2.Status().Chosen != Dropbox {
		t.Fatalf("chosen = %q", m2.Status().Chosen)
	}
	if err := e.m.Choose("nope"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatal(err)
	}
}

func TestStartLinkNeedsClientID(t *testing.T) {
	m := New(Config{}, &oauth.MemoryStore{}, t.TempDir())
	_, err := m.StartLink(GoogleDrive)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if st := m.Status(); st.Providers[0].Configured || st.Providers[1].Configured {
		t.Fatalf("%+v", st)
	}
}

func TestCancelLink(t *testing.T) {
	e := newEnv(t, both)
	e.m.OpenURL = nil
	if _, err := e.m.StartLink(GoogleDrive); err != nil {
		t.Fatal(err)
	}
	if e.m.Status().State != StateLinking {
		t.Fatalf("%+v", e.m.Status())
	}
	e.m.CancelLink()
	if st := e.m.Status(); st.State != StateUnlinked {
		t.Fatalf("%+v", st)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cloud.json"), []byte(`{"googleClientId":"fromfile","dropboxAppKey":"k"}`), 0o600)
	t.Setenv("WORLDHOP_GOOGLE_CLIENT_ID", "fromenv")
	t.Setenv("WORLDHOP_DROPBOX_REDIRECT_PORT", "53682")
	c, err := LoadConfig(dir)
	if err != nil || c.GoogleClientID != "fromenv" || c.DropboxAppKey != "k" || c.DropboxRedirectPort != 53682 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := LoadConfig(t.TempDir()); err != nil {
		t.Fatal(err) // missing file is fine
	}
}
