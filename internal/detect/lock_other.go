//go:build !linux && !darwin && !windows

package detect

// PlatformLockProber is unsupported here; Detector falls back to the
// process check.
type PlatformLockProber struct{}

func (PlatformLockProber) Held(string) (bool, error) { return false, ErrUnsupported }
