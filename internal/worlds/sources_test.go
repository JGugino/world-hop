package worlds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkWorld(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "level.dat"), []byte("x"), 0o644)
}

func TestResolveMixedSources(t *testing.T) {
	base := t.TempDir()
	savesA := filepath.Join(base, "a", "saves")
	savesB := filepath.Join(base, "b", "saves")
	mkWorld(t, filepath.Join(savesA, "One"))
	mkWorld(t, filepath.Join(savesA, "Two"))
	mkWorld(t, filepath.Join(savesB, "Three"))
	single := filepath.Join(base, "elsewhere", "Solo")
	mkWorld(t, single)
	single2 := filepath.Join(base, "else2", "Solo2")
	mkWorld(t, single2)

	ws, problems := Resolve([]Source{
		{Kind: KindSaves, Path: savesA},
		{Kind: KindWorld, Path: single},
		{Kind: KindSaves, Path: savesB},
		{Kind: KindWorld, Path: single2},
		{Kind: KindWorld, Path: filepath.Join(savesA, "One")}, // duplicate of a discovered world
	})
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	got := map[string]bool{}
	for _, w := range ws {
		got[w.ID] = true
	}
	for _, id := range []string{"One", "Two", "Three", "Solo", "Solo2"} {
		if !got[id] {
			t.Errorf("missing %s in %v", id, ws)
		}
	}
	if len(ws) != 5 {
		t.Errorf("want 5 worlds, got %d", len(ws))
	}
}

func TestResolveProblems(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "x", "Same")
	b := filepath.Join(base, "y", "Same")
	mkWorld(t, a)
	mkWorld(t, b)
	notWorld := filepath.Join(base, "empty")
	os.MkdirAll(notWorld, 0o755)
	ws, problems := Resolve([]Source{
		{Kind: KindWorld, Path: a},
		{Kind: KindWorld, Path: b},
		{Kind: KindWorld, Path: notWorld},
		{Kind: KindSaves, Path: filepath.Join(base, "nope")},
		{Kind: "weird", Path: base},
	})
	if len(ws) != 1 || ws[0].Path != a {
		t.Fatalf("worlds: %+v", ws)
	}
	if len(problems) != 4 {
		t.Fatalf("problems: %v", problems)
	}
	if !strings.Contains(problems[0].Error(), "already used") {
		t.Fatalf("first problem: %v", problems[0])
	}
}
