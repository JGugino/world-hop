package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/worlds"
)

func TestRunnerForceSync(t *testing.T) {
	dir := t.TempDir()
	wp := filepath.Join(dir, "saves", "W1")
	os.MkdirAll(wp, 0o755)
	os.WriteFile(filepath.Join(wp, "level.dat"), []byte("data"), 0o644)
	e, err := New(localdir.New(filepath.Join(dir, "shared")), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	list := func() []worlds.World {
		return worlds.Discover([]worlds.Root{{Launcher: "t", Path: filepath.Join(dir, "saves")}})
	}
	r := NewRunner("t", e, list)
	st := r.ForceSync(context.Background())
	if st.State != "idle" || st.LastSync == nil || !strings.Contains(st.LastResult, "1 uploaded") {
		t.Fatalf("first sync: %+v", st)
	}
	if e.CleanupDue() {
		t.Fatal("a good full pass should have run cloud cleanup and noted it")
	}
	st = r.ForceSync(context.Background())
	if !strings.Contains(st.LastResult, "1 up to date") {
		t.Fatalf("second sync: %+v", st)
	}
}
