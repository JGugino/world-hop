// rekey.go: changing the passphrase. See the comment below for how it works.

package crypt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

// Changing the passphrase
//
// Every key in the hierarchy comes from the passphrase and a salt, and the
// names of everything in the store (world ids, pack names) come from the naming
// key. A new passphrase therefore means a new layout, and because the store is
// write-once the old one stays where it is. Changing the passphrase is a copy:
//
//  1. read every world through the old key,
//  2. write it through the new key (packs first, then manifests),
//  3. write the next key header generation. This is the commit point: until it
//     exists no other computer sees any of it, and an interrupted change leaves
//     nothing but unused data behind.
//
// Manifest names are identical in both layouts, so each computer's record of
// what it last synced stays valid and nothing looks like a conflict.
//
// Every manifest is copied (the history is small and keeps the "which versions
// are newest" answer identical). Packs are copied only for the newest
// versions, because that is all a sync ever reads: older versions' data stays
// in the old encrypted copy.

var (
	// ErrSamePassphrase: the new passphrase is the current one.
	ErrSamePassphrase = errors.New("choose a passphrase different from the current one")
	// ErrRekeyConflict: another computer changed the passphrase first.
	ErrRekeyConflict = errors.New("the passphrase was changed on another computer while this change was running; nothing was changed here")
	// ErrNotEnabled: there is no encryption to change the passphrase of.
	ErrNotEnabled = errors.New("encryption is not turned on")
)

// Rekey states.
const (
	RekeyRunning   = "running"
	RekeyDone      = "done"
	RekeyFailed    = "failed"
	RekeyCancelled = "cancelled"
)

// Rekey phases, while running.
const (
	PhasePreparing = "preparing"
	PhaseCopying   = "copying"
	PhaseFinishing = "finishing"
)

// Rekey is the state of a passphrase change, for the UI.
type Rekey struct {
	State string `json:"state"`
	Phase string `json:"phase,omitempty"`
	// Done and Total count the things to copy: manifests and packs.
	Done  int `json:"done"`
	Total int `json:"total"`
	// Worlds is how many worlds are being carried over.
	Worlds int `json:"worlds"`
	// Error says why a change failed, in words for the person.
	Error string `json:"error,omitempty"`
}

// rekeyWorkers bounds the packs copied at once (each is up to about 16 MiB).
const rekeyWorkers = 4

// pending is a change that was started and not finished. It lets a retry with
// the same new passphrase reuse the same salt, so the data already copied is
// found again instead of being left behind under a key nobody can derive.
type pending struct {
	From   int64  `json:"from"` // key header generation being replaced
	Header Header `json:"header"`
}

// pendingPath is where an unfinished passphrase change is remembered (rekey.json,
// next to encryption.json).
func (m *Manager) pendingPath() string {
	return filepath.Join(filepath.Dir(m.path), "rekey.json")
}

// loadPending reads the remembered unfinished change, or nil.
func (m *Manager) loadPending() *pending {
	b, err := os.ReadFile(m.pendingPath())
	if err != nil {
		return nil
	}
	var p pending
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	return &p
}

// savePending remembers an unfinished change (written atomically, owner-only). It
// holds the new header (salt, cost, check: no secret) so a retry with the same new
// passphrase reuses the salt and finds the data already copied.
func (m *Manager) savePending(p pending) {
	b, _ := json.Marshal(p)
	tmp := m.pendingPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, m.pendingPath())
	}
}

// clearPending forgets the unfinished change (after it committed, or was superseded).
func (m *Manager) clearPending() { os.Remove(m.pendingPath()) }

// StartChangePassphrase checks the request and, if it is in order, starts
// re-encrypting everything under the new passphrase in the background. It
// returns once the change is running (or the request was refused); watch
// Info().Rekey for progress.
//
// current must be the passphrase now in force; the new one must be typed twice
// and its loss acknowledged, as when the key was first made. Changing the
// passphrase is not a Pro feature: it only maintains data that is already
// encrypted.
func (m *Manager) StartChangePassphrase(ctx context.Context, current, next, confirm string, acknowledged, remember bool) error {
	m.mu.Lock()
	enabled := m.cfg.Enabled
	busy := m.rekey != nil && m.rekey.State == RekeyRunning
	m.mu.Unlock()
	switch {
	case !enabled:
		return ErrNotEnabled
	case busy:
		return ErrRekeying
	}
	if next != confirm || !acknowledged {
		return ErrNotConfirmed
	}
	h, err := ReadHeader(ctx, m.Inner)
	if err != nil {
		return err
	}
	oldKey, err := h.Unlock(current)
	if err != nil {
		return err
	}
	if current == next {
		return ErrSamePassphrase
	}
	if len([]rune(next)) < MinPassphrase {
		return ErrWeakPassphrase
	}

	// Reuse the salt of an earlier unfinished attempt at this same passphrase.
	var newHdr *Header
	var newKey *Key
	if p := m.loadPending(); p != nil && p.From == h.Generation() {
		hh := p.Header
		hh.gen = h.Generation() + 1
		if k, err := hh.Unlock(next); err == nil {
			newHdr, newKey = &hh, k
		}
	}
	if newHdr == nil {
		params := m.Params
		if params == (Params{}) {
			params = DefaultParams
		}
		if newHdr, newKey, err = NewHeaderAfter(h, next, params); err != nil {
			return err
		}
	}
	m.savePending(pending{From: h.Generation(), Header: *newHdr})

	rctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	if m.rekey != nil && m.rekey.State == RekeyRunning { // lost a race with another request
		m.mu.Unlock()
		cancel()
		return ErrRekeying
	}
	m.rekey = &Rekey{State: RekeyRunning, Phase: PhasePreparing}
	m.rekeyCancel, m.rekeyDone = cancel, done
	m.mu.Unlock()

	go func() {
		defer close(done)
		defer cancel()
		err := m.runRekey(rctx, h, oldKey, newKey, newHdr, remember)
		m.finishRekey(err)
	}()
	return nil
}

// CancelChangePassphrase stops a running change. Nothing was committed, so
// nothing changes; a later attempt with the same new passphrase picks up where
// this one stopped.
func (m *Manager) CancelChangePassphrase() {
	m.mu.Lock()
	cancel := m.rekeyCancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// DismissRekey clears a finished change's result from Info.
func (m *Manager) DismissRekey() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rekey != nil && m.rekey.State != RekeyRunning {
		m.rekey = nil
	}
}

// WaitChangePassphrase blocks until the running change (if any) has finished.
func (m *Manager) WaitChangePassphrase(ctx context.Context) {
	m.mu.Lock()
	done := m.rekeyDone
	m.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// update applies f to the running change's progress record under the lock.
func (m *Manager) update(f func(r *Rekey)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rekey != nil {
		f(m.rekey)
	}
}

// finishRekey records how the background change ended (done, cancelled or failed
// with a message) once runRekey returns.
func (m *Manager) finishRekey(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rekey == nil {
		return
	}
	m.rekey.Phase = ""
	switch {
	case err == nil:
		m.rekey.State = RekeyDone
	case errors.Is(err, context.Canceled):
		m.rekey.State = RekeyCancelled
	default:
		m.rekey.State = RekeyFailed
		m.rekey.Error = err.Error()
	}
}

// runRekey does the copy and the commit.
func (m *Manager) runRekey(ctx context.Context, oldHdr *Header, oldKey, newKey *Key, newHdr *Header, remember bool) error {
	c := &copier{
		ctx: ctx, old: NewForGeneration(m.Inner, oldKey, oldHdr.Generation()), nw: NewForGeneration(m.Inner, newKey, newHdr.Generation()),
		packs: map[string]bool{}, manifests: map[string]map[string]bool{},
		progress: func(done, total, worlds int) {
			m.update(func(r *Rekey) { r.Done, r.Total, r.Worlds, r.Phase = done, total, worlds, PhaseCopying })
		},
	}
	// A computer that keeps syncing while this runs can add versions after the
	// first pass; copy those too, until a pass finds nothing new.
	for pass := 0; pass < 4; pass++ {
		n, err := c.pass()
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}

	m.update(func(r *Rekey) { r.Phase = PhaseFinishing })
	if err := ctx.Err(); err != nil {
		return err
	}
	switch err := WriteHeader(ctx, m.Inner, newHdr); {
	case err == nil:
	case errors.Is(err, store.ErrExists):
		m.clearPending()
		return ErrRekeyConflict
	default:
		return err
	}
	m.clearPending()

	// The new key is in force. Switch this computer over and drop the old key.
	m.mu.Lock()
	defer m.mu.Unlock()
	oldID := ""
	if m.hdr != nil {
		oldID = m.hdr.ID()
	}
	if remember && m.Keys != nil {
		if err := m.Keys.Set(newHdr.ID(), newKey.Bytes()); err != nil {
			remember = false // the change itself succeeded; the key just is not remembered
		}
	}
	if m.Keys != nil {
		if oldID != "" && oldID != newHdr.ID() {
			m.Keys.Delete(oldID)
		}
		if !remember {
			m.Keys.Delete(newHdr.ID())
		}
	}
	m.cfg = settings{Enabled: true, Remember: remember && m.Keys != nil, KeyID: newHdr.ID(), KeyGen: newHdr.Generation(), Header: newHdr}
	m.save()
	m.hdr, m.key, m.enc, m.checked = newHdr, newKey, NewForGeneration(m.Inner, newKey, newHdr.Generation()), m.now()
	return nil
}

// copier carries worlds from one key's layout to the next.
type copier struct {
	ctx      context.Context
	old, nw  *Store
	progress func(done, total, worlds int)

	mu        sync.Mutex
	packs     map[string]bool            // packs already copied
	manifests map[string]map[string]bool // world -> manifests already copied
	done      int
	total     int
	worlds    int
}

// worldPlan is what is left to copy for one world in a pass: its manifests not yet
// copied, and the packs its newest versions need.
type worldPlan struct {
	id        string
	manifests []string // not yet copied
	packs     []string // newest versions' packs, not yet copied
}

// pass copies whatever the previous passes have not, and reports how many
// things that was.
func (c *copier) pass() (int, error) {
	ids, err := c.old.ListWorlds(c.ctx)
	if err != nil {
		return 0, err
	}
	var plans []worldPlan
	newPacks := map[string]bool{}
	for _, id := range ids {
		pl, err := c.plan(id, newPacks)
		if err != nil {
			return 0, err
		}
		if len(pl.manifests) > 0 || len(pl.packs) > 0 {
			plans = append(plans, pl)
		}
	}
	items := len(newPacks)
	for _, pl := range plans {
		items += len(pl.manifests)
	}
	if items == 0 {
		return 0, nil
	}
	c.mu.Lock()
	c.total += items
	c.worlds = len(ids)
	c.mu.Unlock()
	c.report()

	// Packs first, then manifests: a manifest never appears before its data.
	var packNames []string
	for n := range newPacks {
		packNames = append(packNames, n)
	}
	sort.Strings(packNames)
	if err := forEach(c.ctx, len(packNames), rekeyWorkers, func(i int) error {
		data, err := c.old.GetPack(c.ctx, packNames[i])
		if err != nil {
			return fmt.Errorf("reading a pack with the current passphrase: %w", err)
		}
		if err := c.nw.PutPack(c.ctx, packNames[i], data); err != nil {
			return err
		}
		c.tick(func() { c.packs[packNames[i]] = true })
		return nil
	}); err != nil {
		return 0, err
	}
	for _, pl := range plans {
		for _, name := range pl.manifests { // oldest first: names sort by generation
			data, err := c.old.GetManifest(c.ctx, pl.id, name)
			if err != nil {
				return 0, fmt.Errorf("reading a version with the current passphrase: %w", err)
			}
			if err := c.nw.PutManifest(c.ctx, pl.id, name, data); err != nil && !errors.Is(err, store.ErrExists) {
				return 0, err
			}
			id, n := pl.id, name
			c.tick(func() {
				if c.manifests[id] == nil {
					c.manifests[id] = map[string]bool{}
				}
				c.manifests[id][n] = true
			})
		}
	}
	return items, nil
}

// plan works out what is left to copy for one world: its manifests that have
// not been copied, and the packs its newest versions need.
func (c *copier) plan(id string, newPacks map[string]bool) (worldPlan, error) {
	pl := worldPlan{id: id}
	names, err := c.old.ListManifests(c.ctx, id)
	if err != nil {
		return pl, err
	}
	sort.Strings(names)
	parsed := map[string]*manifest.Manifest{}
	superseded := map[string]bool{}
	for _, n := range names {
		data, err := c.old.GetManifest(c.ctx, id, n)
		if err != nil {
			return pl, fmt.Errorf("reading a version with the current passphrase: %w", err)
		}
		mf, err := manifest.Parse(data)
		if err != nil {
			return pl, err
		}
		parsed[n] = mf
		for _, p := range mf.Parents {
			superseded[p] = true
		}
		c.mu.Lock()
		copied := c.manifests[id][n]
		c.mu.Unlock()
		if !copied {
			pl.manifests = append(pl.manifests, n)
		}
	}
	for _, n := range names {
		if superseded[n] {
			continue
		}
		for _, loc := range parsed[n].Chunks {
			c.mu.Lock()
			copied := c.packs[loc.Pack]
			c.mu.Unlock()
			if !copied && !newPacks[loc.Pack] {
				newPacks[loc.Pack] = true
				pl.packs = append(pl.packs, loc.Pack)
			}
		}
	}
	return pl, nil
}

// tick marks one copied item (via mark, under the lock) and reports progress.
func (c *copier) tick(mark func()) {
	c.mu.Lock()
	mark()
	c.done++
	c.mu.Unlock()
	c.report()
}

// report sends the current done / total / worlds counts to the progress callback.
func (c *copier) report() {
	c.mu.Lock()
	d, t, w := c.done, c.total, c.worlds
	c.mu.Unlock()
	c.progress(d, t, w)
}

// forEach runs f for 0..n-1 with at most workers at once and stops at the first
// error or when ctx ends.
func forEach(ctx context.Context, n, workers int, f func(i int) error) error {
	if n == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	next := make(chan int)
	for w := 0; w < workers && w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if err := f(i); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
			}
		}()
	}
loop:
	for i := 0; i < n; i++ {
		select {
		case next <- i:
		case <-ctx.Done():
			break loop
		}
	}
	close(next)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
