// require.go: the Pro gate for features other than syncing worlds (S3 storage, turning
// encryption on). Syncing itself is gated by Manager.Authorize in license.go.

package license

import "errors"

// ErrProRequired is returned when a Pro-only feature is used without Pro.
var ErrProRequired = errors.New("this feature needs a WorldHop Pro license")

// Checker reports whether Pro features are unlocked. *Manager implements it.
type Checker interface {
	Pro() bool
}

var _ Checker = (*Manager)(nil)

// Require returns ErrProRequired unless c reports Pro. A nil Checker means no
// license is wired in, which counts as not Pro.
func Require(c Checker) error {
	if c == nil || !c.Pro() {
		return ErrProRequired
	}
	return nil
}
