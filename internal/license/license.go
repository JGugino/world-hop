// Package license answers one question: does this computer have Pro? Features
// ask a Checker instead of knowing how licences are verified, so the real key
// check (plan.md step 6) can be dropped in without touching them.
package license

import (
	"errors"
	"os"
)

// ErrProRequired is returned when a Pro feature is used without a Pro licence.
var ErrProRequired = errors.New("this feature needs a WorldHop Pro licence")

// Checker reports whether Pro features are unlocked.
type Checker interface {
	Pro() bool
}

// Func adapts a function to a Checker.
type Func func() bool

func (f Func) Pro() bool { return f() }

// Static is a fixed answer, for tests.
type Static bool

func (s Static) Pro() bool { return bool(s) }

// FromEnv is a development stand-in until licence-key verification exists:
// Pro is on only when WORLDHOP_PRO=1. It is not a security boundary.
func FromEnv() Checker {
	return Func(func() bool { return os.Getenv("WORLDHOP_PRO") == "1" })
}

// Require returns ErrProRequired unless c reports Pro. A nil Checker means no
// licence module is wired in, which is treated as not Pro.
func Require(c Checker) error {
	if c == nil || !c.Pro() {
		return ErrProRequired
	}
	return nil
}
