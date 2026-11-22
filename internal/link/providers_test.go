package link

import (
	"context"
	"errors"
	"testing"

	"github.com/JGugino/world-hopper/internal/api"
)

func TestAPIAdapter(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Config{GoogleClientID: "gid"})
	a := API{M: e.m}

	l := a.List(ctx)
	if len(l) != 1 || l[0].ID != GoogleDrive || !l[0].Available {
		t.Fatalf("list = %+v", l)
	}
	unconfigured := API{M: newEnv(t, Config{}).m}
	if _, err := unconfigured.Link(ctx, GoogleDrive); !errors.Is(err, api.ErrProviderUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := a.Link(ctx, "nope"); !errors.Is(err, api.ErrUnknownProvider) {
		t.Fatalf("err = %v", err)
	}
	start, err := a.Link(ctx, GoogleDrive)
	if err != nil || start.URL != "" { // system browser was opened
		t.Fatalf("link: %+v %v", start, err)
	}
	e.waitState(t, StateLinked)
	if p := a.List(ctx)[0]; p.ID != GoogleDrive || !p.Linked || p.Pending {
		t.Fatalf("after link: %+v", p)
	}
	e.revoked = true
	e.m.ListManifests(ctx, "W")
	if p := a.List(ctx)[0]; p.Linked || p.Error == "" {
		t.Fatalf("revoked: %+v", p)
	}
	if err := a.Unlink(ctx, GoogleDrive); err != nil {
		t.Fatal(err)
	}
	if err := a.Unlink(ctx, "nope"); !errors.Is(err, api.ErrUnknownProvider) {
		t.Fatal(err)
	}
}
