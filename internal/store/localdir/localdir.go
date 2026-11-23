// Package localdir is a Store backed by a plain directory (tests, and users
// who sync a folder with Syncthing or similar).
package localdir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

type Dir struct{ Root string }

var _ store.Store = (*Dir)(nil)

var (
	packRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	nameRe = regexp.MustCompile(`^manifest-\d{12}-[A-Za-z0-9]{1,64}\.json$`)
)

func New(root string) *Dir { return &Dir{Root: root} }

func (d *Dir) packPath(name string) (string, error) {
	if !packRe.MatchString(name) {
		return "", fmt.Errorf("localdir: bad pack name %q", name)
	}
	return filepath.Join(d.Root, "packs", name), nil
}

func (d *Dir) manifestPath(world, name string) (string, error) {
	if !manifest.ValidID(world) || !nameRe.MatchString(name) {
		return "", fmt.Errorf("localdir: bad manifest name %q/%q", world, name)
	}
	return filepath.Join(d.Root, "worlds", world, name), nil
}

// writeTemp writes data durably to a temp file next to final.
func writeTemp(final string, data []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(final), ".tmp-*")
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (d *Dir) PutPack(_ context.Context, name string, data []byte) error {
	p, err := d.packPath(name)
	if err != nil {
		return err
	}
	if st, err := os.Stat(p); err == nil && st.Size() == int64(len(data)) {
		return nil
	}
	tmp, err := writeTemp(p, data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (d *Dir) GetPack(_ context.Context, name string) ([]byte, error) {
	p, err := d.packPath(name)
	if err != nil {
		return nil, err
	}
	return readFile(p)
}

func (d *Dir) PutManifest(_ context.Context, world, name string, data []byte) error {
	p, err := d.manifestPath(world, name)
	if err != nil {
		return err
	}
	tmp, err := writeTemp(p, data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Hard link fails if the target exists, giving us write-once semantics.
	if err := os.Link(tmp, p); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return store.ErrExists
		}
		return err
	}
	return nil
}

func (d *Dir) GetManifest(_ context.Context, world, name string) ([]byte, error) {
	p, err := d.manifestPath(world, name)
	if err != nil {
		return nil, err
	}
	return readFile(p)
}

func (d *Dir) ListManifests(_ context.Context, world string) ([]string, error) {
	if !manifest.ValidID(world) {
		return nil, fmt.Errorf("localdir: bad world id %q", world)
	}
	ents, err := os.ReadDir(filepath.Join(d.Root, "worlds", world))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && nameRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d *Dir) ListWorlds(_ context.Context) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(d.Root, "worlds"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || !manifest.ValidID(e.Name()) {
			continue
		}
		if names, err := d.ListManifests(context.Background(), e.Name()); err == nil && len(names) > 0 {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func readFile(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, store.ErrNotFound
	}
	return b, err
}
