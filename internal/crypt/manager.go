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
	// ErrPassphraseChanged: another computer changed the passphrase.
	ErrPassphraseChanged = store.ErrPassphraseChanged
	// ErrKeyRollback: the cloud's key header is older than the one used here.
	ErrKeyRollback = store.ErrKeyRollback
	// ErrNoSavedHeader: this computer has no copy of the key header to restore.
	ErrNoSavedHeader = errors.New("this computer has no saved copy of the key header")
	// ErrRekeying: a passphrase change is running on this computer.
	ErrRekeying = store.ErrRekeying
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
	// KeyID is the key header this computer last unlocked, so that a header
	// written by another computer's passphrase change is noticed, even after a
	// restart. Empty for settings saved by an earlier version.
	KeyID string `json:"keyId,omitempty"`
	// KeyGen and Header are the newest key header this computer has used. The
	// header holds no secret (salt, cost, a check value). They let this computer
	// notice that the cloud now shows an older one (rolled back or deleted) and
	// offer to put it back.
	KeyGen int64   `json:"keyGen,omitempty"`
	Header *Header `json:"header,omitempty"`
}

// headerRecheck is how often an unlocked computer looks for a key header that
// another computer wrote.
const headerRecheck = time.Minute

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

	// Now is the clock (time.Now if nil).
	Now func() time.Time

	path    string
	mu      sync.Mutex
	cfg     settings
	hdr     *Header
	key     *Key
	enc     *Store
	checked time.Time // when the key header was last compared with the store's

	rekey       *Rekey
	rekeyCancel context.CancelFunc
	rekeyDone   chan struct{}

	rollback bool // the cloud's key header is behind this computer's
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
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
	if m.rekey != nil && m.rekey.State == RekeyRunning {
		return nil, ErrRekeying
	}
	if m.rollback {
		return nil, ErrKeyRollback
	}
	if m.enc != nil {
		if m.now().Sub(m.checked) < headerRecheck {
			return m.enc, nil
		}
		// Another computer may have changed the passphrase since, or the cloud
		// may have lost the header. A failed look is not a reason to stop: the
		// real request will say what is wrong.
		h, err := ReadHeader(ctx, m.Inner)
		switch {
		case err == nil && m.hdr != nil && h.Generation() > m.hdr.Generation():
			m.forget(m.hdr.ID())
			return nil, ErrPassphraseChanged
		case m.behind(h, err):
			m.rollback = true
			return nil, ErrKeyRollback
		}
		m.checked = m.now()
		return m.enc, nil
	}
	h, err := ReadHeader(ctx, m.Inner)
	if m.behind(h, err) {
		m.rollback = true
		return nil, ErrKeyRollback
	}
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
		if m.cfg.KeyID != "" && m.cfg.KeyID != h.ID() {
			m.forget(m.cfg.KeyID)
			return nil, ErrPassphraseChanged
		}
		return nil, ErrLocked
	}
	if err := h.Verify(k); err != nil {
		return nil, err
	}
	m.hdr, m.key, m.enc, m.checked = h, k, NewForGeneration(m.Inner, k, h.Generation()), m.now()
	return m.enc, nil
}

// behind reports whether the key header the cloud shows (h, or err when it
// shows none) is older than the newest this computer has used. The caller
// holds m.mu.
func (m *Manager) behind(h *Header, err error) bool {
	if m.cfg.KeyGen < 1 {
		return false // settings from an earlier version: nothing to compare
	}
	if errors.Is(err, ErrNoKey) {
		return true
	}
	return err == nil && h.Generation() < m.cfg.KeyGen
}

// forget drops the key that no longer matches the store, including the copy
// in the keychain. The caller holds m.mu.
func (m *Manager) forget(oldID string) {
	if m.Keys != nil && oldID != "" {
		m.Keys.Delete(oldID)
	}
	m.key, m.enc = nil, nil
	if m.cfg.Remember {
		m.cfg.Remember = false
		m.save()
	}
}

// Info is what the UI shows.
type Info struct {
	Pro      bool `json:"pro"`
	Enabled  bool `json:"enabled"`
	Unlocked bool `json:"unlocked"`
	Remember bool `json:"remember"`
	// KeyInStore: whether the store already has a key (another computer set it
	// up). Nil when it could not be checked (for example no account linked).
	KeyInStore *bool `json:"keyInStore"`
	// KeyChanged: another computer changed the passphrase; enter the new one.
	KeyChanged bool `json:"keyChanged,omitempty"`
	// KeyRollback: the cloud's key header is older than the one this computer
	// has used, or gone. Syncing is stopped until the person chooses.
	KeyRollback bool `json:"keyRollback,omitempty"`
	// Rekey is the passphrase change in progress or just finished, if any.
	Rekey *Rekey `json:"rekey,omitempty"`
	Error string `json:"error,omitempty"`
}

// Info reports the current state. It may touch the store to look for a key.
func (m *Manager) Info(ctx context.Context) Info {
	in := Info{Pro: m.Pro != nil && m.Pro()}
	m.mu.Lock()
	in.Enabled, in.Remember = m.cfg.Enabled, m.cfg.Remember
	in.Unlocked = m.enc != nil
	in.KeyRollback = m.rollback
	if m.rekey != nil {
		r := *m.rekey
		in.Rekey = &r
	}
	m.mu.Unlock()
	if in.Enabled && !in.Unlocked {
		if _, err := m.current(ctx); err == nil {
			in.Unlocked = true // key came from the keychain
		} else if errors.Is(err, ErrPassphraseChanged) {
			in.KeyChanged = true
		} else if errors.Is(err, ErrKeyRollback) {
			in.KeyRollback = true
		} else if !errors.Is(err, ErrLocked) && !errors.Is(err, ErrRekeying) {
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
	if m.isRekeying() {
		return ErrRekeying
	}
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
	if m.isRekeying() {
		return ErrRekeying
	}
	h, err := ReadHeader(ctx, m.Inner)
	m.mu.Lock()
	rolledBack := m.behind(h, err)
	m.mu.Unlock()
	if rolledBack {
		return ErrKeyRollback // never quietly accept an older key
	}
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
	m.cfg = settings{Enabled: true, Remember: remember && m.Keys != nil, KeyID: h.ID(), KeyGen: h.Generation(), Header: h}
	if err := m.save(); err != nil {
		m.cfg = prev
		return err
	}
	m.hdr, m.key, m.enc, m.checked = h, k, NewForGeneration(m.Inner, k, h.Generation()), m.now()
	return nil
}

func (m *Manager) isRekeying() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rekey != nil && m.rekey.State == RekeyRunning
}

// Lock forgets the key (in memory and in the keychain). Encryption stays on,
// so nothing is uploaded unencrypted; syncing waits for the passphrase.
func (m *Manager) Lock() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rekey != nil && m.rekey.State == RekeyRunning {
		return ErrRekeying
	}
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
	if m.rekey != nil && m.rekey.State == RekeyRunning {
		return ErrRekeying
	}
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

// MissingPacks implements store.PackChecker.
func (m *Manager) MissingPacks(ctx context.Context, names []string) ([]string, error) {
	s, err := m.current(ctx)
	if err != nil {
		return nil, err
	}
	chk, ok := s.(store.PackChecker)
	if !ok {
		return nil, store.ErrUnsupported
	}
	return chk.MissingPacks(ctx, names)
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
