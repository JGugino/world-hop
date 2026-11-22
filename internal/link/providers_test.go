package link

import (
	"context"
	"errors"
	"testing"

	"github.com/JGugino/world-hopper/internal/api"
)

func TestAPIAdapter(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Config{GoogleClientID: "gid"}) // Dropbox not configured
	a := API{M: e.m}

	l := a.List(ctx)
	if len(l) != 2 || !l[0].Available && !l[1].Available {
		t.Fatalf("list = %+v", l)
	}
	byID := map[string]api.Provider{l[0].ID: l[0], l[1].ID: l[1]}
	if !byID[GoogleDrive].Available || byID[Dropbox].Available {
		t.Fatalf("availability: %+v", byID)
	}
	if _, err := a.Link(ctx, Dropbox); !errors.Is(err, api.ErrProviderUnavailable) {
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
