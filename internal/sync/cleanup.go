// cleanup.go: removing old versions of worlds, and the packs only they used, from the
// cloud. Without it the cloud only ever grows: every push adds a manifest and
// whatever packs it needed, and nothing refers back to the old ones.

package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/JGugino/world-hopper/internal/config"
	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

// What cleanup keeps of each world's history. A version survives if it is a head
// (the current versions, whatever their age, so an unresolved conflict is never
// touched), one of the newest versions (Engine.cloudKeep, a setting, 5 by default),
// or younger than KeepFor. The age rule is
// what lets a computer that was switched off for a while still find the version it
// last synced: by the time that version is old enough to go, it has long been
// superseded.
const (
	KeepFor = 14 * 24 * time.Hour
	// CleanupEvery is how often a computer runs cleanup on its own (the Runner,
	// after a pass that went well).
	CleanupEvery = 24 * time.Hour
)

// CleanupResult is what a cleanup removed.
type CleanupResult struct {
	Worlds    int // worlds that had old versions removed
	Manifests int // old versions removed
	Packs     int // packs removed, because no remaining version uses them
}

// retention splits a world's listed versions into those to keep and those to
// remove, following the rules above. ms maps manifest name to its content.
func retention(ms map[string]*manifest.Manifest, now time.Time, newest int) (keep, prune []string) {
	superseded := map[string]bool{}
	names := make([]string, 0, len(ms))
	for n, m := range ms {
		names = append(names, n)
		for _, p := range m.Parents {
			superseded[p] = true
		}
	}
	sort.Slice(names, func(i, j int) bool { // newest first
		a, b := ms[names[i]], ms[names[j]]
		if a.Generation != b.Generation {
			return a.Generation > b.Generation
		}
		return names[i] > names[j]
	})
	for i, n := range names {
		if !superseded[n] || i < newest || now.Sub(ms[n].CreatedAt) < KeepFor {
			keep = append(keep, n)
		} else {
			prune = append(prune, n)
		}
	}
	return keep, prune
}

// cloudKeep is how many of each world's newest versions cleanup keeps: the
// CloudKeep setting, bounded below so a bad value can never delete the history.
func (e *Engine) cloudKeep() int {
	if e.CloudKeep != nil {
		if n := e.CloudKeep(); n >= config.MinCloudSnapshots {
			return n
		}
	}
	return config.DefaultCloudSnapshots
}

// Cleanup deletes old versions and the packs nothing else uses. It is safe to stop
// at any point and to run again: packs go first, then manifests, so an interrupted
// run leaves old manifests that the next run removes.
//
// Why it is safe with other computers working at the same time and no locks:
//
//   - it deletes only by name, and only names found in a manifest, so it can't
//     reach data from another layout (the plaintext copy beside an encrypted one);
//   - the heads are never removed, and a push only builds on the heads it saw, so
//     the packs a concurrent push reuses belong to versions that stay;
//   - a pack counts as used if any version that stays, in any world, points at it
//     (identical worlds share packs), and the whole run stops at the first
//     manifest it can't read or list it can't get, deleting nothing;
//   - a push checks its packs again just before writing its manifest (see
//     uploadWith), which covers the one remaining gap: a pack that a push found
//     already stored while this run was deciding to delete it.
//
// Worlds the cloud lost versions of are left alone (the person decides about those
// first). Other computers do not mistake the removed versions for lost data (see
// withoutPruned in lost.go).
func (e *Engine) Cleanup(ctx context.Context) (CleanupResult, error) {
	rm, ok := e.Store.(store.Remover)
	if !ok {
		return CleanupResult{}, store.ErrUnsupported
	}
	ids, err := e.Store.ListWorlds(ctx)
	if err != nil {
		return CleanupResult{}, err
	}
	type plan struct {
		id    string
		prune []string
	}
	var plans []plan
	live := map[string]bool{}  // packs a remaining version points at
	maybe := map[string]bool{} // packs only removed versions point at
	now := e.now()
	for _, id := range ids {
		names, err := e.Store.ListManifests(ctx, id)
		if err != nil {
			return CleanupResult{}, err
		}
		loaded, err := e.loadListed(ctx, id, names)
		if err != nil {
			return CleanupResult{}, fmt.Errorf("cleanup stopped, nothing removed: %w", err)
		}
		ms := make(map[string]*manifest.Manifest, len(names))
		for i, n := range names {
			ms[n] = loaded[i]
		}
		keep, prune := retention(ms, now, e.cloudKeep())
		if e.lostVersions(ctx, id, names) != nil {
			keep, prune = names, nil
		}
		for _, n := range keep {
			for _, c := range ms[n].Chunks {
				live[c.Pack] = true
			}
		}
		for _, n := range prune {
			for _, c := range ms[n].Chunks {
				maybe[c.Pack] = true
			}
		}
		if len(prune) > 0 {
			plans = append(plans, plan{id, prune})
		}
	}
	var packs []string
	for p := range maybe {
		if !live[p] {
			packs = append(packs, p)
		}
	}
	sort.Strings(packs)
	var res CleanupResult
	err = parallel(ctx, len(packs), transferWorkers, func(ctx context.Context, i int) error {
		return rm.DeletePack(ctx, packs[i])
	})
	if err != nil {
		return res, err
	}
	res.Packs = len(packs)
	for _, p := range plans {
		for _, n := range p.prune {
			if err := rm.DeleteManifest(ctx, p.id, n); err != nil {
				return res, err
			}
			os.Remove(e.manifestCachePath(p.id, n))
			res.Manifests++
		}
		res.Worlds++
	}
	return res, nil
}

// cleanupStamp is where this computer records when it last ran cleanup, per layout
// (plain or encrypted) like the other per-layout state.
func (e *Engine) cleanupStamp() string {
	return e.scoped("cleanup") + ".stamp"
}

// CleanupDue reports whether CleanupEvery has passed since the last run here.
func (e *Engine) CleanupDue() bool {
	st, err := os.Stat(e.cleanupStamp())
	return err != nil || e.now().Sub(st.ModTime()) >= CleanupEvery
}

// MarkCleaned records that cleanup just ran (also after a failure, so a broken
// provider is not hammered every pass; the next try is in CleanupEvery).
func (e *Engine) MarkCleaned() {
	p := e.cleanupStamp()
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		os.WriteFile(p, nil, 0o644)
		os.Chtimes(p, e.now(), e.now())
	}
}
