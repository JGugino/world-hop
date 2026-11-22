// Package detect works out whether Minecraft is running and whether a world
// is currently open in it. Both checks are behind small interfaces so they
// can be faked in tests and implemented per platform.
package detect

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LockFile is the per-world file Minecraft holds a lock on while the world
// is open. It is never synced.
const LockFile = "session.lock"

// ErrUnsupported is returned by a LockProber that cannot tell on this
// platform or file system.
var ErrUnsupported = errors.New("lock probing unsupported")

// Proc is a running process.
type Proc struct {
	PID     int
	Cmdline string
}

// ProcessLister lists running processes.
type ProcessLister interface {
	List() ([]Proc, error)
}

// LockProber reports whether another process holds the OS-level lock on the
// file at path. A missing file is not held. It returns ErrUnsupported (or
// another error) when it cannot tell.
type LockProber interface {
	Held(path string) (bool, error)
}

// Detector answers "is the game running?" and "is this world open?".
type Detector struct {
	Procs ProcessLister
	Lock  LockProber

	// FreshLock is how recently session.lock must have been written for a
	// running game to count the world as open when the OS lock check cannot
	// be trusted (e.g. in the instant between creating and locking the file).
	FreshLock time.Duration
	// CacheTTL bounds how often the process list is re-read.
	CacheTTL time.Duration

	Now func() time.Time

	mu      sync.Mutex
	cachedA time.Time
	cached  bool
	cachedE error
}

// New returns a Detector using the platform's process lister and lock prober.
func New() *Detector {
	return &Detector{
		Procs:     NewExecLister(),
		Lock:      PlatformLockProber{},
		FreshLock: 10 * time.Second,
		CacheTTL:  2 * time.Second,
	}
}

func (d *Detector) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// GameRunning reports whether a Minecraft Java process is running.
func (d *Detector) GameRunning() (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.CacheTTL > 0 && !d.cachedA.IsZero() && d.now().Sub(d.cachedA) < d.CacheTTL {
		return d.cached, d.cachedE
	}
	procs, err := d.Procs.List()
	running := false
	if err == nil {
		for _, p := range procs {
			if IsMinecraftProcess(p) {
				running = true
				break
			}
		}
	}
	d.cachedA, d.cached, d.cachedE = d.now(), running, err
	return running, err
}

// WorldOpen reports whether the world at worldPath is open in Minecraft.
// When it cannot tell it errs on the side of "open", because syncing an open
// world risks corrupting it.
func (d *Detector) WorldOpen(worldPath string) bool {
	lock := filepath.Join(worldPath, LockFile)
	st, err := os.Stat(lock)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return false // never opened, or lock removed
	}
	if held, err := d.Lock.Held(lock); err == nil {
		if held {
			return true
		}
		// Not locked. A running game plus a just-written lock file is the
		// moment between create and lock.
		if st != nil && d.FreshLock > 0 && d.now().Sub(st.ModTime()) < d.FreshLock {
			if running, _ := d.GameRunning(); running {
				return true
			}
		}
		return false
	}
	// Could not probe the lock: fall back to the process check.
	running, err := d.GameRunning()
	if err != nil {
		return true
	}
	return running
}
