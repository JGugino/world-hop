// Package manifest defines the small JSON document that describes one
// immutable version of a world: its files, their chunks and where each chunk
// lives in the store.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Version = 1

// LockFile is never synced.
const LockFile = "session.lock"

type ChunkLoc struct {
	Pack   string `json:"pack"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type File struct {
	Path   string   `json:"path"` // slash separated, relative to the world
	Size   int64    `json:"size"`
	Mode   uint32   `json:"mode"`
	MTime  int64    `json:"mtime"` // unix nanoseconds; informational
	Chunks []string `json:"chunks"`
}

type Manifest struct {
	Version    int                 `json:"version"`
	WorldID    string              `json:"worldId"`
	DeviceID   string              `json:"deviceId"`
	Generation int64               `json:"generation"`
	Parents    []string            `json:"parents"` // manifest names this one supersedes
	CreatedAt  time.Time           `json:"createdAt"`
	Files      []File              `json:"files"`
	Chunks     map[string]ChunkLoc `json:"chunks"`
}

var (
	idRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	hashRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	nameRe   = regexp.MustCompile(`^manifest-(\d{12})-([A-Za-z0-9]{1,64})\.json$`)
	deviceRe = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
)

// ValidID reports whether s is a legal world id.
func ValidID(s string) bool { return idRe.MatchString(s) && s != "." && s != ".." }

// ValidDeviceID reports whether s is a legal device id.
func ValidDeviceID(s string) bool { return deviceRe.MatchString(s) }

// FileName returns the unique store name for a manifest.
func FileName(generation int64, deviceID string) string {
	return fmt.Sprintf("manifest-%012d-%s.json", generation, deviceID)
}

// Name returns this manifest's store name.
func (m *Manifest) Name() string { return FileName(m.Generation, m.DeviceID) }

// ParseName extracts generation and device from a manifest name.
func ParseName(name string) (gen int64, device string, ok bool) {
	g := nameRe.FindStringSubmatch(name)
	if g == nil {
		return 0, "", false
	}
	fmt.Sscanf(g[1], "%d", &gen)
	return gen, g[2], true
}

func (m *Manifest) Marshal() ([]byte, error) { return json.MarshalIndent(m, "", " ") }

// SafePath reports whether p is a clean, relative, slash-separated path that
// is not the session lock. Used for both scanning and for validating
// manifests that arrive from the (untrusted) store.
func SafePath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return false
	}
	if path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	// Reject Windows drive-style paths.
	if len(p) >= 2 && p[1] == ':' {
		return false
	}
	return path.Base(p) != LockFile
}

// Parse decodes and fully validates a manifest.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks structural integrity.
func (m *Manifest) Validate() error {
	if m.Version != Version {
		return fmt.Errorf("manifest: unsupported version %d", m.Version)
	}
	if !ValidID(m.WorldID) {
		return errors.New("manifest: bad world id")
	}
	if !ValidDeviceID(m.DeviceID) {
		return errors.New("manifest: bad device id")
	}
	if m.Generation < 1 {
		return errors.New("manifest: bad generation")
	}
	for _, p := range m.Parents {
		if _, _, ok := ParseName(p); !ok {
			return fmt.Errorf("manifest: bad parent %q", p)
		}
	}
	for h, loc := range m.Chunks {
		if !hashRe.MatchString(h) || !hashRe.MatchString(loc.Pack) || loc.Offset < 0 || loc.Length < 0 || loc.Offset+loc.Length < loc.Offset {
			return fmt.Errorf("manifest: bad chunk entry %q", h)
		}
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !SafePath(f.Path) {
			return fmt.Errorf("manifest: unsafe path %q", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("manifest: duplicate path %q", f.Path)
		}
		seen[f.Path] = true
		var total int64
		for _, c := range f.Chunks {
			loc, ok := m.Chunks[c]
			if !ok {
				return fmt.Errorf("manifest: file %q references unknown chunk", f.Path)
			}
			total += loc.Length
		}
		if total != f.Size || f.Size < 0 {
			return fmt.Errorf("manifest: size mismatch for %q", f.Path)
		}
	}
	return nil
}

// TreeHash is a digest of the world content (paths, modes-independent, chunk
// hashes). Two worlds with equal TreeHash are byte-identical.
func TreeHash(files []File) string {
	fs := append([]File(nil), files...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
	h := sha256.New()
	for _, f := range fs {
		fmt.Fprintf(h, "%d:%s\n%d\n", len(f.Path), f.Path, f.Size)
		for _, c := range f.Chunks {
			h.Write([]byte(c))
		}
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Manifest) TreeHash() string { return TreeHash(m.Files) }
