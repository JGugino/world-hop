// plaintext.go: removing the unencrypted copy of the worlds from the cloud once
// encryption holds their replacement. Turning encryption on starts a separate
// encrypted layout beside the plaintext one and each world is uploaded encrypted by
// its next sync; this is what then deletes what was left behind in the clear.

package crypt

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

// plainWorld is one world of the plaintext layout with everything it points at.
type plainWorld struct {
	id    string
	ms    map[string]*manifest.Manifest // manifest name -> content
	heads []*manifest.Manifest
}

// readPlain loads every manifest of a plaintext world straight from the inner store.
func (m *Manager) readPlain(ctx context.Context, id string) (*plainWorld, error) {
	names, err := m.Inner.ListManifests(ctx, id)
	if err != nil {
		return nil, err
	}
	w := &plainWorld{id: id, ms: map[string]*manifest.Manifest{}}
	superseded := map[string]bool{}
	for _, n := range names {
		b, err := m.Inner.GetManifest(ctx, id, n)
		if err != nil {
			return nil, err
		}
		mf, err := manifest.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", id, n, err)
		}
		w.ms[n] = mf
		for _, p := range mf.Parents {
			superseded[p] = true
		}
	}
	for n, mf := range w.ms {
		if !superseded[n] {
			w.heads = append(w.heads, mf)
		}
	}
	return w, nil
}

// replaced reports whether the encrypted layout holds this world's newest content:
// one of its versions has the same files as a plaintext head, or the newest
// encrypted version was written after every plaintext head (the person went on
// working after encryption was turned on). Content is never compared by name,
// because both layouts use the same manifest names.
func replaced(ctx context.Context, enc *Store, w *plainWorld) (bool, error) {
	names, err := enc.ListManifests(ctx, w.id)
	if err != nil || len(names) == 0 {
		return false, err
	}
	var newest *manifest.Manifest
	trees := map[string]bool{}
	for _, n := range names {
		b, err := enc.GetManifest(ctx, w.id, n)
		if err != nil {
			return false, err
		}
		mf, err := manifest.Parse(b)
		if err != nil {
			return false, err
		}
		trees[mf.TreeHash()] = true
		if newest == nil || mf.Generation > newest.Generation {
			newest = mf
		}
	}
	newer := true
	for _, h := range w.heads {
		if trees[h.TreeHash()] {
			return true, nil
		}
		if !newest.CreatedAt.After(h.CreatedAt) {
			newer = false
		}
	}
	return newer && len(w.heads) > 0, nil
}

// RemovePlaintext deletes the unencrypted copy (packs, then manifests) of every
// world the encrypted layout already holds in its newest form, and returns how many
// worlds that was. It does nothing while encryption is off or locked.
//
// Worlds the encrypted layout does not have yet stay as they are, so nothing is
// ever deleted that has no encrypted replacement: a world that only exists in the
// cloud is replaced once a computer that has it syncs it. Packs shared with a world
// that stays are kept. Only plaintext names are touched (a world id outside the
// reserved range, a pack named by a manifest), never the encrypted data beside them.
// It is safe to stop and run again: manifests go last, so an interrupted run is
// found again.
//
// Another computer that has not turned encryption on yet keeps uploading plaintext
// until it does; once the plaintext copy is gone it reports the cloud as having lost
// what it had seen, and enabling encryption there is the way forward.
func (m *Manager) RemovePlaintext(ctx context.Context) (int, error) {
	if m.isRekeying() {
		return 0, nil
	}
	s, err := m.current(ctx)
	if err != nil {
		if errors.Is(err, ErrLocked) || errors.Is(err, ErrRekeying) {
			return 0, nil
		}
		return 0, err
	}
	enc, ok := s.(*Store)
	if !ok { // encryption is off
		return 0, nil
	}
	rm, ok := m.Inner.(store.Remover)
	if !ok {
		return 0, store.ErrUnsupported
	}
	ids, err := m.Inner.ListWorlds(ctx)
	if err != nil {
		return 0, err
	}
	var all []*plainWorld
	for _, id := range ids {
		if IsReserved(id) {
			continue
		}
		w, err := m.readPlain(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("unencrypted copy not removed: %w", err)
		}
		all = append(all, w)
	}
	var gone, stay []*plainWorld
	for _, w := range all {
		ok, err := replaced(ctx, enc, w)
		if err != nil {
			return 0, err
		}
		if ok {
			gone = append(gone, w)
		} else {
			stay = append(stay, w)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	live := map[string]bool{}
	for _, w := range stay {
		for _, mf := range w.ms {
			for _, c := range mf.Chunks {
				live[c.Pack] = true
			}
		}
	}
	doomed := map[string]bool{}
	for _, w := range gone {
		for _, mf := range w.ms {
			for _, c := range mf.Chunks {
				if !live[c.Pack] {
					doomed[c.Pack] = true
				}
			}
		}
	}
	packs := make([]string, 0, len(doomed))
	for p := range doomed {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	for _, p := range packs {
		if err := rm.DeletePack(ctx, p); err != nil {
			return 0, err
		}
	}
	for _, w := range gone {
		for n := range w.ms {
			if err := rm.DeleteManifest(ctx, w.id, n); err != nil {
				return 0, err
			}
		}
	}
	return len(gone), nil
}
