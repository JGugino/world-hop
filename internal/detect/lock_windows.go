//go:build windows

// lock_windows.go: probing Minecraft's world lock on Windows.

package detect

import (
	"errors"
	"io"
	"os"
)

// PlatformLockProber detects Java's mandatory byte-range lock on Windows: a
// read of a locked file fails with a lock violation.
type PlatformLockProber struct{}

// Held reports whether another process holds the lock on the file at path. Java's
// lock is mandatory on Windows, so merely opening or reading the file fails while the
// game holds it; either failure counts as held. A missing file is not held.
func (PlatformLockProber) Held(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		// Opening can itself be refused while the game holds the file.
		return true, nil
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, nil
}
