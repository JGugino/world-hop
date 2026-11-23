// Package store defines the storage backend interface. Backends only need
// write-once blobs and listing: no locking or overwrite semantics.
package store

import (
	"context"
	"errors"
)

var (
	ErrNotFound = errors.New("store: not found")
	// ErrExists is returned by PutManifest when the name is taken.
	ErrExists = errors.New("store: already exists")
)

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
