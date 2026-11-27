package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// The cloud is not trusted to keep what it was given. Every computer keeps the
// versions of a world it has seen (the manifest cache), so it can tell when the
// cloud no longer lists one of them (deleted, or rolled back to an older state),
// and it checks that the packs a stored version points at still exist before
// building on them. Either way nothing is changed or repaired by itself: the
// world stops syncing and the person chooses what happens (see ResolveLost).
//
// A computer that has never seen a version cannot miss it, so this catches what
// any computer that has synced the world could notice, not a rollback to a state
// the checking computer never saw.

// LostKind says what the cloud lost.
type LostKind string

const (
	// LostVersions: stored versions this computer has seen are no longer listed.
	LostVersions LostKind = "versions"
	// LostWorld: no version of the world is listed any more.
	LostWorld LostKind = "world"
	// LostData: versions are listed but packs they need are gone.
	LostData LostKind = "data"
)

// LostError means the cloud's copy of a world is no longer what this computer
// last saw. Nothing was changed.
type LostError struct {
	WorldID string
	Kind    LostKind
	// Versions are the manifest names seen before and not listed now.
	Versions []string
	// Remaining is how many versions the cloud still lists.
	Remaining int
	// Packs is how many packs were found missing (LostData; 0 when unknown).
	Packs int
}

func (e *LostError) Error() string {
	switch e.Kind {
	case LostWorld:
		return fmt.Sprintf("world %q is no longer in the cloud: all %d version(s) this computer had seen are gone", e.WorldID, len(e.Versions))
	case LostData:
		return fmt.Sprintf("world %q: data its stored versions need is missing from the cloud", e.WorldID)
	}
	return fmt.Sprintf("world %q: %d stored version(s) this computer had seen are no longer in the cloud", e.WorldID, len(e.Versions))
}

// LostAction is what the person chooses to do about it.
type LostAction string

const (
	// LostAcceptCloud: take the cloud as it is now. The missing history is
	// forgotten, and the world is then treated like any other (usually a
	// conflict to resolve, which changes nothing until the person picks).
	LostAcceptCloud LostAction = "accept-cloud"
	// LostReupload: upload this computer's copy as a new version, rebuilding
	// whatever the cloud lost.
	LostReupload LostAction = "reupload"
)

var (
	// ErrBadLostAction: not one of the two actions.
	ErrBadLostAction = errors.New("unknown action")
	// ErrNotLost: nothing is missing from the cloud for this world.
	ErrNotLost = errors.New("nothing is missing from the cloud for this world")
	// ErrCannotAccept: lost data can't be accepted, only rebuilt.
	ErrCannotAccept = errors.New("missing data can't be accepted; upload this computer's copy to rebuild it")
	// ErrNoLocalCopy: there is no local world to upload.
	ErrNoLocalCopy = errors.New("this computer has no copy of the world to upload")
)

// LostResult is what a resolution did.
type LostResult struct {
	Action   LostAction `json:"action"`
	Manifest string     `json:"manifest,omitempty"`
}

// ---- what this computer has seen ----

// seenNames lists the manifests this computer has saved for the world.
func (e *Engine) seenNames(id string) []string {
	ents, err := os.ReadDir(filepath.Join(e.scoped("manifests"), id))
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range ents {
		if !ent.IsDir() && store.ValidManifestName(ent.Name()) {
			out = append(out, ent.Name())
		}
	}
	sort.Strings(out)
	return out
}

// missingFrom returns the seen names that listed does not contain.
func missingFrom(seen, listed []string) []string {
	have := make(map[string]bool, len(listed))
	for _, n := range listed {
		have[n] = true
	}
	var out []string
	for _, n := range seen {
		if !have[n] {
			out = append(out, n)
		}
	}
	return out
}

// lostVersions compares the listing with what has been seen.
func (e *Engine) lostVersions(id string, listed []string) *LostError {
	miss := missingFrom(e.seenNames(id), listed)
	if len(miss) == 0 {
		return nil
	}
	kind := LostVersions
	if len(listed) == 0 {
		kind = LostWorld
	}
	return &LostError{WorldID: id, Kind: kind, Versions: miss, Remaining: len(listed)}
}

// forgetMissing drops the saved copies of manifests the cloud no longer lists,
// and the "last synced" record if it points at one, so the world is judged by
// what the cloud holds now.
func (e *Engine) forgetMissing(id string, missing []string) {
	gone := map[string]bool{}
	for _, n := range missing {
		os.Remove(e.manifestCachePath(id, n))
		gone[n] = true
	}
	if st := e.loadState(id); gone[st.Base] {
		e.saveState(id, state{})
	}
}

// maxGeneration is the highest generation among manifest names.
func maxGeneration(names []string) int64 {
	var g int64
	for _, n := range names {
		if gen, _, ok := manifest.ParseName(n); ok && gen > g {
			g = gen
		}
	}
	return g
}

// ---- data the cloud lost (found when a push or pull needs it) ----

type lostData struct {
	mu stdsync.Mutex
	m  map[string]*LostError
}

func (l *lostData) set(id string, e *LostError) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*LostError{}
	}
	l.m[id] = e
}

func (l *lostData) get(id string) *LostError {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m[id]
}

func (l *lostData) clear(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, id)
}

// missingPacks returns the packs the chunk locations point at that the store no
// longer holds. A store that can't say reports none.
func (e *Engine) missingPacks(ctx context.Context, locs map[string]manifest.ChunkLoc) (map[string]bool, error) {
	chk, ok := e.Store.(store.PackChecker)
	if !ok || len(locs) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var names []string
	for _, l := range locs {
		if !seen[l.Pack] {
			seen[l.Pack] = true
			names = append(names, l.Pack)
		}
	}
	sort.Strings(names)
	miss, err := chk.MissingPacks(ctx, names)
	if errors.Is(err, store.ErrUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(miss) > 0 && packRecheckDelay > 0 {
		// Listings can lag behind uploads (Google Drive's can, by seconds), and a
		// false alarm would stop a healthy world. Look again before saying so.
		select {
		case <-time.After(packRecheckDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if again, err := chk.MissingPacks(ctx, miss); err == nil {
			miss = again
		}
	}
	out := make(map[string]bool, len(miss))
	for _, n := range miss {
		out[n] = true
	}
	return out, nil
}

// packRecheckDelay is how long to wait before confirming that packs are missing.
var packRecheckDelay = 3 * time.Second

// ---- reading and resolving ----

// LostDetails says what is missing from the cloud for a world, or nil.
func (e *Engine) LostDetails(ctx context.Context, id string) (*LostError, error) {
	listed, err := e.Store.ListManifests(ctx, id)
	if err != nil {
		return nil, err
	}
	if le := e.lostVersions(id, listed); le != nil {
		return le, nil
	}
	return e.lost.get(id), nil
}

// ResolveLost carries out what the person chose about data the cloud lost. It
// never runs by itself.
func (e *Engine) ResolveLost(ctx context.Context, w worlds.World, a LostAction) (LostResult, error) {
	if a != LostAcceptCloud && a != LostReupload {
		return LostResult{}, fmt.Errorf("%w %q", ErrBadLostAction, a)
	}
	if err := e.check(w); err != nil {
		return LostResult{}, err
	}
	listed, err := e.Store.ListManifests(ctx, w.ID)
	if err != nil {
		return LostResult{}, err
	}
	seen := e.seenNames(w.ID)
	missing := missingFrom(seen, listed)
	lostData := e.lost.get(w.ID)
	if len(missing) == 0 && lostData == nil {
		return LostResult{}, ErrNotLost
	}

	if a == LostAcceptCloud {
		if len(missing) == 0 {
			return LostResult{}, ErrCannotAccept
		}
		e.forgetMissing(w.ID, missing)
		return LostResult{Action: a}, nil
	}

	if !worlds.IsWorld(w.Path) {
		return LostResult{}, ErrNoLocalCopy
	}
	hs, err := e.headsOpt(ctx, w.ID, true)
	if err != nil {
		return LostResult{}, err
	}
	sc, err := scan(w.Path, e.statCache(w.ID))
	if err != nil {
		return LostResult{}, err
	}
	// Past every generation this computer has seen, so the new version can't
	// collide with one the cloud merely hid.
	opts := uploadOpts{repair: true, noReuse: lostData != nil && lostData.Kind == LostData, minGen: maxGeneration(seen)}
	e.forgetMissing(w.ID, missing)
	pr, err := e.uploadWith(ctx, w, hs, sc, opts)
	if err != nil {
		return LostResult{}, err
	}
	e.lost.clear(w.ID)
	return LostResult{Action: a, Manifest: pr.Manifest}, nil
}
