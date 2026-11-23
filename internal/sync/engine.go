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
func (e *Engine) heads(ctx context.Context, id string) ([]head, error) {
	names, err := e.Store.ListManifests(ctx, id)
	if err != nil {
		return nil, err
	}
	all := map[string]*manifest.Manifest{}
	superseded := map[string]bool{}
	for _, n := range names {
		m, err := e.loadManifest(ctx, id, n)
		if err != nil {
			return nil, err
		}
		all[n] = m
		for _, p := range m.Parents {
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
)

// Status compares the local world with the store without changing anything.
func (e *Engine) Status(ctx context.Context, w worlds.World) (Status, error) {
	hs, err := e.heads(ctx, w.ID)
	if err != nil {
		return "", err
	}
	exists := worlds.IsWorld(w.Path)
	switch {
	case len(hs) == 0 && !exists:
		return StatusNoRemote, nil
	case len(hs) == 0:
		return StatusLocalAhead, nil
	case !exists:
		return StatusNoLocal, nil
	}
	files, err := scanTree(w.Path, nil)
	if err != nil {
		return "", err
	}
	return e.classify(w.ID, hs, manifest.TreeHash(files)), nil
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
	hs, err := e.heads(ctx, w.ID)
	if err != nil {
		return PushResult{}, err
	}
	files, err := scanTree(w.Path, nil)
	if err != nil {
		return PushResult{}, err
	}
	tree := manifest.TreeHash(files)
	if len(hs) == 1 && hs[0].M.TreeHash() == tree { // already stored: just adopt
		return PushResult{Manifest: hs[0].Name}, e.saveState(w.ID, state{hs[0].Name, tree})
	}
	switch e.classify(w.ID, hs, tree) {
	case StatusRemoteAhead:
		return PushResult{}, ErrBehind
	case StatusConflict:
		return PushResult{}, &ConflictError{w.ID, headNames(hs)}
	}
	return e.upload(ctx, w, hs)
}

// upload writes packs, then a manifest whose parents are hs.
func (e *Engine) upload(ctx context.Context, w worlds.World, hs []head) (PushResult, error) {
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
	sort.Strings(parents)
	chunks := map[string]manifest.ChunkLoc{}
	var (
		buf      []byte
		pending  []string // chunk hashes in buf, in order
		offsets  []int64
		inPack   = map[string]bool{}
		newBytes int64
	)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		pack := chunker.Hash(buf)
		if err := e.Store.PutPack(ctx, pack, buf); err != nil {
			return err
		}
		for i, h := range pending {
			end := int64(len(buf))
			if i+1 < len(pending) {
				end = offsets[i+1]
			}
			chunks[h] = manifest.ChunkLoc{Pack: pack, Offset: offsets[i], Length: end - offsets[i]}
		}
		newBytes += int64(len(buf))
		buf, pending, offsets, inPack = nil, nil, nil, map[string]bool{}
		return nil
	}
	files, err := scanTree(w.Path, func(h string, data []byte) error {
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
	})
	if err != nil {
		return PushResult{}, err
	}
	if err := flush(); err != nil {
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
	if err := e.saveState(w.ID, state{m.Name(), m.TreeHash()}); err != nil {
		return PushResult{}, err
	}
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
	if err := Recover(w.Path); err != nil {
		return PullResult{}, err
	}
	hs, err := e.heads(ctx, w.ID)
	if err != nil {
		return PullResult{}, err
	}
	if len(hs) == 0 {
		return PullResult{}, nil
	}
	if !worlds.IsWorld(w.Path) {
		return e.apply(ctx, w, hs[0], "pull")
	}
	files, err := scanTree(w.Path, nil)
	if err != nil {
		return PullResult{}, err
	}
	tree := manifest.TreeHash(files)
	switch e.classify(w.ID, hs, tree) {
	case StatusSynced:
		return PullResult{Manifest: hs[0].Name}, e.saveState(w.ID, state{hs[0].Name, tree})
	case StatusLocalAhead:
		return PullResult{}, nil
	case StatusRemoteAhead:
		return e.apply(ctx, w, hs[0], "pull")
	}
	return PullResult{}, &ConflictError{w.ID, headNames(hs)}
}

// apply downloads h, builds it in a staging dir, verifies every hash,
// snapshots the current world and swaps it in atomically.
func (e *Engine) apply(ctx context.Context, w worlds.World, h head, reason string) (PullResult, error) {
	staging := stagingPath(w.Path)
	if err := os.RemoveAll(staging); err != nil {
		return PullResult{}, err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return PullResult{}, err
	}
	if err := e.materialize(ctx, h.M, staging); err != nil {
		os.RemoveAll(staging)
		return PullResult{}, err
	}
	// Verify the result before touching the real world.
	files, err := scanTree(staging, nil)
	if err == nil && manifest.TreeHash(files) != h.M.TreeHash() {
		err = fmt.Errorf("%w: rebuilt world differs from manifest", ErrIntegrity)
	}
	if err != nil {
		os.RemoveAll(staging)
		return PullResult{}, err
	}
	res := PullResult{Manifest: h.Name, Updated: true}
	if worlds.IsWorld(w.Path) {
		if res.Snapshot, err = e.Snapshot(w, reason); err != nil {
			os.RemoveAll(staging)
			return PullResult{}, fmt.Errorf("snapshot before overwrite: %w", err)
		}
	}
	if err := swapIn(w.Path, staging); err != nil {
		os.RemoveAll(staging)
		return PullResult{}, err
	}
	return res, e.saveState(w.ID, state{h.Name, h.M.TreeHash()})
}

// materialize writes manifest m's files into dir, verifying chunk hashes.
func (e *Engine) materialize(ctx context.Context, m *manifest.Manifest, dir string) error {
	var packName string
	var pack []byte
	for _, f := range m.Files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(f.Mode&0o777)|0o600)
		if err != nil {
			return err
		}
		for _, h := range f.Chunks {
			loc := m.Chunks[h]
			if loc.Pack != packName {
				b, err := e.Store.GetPack(ctx, loc.Pack)
				if err == nil && chunker.Hash(b) != loc.Pack {
					err = fmt.Errorf("%w: pack %s", ErrIntegrity, loc.Pack)
				}
				if err != nil {
					out.Close()
					return err
				}
				packName, pack = loc.Pack, b
			}
			if loc.Offset+loc.Length > int64(len(pack)) {
				out.Close()
				return fmt.Errorf("%w: chunk outside pack", ErrIntegrity)
			}
			data := pack[loc.Offset : loc.Offset+loc.Length]
			if chunker.Hash(data) != h {
				out.Close()
				return fmt.Errorf("%w: chunk %s", ErrIntegrity, h)
			}
			if _, err := out.Write(data); err != nil {
				out.Close()
				return err
			}
		}
		if err := out.Close(); err != nil {
			return err
		}
		if f.MTime > 0 {
			t := time.Unix(0, f.MTime)
			os.Chtimes(target, t, t)
		}
	}
	return nil
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
	if s == KeepNewest {
		files, err := scanTree(w.Path, nil)
		if err != nil {
			return res, err
		}
		if newestMTime(files) > hs[0].M.CreatedAt.UnixNano() {
			s = KeepLocal
		} else {
			s = KeepRemote
		}
		res.Used = s
	}
	switch s {
	case KeepLocal:
		r, err := e.upload(ctx, w, hs) // parents = all heads, so it supersedes them
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
