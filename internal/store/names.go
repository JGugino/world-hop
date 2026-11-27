// names.go: the shapes of legal pack and manifest names. Every backend validates names
// with these before touching storage, so a name from the network or a tampered listing
// can never become a path outside the store.

// names.go: the shapes of legal pack and manifest names. Every backend validates names
// with these before touching storage, so a name from the network or a tampered listing
// can never become a path outside the store.

package store

import "regexp"

var (
	packRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	manifestRe = regexp.MustCompile(`^manifest-\d{12}-[A-Za-z0-9]{1,64}\.json$`)
)

// ValidPackName reports whether s is a legal pack name (a SHA-256 hex digest).
func ValidPackName(s string) bool { return packRe.MatchString(s) }

// ValidManifestName reports whether s is a legal manifest file name.
func ValidManifestName(s string) bool { return manifestRe.MatchString(s) }
