package worlds

import (
	"os"
	"path/filepath"
	"testing"
)

func mk(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscover(t *testing.T) {
	home := t.TempDir()
	saves := mk(t, home, ".minecraft", "saves")
	for _, n := range []string{"Survival 1", "Creative"} {
		w := mk(t, saves, n)
		os.WriteFile(filepath.Join(w, "level.dat"), []byte("x"), 0o644)
	}
	mk(t, saves, "not-a-world")
	prism := mk(t, home, ".local", "share", "PrismLauncher", "instances", "Pack", ".minecraft", "saves", "Modded")
	os.WriteFile(filepath.Join(prism, "level.dat"), []byte("x"), 0o644)
	mk(t, saves, ".worldhopper-old-Creative") // leftover from interrupted swap
	os.WriteFile(filepath.Join(saves, ".worldhopper-old-Creative", "level.dat"), []byte("x"), 0o644)

	got := Discover(DefaultRoots("linux", home, ""))
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	ids := map[string]string{}
	for _, w := range got {
		ids[w.ID] = w.Launcher
	}
	if ids["Survival-1"] != "vanilla" || ids["Modded"] != "prism" || ids["Creative"] != "vanilla" {
		t.Fatalf("%v", ids)
	}
}

func TestIDFromName(t *testing.T) {
	if IDFromName("../My World!") != "My-World" || IDFromName("???") != "world" {
		t.Fatal(IDFromName("../My World!"))
	}
}
