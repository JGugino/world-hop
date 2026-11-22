package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JGugino/world-hopper/internal/worlds"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	saves := filepath.Join(dir, "saves")
	world := filepath.Join(dir, "Solo")
	os.MkdirAll(saves, 0o755)
	os.MkdirAll(world, 0o755)
	os.WriteFile(filepath.Join(world, "level.dat"), []byte("x"), 0o644)
	path := filepath.Join(dir, "cfg", "config.json")

	c, fresh, err := Load(path)
	if err != nil || !fresh {
		t.Fatalf("load: %v fresh=%v", err, fresh)
	}
	if _, err := c.Add(worlds.Source{Kind: worlds.KindSaves, Path: saves}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(worlds.Source{Kind: worlds.KindWorld, Path: world}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(worlds.Source{Kind: worlds.KindSaves, Path: saves}); err != nil {
		t.Fatalf("duplicate add should be a no-op: %v", err)
	}
	if _, err := c.Add(worlds.Source{Kind: worlds.KindWorld, Path: saves}); err == nil {
		t.Fatal("a folder without level.dat must not be accepted as a world")
	}
	if _, err := c.Add(worlds.Source{Kind: worlds.KindSaves, Path: "relative/dir"}); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := c.Add(worlds.Source{Kind: worlds.KindSaves, Path: filepath.Join(dir, "nope")}); err == nil {
		t.Fatal("missing folder accepted")
	}

	c2, fresh, err := Load(path)
	if err != nil || fresh || len(c2.Sources()) != 2 {
		t.Fatalf("reload: %v fresh=%v %v", err, fresh, c2.Sources())
	}
	if err := c2.Remove(worlds.KindSaves, saves); err != nil {
		t.Fatal(err)
	}
	if err := c2.Remove(worlds.KindSaves, saves); err == nil {
		t.Fatal("removing twice should fail")
	}
	c3, _, _ := Load(path)
	if s := c3.Sources(); len(s) != 1 || s[0].Kind != worlds.KindWorld {
		t.Fatalf("after remove: %v", s)
	}
}
