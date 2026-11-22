// Package link owns the user's cloud account: which provider is chosen, the
// OAuth link flow, token storage, and the store.Store that syncs through it.
// It is the Go API the web UI's /api/cloud endpoints sit on.
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

	"github.com/JGugino/world-hopper/internal/httpx"
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
	ErrNotLinked = errors.New("link: no cloud account linked")
)

// ProviderStatus describes one provider.
type ProviderStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"` // a client id is available
	Linked     bool   `json:"linked"`     // tokens are stored
}

// Status is the snapshot served to the UI.
type Status struct {
	Chosen    string           `json:"chosen"` // provider id, or "" if none
	State     string           `json:"state"`  // one of the State* constants
	Error     string           `json:"error,omitempty"`
	Providers []ProviderStatus `json:"providers"`
}

type provider struct {
	id, name   string
	configured bool
	hint       string // what to set when not configured
	oauth      oauth.Config
	newStore   func(httpx.Auth, *http.Client) store.Store
}

// Manager implements store.Store by delegating to the chosen, linked provider,
// so the sync engine can be built once and follow the user's choice.
type Manager struct {
	// OpenURL, if set, is called with the consent URL when linking starts.
	OpenURL func(url string) error
	// HTTP is used for all provider calls (default: http.DefaultClient).
	HTTP *http.Client

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
					AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
					TokenURL: "https://oauth2.googleapis.com/token",
					Scopes:   []string{gdrive.Scope},
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

func (m *Manager) choiceFile() string { return filepath.Join(m.dir, "provider.json") }

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

func (m *Manager) linked(id string) bool {
	_, err := m.tokens.Get(id)
	return err == nil
}

// Status returns the current state for the UI.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Chosen: m.chosen, Error: m.lastErr}
	ids := []string{GoogleDrive} // add new providers here
	for _, id := range ids {
		p := m.providers[id]
		st.Providers = append(st.Providers, ProviderStatus{ID: id, Name: p.name, Configured: p.configured, Linked: m.linked(id)})
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
	flow := &oauth.Flow{Config: p.oauth, HTTP: m.HTTP}
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

func (m *Manager) finish(id string, pend *oauth.Pending) {
	tok, err := pend.Wait(context.Background())
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != pend {
		return // superseded or cancelled
	}
	m.pending = nil
	if err != nil {
		m.lastErr = err.Error()
		return
	}
	if err := m.tokens.Set(id, tok); err != nil {
		m.lastErr = "could not save credentials to the system keychain: " + err.Error()
		return
	}
	m.resetProvider(id)
}

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

// Unlink forgets the stored credentials for a provider. Cloud data is left
// untouched. (Access can also be revoked in the provider's account settings.)
func (m *Manager) Unlink(id string) error {
	if m.providers[id] == nil {
		return ErrUnknownProvider
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil && m.pendID == id {
		m.pending.Cancel()
		m.pending = nil
	}
	if err := m.tokens.Delete(id); err != nil {
		return err
	}
	m.resetProvider(id)
	m.lastErr = ""
	return nil
}

// current returns the chosen provider's store, building it on first use.
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
	if s := m.stores[id]; s != nil {
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

func (m *Manager) PutPack(ctx context.Context, name string, data []byte) error {
	s, id, err := m.current()
	if err != nil {
		return err
	}
	return m.wrap(id, s.PutPack(ctx, name, data))
}

func (m *Manager) GetPack(ctx context.Context, name string) ([]byte, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	b, err := s.GetPack(ctx, name)
	return b, m.wrap(id, err)
}

func (m *Manager) PutManifest(ctx context.Context, world, name string, data []byte) error {
	s, id, err := m.current()
	if err != nil {
		return err
	}
	return m.wrap(id, s.PutManifest(ctx, world, name, data))
}

func (m *Manager) GetManifest(ctx context.Context, world, name string) ([]byte, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	b, err := s.GetManifest(ctx, world, name)
	return b, m.wrap(id, err)
}

func (m *Manager) ListManifests(ctx context.Context, world string) ([]string, error) {
	s, id, err := m.current()
	if err != nil {
		return nil, err
	}
	l, err := s.ListManifests(ctx, world)
	return l, m.wrap(id, err)
}

// NotConfiguredError means this build has no OAuth client ID for the provider.
// Its message is aimed at developers: end users of a release build never see it.
type NotConfiguredError struct{ Provider, Hint string }

func (e *NotConfiguredError) Error() string {
	return fmt.Sprintf("%s sign-in is not set up in this build: no OAuth client ID was compiled in. "+
		"Release builds include one (see docs/cloud-setup.md); for a development build set %s or add it to cloud.json",
		e.Provider, e.Hint)
}

func (e *NotConfiguredError) Is(target error) bool { return target == ErrNotConfigured }
