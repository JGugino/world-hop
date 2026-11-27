//go:build linux || darwin

// lock_unix.go: probing Minecraft's world lock on Linux and macOS with fcntl.

package detect

import (
	"errors"
	"os"
	"syscall"
)

// PlatformLockProber asks the OS whether another process holds a lock on the
// file. Java's FileLock is an fcntl lock on Linux and macOS, which F_GETLK
// can see without taking the lock ourselves.
type PlatformLockProber struct{}

// Held reports whether another process holds a lock on the file at path. It asks
// the OS with F_GETLK, which describes a conflicting lock without taking one, so
// probing never disturbs the game. A missing file is not held.
func (PlatformLockProber) Held(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	lk := syscall.Flock_t{Type: syscall.F_RDLCK, Whence: 0, Start: 0, Len: 0}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		return false, ErrUnsupported
	}
	return lk.Type != syscall.F_UNLCK, nil
}
