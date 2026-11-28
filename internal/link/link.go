// Package link owns the user's cloud account: which provider is chosen, the
// OAuth link flow, token storage, and the store.Store that syncs through it.
// It is the Go API the web UI's /api/cloud endpoints sit on.
//
// The Manager here is the bottom of the store stack the sync engine sees (engine ->
// crypt.Manager -> link.Manager -> gdrive/s3). Because it implements store.Store by
// delegating to whichever provider is chosen and linked, the engine is built once and
// follows the user's choice: connecting, disconnecting or switching provider needs no
// restart. When nothing usable is linked, every store call fails with ErrNotLinked
// and the UI says so.
//
// Files: link.go (the Manager and the sign-in flow), s3.go (the S3-compatible
// provider, a Pro feature, set up with a form), providers.go (adapters to the
// api package's interfaces), config.go (OAuth client IDs).
package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/gdrive"
)

// Provider ids, as used by the API.
const (
	GoogleDrive = "gdrive"
)

// Link states reported for the chosen provider.
const (
	StateNone      = "none"            // no provider chosen
	StateUnlinked  = "unlinked"        // chosen, not connected
	StateLinking   = "linking"         // consent page open, waiting for the browser
	StateLinked    = "linked"          // connected
	StateReconnect = "needs-reconnect" // grant revoked or expired
)

var (
	ErrUnknownProvider = errors.New("link: unknown provider")
	ErrNotConfigured   = errors.New("link: provider has no client id configured")
	// ErrNotLinked is returned by store calls when no usable account is linked.
	ErrNotLinked = store.ErrNotLinked
)

// ProviderStatus describes one provider.
type ProviderStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"` // a client id is available
	Linked     bool   `json:"linked"`     // tokens are stored
	Pro        bool   `json:"pro"`        // needs a Pro licence
	Locked     bool   `json:"locked"`     // Pro, and this computer has none
}

// Status is the snapshot served to the UI.
type Status struct {
	Chosen    string           `json:"chosen"` // provider id, or "" if none
	State     string           `json:"state"`  // one of the State* constants
	Error     string           `json:"error,omitempty"`
	Providers []ProviderStatus `json:"providers"`
}

// provider is one cloud choice: its OAuth settings (Drive), how to build its store
// from credentials, and flags saying it is set up with a form instead of a sign-in
// (S3) or needs a Pro license.
type provider struct {
	id, name   string
	configured bool
	hint       string // what to set when not configured
	oauth      oauth.Config
	newStore   func(httpx.Auth, *http.Client) store.Store
	manual     bool // set up with a form, not an OAuth sign-in
	pro        bool // needs a Pro licence
}

// Manager implements store.Store by delegating to the chosen, linked provider,
// so the sync engine can be built once and follow the user's choice.
type Manager struct {
	// OpenURL, if set, is called with the consent URL when linking starts.
	OpenURL func(url string) error
	// HTTP is used for all provider calls (default: httpx.Shared, a pooled client).
	HTTP *http.Client
	// OnChange, if set, is called (in its own goroutine) after an account is
	// connected, disconnected or reconfigured, so callers can re-check sync
	// status instead of showing a stale "no cloud account" message.
	OnChange func()

	dir       string
	tokens    oauth.TokenStore
	providers map[string]*provider

	mu      stdsync.Mutex
	chosen  string
	pending *oauth.Pending
	pendID  string
	lastErr string
	reauth  map[string]bool
	sources map[string]*oauth.Source
	stores  map[string]store.Store

	secrets SecretStore     // set by EnableS3
	license license.Checker // set by EnableS3
}

var _ store.Store = (*Manager)(nil)

// New returns a Manager. stateDir holds the chosen-provider file; tokens go to
// the given TokenStore (the OS keychain in production).
func New(cfg Config, tokens oauth.TokenStore, stateDir string) *Manager {
	m := &Manager{
		dir:     stateDir,
		tokens:  tokens,
		reauth:  map[string]bool{},
		sources: map[string]*oauth.Source{},
		stores:  map[string]store.Store{},
		providers: map[string]*provider{
			GoogleDrive: {
				id: GoogleDrive, name: "Google Drive",
				configured: cfg.GoogleClientID != "",
				hint:       "WORLDHOP_GOOGLE_CLIENT_ID",
				oauth: oauth.Config{
					ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret,
					AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
					TokenURL:  "https://oauth2.googleapis.com/token",
					RevokeURL: "https://oauth2.googleapis.com/revoke",
					Scopes:    []string{gdrive.Scope},
					// offline + consent make Google return a refresh token.
					ExtraAuthParams: map[string]string{"access_type": "offline", "prompt": "consent"},
				},
				newStore: func(a httpx.Auth, hc *http.Client) store.Store { return gdrive.New(a, hc) },
			},
		},
	}
	m.chosen = m.loadChoice()
	return m
}

// choiceFile is where the chosen provider is saved (provider.json in the state dir).
func (m *Manager) choiceFile() string { return filepath.Join(m.dir, "provider.json") }

// loadChoice reads the saved provider id; an unknown or missing choice is none.
func (m *Manager) loadChoice() string {
	b, err := os.ReadFile(m.choiceFile())
	if err != nil {
		return ""
	}
	var v struct {
		Provider string `json:"provider"`
	}
	if json.Unmarshal(b, &v) != nil || m.providers[v.Provider] == nil {
		return ""
	}
	return v.Provider
}

// saveChoice records the chosen provider (written atomically, owner-only).
func (m *Manager) saveChoice(id string) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"provider": id})
	tmp := m.choiceFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.choiceFile())
}

// linked reports whether credentials are stored for a provider (for S3, both the
// settings and the keys), without checking they still work.
func (m *Manager) linked(id string) bool {
	if id == S3 {
		return m.s3Linked()
	}
	_, err := m.tokens.Get(id)
	return err == nil
}

// Status returns the current state for the UI.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Chosen: m.chosen, Error: m.lastErr}
	ids := []string{GoogleDrive} // add new providers here
	if m.providers[S3] != nil {
		ids = append(ids, S3)
	}
	for _, id := range ids {
		p := m.providers[id]
		st.Providers = append(st.Providers, ProviderStatus{
			ID: id, Name: p.name, Configured: p.configured, Linked: m.linked(id),
			Pro: p.pro, Locked: p.pro && !m.Pro(),
		})
	}
	switch {
	case m.chosen == "":
		st.State = StateNone
	case m.pending != nil && m.pendID == m.chosen:
		st.State = StateLinking
	case m.reauth[m.chosen]:
		st.State = StateReconnect
	case m.linked(m.chosen):
		st.State = StateLinked
	default:
		st.State = StateUnlinked
	}
	return st
}

// Choose selects the provider used for syncing. It does not link; a provider
// that is not linked yet shows as "unlinked" until StartLink completes.
func (m *Manager) Choose(id string) error {
	if m.providers[id] == nil {
		return ErrUnknownProvider
	}
	if m.providers[id].pro {
		if err := license.Require(m.license); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveChoice(id); err != nil {
		return err
	}
	m.chosen = id
	m.lastErr = ""
	return nil
}

// StartLink begins the OAuth flow for a provider: it listens on a loopback
// port, opens the consent page via OpenURL (if set) and returns the consent URL
// so the UI can also show it. Completion is asynchronous; poll Status until the
// state is "linked" (or Error is set). It also chooses the provider.
func (m *Manager) StartLink(id string) (string, error) {
	p := m.providers[id]
	if p == nil {
		return "", ErrUnknownProvider
	}
	if p.manual {
		return "", ErrManualSetup
	}
	if !p.configured {
		return "", &NotConfiguredError{Provider: p.name, Hint: p.hint}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		m.pending.Cancel()
		m.pending = nil
	}
	if err := m.saveChoice(id); err != nil {
		return "", err
	}
	flow := &oauth.Flow{Name: p.name, Config: p.oauth, HTTP: m.HTTP}
	pend, err := flow.Start()
	if err != nil {
		return "", err
	}
	m.chosen, m.pending, m.pendID, m.lastErr = id, pend, id, ""
	go m.finish(id, pend)
	if m.OpenURL != nil {
		if err := m.OpenURL(pend.AuthURL); err != nil {
			m.lastErr = "could not open the browser; open the link manually"
		}
	}
	return pend.AuthURL, nil
}

// finish runs in the background after StartLink: it waits for the browser sign-in
// to complete, saves the tokens in the keychain, and then tells the browser page how it
// ended (Pending.Saved), so the page only says "connected" when it really is. If the
// attempt was cancelled or replaced meanwhile it just tells the page that.
func (m *Manager) finish(id string, pend *oauth.Pending) {
	tok, err := pend.Wait(context.Background())
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != pend {
		pend.Cancel() // superseded or cancelled: tell the browser page
		return
	}
	m.pending = nil
	if err != nil {
		m.lastErr = err.Error()
		return
	}
	if err := m.tokens.Set(id, tok); err != nil {
		m.lastErr = "could not save credentials to the system keychain: " + err.Error()
		pend.Saved(err)
		return
	}
	pend.Saved(nil)
	m.resetProvider(id)
	m.changed()
}

// changed tells OnChange that the set of linked accounts changed.
func (m *Manager) changed() {
	if f := m.OnChange; f != nil {
		go f()
	}
}

// resetProvider drops everything built for a provider (its store and token source)
// and any re-authorisation flag, so the next call rebuilds from what is stored now.
func (m *Manager) resetProvider(id string) {
	delete(m.reauth, id)
	delete(m.stores, id)
	delete(m.sources, id)
}

// CancelLink abandons a link in progress.
func (m *Manager) CancelLink() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		m.pending.Cancel()
		m.pending = nil
	}
}

// Revocation says whether unlinking also ended WorldHop's access at the provider.
type Revocation struct {
	// Revoked: the provider confirmed the access was ended.
	Revoked bool
	// Err is why it was not, when there was an account to revoke and it could not be
	// done (offline, provider unavailable). The credentials were still forgotten here.
	Err error
	// ManualKey is set when WorldHop cannot revoke the credential at all and the
	// person has to: for S3 it is the access key id that was forgotten here, which
	// stays valid with the storage service until it is deleted or rotated there.
	// (Revoking a key is an account-management call that differs for every
	// S3-compatible service, and a bucket key usually may not delete itself.)
	ManualKey string
}

// revokeTimeout bounds the call to the provider, so unlinking while offline is
// slow for a few seconds, not for minutes.
const revokeTimeout = 10 * time.Second

// Unlink forgets the stored credentials for a provider, and for an OAuth account
// first asks the provider to revoke WorldHop's access, so it ends there too and a
// copied token stops working. The local credentials are forgotten even if revoking
// fails (being offline must not keep an account linked); the Revocation says what
// happened so the person can be told to remove the access in the provider's account
// settings. Cloud data is left untouched. S3 has no grant to revoke: its access
// keys stay valid with the service until the person deletes them there, so the
// Revocation names the key to delete.
func (m *Manager) Unlink(id string) (Revocation, error) {
	p := m.providers[id]
	if p == nil {
		return Revocation{}, ErrUnknownProvider
	}
	var rev Revocation
	if id != S3 {
		rev = m.revoke(id, p)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil && m.pendID == id {
		m.pending.Cancel()
		m.pending = nil
	}
	if id == S3 {
		if c, err := m.loadS3Creds(); err == nil {
			rev.ManualKey = c.AccessKey
		}
		if err := m.forgetS3(); err != nil {
			return rev, err
		}
	} else if err := m.tokens.Delete(id); err != nil {
		return rev, err
	}
	m.resetProvider(id)
	m.lastErr = ""
	m.changed()
	return rev, nil
}

// revoke asks the provider to end the stored grant. It does not hold the manager's
// lock while it talks to the network, so store calls are not blocked meanwhile.
func (m *Manager) revoke(id string, p *provider) Revocation {
	m.mu.Lock()
	src := m.sources[id]
	m.mu.Unlock()
	if src == nil { // never used in this run: read the stored token directly
		src = &oauth.Source{Config: p.oauth, HTTP: m.HTTP, Store: m.tokens, Key: id}
	}
	ctx, cancel := context.WithTimeout(context.Background(), revokeTimeout)
	defer cancel()
	switch err := src.Revoke(ctx); {
	case err == nil:
		return Revocation{Revoked: true}
	case errors.Is(err, oauth.ErrNoToken): // nothing was linked
		return Revocation{}
	default:
		return Revocation{Err: err}
	}
}

// current returns the chosen provider's store, building it on first use. It is the
// single place that decides whether a store call may proceed: nothing chosen or linked
// is ErrNotLinked, a revoked or expired grant is oauth.ErrReauth, and Pro storage is
// re-checked against the license on every call, so a lapsed license stops S3 even
// though its store is already built.
func (m *Manager) current() (store.Store, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.chosen
	if id == "" {
		return nil, "", ErrNotLinked
	}
	if m.reauth[id] {
		return nil, id, oauth.ErrReauth
	}
	if m.providers[id].pro {
		// Checked on every call, so a licence that lapses stops Pro storage
		// even though the store is already built.
		if err := license.Require(m.license); err != nil {
			return nil, id, err
		}
	}
	if s := m.stores[id]; s != nil {
		return s, id, nil
	}
	if id == S3 {
		s, err := m.newS3Store()
		if err != nil {
			return nil, id, err
		}
		m.stores[id] = s
		return s, id, nil
	}
	if !m.linked(id) {
		return nil, id, ErrNotLinked
	}
	p := m.providers[id]
	src := &oauth.Source{Config: p.oauth, HTTP: m.HTTP, Store: m.tokens, Key: id}
	s := p.newStore(src, m.HTTP)
	m.sources[id], m.stores[id] = src, s
	return s, id, nil
}

// wrap maps credential failures onto the link state.
func (m *Manager) wrap(id string, err error) error {
	switch {
	case errors.Is(err, oauth.ErrNoToken):
		return ErrNotLinked
	case errors.Is(err, oauth.ErrReauth):
		m.mu.Lock()
		m.reauth[id] = true
		m.mu.Unlock()
	}
	return err
}

// PutPack stores a pack on the chosen provider. Like GetPack, PutManifest, GetManifest,
// ListManifests, MissingPacks and ListWorlds below, it implements store.Store: it asks
// current for the provider's store (failing if none is linked, the grant expired, or
// Pro storage is no longer licensed), runs the call, and maps credential failures onto
// the link state with wrap.
func (m *Manager) PutPack(ctx context.Context, name string, data []byte) error {
	s, id, err := m.current()
	if err != nil {
		return err
	}
	return m.wrap(id, s.PutPack(ctx, name, data))
}

// GetPack fetches a pack from the chosen provider.
func (m *Manager) GetPack(ctx context.Context, name string) ([]byte, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	b, err := s.GetPack(ctx, name)
	return b, m.wrap(id, err)
}

// PutManifest stores a manifest on the chosen provider (never overwriting).
func (m *Manager) PutManifest(ctx context.Context, world, name string, data []byte) error {
	s, id, err := m.current()
	if err != nil {
		return err
	}
	return m.wrap(id, s.PutManifest(ctx, world, name, data))
}

// GetManifest fetches a manifest from the chosen provider.
func (m *Manager) GetManifest(ctx context.Context, world, name string) ([]byte, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	b, err := s.GetManifest(ctx, world, name)
	return b, m.wrap(id, err)
}

// ListManifests lists a world's manifest names on the chosen provider.
func (m *Manager) ListManifests(ctx context.Context, world string) ([]string, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	l, err := s.ListManifests(ctx, world)
	return l, m.wrap(id, err)
}

// MissingPacks implements store.PackChecker.
func (m *Manager) MissingPacks(ctx context.Context, names []string) ([]string, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	chk, ok := s.(store.PackChecker)
	if !ok {
		return nil, store.ErrUnsupported
	}
	miss, err := chk.MissingPacks(ctx, names)
	return miss, m.wrap(id, err)
}

// ListWorlds lists the worlds on the chosen provider.
func (m *Manager) ListWorlds(ctx context.Context) ([]string, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	l, err := s.ListWorlds(ctx)
	return l, m.wrap(id, err)
}

// NotConfiguredError means this build has no OAuth client ID for the provider.
// Its message is aimed at developers: end users of a release build never see it.
type NotConfiguredError struct{ Provider, Hint string }

// Error says which provider has no client id and how a developer would set one.
func (e *NotConfiguredError) Error() string {
	return fmt.Sprintf("%s sign-in is not set up in this build: no OAuth client ID was compiled in. "+
		"Release builds include one (see docs/cloud-setup.md); for a development build set %s or add it to cloud.json",
		e.Provider, e.Hint)
}

// Is lets errors.Is(err, ErrNotConfigured) match this error.
func (e *NotConfiguredError) Is(target error) bool { return target == ErrNotConfigured }
