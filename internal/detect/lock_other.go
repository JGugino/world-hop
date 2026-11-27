//go:build !linux && !darwin && !windows

// lock_other.go: platforms with no way to probe the lock; the detector uses the process
// check instead.

package detect

// PlatformLockProber is unsupported here; Detector falls back to the
// process check.
type PlatformLockProber struct{}

// Held always reports ErrUnsupported on this platform.
func (PlatformLockProber) Held(string) (bool, error) { return false, ErrUnsupported }
