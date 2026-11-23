// Package crypt adds client-side encryption on top of any store.Store, so the
// cloud provider only ever sees ciphertext. It knows nothing about providers:
// it wraps the Store interface.
//
// What is hidden: pack and manifest contents (file paths, sizes, chunk layout),
// pack names and world ids. What stays visible to the provider: roughly how
// much data there is, when it changed, and manifest file names, which carry a
// generation number and a random device id.
//
// Key hierarchy: Argon2id(passphrase, salt) gives a master key; HKDF splits it
// into a data key (XChaCha20-Poly1305), a naming key (HMAC-SHA256, so identical
// data still lands under the same name without exposing its plaintext hash) and
// a key-check key. The salt and KDF cost live in a small key header stored next
// to the data, so a second computer only needs the passphrase.
package crypt

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

var (
	// ErrWrongPassphrase: the passphrase does not match this store's key header.
	ErrWrongPassphrase = errors.New("wrong passphrase")
	// ErrTampered: data failed authentication. It was modified, corrupted, or
	// was encrypted with a different key.
	ErrTampered = errors.New("encrypted data failed its integrity check")
	// ErrNoKey: the store has no key header yet (encryption was never set up).
	ErrNoKey = errors.New("this store has no encryption key yet")
	// ErrBadHeader: the key header is malformed or asks for unreasonable cost.
	ErrBadHeader = errors.New("unreadable encryption key header")
	// ErrWeakPassphrase: too short.
	ErrWeakPassphrase = errors.New("choose a longer passphrase (at least 10 characters)")
)

// MinPassphrase is the shortest passphrase accepted for a new key.
const MinPassphrase = 10

const (
	magic = "WHE1" // prefix of every encrypted blob; also the format version

	// Reserved world ids. Real world ids are stored as namePrefix+hex, so none
	// of these can collide with them. Plain (unencrypted) listings hide them.
	ReservedPrefix = "_worldhop-"
	keyWorld       = ReservedPrefix + "key"
	indexWorld     = ReservedPrefix + "index"
	worldPrefix    = ReservedPrefix + "w-"
	keyManifest    = "manifest-000000000001-key.json"
)

// IsReserved reports whether a world id belongs to the encryption layer.
func IsReserved(id string) bool { return strings.HasPrefix(id, ReservedPrefix) }

// Params is the Argon2id cost.
type Params struct {
	Time    uint32 `json:"time"`
	MemKiB  uint32 `json:"memKiB"`
	Threads uint8  `json:"threads"`
}

// DefaultParams: about 64 MiB and a fraction of a second on a laptop.
var DefaultParams = Params{Time: 3, MemKiB: 64 << 10, Threads: 4}

func (p Params) valid() bool {
	return p.Time >= 1 && p.Time <= 10 && p.MemKiB >= 8 && p.MemKiB <= 1<<20 && p.Threads >= 1 && p.Threads <= 16
}

// Header is the key header stored next to the data. It holds no secrets: the
// salt, the cost, and a value that only the right passphrase reproduces.
type Header struct {
	Version int    `json:"v"`
	KDF     string `json:"kdf"`
	Salt    string `json:"salt"`
	Params
	Check string `json:"check"`
}

// Key is the unlocked master key.
type Key struct{ master [32]byte }

// KeyFromBytes rebuilds a Key (for example one kept in the OS keychain).
func KeyFromBytes(b []byte) (*Key, error) {
	if len(b) != 32 {
		return nil, errors.New("crypt: bad key length")
	}
	k := &Key{}
	copy(k.master[:], b)
	return k, nil
}

// Bytes returns the raw master key. Treat it like the passphrase itself.
func (k *Key) Bytes() []byte { return append([]byte(nil), k.master[:]...) }

func (k *Key) sub(info string) []byte {
	b, err := hkdf.Key(sha256.New, k.master[:], nil, "worldhop "+info+" v1", 32)
	if err != nil {
		panic(err) // only for an absurd length
	}
	return b
}

func (k *Key) check() string {
	m := hmac.New(sha256.New, k.sub("check"))
	m.Write([]byte("key check"))
	return hex.EncodeToString(m.Sum(nil))
}

func derive(pass string, salt []byte, p Params) *Key {
	k := &Key{}
	copy(k.master[:], argon2.IDKey([]byte(pass), salt, p.Time, p.MemKiB, p.Threads, 32))
	return k
}

// NewHeader creates a fresh header and the key for pass.
func NewHeader(pass string, p Params) (*Header, *Key, error) {
	if len([]rune(pass)) < MinPassphrase {
		return nil, nil, ErrWeakPassphrase
	}
	if !p.valid() {
		return nil, nil, ErrBadHeader
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	k := derive(pass, salt, p)
	return &Header{Version: 1, KDF: "argon2id", Salt: base64.StdEncoding.EncodeToString(salt), Params: p, Check: k.check()}, k, nil
}

func (h *Header) validate() ([]byte, error) {
	if h.Version != 1 || h.KDF != "argon2id" || !h.Params.valid() {
		return nil, ErrBadHeader
	}
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return nil, ErrBadHeader
	}
	return salt, nil
}

// Unlock derives the key from pass and checks it against the header.
func (h *Header) Unlock(pass string) (*Key, error) {
	salt, err := h.validate()
	if err != nil {
		return nil, err
	}
	k := derive(pass, salt, h.Params)
	return k, h.verify(k)
}

// Verify checks a key obtained elsewhere (such as the keychain).
func (h *Header) Verify(k *Key) error {
	if _, err := h.validate(); err != nil {
		return err
	}
	return h.verify(k)
}

func (h *Header) verify(k *Key) error {
	if !hmac.Equal([]byte(k.check()), []byte(h.Check)) {
		return ErrWrongPassphrase
	}
	return nil
}

// ID is a short, non-secret label for the key header (for keychain entries and
// local state), so a different store never reuses the wrong key.
func (h *Header) ID() string {
	s := sha256.Sum256([]byte(h.Salt))
	return hex.EncodeToString(s[:6])
}

// ---- header in the store ----

// ReadHeader loads the key header, or ErrNoKey.
func ReadHeader(ctx context.Context, st store.Store) (*Header, error) {
	b, err := st.GetManifest(ctx, keyWorld, keyManifest)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, err
	}
	var h Header
	if json.Unmarshal(b, &h) != nil {
		return nil, ErrBadHeader
	}
	if _, err := h.validate(); err != nil {
		return nil, err
	}
	return &h, nil
}

// WriteHeader stores the header. It never overwrites: if another computer got
// there first, it returns store.ErrExists and the caller should read theirs.
func WriteHeader(ctx context.Context, st store.Store, h *Header) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return st.PutManifest(ctx, keyWorld, keyManifest, b)
}

// ---- the encrypting store ----

// Store encrypts everything going into Inner. Use New.
type Store struct {
	Inner store.Store
	data  []byte // data key
	names []byte // naming key
}

var _ store.Store = (*Store)(nil)

// New wraps inner with key k. The key must already be verified against the
// store's header.
func New(inner store.Store, k *Key) *Store {
	return &Store{Inner: inner, data: k.sub("data"), names: k.sub("names")}
}

func (s *Store) hmacHex(parts ...string) string {
	m := hmac.New(sha256.New, s.names)
	m.Write([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Store) packName(name string) string { return s.hmacHex("pack", name) }
func (s *Store) worldID(id string) string    { return worldPrefix + s.hmacHex("world", id) }
func (s *Store) indexName(id string) string {
	return "manifest-000000000001-" + s.hmacHex("index", id)[:32] + ".json"
}

func (s *Store) seal(plain []byte, aad ...string) ([]byte, error) {
	a, err := chacha20poly1305.NewX(s.data)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(magic)+a.NonceSize()+len(plain)+a.Overhead())
	out = append(out, magic...)
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return a.Seal(out, nonce, plain, []byte(strings.Join(aad, "\x00"))), nil
}

func (s *Store) open(blob []byte, aad ...string) ([]byte, error) {
	a, err := chacha20poly1305.NewX(s.data)
	if err != nil {
		return nil, err
	}
	n := len(magic) + a.NonceSize()
	if len(blob) < n+a.Overhead() || !bytes.HasPrefix(blob, []byte(magic)) {
		return nil, ErrTampered
	}
	plain, err := a.Open(nil, blob[len(magic):n], blob[n:], []byte(strings.Join(aad, "\x00")))
	if err != nil {
		return nil, ErrTampered
	}
	return plain, nil
}

func (s *Store) PutPack(ctx context.Context, name string, data []byte) error {
	if !store.ValidPackName(name) {
		return fmt.Errorf("crypt: bad pack name")
	}
	blob, err := s.seal(data, "pack", name)
	if err != nil {
		return err
	}
	return s.Inner.PutPack(ctx, s.packName(name), blob)
}

func (s *Store) GetPack(ctx context.Context, name string) ([]byte, error) {
	if !store.ValidPackName(name) {
		return nil, fmt.Errorf("crypt: bad pack name")
	}
	blob, err := s.Inner.GetPack(ctx, s.packName(name))
	if err != nil {
		return nil, err
	}
	return s.open(blob, "pack", name)
}

type indexEntry struct {
	WorldID string `json:"worldId"`
}

func (s *Store) PutManifest(ctx context.Context, worldID, name string, data []byte) error {
	if !manifest.ValidID(worldID) || !store.ValidManifestName(name) {
		return fmt.Errorf("crypt: bad world id or manifest name")
	}
	// Record the world id (encrypted) so ListWorlds can recover it. Idempotent.
	idx, _ := json.Marshal(indexEntry{worldID})
	blob, err := s.seal(idx, "index", s.indexName(worldID))
	if err != nil {
		return err
	}
	if err := s.Inner.PutManifest(ctx, indexWorld, s.indexName(worldID), blob); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	blob, err = s.seal(data, "manifest", worldID, name)
	if err != nil {
		return err
	}
	return s.Inner.PutManifest(ctx, s.worldID(worldID), name, blob)
}

func (s *Store) GetManifest(ctx context.Context, worldID, name string) ([]byte, error) {
	if !manifest.ValidID(worldID) || !store.ValidManifestName(name) {
		return nil, fmt.Errorf("crypt: bad world id or manifest name")
	}
	blob, err := s.Inner.GetManifest(ctx, s.worldID(worldID), name)
	if err != nil {
		return nil, err
	}
	return s.open(blob, "manifest", worldID, name)
}

func (s *Store) ListManifests(ctx context.Context, worldID string) ([]string, error) {
	if !manifest.ValidID(worldID) {
		return nil, fmt.Errorf("crypt: bad world id")
	}
	return s.Inner.ListManifests(ctx, s.worldID(worldID))
}

func (s *Store) ListWorlds(ctx context.Context) ([]string, error) {
	names, err := s.Inner.ListManifests(ctx, indexWorld)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		blob, err := s.Inner.GetManifest(ctx, indexWorld, n)
		if err != nil {
			return nil, err
		}
		plain, err := s.open(blob, "index", n)
		if err != nil {
			return nil, err
		}
		var e indexEntry
		if json.Unmarshal(plain, &e) != nil || !manifest.ValidID(e.WorldID) || s.indexName(e.WorldID) != n {
			return nil, ErrTampered
		}
		// An index entry is written just before the first manifest; only
		// report worlds that actually have one.
		ms, err := s.Inner.ListManifests(ctx, s.worldID(e.WorldID))
		if err != nil {
			return nil, err
		}
		if len(ms) > 0 {
			out = append(out, e.WorldID)
		}
	}
	sort.Strings(out)
	return out, nil
}
