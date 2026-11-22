// Package oauth implements the installed-app OAuth flow with PKCE on a
// loopback port, plus a refreshing token source.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
)

// ErrReauth means the stored grant is no longer valid (revoked or expired) and
// the user must link the account again.
var ErrReauth = errors.New("oauth: authorization lost, reconnect the account")

// Config describes one provider's OAuth client.
type Config struct {
	ClientID     string
	ClientSecret string // optional; Google's installed-app secret is not confidential
	AuthURL      string
	TokenURL     string
	Scopes       []string
	// ExtraAuthParams are added to the authorization URL (offline access etc).
	ExtraAuthParams map[string]string
	// RedirectPort fixes the loopback port; 0 picks a free one. Use it for
	// providers that require an exact registered redirect URI.
	RedirectPort int
}

// NewVerifier returns a random PKCE code verifier.
func NewVerifier() string { return randString(32) }

// Challenge returns the S256 PKCE challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Flow runs one authorization.
type Flow struct {
	Config  Config
	HTTP    *http.Client
	Timeout time.Duration // how long to wait for the user (default 5 minutes)
}

// Pending is an authorization in progress, waiting for the browser redirect.
type Pending struct {
	// AuthURL is the consent page URL to open in the browser.
	AuthURL string

	f        *Flow
	verifier string
	redirect string
	ln       net.Listener
	srv      *http.Server
	result   chan result
	cancel   context.CancelFunc
}

type result struct {
	code string
	err  error
}

// Start opens the loopback listener and returns the consent URL. Call Wait for
// the token, or Cancel to abandon. The listener closes when either finishes.
func (f *Flow) Start() (*Pending, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", f.Config.RedirectPort))
	if err != nil {
		return nil, fmt.Errorf("oauth: listen on loopback: %w", err)
	}
	p := &Pending{
		f:        f,
		verifier: NewVerifier(),
		ln:       ln,
		result:   make(chan result, 1),
		redirect: fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port),
	}
	state := randString(24)

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", f.Config.ClientID)
	q.Set("redirect_uri", p.redirect)
	q.Set("code_challenge", Challenge(p.verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if len(f.Config.Scopes) > 0 {
		q.Set("scope", strings.Join(f.Config.Scopes, " "))
	}
	for k, v := range f.Config.ExtraAuthParams {
		q.Set(k, v)
	}
	sep := "?"
	if strings.Contains(f.Config.AuthURL, "?") {
		sep = "&"
	}
	p.AuthURL = f.Config.AuthURL + sep + q.Encode()

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		qs := r.URL.Query()
		if qs.Get("state") != state {
			http.Error(w, "unexpected state", http.StatusBadRequest)
			return // ignore stray requests; keep waiting for the real redirect
		}
		var res result
		switch {
		case qs.Get("error") != "":
			res.err = fmt.Errorf("oauth: provider returned %q", qs.Get("error"))
		case qs.Get("code") == "":
			res.err = errors.New("oauth: redirect had no code")
		default:
			res.code = qs.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprintf(w, "<h2>Could not connect</h2><p>%s</p>", html.EscapeString(res.err.Error()))
		} else {
			fmt.Fprint(w, "<h2>Connected</h2><p>You can close this tab and return to WorldHop.</p>")
		}
		select {
		case p.result <- res:
		default:
		}
	})
	p.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go p.srv.Serve(ln)
	return p, nil
}

// Cancel abandons the flow and closes the listener.
func (p *Pending) Cancel() {
	select {
	case p.result <- result{err: context.Canceled}:
	default:
	}
	p.srv.Close()
}

// Wait blocks until the redirect arrives (or timeout/ctx end), exchanges the
// code and returns the token. It always closes the listener.
func (p *Pending) Wait(ctx context.Context) (Token, error) {
	defer p.srv.Close()
	timeout := p.f.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res result
	select {
	case res = <-p.result:
	case <-ctx.Done():
		return Token{}, fmt.Errorf("oauth: timed out waiting for authorization: %w", ctx.Err())
	}
	if res.err != nil {
		return Token{}, res.err
	}
	return exchange(ctx, p.f.HTTP, p.f.Config, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {p.redirect},
		"code_verifier": {p.verifier},
	}, "")
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func exchange(ctx context.Context, hc *http.Client, cfg Config, form url.Values, oldRefresh string) (Token, error) {
	form.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	body := form.Encode()
	resp, err := httpx.Do(ctx, hc, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", cfg.TokenURL, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	})
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	var tr tokenResp
	raw := httpx.ReadErrBody(resp)
	_ = json.Unmarshal([]byte(raw), &tr)
	if resp.StatusCode != http.StatusOK {
		if tr.Error == "invalid_grant" {
			return Token{}, ErrReauth
		}
		return Token{}, fmt.Errorf("oauth: token endpoint %d: %s %s", resp.StatusCode, tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return Token{}, errors.New("oauth: token response had no access token")
	}
	t := Token{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if t.RefreshToken == "" {
		t.RefreshToken = oldRefresh
	}
	if tr.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

// Source is a refreshing token source backed by a TokenStore. It implements
// httpx.Auth.
type Source struct {
	Config Config
	HTTP   *http.Client
	Store  TokenStore
	Key    string

	mu  stdsync.Mutex
	tok *Token
}

var _ httpx.Auth = (*Source)(nil)

// Token returns a valid access token, refreshing and persisting as needed.
// It returns ErrNoToken when nothing is linked and ErrReauth when the grant
// has been revoked.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok == nil {
		t, err := s.Store.Get(s.Key)
		if err != nil {
			return "", err
		}
		s.tok = &t
	}
	if s.tok.AccessToken != "" && (s.tok.Expiry.IsZero() || time.Until(s.tok.Expiry) > time.Minute) {
		return s.tok.AccessToken, nil
	}
	if s.tok.RefreshToken == "" {
		return "", ErrReauth
	}
	t, err := exchange(ctx, s.HTTP, s.Config, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {s.tok.RefreshToken},
	}, s.tok.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := s.Store.Set(s.Key, t); err != nil {
		return "", err
	}
	s.tok = &t
	return t.AccessToken, nil
}

// Invalidate marks the cached access token stale so the next Token refreshes.
func (s *Source) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok != nil {
		s.tok.AccessToken = ""
	}
}

// Reset drops the cached token (after unlink or relink).
func (s *Source) Reset() {
	s.mu.Lock()
	s.tok = nil
	s.mu.Unlock()
}
