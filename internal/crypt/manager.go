package crypt

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/store"
)

var (
	// ErrLocked: encryption is on but no key is loaded (after a restart without
	// "remember", or after locking).
	ErrLocked = store.ErrEncryptionLocked
	// ErrProRequired: turning encryption on is a Pro feature.
	ErrProRequired = license.ErrProRequired
	// ErrNotConfirmed: creating a new key needs the passphrase twice and an
	// explicit acknowledgement that a lost passphrase cannot be recovered.
	ErrNotConfirmed = errors.New("type the passphrase twice and confirm that you understand it cannot be recovered")
)

// Keychain keeps the unlocked master key for users who opt to remember it.
type Keychain interface {
	Get(id string) ([]byte, error) // (nil, nil) if absent
	Set(id string, key []byte) error
	Delete(id string) error
}

// KeyringKeys keeps keys in the OS keychain.
type KeyringKeys struct{ Service string }

func (k KeyringKeys) Get(id string) ([]byte, error) {
	s, err := keyring.Get(k.Service, "encryption-"+id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(s)
}

func (k KeyringKeys) Set(id string, key []byte) error {
	return keyring.Set(k.Service, "encryption-"+id, hex.EncodeToString(key))
}

func (k KeyringKeys) Delete(id string) error {
	err := keyring.Delete(k.Service, "encryption-"+id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

type settings struct {
	Enabled  bool `json:"enabled"`
	Remember bool `json:"remember"`
}

// Manager is the store the engine uses. It passes straight through while
// encryption is off and encrypts once it is on, switchable at runtime. It
// implements store.Store.
type Manager struct {
	Inner store.Store
	Keys  Keychain
	// Pro reports whether the Pro license is active. Only turning encryption on
	// is gated: data that is already encrypted stays readable if Pro lapses.
	Pro func() bool
	// Params is the KDF cost for new keys (DefaultParams if zero).
	Params Params

	path string
	mu   sync.Mutex
	cfg  settings
	hdr  *Header
	key  *Key
	enc  *Store
}

var _ store.Store = (*Manager)(nil)

// NewManager loads settings from dir/encryption.json.
func NewManager(inner store.Store, dir string, keys Keychain, pro func() bool) (*Manager, error) {
	m := &Manager{Inner: inner, Keys: keys, Pro: pro, path: filepath.Join(dir, "encryption.json")}
	b, err := os.ReadFile(m.path)
	if err == nil {
		if err := json.Unmarshal(b, &m.cfg); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return m, nil
}

func (m *Manager) save() error {
	b, _ := json.Marshal(m.cfg)
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// Scope names the sync-state namespace: encrypted and plain stores are
// different worlds as far as "what did I last sync" is concerned.
func (m *Manager) Scope() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Enabled {
		return "enc"
	}
	return ""
}

// current returns the store to use right now.
func (m *Manager) current(ctx context.Context) (store.Store, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.Enabled {
		return m.Inner, nil
	}
	if m.enc != nil {
		return m.enc, nil
	}
	h, err := ReadHeader(ctx, m.Inner)
	if err != nil {
		return nil, err
	}
	k := m.key
	if k == nil && m.Keys != nil {
		if raw, err := m.Keys.Get(h.ID()); err == nil && raw != nil {
			k, _ = KeyFromBytes(raw)
		}
	}
	if k == nil {
		return nil, ErrLocked
	}
	if err := h.Verify(k); err != nil {
		return nil, err
	}
	m.hdr, m.key, m.enc = h, k, New(m.Inner, k)
	return m.enc, nil
}

// Info is what the UI shows.
type Info struct {
	Pro      bool `json:"pro"`
	Enabled  bool `json:"enabled"`
	Unlocked bool `json:"unlocked"`
	Remember bool `json:"remember"`
	// KeyInStore: whether the store already has a key (another computer set it
	// up). Nil when it could not be checked (for example no account linked).
	KeyInStore *bool  `json:"keyInStore"`
	Error      string `json:"error,omitempty"`
}

// Info reports the current state. It may touch the store to look for a key.
func (m *Manager) Info(ctx context.Context) Info {
	in := Info{Pro: m.Pro != nil && m.Pro()}
	m.mu.Lock()
	in.Enabled, in.Remember = m.cfg.Enabled, m.cfg.Remember
	in.Unlocked = m.enc != nil
	m.mu.Unlock()
	if in.Enabled && !in.Unlocked {
		if _, err := m.current(ctx); err == nil {
			in.Unlocked = true // key came from the keychain
		} else if !errors.Is(err, ErrLocked) {
			in.Error = err.Error()
		}
	}
	if !in.Unlocked {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err := ReadHeader(cctx, m.Inner)
		switch {
		case err == nil:
			t := true
			in.KeyInStore = &t
		case errors.Is(err, ErrNoKey):
			f := false
			in.KeyInStore = &f
		}
	}
	return in
}

// Enable turns encryption on. If the store has no key yet it creates one (the
// passphrase must be typed twice and acknowledged); otherwise the passphrase
// must match the existing key, which is how a second computer joins.
func (m *Manager) Enable(ctx context.Context, pass, confirm string, acknowledged, remember bool) error {
	if m.Pro == nil || !m.Pro() {
		return ErrProRequired
	}
	h, err := ReadHeader(ctx, m.Inner)
	var k *Key
	switch {
	case errors.Is(err, ErrNoKey):
		if pass != confirm || !acknowledged {
			return ErrNotConfirmed
		}
		p := m.Params
		if p == (Params{}) {
			p = DefaultParams
		}
		var nh *Header
		if nh, k, err = NewHeader(pass, p); err != nil {
			return err
		}
		switch werr := WriteHeader(ctx, m.Inner, nh); {
		case werr == nil:
			h = nh
		case errors.Is(werr, store.ErrExists): // another computer won the race
			if h, err = ReadHeader(ctx, m.Inner); err != nil {
				return err
			}
			if k, err = h.Unlock(pass); err != nil {
				return err
			}
		default:
			return werr
		}
	case err != nil:
		return err
	default:
		if k, err = h.Unlock(pass); err != nil {
			return err
		}
	}
	return m.set(h, k, remember)
}

// Unlock supplies the passphrase after a restart or a lock.
func (m *Manager) Unlock(ctx context.Context, pass string, remember bool) error {
	h, err := ReadHeader(ctx, m.Inner)
	if err != nil {
		return err
	}
	k, err := h.Unlock(pass)
	if err != nil {
		return err
	}
	return m.set(h, k, remember)
}

func (m *Manager) set(h *Header, k *Key, remember bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if remember && m.Keys != nil {
		if err := m.Keys.Set(h.ID(), k.Bytes()); err != nil {
			return errors.New("could not save to the system keychain: " + err.Error())
		}
	} else if m.Keys != nil {
		m.Keys.Delete(h.ID())
	}
	prev := m.cfg
	m.cfg = settings{Enabled: true, Remember: remember && m.Keys != nil}
	if err := m.save(); err != nil {
		m.cfg = prev
		return err
	}
	m.hdr, m.key, m.enc = h, k, New(m.Inner, k)
	return nil
}

// Lock forgets the key (in memory and in the keychain). Encryption stays on,
// so nothing is uploaded unencrypted; syncing waits for the passphrase.
func (m *Manager) Lock() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hdr != nil && m.Keys != nil {
		m.Keys.Delete(m.hdr.ID())
	}
	m.key, m.enc = nil, nil
	m.cfg.Remember = false
	return m.save()
}

// Disable turns encryption off on this computer. Data already uploaded stays
// encrypted in the store; this computer goes back to the plain layout.
func (m *Manager) Disable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hdr != nil && m.Keys != nil {
		m.Keys.Delete(m.hdr.ID())
	}
	m.key, m.enc, m.hdr = nil, nil, nil
	m.cfg = settings{}
	return m.save()
}

// ---- store.Store ----

func (m *Manager) PutPack(ctx context.Context, name string, data []byte) error {
	s, err := m.current(ctx)
	if err != nil {
		return err
	}
	return s.PutPack(ctx, name, data)
}

func (m *Manager) GetPack(ctx context.Context, name string) ([]byte, error) {
	s, err := m.current(ctx)
	if err != nil {
		return nil, err
	}
	return s.GetPack(ctx, name)
}

func (m *Manager) PutManifest(ctx context.Context, worldID, name string, data []byte) error {
	s, err := m.current(ctx)
	if err != nil {
		return err
	}
	return s.PutManifest(ctx, worldID, name, data)
}

func (m *Manager) GetManifest(ctx context.Context, worldID, name string) ([]byte, error) {
	s, err := m.current(ctx)
	if err != nil {
		return nil, err
	}
	return s.GetManifest(ctx, worldID, name)
}

func (m *Manager) ListManifests(ctx context.Context, worldID string) ([]string, error) {
	s, err := m.current(ctx)
	if err != nil {
		return nil, err
	}
	return s.ListManifests(ctx, worldID)
}

func (m *Manager) ListWorlds(ctx context.Context) ([]string, error) {
	s, err := m.current(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.ListWorlds(ctx)
	if err != nil {
		return nil, err
	}
	out := ids[:0:0]
	for _, id := range ids { // hide the encryption layer's own bookkeeping
		if !IsReserved(id) {
			out = append(out, id)
		}
	}
	return out, nil
}
