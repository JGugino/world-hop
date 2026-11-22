package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestChallengeMatchesRFC7636Vector(t *testing.T) {
	got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"; got != want {
		t.Fatalf("challenge = %s, want %s", got, want)
	}
}

// fakeProvider implements the consent redirect and token endpoint.
type fakeProvider struct {
	*httptest.Server
	challenge, redirect, state string
	refreshes                  int
	revoked                    bool
}

func newFakeProvider(t *testing.T) *fakeProvider {
	p := &fakeProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p.challenge, p.redirect, p.state = q.Get("code_challenge"), q.Get("redirect_uri"), q.Get("state")
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid" || q.Get("response_type") != "code" ||
			q.Get("scope") != "s1 s2" || q.Get("token_access_type") != "offline" {
			http.Error(w, "bad auth params: "+r.URL.RawQuery, 400)
			return
		}
		http.Redirect(w, r, p.redirect+"?code=thecode&state="+url.QueryEscape(p.state), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "thecode" || Challenge(r.Form.Get("code_verifier")) != p.challenge ||
				r.Form.Get("redirect_uri") != p.redirect || r.Form.Get("client_id") != "cid" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Write([]byte(`{"access_token":"a1","refresh_token":"r1","expires_in":3600}`))
		case "refresh_token":
			p.refreshes++
			if p.revoked || r.Form.Get("refresh_token") != "r1" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Write([]byte(`{"access_token":"a2","expires_in":3600}`)) // no new refresh token
		}
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProvider) cfg() Config {
	return Config{
		ClientID: "cid", AuthURL: p.URL + "/auth", TokenURL: p.URL + "/token",
		Scopes: []string{"s1", "s2"}, ExtraAuthParams: map[string]string{"token_access_type": "offline"},
	}
}

func TestLoopbackFlowAndRefresh(t *testing.T) {
	p := newFakeProvider(t)
	f := &Flow{Config: p.cfg(), HTTP: p.Client()}
	pend, err := f.Start()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pend.redirect, "http://127.0.0.1:") {
		t.Fatalf("redirect not loopback: %s", pend.redirect)
	}
	// The "browser": follows the consent URL, which redirects to the loopback.
	go func() {
		resp, err := http.Get(pend.AuthURL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	tok, err := pend.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "a1" || tok.RefreshToken != "r1" {
		t.Fatalf("token = %+v", tok)
	}

	// Source: stored token near expiry refreshes, keeps the refresh token, persists.
	store := &MemoryStore{}
	store.Set("k", Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	src := &Source{Config: p.cfg(), HTTP: p.Client(), Store: store, Key: "k"}
	got, err := src.Token(context.Background())
	if err != nil || got != "a2" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	saved, _ := store.Get("k")
	if saved.AccessToken != "a2" || saved.RefreshToken != "r1" {
		t.Fatalf("persisted = %+v", saved)
	}
	if got, _ = src.Token(context.Background()); got != "a2" || p.refreshes != 1 {
		t.Fatalf("cached token expected, refreshes=%d", p.refreshes)
	}
	src.Invalidate()
	if _, err := src.Token(context.Background()); err != nil || p.refreshes != 2 {
		t.Fatalf("invalidate must refresh: %v %d", err, p.refreshes)
	}
}

func TestRevokedGrantNeedsReauth(t *testing.T) {
	p := newFakeProvider(t)
	p.revoked = true
	store := &MemoryStore{}
	store.Set("k", Token{AccessToken: "x", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	src := &Source{Config: p.cfg(), HTTP: p.Client(), Store: store, Key: "k"}
	if _, err := src.Token(context.Background()); !errors.Is(err, ErrReauth) {
		t.Fatalf("err = %v", err)
	}
	empty := &Source{Config: p.cfg(), HTTP: p.Client(), Store: &MemoryStore{}, Key: "none"}
	if _, err := empty.Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongStateIsIgnoredAndErrorsReported(t *testing.T) {
	p := newFakeProvider(t)
	f := &Flow{Config: p.cfg(), HTTP: p.Client()}
	pend, _ := f.Start()
	// A stray request with the wrong state must not complete or poison the flow.
	resp, err := http.Get(pend.redirect + "?code=evil&state=nope")
	if err != nil || resp.StatusCode != 400 {
		t.Fatalf("stray request: %v %v", resp, err)
	}
	resp.Body.Close()
	state := pend.AuthURL[strings.Index(pend.AuthURL, "state=")+6:]
	state, _, _ = strings.Cut(state, "&")
	go http.Get(pend.redirect + "?error=access_denied&state=" + state)
	if _, err := pend.Wait(context.Background()); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	p := newFakeProvider(t)
	f := &Flow{Config: p.cfg(), HTTP: p.Client(), Timeout: 50 * time.Millisecond}
	pend, _ := f.Start()
	if _, err := pend.Wait(context.Background()); err == nil {
		t.Fatal("expected timeout")
	}
	pend, _ = f.Start()
	pend.Cancel()
	if _, err := pend.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// Listener is closed afterwards.
	if _, err := http.Get(pend.redirect); err == nil {
		t.Fatal("listener still open")
	}
}
