//go:build windows

package detect

import (
	"errors"
	"io"
	"os"
)

// PlatformLockProber detects Java's mandatory byte-range lock on Windows: a
// read of a locked file fails with a lock violation.
type PlatformLockProber struct{}

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
