package sync

import (
	"errors"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// The license gate lives in the Engine, so every write path honours it.
func TestAuthorizeGatesPushPullResolve(t *testing.T) {
	a, b, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	if _, err := a.e.Push(ctx, a.w); err != nil {
		t.Fatal(err)
	}
	b.e.Authorize = func(string) error { return license.ErrWorldLimit }
	if _, err := b.e.Pull(ctx, b.w); !errors.Is(err, license.ErrWorldLimit) {
		t.Fatalf("pull: %v", err)
	}
	if _, err := b.e.Push(ctx, b.w); !errors.Is(err, license.ErrWorldLimit) {
		t.Fatalf("push: %v", err)
	}
	if _, err := b.e.Resolve(ctx, b.w, KeepRemote); !errors.Is(err, license.ErrWorldLimit) {
		t.Fatalf("resolve: %v", err)
	}
	// Reading status stays allowed.
	if _, err := b.e.Status(ctx, b.w); err != nil {
		t.Fatal(err)
	}
	b.e.Authorize = nil
	if p, err := b.e.Pull(ctx, b.w); err != nil || !p.Updated {
		t.Fatal(p, err)
	}
}

func TestRunnerReportsLimitedWorlds(t *testing.T) {
	a, _, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	a.e.Authorize = func(string) error { return license.ErrWorldLimit }
	r := NewRunner("t", a.e, func() []worlds.World { return []worlds.World{a.w} })
	r.Limited = func(string) bool { return true }
	st := r.ForceSync(ctx)
	if want := "1 not synced (free version: 1 world)"; !strings.Contains(st.LastResult, want) {
		t.Fatalf("result %q", st.LastResult)
	}
	if wi := r.Worlds(ctx); !wi[0].Locked {
		t.Fatal("world should be marked locked")
	}
}
