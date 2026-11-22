package sync

import (
	"context"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/worlds"
)

// SchedulerConfig sets the scheduler's timings. Zero values use defaults.
type SchedulerConfig struct {
	// PollInterval is how often world open/closed state is checked.
	PollInterval time.Duration
	// SyncInterval is how often a full pass runs to pick up changes made on
	// other computers.
	SyncInterval time.Duration
	// Settle is how long a world must stay closed before it is pushed, so a
	// quick close-and-reopen does not trigger a sync mid-session.
	Settle time.Duration
}

func (c SchedulerConfig) withDefaults() SchedulerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.SyncInterval <= 0 {
		c.SyncInterval = 10 * time.Minute
	}
	if c.Settle <= 0 {
		c.Settle = 3 * time.Second
	}
	return c
}

// Scheduler decides when to sync:
//   - once at start,
//   - for a world, when it goes from open to closed (after Settle),
//   - on a timer, for every world.
//
// Force Sync goes through Runner directly and shares its lock, so passes
// never overlap. Worlds that are open are never synced: the Engine refuses
// them, and the Scheduler does not try.
type Scheduler struct {
	runner *Runner
	list   func() []worlds.World
	isOpen func(worldPath string) bool
	cfg    SchedulerConfig
	now    func() time.Time

	mu         stdsync.Mutex
	wasOpen    map[string]bool      // by world path, from the last poll
	closedAt   map[string]time.Time // closed, waiting to settle, then push
	lastFull   time.Time
	startupRan bool
}

// NewScheduler returns a Scheduler. list returns the worlds to watch and
// isOpen reports whether a world is open in the game (see internal/detect).
func NewScheduler(r *Runner, list func() []worlds.World, isOpen func(string) bool, cfg SchedulerConfig) *Scheduler {
	return &Scheduler{
		runner:   r,
		list:     list,
		isOpen:   isOpen,
		cfg:      cfg.withDefaults(),
		now:      time.Now,
		wasOpen:  map[string]bool{},
		closedAt: map[string]time.Time{},
	}
}

// Run blocks until ctx is done: a startup pass, then Poll every PollInterval.
func (s *Scheduler) Run(ctx context.Context) {
	s.Poll(ctx)
	t := time.NewTicker(s.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Poll(ctx)
		}
	}
}

// Poll does one scheduling step. The first call also runs the startup pass.
func (s *Scheduler) Poll(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ws := s.list()
	now := s.now()

	s.mu.Lock()
	first := !s.startupRan
	s.startupRan = true
	var due []worlds.World
	seen := map[string]bool{}
	for _, w := range ws {
		seen[w.Path] = true
		open := s.isOpen(w.Path)
		prev, known := s.wasOpen[w.Path]
		s.wasOpen[w.Path] = open
		switch {
		case open:
			delete(s.closedAt, w.Path) // reopened before settling
		case known && prev:
			s.closedAt[w.Path] = now // just closed
		}
		if t, ok := s.closedAt[w.Path]; ok && !open && now.Sub(t) >= s.cfg.Settle {
			delete(s.closedAt, w.Path)
			due = append(due, w)
		}
	}
	for p := range s.wasOpen { // forget worlds no longer listed
		if !seen[p] {
			delete(s.wasOpen, p)
			delete(s.closedAt, p)
		}
	}
	full := first || now.Sub(s.lastFull) >= s.cfg.SyncInterval
	if full {
		s.lastFull = now
	}
	s.mu.Unlock()

	if full {
		// A full pass covers the worlds that just settled too.
		s.runner.SyncAll(ctx)
		return
	}
	if len(due) > 0 {
		s.runner.SyncWorlds(ctx, due)
	}
}
