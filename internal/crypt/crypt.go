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
//
// How the files divide the work:
//
//   - crypt.go: the cryptography and the encrypting Store (Header, Key, Store).
//     Pure: it has no settings, keychain or UI knowledge.
//   - manager.go: the Manager, the store the engine actually uses. It switches
//     encryption on and off at runtime, holds the unlocked key, talks to the OS
//     keychain, and notices when another computer changes the passphrase.
//   - rekey.go: changing the passphrase, which means re-encrypting everything into
//     a new layout and committing by writing the next key header generation.
//   - keyrollback.go: noticing and handling a cloud that shows an older key header
//     than this computer has used (or none).
//
// See docs/encryption.md for the user-facing description and docs/technical.md
// section 5 for the design.
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
	ErrWrongPassphrase = store.ErrWrongPassphrase
	// ErrTampered: data failed authentication. It was modified, corrupted, or
	// was encrypted with a different key.
	ErrTampered = store.ErrTampered
	// ErrNoKey: the store has no key header yet (encryption was never set up).
	ErrNoKey = store.ErrNoKey
	// ErrBadHeader: the key header is malformed or asks for unreasonable cost.
	ErrBadHeader = store.ErrBadHeader
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
	keyDevice      = "key" // the "device" in a key header's manifest name
)

// headerName is the manifest name of key header generation gen. Generation 1
// is the first key; every passphrase change writes the next generation, and
// the newest one is the key in force. The store never overwrites, so older
// headers stay as they were.
func headerName(gen int64) string { return manifest.FileName(gen, keyDevice) }

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

// valid reports whether the cost is within sane bounds. A key header from the
// cloud is untrusted input: without this, a tampered header could ask for gigabytes
// of memory or hours of hashing.
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

	gen int64 // generation, from the manifest name; 1 when new
}

// Generation is how many keys this store has had: 1 for the first, 2 after the
// first passphrase change, and so on.
func (h *Header) Generation() int64 {
	if h.gen < 1 {
		return 1
	}
	return h.gen
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

// sub derives a purpose-specific 32-byte key from the master key with HKDF
// ("data", "names", "check"). Separate keys mean a weakness in how one is used can't
// affect the others.
func (k *Key) sub(info string) []byte {
	b, err := hkdf.Key(sha256.New, k.master[:], nil, "worldhop "+info+" v1", 32)
	if err != nil {
		panic(err) // only for an absurd length
	}
	return b
}

// check returns the key-check value stored in the header. Only the right
// passphrase reproduces it, which is how a wrong passphrase is reported at once
// instead of failing in the middle of a sync. It is an HMAC of a constant under a
// derived key, so it reveals nothing about the passphrase.
func (k *Key) check() string {
	m := hmac.New(sha256.New, k.sub("check"))
	m.Write([]byte("key check"))
	return hex.EncodeToString(m.Sum(nil))
}

// derive stretches a passphrase and salt into a master key with Argon2id at the
// given cost.
func derive(pass string, salt []byte, p Params) *Key {
	k := &Key{}
	copy(k.master[:], argon2.IDKey([]byte(pass), salt, p.Time, p.MemKiB, p.Threads, 32))
	return k
}

// NewHeader creates a fresh header and the key for pass.
func NewHeader(pass string, p Params) (*Header, *Key, error) {
	return newHeader(pass, p, 1)
}

// NewHeaderAfter creates the header that replaces prev: a new salt, so a new
// key, one generation on.
func NewHeaderAfter(prev *Header, pass string, p Params) (*Header, *Key, error) {
	return newHeader(pass, p, prev.Generation()+1)
}

// newHeader makes a header (fresh random salt) and its key for pass, at key header
// generation gen. Used by NewHeader (generation 1) and NewHeaderAfter (a change).
func newHeader(pass string, p Params, gen int64) (*Header, *Key, error) {
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
	return &Header{Version: 1, KDF: "argon2id", Salt: base64.StdEncoding.EncodeToString(salt), Params: p, Check: k.check(), gen: gen}, k, nil
}

// validate checks the header is a version and KDF this code understands, the cost
// is sane, and the salt decodes to a plausible length. It returns the salt.
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

// verify reports ErrWrongPassphrase unless k reproduces the header's key check
// (compared in constant time).
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

// ReadHeader loads the newest key header, or ErrNoKey.
func ReadHeader(ctx context.Context, st store.Store) (*Header, error) {
	names, err := st.ListManifests(ctx, keyWorld)
	if err != nil {
		return nil, err
	}
	var best string
	var bestGen int64
	for _, n := range names {
		if gen, dev, ok := manifest.ParseName(n); ok && dev == keyDevice && gen > bestGen {
			best, bestGen = n, gen
		}
	}
	if best == "" {
		return nil, ErrNoKey
	}
	b, err := st.GetManifest(ctx, keyWorld, best)
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
	h.gen = bestGen
	return &h, nil
}

// WriteHeader stores the header under its generation. It never overwrites: if
// another computer got there first, it returns store.ErrExists and the caller
// should read theirs.
func WriteHeader(ctx context.Context, st store.Store, h *Header) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return st.PutManifest(ctx, keyWorld, headerName(h.Generation()), b)
}

// ---- the encrypting store ----

// Store encrypts everything going into Inner. Use New.
type Store struct {
	Inner store.Store
	data  []byte // data key
	names []byte // naming key
	index string // world id of this key's index of worlds
}

var _ store.Store = (*Store)(nil)

// New wraps inner with key k. The key must already be verified against the
// store's header.
func New(inner store.Store, k *Key) *Store { return NewForGeneration(inner, k, 1) }

// NewForGeneration is New for the key of key header generation gen. Each
// generation keeps its own index of worlds: entries sealed with one key can't
// be read with another, so a shared index would stop either from listing.
// Everything else is told apart by names derived from the key itself.
func NewForGeneration(inner store.Store, k *Key, gen int64) *Store {
	index := indexWorld
	if gen > 1 {
		index = fmt.Sprintf("%s-g%d", indexWorld, gen)
	}
	return &Store{Inner: inner, data: k.sub("data"), names: k.sub("names"), index: index}
}

// hmacHex is HMAC-SHA256 under the naming key over the parts, as hex. It is how
// names the provider sees (packs, worlds, index entries) are derived from the real
// ones without revealing them, while staying the same every time so deduplication
// still works.
func (s *Store) hmacHex(parts ...string) string {
	m := hmac.New(sha256.New, s.names)
	m.Write([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(m.Sum(nil))
}

// packName is the name a pack is stored under: an HMAC of its plaintext hash.
func (s *Store) packName(name string) string { return s.hmacHex("pack", name) }

// worldID is the id a world is stored under: a fixed prefix plus an HMAC of the
// real id.
func (s *Store) worldID(id string) string { return worldPrefix + s.hmacHex("world", id) }

// indexName is the manifest name of a world's entry in the index of worlds.
func (s *Store) indexName(id string) string {
	return "manifest-000000000001-" + s.hmacHex("index", id)[:32] + ".json"
}

// seal encrypts and authenticates plain with XChaCha20-Poly1305 and a random
// nonce, prefixed with the format marker. The aad parts (for example the pack
// name, or world id plus manifest name) are authenticated but not stored, so a
// sealed blob only opens under the exact name it was sealed for. That is what stops
// a provider swapping blobs between names or moving a manifest to another world.
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

// open is the reverse of seal. Any failure (wrong key, modified data, wrong name,
// missing marker) is reported as ErrTampered.
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

// PutPack seals the pack under its plaintext name and stores it under the keyed
// name. Idempotent, like the Store contract requires.
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

// GetPack fetches and opens a pack; ErrTampered if it was modified.
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

// MissingPacks implements store.PackChecker: names are the plaintext pack names
// the engine knows; the inner store holds them under keyed names.
func (s *Store) MissingPacks(ctx context.Context, names []string) ([]string, error) {
	chk, ok := s.Inner.(store.PackChecker)
	if !ok {
		return nil, store.ErrUnsupported
	}
	inner := make([]string, len(names))
	byInner := make(map[string]string, len(names))
	for i, n := range names {
		if !store.ValidPackName(n) {
			return nil, fmt.Errorf("crypt: bad pack name")
		}
		inner[i] = s.packName(n)
		byInner[inner[i]] = n
	}
	miss, err := chk.MissingPacks(ctx, inner)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(miss))
	for _, m := range miss {
		out = append(out, byInner[m])
	}
	return out, nil
}

var _ store.Remover = (*Store)(nil)

// DeletePack removes a pack by its plaintext name; the inner store holds it under
// the keyed name. Only this key's own pack can be named this way, so deleting never
// reaches another layout's data.
func (s *Store) DeletePack(ctx context.Context, name string) error {
	rm, ok := s.Inner.(store.Remover)
	if !ok {
		return store.ErrUnsupported
	}
	if !store.ValidPackName(name) {
		return fmt.Errorf("crypt: bad pack name")
	}
	return rm.DeletePack(ctx, s.packName(name))
}

// DeleteManifest removes a manifest of a world. The index entry that lets
// ListWorlds find the world is left alone: cleanup never removes a world's last
// manifest.
func (s *Store) DeleteManifest(ctx context.Context, worldID, name string) error {
	rm, ok := s.Inner.(store.Remover)
	if !ok {
		return store.ErrUnsupported
	}
	if !manifest.ValidID(worldID) || !store.ValidManifestName(name) {
		return fmt.Errorf("crypt: bad world id or manifest name")
	}
	return rm.DeleteManifest(ctx, s.worldID(worldID), name)
}

// indexEntry is the plaintext of one entry of the index of worlds: the real world
// id, sealed before it is stored, so the provider sees only hashed ids.
type indexEntry struct {
	WorldID string `json:"worldId"`
}

// PutManifest stores a manifest sealed under its world id and name. It also
// records the world in this key generation's index first (so ListWorlds can recover
// the real id on another computer); an entry that already exists is fine, so that
// step is idempotent.
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
	if err := s.Inner.PutManifest(ctx, s.index, s.indexName(worldID), blob); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	blob, err = s.seal(data, "manifest", worldID, name)
	if err != nil {
		return err
	}
	return s.Inner.PutManifest(ctx, s.worldID(worldID), name, blob)
}

// GetManifest fetches and opens a manifest; ErrTampered if it was modified or
// moved from another world or name.
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

// ListManifests lists the manifest names of a world. The names are not encrypted
// (they hold a generation number and a random device id), so this needs no
// decryption.
func (s *Store) ListManifests(ctx context.Context, worldID string) ([]string, error) {
	if !manifest.ValidID(worldID) {
		return nil, fmt.Errorf("crypt: bad world id")
	}
	return s.Inner.ListManifests(ctx, s.worldID(worldID))
}

// ListWorlds reads this key generation's index, opens each entry and checks it
// (valid id, and the entry's name matches the id), returning the real world ids
// that have at least one manifest. A bad entry is ErrTampered.
func (s *Store) ListWorlds(ctx context.Context) ([]string, error) {
	names, err := s.Inner.ListManifests(ctx, s.index)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		blob, err := s.Inner.GetManifest(ctx, s.index, n)
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
