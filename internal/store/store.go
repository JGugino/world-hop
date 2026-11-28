// Package store defines the storage backend interface. Backends only need
// write-once blobs and listing: no locking or overwrite semantics.
package store

import (
	"context"
	"errors"
)

var (
	ErrNotFound = errors.New("store: not found")
	// ErrNotLinked: no cloud account is linked to store anything in.
	ErrNotLinked = errors.New("link: no cloud account linked")
	// Encryption errors live here, not in package crypt, so callers that only
	// explain them (the sync engine) need not import crypt. crypt re-exports them.
	ErrEncryptionLocked = errors.New("encryption is on but locked: enter your passphrase under Encryption")
	// ErrPassphraseChanged: another computer changed the encryption passphrase, so
	// the key this one holds no longer matches the store.
	ErrPassphraseChanged = errors.New("the encryption passphrase was changed on another computer: enter the new passphrase under Encryption")
	// ErrKeyRollback: the key header in the cloud is older than the one this
	// computer has used, or is gone. Something removed it or rolled the account
	// back; nothing is changed until the person chooses.
	ErrKeyRollback = errors.New("the encryption key in your cloud is older than the one this computer has used, or is missing: enter the Encryption tab to choose what to do")
	// ErrRekeying: this computer is re-encrypting everything under a new passphrase.
	ErrRekeying        = errors.New("the encryption passphrase is being changed: syncing resumes when it finishes")
	ErrWrongPassphrase = errors.New("wrong passphrase")
	ErrTampered        = errors.New("encrypted data failed its integrity check")
	ErrNoKey           = errors.New("this store has no encryption key yet")
	ErrBadHeader       = errors.New("unreadable encryption key header")
	// ErrExists is returned by PutManifest when the name is taken.
	ErrExists = errors.New("store: already exists")
)

// ErrUnsupported: this store can't answer that question. Callers carry on
// without the check.
var ErrUnsupported = errors.New("store: not supported")

// PackChecker is implemented by stores that can say which packs they still
// hold without downloading them. It is how a push notices that data a stored
// version points at has gone missing, instead of building on it.
type PackChecker interface {
	// MissingPacks returns those of names that the store does not hold.
	MissingPacks(ctx context.Context, names []string) ([]string, error)
}

// Remover is implemented by stores that can delete what they hold. It is what
// cloud cleanup uses: the sync engine only ever deletes by name, and only names it
// found in a manifest, so it can never touch data belonging to another layout (the
// encrypted copy beside a plaintext one, say). Deleting something that is already
// gone is not an error, so an interrupted cleanup can simply run again.
type Remover interface {
	// DeletePack removes a pack. Missing is fine.
	DeletePack(ctx context.Context, name string) error
	// DeleteManifest removes one manifest of a world. Missing is fine.
	DeleteManifest(ctx context.Context, worldID, name string) error
}

// Missing returns the names not in have, in the order given.
func Missing(names []string, have map[string]bool) []string {
	var out []string
	for _, n := range names {
		if !have[n] {
			out = append(out, n)
		}
	}
	return out
}

// Store is where a world's versions live: the user's cloud. It is deliberately
// tiny and write-once, so every backend (a folder, Google Drive, S3) can implement it,
// and the sync engine never has to reason about locking, overwriting or deleting:
//
//   - packs hold chunk data and are immutable and named by their content hash, so
//     writing one twice is harmless;
//   - manifests describe one version of a world and are never overwritten (a second
//     write of the same name is ErrExists), which is what makes two computers
//     writing at once safe: they write different names and the engine notices;
//   - nothing is ever overwritten, and the engine never relies on deleting: removing
//     data is the optional Remover interface, used only by cloud cleanup for old
//     versions and their packs (and by the Drive backend clearing a junk file that
//     shares a pack's name but is the wrong size, left by an interrupted upload).
//
// The sync engine does not talk to a backend directly: it goes through crypt.Manager
// (optional encryption) and link.Manager (which backend the user chose).
type Store interface {
	// PutPack stores an immutable, content-addressed blob. Idempotent.
	PutPack(ctx context.Context, name string, data []byte) error
	GetPack(ctx context.Context, name string) ([]byte, error)
	// PutManifest must never overwrite: it returns ErrExists instead.
	PutManifest(ctx context.Context, worldID, name string, data []byte) error
	GetManifest(ctx context.Context, worldID, name string) ([]byte, error)
	// ListManifests returns manifest file names for a world (empty if none).
	ListManifests(ctx context.Context, worldID string) ([]string, error)
	// ListWorlds returns the ids of worlds that have at least one manifest,
	// sorted. Used to offer worlds that exist only in the store.
	ListWorlds(ctx context.Context) ([]string, error)
}
