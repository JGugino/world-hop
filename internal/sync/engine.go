// Package sync implements push, pull, conflict handling, snapshots and the
// atomic swap on top of any store.Store.
package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JGugino/world-hopper/internal/chunker"
	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/worlds"
)

const packTarget = 16 << 20 // flush a pack at about 16 MiB

var (
	// ErrWorldOpen: the world is open in Minecraft; nothing was changed.
	ErrWorldOpen = errors.New("world is open on this computer")
	// ErrBehind: the store has a newer version and local has no changes.
	ErrBehind = errors.New("a newer version exists in the store; pull first")
	// ErrIntegrity: downloaded data did not match its hash.
	ErrIntegrity = errors.New("integrity check failed")
)

// ConflictError means local and stored versions diverged. Nothing was changed.
type ConflictError struct {
	WorldID string
	Heads   []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("conflict on world %q: local changes and %d stored version(s) diverged", e.WorldID, len(e.Heads))
}

type Engine struct {
	Store         store.Store
	StateDir      string // per-device state and snapshots
	DeviceID      string // filled by New
	SnapshotsKeep int    // snapshots kept per world (default 5)
	// IsOpen reports whether the world is in use by the game. Optional.
	IsOpen func(worldPath string) bool
	// Authorize, if set, decides whether a world may be synced (the license
	// gate). It is checked before any push, pull or conflict resolution; a
	// non-nil error refuses the operation. Reading status is always allowed.
	Authorize func(worldID string) error
	// StateScope, if set, names a sub-namespace for the "last synced" records.
	// Encryption uses it so the plain and encrypted layouts of one store keep
	// separate records and never mistake one for the other being newer.
	StateScope func() string
	Now        func() time.Time
	// OnPhase, if set, is told when a sync starts moving data for a world:
	// phase is "uploading" or "downloading". It may be called from any
	// goroutine and must not block.
	OnPhase func(worldID, phase string)

	lost lostData // worlds whose stored versions need packs the cloud lost
}

func (e *Engine) phase(worldID, phase string) {
	if e.OnPhase != nil {
		e.OnPhase(worldID, phase)
	}
}

// New creates an engine, loading or creating this device's id.
func New(st store.Store, stateDir string) (*Engine, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	idPath := filepath.Join(stateDir, "device-id")
	b, err := os.ReadFile(idPath)
	id := strings.TrimSpace(string(b))
	if err != nil || !manifest.ValidDeviceID(id) {
		raw := make([]byte, 6)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		id = hex.EncodeToString(raw)
		if err := os.WriteFile(idPath, []byte(id+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	return &Engine{Store: st, StateDir: stateDir, DeviceID: id, SnapshotsKeep: 5, Now: time.Now}, nil
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// ---- local state ----

type state struct {
	Base     string `json:"base"`     // manifest name last synced with
	BaseTree string `json:"baseTree"` // its tree hash
}

func (e *Engine) statePath(id string) string {
	dir := filepath.Join(e.StateDir, "worlds")
	if e.StateScope != nil {
		if sc := e.StateScope(); sc != "" {
			dir = filepath.Join(dir, sc)
		}
	}
	return filepath.Join(dir, id+".json")
}

func (e *Engine) loadState(id string) state {
	var s state
	if b, err := os.ReadFile(e.statePath(id)); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (e *Engine) saveState(id string, s state) error {
	p := e.statePath(id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ---- store helpers ----

type head struct {
	Name string
	M    *manifest.Manifest
}

// heads returns manifests that no other manifest lists as a parent.
//
// Manifests are immutable and named by generation and device, so each one is
// kept on this computer after its first download: a pass normally costs one
// listing request, and only manifests written since the last pass are fetched
// (concurrently).
func (e *Engine) heads(ctx context.Context, id string) ([]head, error) {
	return e.headsOpt(ctx, id, false)
}

// headsOpt is heads; ignoreLost carries on when the cloud no longer lists
// versions this computer has seen (only the person's own choice does that).
func (e *Engine) headsOpt(ctx context.Context, id string, ignoreLost bool) ([]head, error) {
	names, err := e.Store.ListManifests(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ignoreLost {
		if le := e.lostVersions(id, names); le != nil {
			// A listing can lag behind an upload (Google Drive's can, by seconds),
			// and this computer's own newest version is in its cache at once. Look
			// again before calling something lost.
			if packRecheckDelay > 0 {
				select {
				case <-time.After(packRecheckDelay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			again, err := e.Store.ListManifests(ctx, id)
			if err != nil {
				return nil, err
			}
			if le = e.lostVersions(id, again); le != nil {
				return nil, le
			}
			names = again
		}
	}
	loaded := make([]*manifest.Manifest, len(names))
	var missing []int
	for i, n := range names {
		if m := e.cachedManifest(id, n); m != nil {
			loaded[i] = m
		} else {
			missing = append(missing, i)
		}
	}
	err = parallel(ctx, len(missing), transferWorkers, func(ctx context.Context, k int) error {
		i := missing[k]
		m, err := e.loadManifest(ctx, id, names[i])
		if err != nil {
			return err
		}
		loaded[i] = m
		e.cacheManifest(id, names[i], m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	all := map[string]*manifest.Manifest{}
	superseded := map[string]bool{}
	for i, n := range names {
		all[n] = loaded[i]
		for _, p := range loaded[i].Parents {
			superseded[p] = true
		}
	}
	var hs []head
	for n, m := range all {
		if !superseded[n] {
			hs = append(hs, head{n, m})
		}
	}
	// newest first
	sort.Slice(hs, func(i, j int) bool {
		a, b := hs[i].M, hs[j].M
		if a.Generation != b.Generation {
			return a.Generation > b.Generation
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return hs[i].Name > hs[j].Name
	})
	return hs, nil
}

func (e *Engine) scoped(dir string) string {
	if e.StateScope != nil {
		if sc := e.StateScope(); sc != "" {
			return filepath.Join(e.StateDir, dir, sc)
		}
	}
	return filepath.Join(e.StateDir, dir)
}

func (e *Engine) manifestCachePath(id, name string) string {
	return filepath.Join(e.scoped("manifests"), id, name)
}

// cachedManifest returns a manifest saved by an earlier pass, or nil.
func (e *Engine) cachedManifest(id, name string) *manifest.Manifest {
	b, err := os.ReadFile(e.manifestCachePath(id, name))
	if err != nil {
		return nil
	}
	m, err := manifest.Parse(b)
	if err != nil || m.WorldID != id || m.Name() != name {
		return nil
	}
	return m
}

func (e *Engine) cacheManifest(id, name string, m *manifest.Manifest) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	p := e.manifestCachePath(id, name)
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil && os.Rename(tmp, p) != nil {
		os.Remove(tmp)
	}
}

func (e *Engine) statCache(id string) *statCache {
	return loadStatCache(filepath.Join(e.scoped("cache"), id+".json"))
}

func (e *Engine) loadManifest(ctx context.Context, id, name string) (*manifest.Manifest, error) {
	b, err := e.Store.GetManifest(ctx, id, name)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if m.WorldID != id || m.Name() != name {
		return nil, fmt.Errorf("%s: %w: manifest does not match its name", name, ErrIntegrity)
	}
	return m, nil
}

func headNames(hs []head) []string {
	var n []string
	for _, h := range hs {
		n = append(n, h.Name)
	}
	sort.Strings(n)
	return n
}

// check runs the checks every write operation shares: the license gate, then
// whether the game has the world open.
func (e *Engine) check(w worlds.World) error {
	if e.Authorize != nil {
		if err := e.Authorize(w.ID); err != nil {
			return err
		}
	}
	return e.checkOpen(w)
}

func (e *Engine) checkOpen(w worlds.World) error {
	if e.IsOpen != nil && e.IsOpen(w.Path) {
		return ErrWorldOpen
	}
	return nil
}

// ---- status ----

type Status string

const (
	StatusNoRemote    Status = "no-remote"    // nothing stored yet
	StatusNoLocal     Status = "no-local"     // stored, but no local world
	StatusSynced      Status = "synced"       // identical
	StatusLocalAhead  Status = "local-ahead"  // local changes to push
	StatusRemoteAhead Status = "remote-ahead" // newer stored version to pull
	StatusConflict    Status = "conflict"
	// StatusCloudLost: the cloud no longer holds what this computer last saw (see
	// LostError). The world is not synced until the person chooses what to do.
	StatusCloudLost Status = "cloud-lost"
)

// view is one look at a world: the stored heads and the local files. Every
// operation takes exactly one, so a pass reads the store's manifests once and
// the world's files once.
type view struct {
	hs     []head
	exists bool
	scan   *scanResult // nil when there is no local world
	tree   string
}

// look gathers the heads and scans the local world at the same time (one is
// network time, the other disk time).
func (e *Engine) look(ctx context.Context, w worlds.World) (*view, error) {
	v := &view{exists: worlds.IsWorld(w.Path)}
	var scanErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		if v.exists {
			v.scan, scanErr = scan(w.Path, e.statCache(w.ID))
		}
	}()
	hs, err := e.heads(ctx, w.ID)
	<-done
	if err != nil {
		return nil, err
	}
	if scanErr != nil {
		return nil, scanErr
	}
	v.hs = hs
	if v.scan != nil {
		v.tree = manifest.TreeHash(v.scan.Files)
	}
	return v, nil
}

func (e *Engine) status(w worlds.World, v *view) Status {
	switch {
	case len(v.hs) == 0 && !v.exists:
		return StatusNoRemote
	case len(v.hs) == 0:
		return StatusLocalAhead
	case !v.exists:
		return StatusNoLocal
	}
	return e.classify(w.ID, v.hs, v.tree)
}

// Status compares the local world with the store without changing anything.
func (e *Engine) Status(ctx context.Context, w worlds.World) (Status, error) {
	v, err := e.look(ctx, w)
	var le *LostError
	if errors.As(err, &le) {
		return StatusCloudLost, nil
	}
	if err != nil {
		return "", err
	}
	if e.lost.get(w.ID) != nil {
		return StatusCloudLost, nil
	}
	return e.status(w, v), nil
}

func (e *Engine) classify(id string, hs []head, local string) Status {
	if len(hs) == 0 {
		return StatusLocalAhead
	}
	st := e.loadState(id)
	if len(hs) == 1 && hs[0].M.TreeHash() == local {
		return StatusSynced
	}
	localChanged := st.Base == "" || st.BaseTree != local
	remoteMoved := !(len(hs) == 1 && hs[0].Name == st.Base)
	switch {
	case !localChanged && remoteMoved && len(hs) == 1:
		return StatusRemoteAhead
	case localChanged && !remoteMoved:
		return StatusLocalAhead
	default:
		return StatusConflict
	}
}

// ---- one sync step ----

// SyncResult is what one Sync did.
type SyncResult struct {
	Status Status // the state found before acting
	Pushed bool
	Pulled bool
}

// Sync looks at the world once and does what its status calls for: push local
// changes, pull a newer stored version, or nothing. It is what the tray and
// scheduler run. It behaves like Status followed by Push or Pull, but reads
// the store's manifests and the world's files once instead of up to three times.
// Conflicts are reported in the result, not as an error.
func (e *Engine) Sync(ctx context.Context, w worlds.World) (SyncResult, error) {
	v, err := e.look(ctx, w)
	if err != nil {
		var le *LostError
		if errors.As(err, &le) {
			return SyncResult{Status: StatusCloudLost}, err
		}
		return SyncResult{}, err
	}
	res := SyncResult{Status: e.status(w, v)}
	switch res.Status {
	case StatusLocalAhead, StatusNoRemote:
		pr, err := e.pushWith(ctx, w, v)
		res.Pushed = pr.Pushed
		return res, err
	case StatusRemoteAhead:
		if err := e.check(w); err != nil {
			return res, err
		}
		e.keepLeftover(w)
		if err := Recover(w.Path); err != nil {
			return res, err
		}
		_, err := e.apply(ctx, w, v.hs[0], "pull")
		res.Pulled = err == nil
		return res, err
	}
	return res, nil
}

// ---- push ----

type PushResult struct {
	Manifest string
	Pushed   bool // false when there was nothing to upload
	NewBytes int64
}

// Push uploads the local world as a new manifest. It refuses on conflict.
func (e *Engine) Push(ctx context.Context, w worlds.World) (PushResult, error) {
	if err := e.check(w); err != nil {
		return PushResult{}, err
	}
	if !worlds.IsWorld(w.Path) {
		return PushResult{}, fmt.Errorf("%s is not a world folder", w.Path)
	}
	v, err := e.look(ctx, w)
	if err != nil {
		return PushResult{}, err
	}
	return e.pushWith(ctx, w, v)
}

// pushWith is Push given a view already taken.
func (e *Engine) pushWith(ctx context.Context, w worlds.World, v *view) (PushResult, error) {
	if err := e.check(w); err != nil {
		return PushResult{}, err
	}
	if !v.exists {
		return PushResult{}, fmt.Errorf("%s is not a world folder", w.Path)
	}
	hs := v.hs
	if len(hs) == 1 && hs[0].M.TreeHash() == v.tree { // already stored: just adopt
		return PushResult{Manifest: hs[0].Name}, e.saveState(w.ID, state{hs[0].Name, v.tree})
	}
	switch e.classify(w.ID, hs, v.tree) {
	case StatusRemoteAhead:
		return PushResult{}, ErrBehind
	case StatusConflict:
		return PushResult{}, &ConflictError{w.ID, headNames(hs)}
	}
	return e.upload(ctx, w, hs, v.scan)
}

// upload writes packs, then a manifest whose parents are hs. sc is a scan of
// the world taken just before; only files holding chunks the store does not
// already have are read again.
func (e *Engine) upload(ctx context.Context, w worlds.World, hs []head, sc *scanResult) (PushResult, error) {
	return e.uploadWith(ctx, w, hs, sc, uploadOpts{})
}

// uploadOpts changes how upload treats data the cloud may have lost. Only the
// person's own choice (ResolveLost) sets them.
type uploadOpts struct {
	// repair carries on past packs the cloud lost, uploading those chunks again
	// instead of refusing.
	repair bool
	// noReuse uploads every chunk, trusting no stored pack.
	noReuse bool
	// minGen: the new version's generation is at least minGen+1.
	minGen int64
}

func (e *Engine) uploadWith(ctx context.Context, w worlds.World, hs []head, sc *scanResult, opts uploadOpts) (PushResult, error) {
	e.phase(w.ID, "uploading")
	known := map[string]manifest.ChunkLoc{}
	var gen int64
	var parents []string
	for _, h := range hs {
		for k, v := range h.M.Chunks {
			known[k] = v
		}
		if h.M.Generation > gen {
			gen = h.M.Generation
		}
		parents = append(parents, h.Name)
	}
	if opts.minGen > gen {
		gen = opts.minGen
	}
	if opts.noReuse {
		known = map[string]manifest.ChunkLoc{}
	}
	// Build only on data the store still has.
	missing, err := e.missingPacks(ctx, known)
	if err != nil {
		return PushResult{}, err
	}
	if len(missing) > 0 {
		if !opts.repair {
			le := &LostError{WorldID: w.ID, Kind: LostData, Packs: len(missing)}
			e.lost.set(w.ID, le)
			return PushResult{}, le
		}
		for h, loc := range known {
			if missing[loc.Pack] {
				delete(known, h)
			}
		}
	}
	sort.Strings(parents)
	chunks := map[string]manifest.ChunkLoc{}
	var (
		buf      []byte
		pending  []string // chunk hashes in buf, in order
		offsets  []int64
		inPack   = map[string]bool{}
		newBytes int64
	)
	up := newPackUploader(ctx, e.Store, transferWorkers)
	defer func() { up.cancel(); up.wg.Wait() }() // no upload outlives the call
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		pack := chunker.Hash(buf)
		for i, h := range pending {
			end := int64(len(buf))
			if i+1 < len(pending) {
				end = offsets[i+1]
			}
			chunks[h] = manifest.ChunkLoc{Pack: pack, Offset: offsets[i], Length: end - offsets[i]}
		}
		newBytes += int64(len(buf))
		data := buf
		buf, pending, offsets, inPack = nil, nil, nil, map[string]bool{}
		return up.put(pack, data) // runs in the background; wait() below reports failures
	}
	onChunk := func(h string, data []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if loc, ok := known[h]; ok {
			chunks[h] = loc
			return nil
		}
		if _, ok := chunks[h]; ok || inPack[h] {
			return nil
		}
		inPack[h] = true
		pending = append(pending, h)
		offsets = append(offsets, int64(len(buf)))
		buf = append(buf, data...)
		if len(buf) >= packTarget {
			return flush()
		}
		return nil
	}
	files := append([]manifest.File(nil), sc.Files...)
	for i, f := range files {
		need := false
		for _, h := range f.Chunks {
			if loc, ok := known[h]; ok {
				chunks[h] = loc
			} else if _, ok := chunks[h]; !ok {
				need = true
			}
		}
		if !need {
			continue
		}
		lf, err := feedChunks(filepath.Join(w.Path, filepath.FromSlash(f.Path)), onChunk)
		if err != nil {
			return PushResult{}, err
		}
		// What was uploaded is what the manifest describes, even if the game
		// touched the file since the scan.
		files[i] = manifest.File{Path: f.Path, Size: lf.Size, Mode: lf.Mode, MTime: lf.MTime, Chunks: lf.Chunks}
	}
	if err := flush(); err != nil {
		return PushResult{}, err
	}
	if err := up.wait(); err != nil {
		return PushResult{}, err
	}
	m := &manifest.Manifest{
		Version: manifest.Version, WorldID: w.ID, DeviceID: e.DeviceID,
		Generation: gen + 1, Parents: parents, CreatedAt: e.now(),
		Files: files, Chunks: chunks,
	}
	if err := m.Validate(); err != nil {
		return PushResult{}, err // e.g. a file changed while scanning
	}
	data, err := m.Marshal()
	if err != nil {
		return PushResult{}, err
	}
	// The manifest is written last: until now the push is invisible.
	if err := e.Store.PutManifest(ctx, w.ID, m.Name(), data); err != nil {
		return PushResult{}, err
	}
	e.cacheManifest(w.ID, m.Name(), m)
	if err := e.saveState(w.ID, state{m.Name(), m.TreeHash()}); err != nil {
		return PushResult{}, err
	}
	e.lost.clear(w.ID)
	return PushResult{Manifest: m.Name(), Pushed: true, NewBytes: newBytes}, nil
}

// ---- pull ----

type PullResult struct {
	Manifest string
	Updated  bool
	Snapshot string // snapshot id taken before overwriting, if any
}

// Pull brings the newest stored version to this computer. It refuses when the
// world is open or on conflict.
func (e *Engine) Pull(ctx context.Context, w worlds.World) (PullResult, error) {
	if err := e.check(w); err != nil {
		return PullResult{}, err
	}
	e.keepLeftover(w)
	if err := Recover(w.Path); err != nil {
		return PullResult{}, err
	}
	v, err := e.look(ctx, w)
	if err != nil {
		return PullResult{}, err
	}
	hs := v.hs
	if len(hs) == 0 {
		return PullResult{}, nil
	}
	if !v.exists {
		return e.apply(ctx, w, hs[0], "pull")
	}
	switch e.classify(w.ID, hs, v.tree) {
	case StatusSynced:
		return PullResult{Manifest: hs[0].Name}, e.saveState(w.ID, state{hs[0].Name, v.tree})
	case StatusLocalAhead:
		return PullResult{}, nil
	case StatusRemoteAhead:
		return e.apply(ctx, w, hs[0], "pull")
	}
	return PullResult{}, &ConflictError{w.ID, headNames(hs)}
}

// apply builds h in a staging dir (reusing local data, downloading the rest),
// verifies every hash, then swaps it in atomically, keeping the replaced world
// as a snapshot.
func (e *Engine) apply(ctx context.Context, w worlds.World, h head, reason string) (PullResult, error) {
	e.phase(w.ID, "downloading")
	staging := stagingPath(w.Path)
	if err := os.RemoveAll(staging); err != nil {
		return PullResult{}, err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return PullResult{}, err
	}
	exists := worlds.IsWorld(w.Path)
	var local *scanResult
	if exists {
		local, _ = scan(w.Path, e.statCache(w.ID)) // only used to avoid downloads
	}
	err := e.materialize(ctx, h.M, staging, w.Path, local)
	if err != nil {
		os.RemoveAll(staging)
		if errors.Is(err, store.ErrNotFound) {
			// The version is listed but a pack it needs is gone. Report it; never
			// patch it up from what happens to be on this computer.
			le := &LostError{WorldID: w.ID, Kind: LostData}
			e.lost.set(w.ID, le)
			return PullResult{}, le
		}
		return PullResult{}, err
	}
	e.lost.clear(w.ID)
	fresh := cacheFromManifest(h.M, staging)
	res := PullResult{Manifest: h.Name, Updated: true}
	if exists {
		if res.Snapshot, err = e.swapWithSnapshot(w, staging, reason); err != nil {
			return PullResult{}, err
		}
	} else if err := swapIn(w.Path, staging); err != nil {
		os.RemoveAll(staging)
		return PullResult{}, err
	}
	sc := e.statCache(w.ID)
	sc.entries, sc.dirty = fresh, true
	sc.save()
	return res, e.saveState(w.ID, state{h.Name, h.M.TreeHash()})
}

// ---- conflict resolution ----

type Strategy string

const (
	KeepLocal  Strategy = "keep-local"  // local wins, uploaded as the new version
	KeepRemote Strategy = "keep-remote" // stored wins, local is snapshotted first
	KeepNewest Strategy = "keep-newest" // whichever was modified last
	KeepBoth   Strategy = "keep-both"   // local copied to a new world folder, then stored wins
)

type ResolveResult struct {
	Used     Strategy
	Manifest string
	Copy     string // path of the preserved copy for KeepBoth
}

// Resolve settles a conflict. Cancelling is simply not calling Resolve.
func (e *Engine) Resolve(ctx context.Context, w worlds.World, s Strategy) (ResolveResult, error) {
	if err := e.check(w); err != nil {
		return ResolveResult{}, err
	}
	hs, err := e.heads(ctx, w.ID)
	if err != nil {
		return ResolveResult{}, err
	}
	if len(hs) == 0 {
		return ResolveResult{}, errors.New("nothing stored; no conflict to resolve")
	}
	if !worlds.IsWorld(w.Path) {
		s = KeepRemote
	}
	res := ResolveResult{Used: s}
	var sc *scanResult
	if worlds.IsWorld(w.Path) && (s == KeepNewest || s == KeepLocal) {
		var err error
		if sc, err = scan(w.Path, e.statCache(w.ID)); err != nil {
			return res, err
		}
	}
	if s == KeepNewest {
		if newestMTime(sc.Files) > hs[0].M.CreatedAt.UnixNano() {
			s = KeepLocal
		} else {
			s = KeepRemote
		}
		res.Used = s
	}
	switch s {
	case KeepLocal:
		r, err := e.upload(ctx, w, hs, sc) // parents = all heads, so it supersedes them
		res.Manifest = r.Manifest
		return res, err
	case KeepBoth:
		cp := fmt.Sprintf("%s (conflict copy %s)", w.Path, e.now().Format("2006-01-02 150405"))
		if err := os.MkdirAll(cp, 0o755); err != nil {
			return res, err
		}
		if err := copyTree(w.Path, cp); err != nil {
			os.RemoveAll(cp)
			return res, err
		}
		res.Copy = cp
		fallthrough
	case KeepRemote:
		target := hs[0]
		if len(hs) > 1 {
			// Write a merge manifest so the losing heads stop being heads.
			merged := *target.M
			merged.DeviceID, merged.Parents = e.DeviceID, headNames(hs)
			merged.Generation, merged.CreatedAt = target.M.Generation+1, e.now()
			data, err := merged.Marshal()
			if err != nil {
				return res, err
			}
			if err := e.Store.PutManifest(ctx, w.ID, merged.Name(), data); err != nil {
				return res, err
			}
			target = head{merged.Name(), &merged}
		}
		r, err := e.apply(ctx, w, target, "conflict")
		res.Manifest = r.Manifest
		return res, err
	}
	return res, fmt.Errorf("unknown strategy %q", s)
}
