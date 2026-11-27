// Package manifest defines the small JSON document that describes one
// immutable version of a world: its files, their chunks and where each chunk
// lives in the store.
//
// A world in the store is a set of manifests. Each is written once, named by a
// generation number and the device that wrote it ("manifest-<12
// digits>-<device>.json"),
// and lists its parents (the versions it supersedes). The "heads" (manifests no other
// manifest names as a parent) are the current versions; several heads mean a
// conflict. Because manifests come from an untrusted store, Parse validates
// everything about them before the rest of the program looks at them.
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

// Version is the manifest format version this build reads and writes.
const Version = 1

// LockFile is never synced.
const LockFile = "session.lock"

// ChunkLoc says where one chunk is stored: which pack, at what byte offset, and
// how long it is. The pack holds many chunks back to back.
type ChunkLoc struct {
	Pack   string `json:"pack"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

// File is one file of the world: its relative path, size, permission bits, last
// modification time and the ordered hashes of its chunks. Concatenating those chunks
// reproduces the file.
type File struct {
	Path   string   `json:"path"` // slash separated, relative to the world
	Size   int64    `json:"size"`
	Mode   uint32   `json:"mode"`
	MTime  int64    `json:"mtime"` // unix nanoseconds; informational
	Chunks []string `json:"chunks"`
}

// Manifest describes one version of a world. Files lists the world's files and
// Chunks maps every chunk hash used to where its bytes live. Generation is one more
// than the highest parent's, Parents are the names of the versions this one
// supersedes, and DeviceID is the random id of the computer that wrote it.
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

// ValidID reports whether s is a legal world id: 1 to 128 characters of letters,
// digits, dot, dash and underscore (never "." or ".."). World ids come from folder
// names and from the store, and are used to build paths, so they are strictly limited.
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

// Marshal encodes the manifest as indented JSON, the form stored in the cloud.
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

// Parse decodes a manifest and fully validates it, so callers can trust its shape.
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

// Validate checks structural integrity, because a manifest arrives from an
// untrusted store: a supported version, legal ids, a positive generation, parent names
// that are real manifest names, well-formed chunk entries, only safe unique file paths,
// and every file's chunks present with lengths that add up to its size.
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

// TreeHash is a digest of a world's content: each file's path, size and chunk
// hashes, in path order (permissions and times are not included). Two worlds with the
// same TreeHash are byte-identical. The engine compares it with the last-synced hash
// to tell whether local files changed.
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
