package worlds

import (
	"fmt"
	"os"
	"path/filepath"
)

// Source kinds.
const (
	// KindSaves is a folder that contains world folders; every world in it
	// is synced, including worlds created later.
	KindSaves = "saves"
	// KindWorld is a single world folder.
	KindWorld = "world"
)

// Source is something the user asked to keep synced. A configuration holds
// a list of these, any mix of saves folders and single worlds.
type Source struct {
	Kind     string `json:"kind"` // KindSaves or KindWorld
	Path     string `json:"path"`
	Launcher string `json:"launcher,omitempty"` // label only
}

// DefaultSources turns the known launcher locations into saves-folder sources.
func DefaultSources(goos, home, appData string) []Source {
	var out []Source
	for _, r := range DefaultRoots(goos, home, appData) {
		out = append(out, Source{Kind: KindSaves, Path: r.Path, Launcher: r.Launcher})
	}
	return out
}

// Resolve expands sources into the worlds to sync, de-duplicated by path.
// Problems (a missing folder, something that is not a world, two worlds that
// would share an id) are returned rather than failing the whole list; the
// first world with an id wins.
func Resolve(sources []Source) ([]World, []error) {
	var (
		out      []World
		problems []error
		seenPath = map[string]bool{}
		byID     = map[string]string{}
	)
	add := func(w World) {
		if seenPath[w.Path] {
			return
		}
		if other, ok := byID[w.ID]; ok {
			problems = append(problems, fmt.Errorf("%s: world id %q already used by %s", w.Path, w.ID, other))
			return
		}
		seenPath[w.Path] = true
		byID[w.ID] = w.Path
		out = append(out, w)
	}
	for _, s := range sources {
		switch s.Kind {
		case KindSaves:
			if st, err := os.Stat(s.Path); err != nil || !st.IsDir() {
				problems = append(problems, fmt.Errorf("%s: saves folder not found", s.Path))
				continue
			}
			for _, w := range Discover([]Root{{Launcher: s.Launcher, Path: s.Path}}) {
				add(w)
			}
		case KindWorld:
			p := filepath.Clean(s.Path)
			if !IsWorld(p) {
				problems = append(problems, fmt.Errorf("%s: not a world folder (no level.dat)", s.Path))
				continue
			}
			name := filepath.Base(p)
			add(World{ID: IDFromName(name), Name: name, Path: p, Launcher: s.Launcher})
		default:
			problems = append(problems, fmt.Errorf("%s: unknown source kind %q", s.Path, s.Kind))
		}
	}
	return out, problems
}
