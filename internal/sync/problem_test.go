package sync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/s3"
	"github.com/JGugino/world-hopper/internal/worlds"
)

func TestExplain(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind string
	}{
		{"open", fmt.Errorf("x: %w", ErrWorldOpen), ProblemWorldOpen},
		{"conflict", &ConflictError{WorldID: "a"}, ProblemConflict},
		{"limit", license.ErrWorldLimit, ProblemLimit},
		{"pro", license.ErrProRequired, ProblemPro},
		{"not linked", store.ErrNotLinked, ProblemNotLinked},
		{"reauth", fmt.Errorf("list: %w", oauth.ErrReauth), ProblemAuth},
		{"401", &httpx.StatusError{Code: 401}, ProblemAuth},
		{"drive quota", &httpx.StatusError{Code: 403, Body: `{"reason":"storageQuotaExceeded"}`}, ProblemQuota},
		{"drive rate", &httpx.StatusError{Code: 403, Body: `{"reason":"userRateLimitExceeded"}`}, ProblemBusy},
		{"503", &httpx.StatusError{Code: 503}, ProblemServer},
		{"s3 keys", &s3.Error{Status: 403, Code: "InvalidAccessKeyId"}, ProblemStorageSetup},
		{"s3 507", &s3.Error{Status: 507}, ProblemQuota},
		{"locked", store.ErrEncryptionLocked, ProblemLocked},
		{"wrong pass", store.ErrWrongPassphrase, ProblemKey},
		{"offline", &net.OpError{Op: "dial", Err: errors.New("no route")}, ProblemOffline},
		{"timeout", context.DeadlineExceeded, ProblemOffline},
		{"disk", fmt.Errorf("write: %w", syscall.ENOSPC), ProblemDisk},
		{"integrity", ErrIntegrity, ProblemDamaged},
		{"mystery", errors.New("boom"), ProblemUnknown},
	}
	for _, c := range cases {
		p := Explain(c.err)
		if p.Kind != c.kind || p.Title == "" {
			t.Errorf("%s: kind %q, want %q", c.name, p.Kind, c.kind)
		}
	}
	if p := Explain(errors.New("boom")); p.Detail != "boom" {
		t.Errorf("unknown errors keep their raw message in Detail, got %q", p.Detail)
	}
	if p := Explain(nil); p.Kind != "" || p.Title != "" {
		t.Error("nil must explain to nothing")
	}
}

// offlineStore makes every call fail, like a computer with no connection.
type offlineStore struct {
	store.Store
	err   error
	calls int
}

func (f *offlineStore) ListManifests(context.Context, string) ([]string, error) {
	f.calls++
	return nil, f.err
}

func TestPassStopsAtFirstGlobalProblem(t *testing.T) {
	a, _, _ := twoDevices(t)
	fixtureWorld(t, a.w.Path, 1)
	w2 := a.w
	w2.ID, w2.Name = "other", "Other"
	fs := &offlineStore{err: &net.OpError{Op: "dial", Err: errors.New("no route to host")}}
	a.e.Store = fs
	r := NewRunner("t", a.e, func() []worlds.World { return []worlds.World{a.w, w2} })
	st := r.ForceSync(context.Background())
	if fs.calls != 1 {
		t.Fatalf("offline should stop the pass after one world, store called %d times", fs.calls)
	}
	if st.Outcome != "error" || len(st.Problems) != 1 || st.Problems[0].Kind != ProblemOffline || len(st.Problems[0].Worlds) != 0 {
		t.Fatalf("status %+v", st)
	}
	if st.LastSync != nil {
		t.Fatal("a pass that only failed must not count as a sync")
	}
	if st.Progress != nil || st.State != "idle" {
		t.Fatalf("progress not cleared: %+v", st)
	}
}

func TestHeadline(t *testing.T) {
	now := time.Now()
	ago := now.Add(-5 * time.Minute)
	cases := []struct {
		st   RunnerStatus
		want string
	}{
		{RunnerStatus{State: "syncing", Progress: &Progress{World: "Survival", Action: "uploading", Done: 1, Total: 3}}, "WorldHop: Uploading Survival (2 of 3)"},
		{RunnerStatus{State: "idle", LastSync: &ago}, "WorldHop: Up to date · synced 5 min ago"},
		{RunnerStatus{State: "idle", LastSync: &ago, Problems: []Problem{{Kind: ProblemWorldOpen, Worlds: []string{"A"}}}}, "WorldHop: Waiting for Minecraft to close: A"},
		{RunnerStatus{State: "idle", LastSync: &ago, Problems: []Problem{{Kind: ProblemOffline, Title: "Can't reach the cloud"}}}, "WorldHop: Can't reach the cloud"},
		{RunnerStatus{State: "idle"}, "WorldHop: No sync yet"},
	}
	for _, c := range cases {
		if got := c.st.Headline(now); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
