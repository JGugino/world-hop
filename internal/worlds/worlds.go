// Package worlds finds Minecraft Java worlds on disk.
//
// A world is any folder that contains a level.dat file. Where to look comes from
// sources (sources.go): saves folders the user added (every world inside, including
// ones created later) and single world folders. Launchers keep saves in different
// places, so DefaultRoots knows the usual layouts (vanilla, Prism, MultiMC,
// CurseForge, Modrinth) on Windows, macOS and Linux.
//
// The world's id (IDFromName) is its folder name made safe for the store. Computers
// match worlds across machines by this id, which is why the folder name has to be
// the same everywhere.
package worlds

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// World is a discovered world folder (contains level.dat).
type World struct {
	ID       string // stable id used in the store; derived from the folder name
	Name     string // folder name
	Path     string
	Launcher string
}

// Root is a directory that directly contains world folders.
type Root struct {
	Launcher string
	Path     string
}

// badID matches the characters not allowed in a world id; each run becomes a dash.
var badID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// IDFromName turns a folder name into a store-safe world id. Devices that
// use the same folder name get the same id.
func IDFromName(name string) string {
	id := strings.Trim(badID.ReplaceAllString(name, "-"), "-.")
	if id == "" {
		id = "world"
	}
	if len(id) > 128 {
		id = id[:128]
	}
	return id
}

// IsWorld reports whether dir looks like a world.
func IsWorld(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "level.dat"))
	return err == nil && st.Mode().IsRegular()
}

// Discover lists worlds under the given roots, de-duplicated by path.
func Discover(roots []Root) []World {
	seen := map[string]bool{}
	var out []World
	for _, r := range roots {
		ents, err := os.ReadDir(r.Path)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(r.Path, e.Name())
			if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if strings.HasPrefix(e.Name(), ".worldhopper-") || !IsWorld(p) || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, World{ID: IDFromName(e.Name()), Name: e.Name(), Path: p, Launcher: r.Launcher})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// DefaultRoots expands the known launcher layouts for the given platform
// into saves directories that exist. appData is %APPDATA% on Windows and
// is ignored elsewhere.
func DefaultRoots(goos, home, appData string) []Root {
	var dataDir string // where Qt/Electron style launchers keep data
	var vanilla string
	switch goos {
	case "windows":
		dataDir = appData
		vanilla = filepath.Join(appData, ".minecraft")
	case "darwin":
		dataDir = filepath.Join(home, "Library", "Application Support")
		vanilla = filepath.Join(dataDir, "minecraft")
	default:
		dataDir = filepath.Join(home, ".local", "share")
		vanilla = filepath.Join(home, ".minecraft")
	}
	type pat struct{ launcher, glob string }
	pats := []pat{
		{"vanilla", filepath.Join(vanilla, "saves")},
		{"prism", filepath.Join(dataDir, "PrismLauncher", "instances", "*", ".minecraft", "saves")},
		{"prism", filepath.Join(dataDir, "PrismLauncher", "instances", "*", "minecraft", "saves")},
		{"multimc", filepath.Join(dataDir, "multimc", "instances", "*", ".minecraft", "saves")},
		{"multimc", filepath.Join(dataDir, "multimc", "instances", "*", "minecraft", "saves")},
		{"curseforge", filepath.Join(home, "curseforge", "minecraft", "Instances", "*", "saves")},
		{"modrinth", filepath.Join(dataDir, "ModrinthApp", "profiles", "*", "saves")},
	}
	var roots []Root
	for _, p := range pats {
		matches, _ := filepath.Glob(p.glob)
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.IsDir() {
				roots = append(roots, Root{Launcher: p.launcher, Path: m})
			}
		}
	}
	return roots
}
