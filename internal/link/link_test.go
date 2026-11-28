package link

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/gdrive"
	"github.com/JGugino/world-hopper/internal/store/storetest"
)

// env wires a Manager to a fake identity provider and fake Drive API.
type env struct {
	m       *Manager
	tokens  *oauth.MemoryStore
	idp     *httptest.Server
	drive   *storetest.FakeDrive
	revoked bool
	// revokeCalls are the tokens sent to the provider's revoke endpoint;
	// revokeDown makes that endpoint fail like an unreachable provider.
	revokeCalls []string
	revokeDown  bool
	state       string
	redir       string
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	httpx.BaseDelay = time.Millisecond
	e := &env{tokens: &oauth.MemoryStore{}, drive: storetest.NewFakeDrive()}
	t.Cleanup(e.drive.Close)
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
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		e.revokeCalls = append(e.revokeCalls, r.Form.Get("token"))
		if e.revokeDown {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"server_error"}`))
		}
	})
	e.idp = httptest.NewServer(mux)
	t.Cleanup(e.idp.Close)

	e.m = New(cfg, e.tokens, t.TempDir())
	e.m.HTTP = e.idp.Client()
	g := e.m.providers[GoogleDrive]
	g.oauth.AuthURL, g.oauth.TokenURL, g.oauth.RevokeURL = e.idp.URL+"/auth", e.idp.URL+"/token", e.idp.URL+"/revoke"
	g.newStore = func(a httpx.Auth, hc *http.Client) store.Store {
		s := gdrive.New(a, e.drive.Client())
		s.APIBase = e.drive.URL
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

var both = Config{GoogleClientID: "gid"}

func TestLinkAndSyncThroughGoogleDrive(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		id    string
		count func(e *env) int
	}{
		{GoogleDrive, func(e *env) int { return len(e.drive.Names()) }},
	} {
		t.Run(tc.id, func(t *testing.T) {
			e := newEnv(t, both)
			if st := e.m.Status(); st.State != StateNone || len(st.Providers) != 1 {
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
			if !st.Providers[0].Linked {
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

			if rev, err := e.m.Unlink(tc.id); err != nil || !rev.Revoked || rev.Err != nil {
				t.Fatalf("unlink: %+v %v", rev, err)
			}
			if len(e.revokeCalls) != 1 || e.revokeCalls[0] != "r" {
				t.Fatalf("the provider was not told to revoke the refresh token: %v", e.revokeCalls)
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
	if err := e.m.Choose(GoogleDrive); err != nil {
		t.Fatal(err)
	}
	m2 := New(both, e.tokens, e.m.dir)
	if m2.Status().Chosen != GoogleDrive {
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
	if st := m.Status(); st.Providers[0].Configured {
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

func TestLoadConfigCompiledInDefaults(t *testing.T) {
	DefaultGoogleClientID, DefaultGoogleClientSecret = "gdef", "gsec"
	t.Cleanup(func() {
		DefaultGoogleClientID, DefaultGoogleClientSecret = "", ""
	})
	c, err := LoadConfig(t.TempDir()) // no cloud.json, no env: defaults apply
	if err != nil || c.GoogleClientID != "gdef" || c.GoogleClientSecret != "gsec" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("WORLDHOP_GOOGLE_CLIENT_ID", "override")
	if c, _ := LoadConfig(t.TempDir()); c.GoogleClientID != "override" {
		t.Fatalf("%+v", c)
	}
}

func TestNotConfiguredMessageIsDeveloperFacing(t *testing.T) {
	m := New(Config{}, &oauth.MemoryStore{}, t.TempDir())
	_, err := m.StartLink(GoogleDrive)
	if !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "docs/cloud-setup.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cloud.json"), []byte(`{"googleClientId":"fromfile"}`), 0o600)
	t.Setenv("WORLDHOP_GOOGLE_CLIENT_ID", "fromenv")
	c, err := LoadConfig(dir)
	if err != nil || c.GoogleClientID != "fromenv" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := LoadConfig(t.TempDir()); err != nil {
		t.Fatal(err) // missing file is fine
	}
}

// Linking and unlinking must notify OnChange so the app can re-run a sync pass;
// otherwise the "no cloud account is connected" message from the last pass
// stays on screen after the account connects.
func TestOnChangeFiresOnLinkAndUnlink(t *testing.T) {
	e := newEnv(t, both)
	changed := make(chan struct{}, 4)
	e.m.OnChange = func() { changed <- struct{}{} }
	wait := func(what string) {
		t.Helper()
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatalf("OnChange not called after %s", what)
		}
	}
	if _, err := e.m.StartLink(GoogleDrive); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateLinked)
	wait("linking")
	if _, err := e.m.Unlink(GoogleDrive); err != nil {
		t.Fatal(err)
	}
	wait("unlinking")
}

// A provider that can't be reached must not keep the account linked: the
// credentials are forgotten here and the result says revoking failed.
func TestUnlinkStillForgetsWhenRevokeFails(t *testing.T) {
	e := newEnv(t, both)
	if _, err := e.m.StartLink(GoogleDrive); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateLinked)
	e.revokeDown = true
	rev, err := e.m.Unlink(GoogleDrive)
	if err != nil || rev.Revoked || rev.Err == nil {
		t.Fatalf("unlink: %+v %v", rev, err)
	}
	if _, err := e.tokens.Get(GoogleDrive); !errors.Is(err, oauth.ErrNoToken) {
		t.Fatalf("token still stored: %v", err)
	}
	if st := e.m.Status(); st.State != StateUnlinked {
		t.Fatalf("after unlink: %+v", st)
	}
}

// Unlinking an account that was never linked asks the provider nothing.
func TestUnlinkWithNothingLinkedDoesNotCallTheProvider(t *testing.T) {
	e := newEnv(t, both)
	rev, err := e.m.Unlink(GoogleDrive)
	if err != nil || rev.Revoked || rev.Err != nil || len(e.revokeCalls) != 0 {
		t.Fatalf("%+v %v %v", rev, err, e.revokeCalls)
	}
}
