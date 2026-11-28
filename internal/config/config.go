// Package config persists the user's settings (the list of sync sources and how
// many snapshots to keep in the cloud) in a small JSON file (config.json). The other state files
// (provider.json, license.json, encryption.json, ...) belong to the managers that own
// them; this one is the Saves tab's list of what to keep synced.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/JGugino/world-hopper/internal/worlds"
)

// Bounds of the "snapshots kept in the cloud" setting: how many of each world's
// newest versions cloud cleanup keeps. It is never lower than MinCloudSnapshots, so
// there is always something to go back to.
const (
	MinCloudSnapshots     = 2
	DefaultCloudSnapshots = 5
	MaxCloudSnapshots     = 50
)

// ErrCloudSnapshots: the value is outside MinCloudSnapshots..MaxCloudSnapshots.
var ErrCloudSnapshots = fmt.Errorf("keep between %d and %d snapshots", MinCloudSnapshots, MaxCloudSnapshots)

// file is the on-disk shape. CloudSnapshots 0 (an older file) means the default.
type file struct {
	Sources        []worlds.Source `json:"sources"`
	CloudSnapshots int             `json:"cloudSnapshots,omitempty"`
}

// Config is the settings file. It is safe for concurrent use.
type Config struct {
	path string

	mu             sync.Mutex
	sources        []worlds.Source
	cloudSnapshots int
}

// Load reads the config at path. A missing file is not an error: it returns
// an empty config and fresh reports true, so the caller can seed defaults.
func Load(path string) (c *Config, fresh bool, err error) {
	c = &Config{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	c.sources = f.Sources
	c.cloudSnapshots = f.CloudSnapshots
	return c, false, nil
}

// CloudSnapshots is how many versions of each world to keep in the cloud (the
// default when never set, and never below the minimum).
func (c *Config) CloudSnapshots() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cloudSnapshots < MinCloudSnapshots {
		return DefaultCloudSnapshots
	}
	return c.cloudSnapshots
}

// SetCloudSnapshots saves the number of versions to keep per world in the cloud.
// Values outside the allowed range are refused with ErrCloudSnapshots.
func (c *Config) SetCloudSnapshots(n int) error {
	if n < MinCloudSnapshots || n > MaxCloudSnapshots {
		return ErrCloudSnapshots
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.cloudSnapshots
	c.cloudSnapshots = n
	if err := c.saveLocked(); err != nil {
		c.cloudSnapshots = old
		return err
	}
	return nil
}

// Sources returns a copy of the configured sources.
func (c *Config) Sources() []worlds.Source {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]worlds.Source(nil), c.sources...)
}

// Normalize checks a source the user typed and returns it cleaned up: "~" is
// expanded, the path made absolute, and the kind verified against the disk.
func Normalize(s worlds.Source) (worlds.Source, error) {
	p := strings.TrimSpace(s.Path)
	if p == "" {
		return s, errors.New("enter a folder path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return s, err
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return s, errors.New("the path must be absolute, e.g. C:\\Users\\you\\AppData\\Roaming\\.minecraft\\saves")
	}
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	if err != nil || !st.IsDir() {
		return s, fmt.Errorf("%s is not a folder on this computer", p)
	}
	switch s.Kind {
	case worlds.KindWorld:
		if !worlds.IsWorld(p) {
			return s, fmt.Errorf("%s is not a world folder (no level.dat). Add it as a saves folder if it holds several worlds", p)
		}
	case worlds.KindSaves:
	default:
		return s, fmt.Errorf("unknown kind %q", s.Kind)
	}
	s.Path = p
	return s, nil
}

// Add validates and stores a source. Adding one already present is a no-op.
func (c *Config) Add(s worlds.Source) (worlds.Source, error) {
	s, err := Normalize(s)
	if err != nil {
		return s, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.sources {
		if e.Path == s.Path {
			if e.Kind == s.Kind {
				return s, nil
			}
			return s, fmt.Errorf("%s is already added as a %s", s.Path, e.Kind)
		}
	}
	c.sources = append(c.sources, s)
	if err := c.saveLocked(); err != nil {
		c.sources = c.sources[:len(c.sources)-1]
		return s, err
	}
	return s, nil
}

// Remove drops a source. Nothing on disk is touched.
func (c *Config) Remove(kind, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.sources
	var kept []worlds.Source
	for _, e := range old {
		if e.Kind == kind && e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(old) {
		return errors.New("that source is not in the list")
	}
	c.sources = kept
	if err := c.saveLocked(); err != nil {
		c.sources = old
		return err
	}
	return nil
}

// Seed adds sources without validation (used for detected launcher folders and
// command-line flags), skipping duplicates, and saves if anything changed.
func (c *Config) Seed(src []worlds.Source) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := false
outer:
	for _, s := range src {
		s.Path = filepath.Clean(s.Path)
		for _, e := range c.sources {
			if e.Path == s.Path {
				continue outer
			}
		}
		c.sources = append(c.sources, s)
		changed = true
	}
	if !changed {
		return nil
	}
	return c.saveLocked()
}

// saveLocked writes the config atomically (temp file and rename). The caller holds
// c.mu.
func (c *Config) saveLocked() error {
	b, err := json.MarshalIndent(file{Sources: c.sources, CloudSnapshots: c.cloudSnapshots}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
