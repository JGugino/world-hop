package license

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func newTest(t *testing.T, handler http.HandlerFunc) (*Manager, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.productID, m.endpoint = "prod", srv.URL
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	return m, &now
}

func ok(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if r.Form.Get("product_id") != "prod" || r.Form.Get("license_key") != "GOOD-KEY-1234" {
		w.WriteHeader(404)
		w.Write([]byte(`{"success":false}`))
		return
	}
	w.Write([]byte(`{"success":true,"purchase":{}}`))
}

func TestFreeTierOneWorld(t *testing.T) {
	m, _ := newTest(t, ok)
	if err := m.Authorize("a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Authorize("a"); err != nil {
		t.Fatal("the free world must keep working:", err)
	}
	if err := m.Authorize("b"); !errors.Is(err, ErrWorldLimit) {
		t.Fatalf("second world: %v", err)
	}
	// Survives restart.
	m2, _ := New(filepath.Dir(m.path))
	if err := m2.Authorize("b"); !errors.Is(err, ErrWorldLimit) {
		t.Fatalf("after reload: %v", err)
	}
	if err := m.SetFreeWorld("b"); err != nil {
		t.Fatal(err)
	}
	if err := m.Authorize("b"); err != nil {
		t.Fatal(err)
	}
}

func TestActivateUnlocksAndRejectsBadKey(t *testing.T) {
	m, _ := newTest(t, ok)
	m.Authorize("a")
	if err := m.Activate(context.Background(), "nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad key: %v", err)
	}
	if m.Pro() {
		t.Fatal("bad key unlocked Pro")
	}
	if err := m.Activate(context.Background(), " GOOD-KEY-1234 "); err != nil {
		t.Fatal(err)
	}
	if !m.Pro() || m.Authorize("b") != nil || m.Authorize("c") != nil {
		t.Fatal("Pro should allow every world")
	}
	if in := m.Info(); in.Tier != "pro" || in.Key != "GOOD…1234" {
		t.Fatalf("info %+v", in)
	}
	if err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if m.Authorize("b") == nil {
		t.Fatal("deactivated should be limited again")
	}
}

func TestNoProductIDMeansUnavailable(t *testing.T) {
	m, _ := newTest(t, ok)
	m.productID = ""
	if err := m.Activate(context.Background(), "GOOD-KEY-1234"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if m.Info().CanActivate {
		t.Fatal("CanActivate")
	}
}

func TestOfflineGraceThenLapse(t *testing.T) {
	down := false
	m, now := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if down {
			w.WriteHeader(503)
			return
		}
		ok(w, r)
	})
	if err := m.Activate(context.Background(), "GOOD-KEY-1234"); err != nil {
		t.Fatal(err)
	}
	down = true
	*now = now.Add(RecheckEvery + time.Hour)
	m.Refresh(context.Background())
	if !m.Pro() {
		t.Fatal("outage must not drop Pro inside the grace period")
	}
	if m.Info().GraceEnds == nil {
		t.Fatal("expected grace end to be shown")
	}
	*now = now.Add(OfflineGrace)
	if m.Pro() {
		t.Fatal("Pro should lapse after the grace period")
	}
	down = false
	m.Refresh(context.Background())
	if !m.Pro() {
		t.Fatal("a successful check restores Pro")
	}
}

func TestRefundDropsToFree(t *testing.T) {
	refunded := false
	m, now := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if refunded {
			w.Write([]byte(`{"success":true,"purchase":{"refunded":true}}`))
			return
		}
		ok(w, r)
	})
	m.Activate(context.Background(), "GOOD-KEY-1234")
	refunded = true
	*now = now.Add(RecheckEvery + time.Minute)
	m.Refresh(context.Background())
	if m.Pro() || m.Info().Error == "" {
		t.Fatalf("refund should drop Pro with a message: %+v", m.Info())
	}
}

func TestClockSetBackDoesNotExtendPro(t *testing.T) {
	m, now := newTest(t, ok)
	m.Activate(context.Background(), "GOOD-KEY-1234")
	*now = now.Add(-48 * time.Hour)
	if m.Pro() {
		t.Fatal("verification in the future should not count")
	}
}

func TestActivateOfflineAndInvalidAreDistinct(t *testing.T) {
	m, _ := newTest(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	if err := m.Activate(context.Background(), "GOOD-KEY-1234"); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("server down: %v", err)
	}
	m.endpoint = "http://127.0.0.1:1/" // nothing listening
	if err := m.Activate(context.Background(), "GOOD-KEY-1234"); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("offline: %v", err)
	}
	m, _ = newTest(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"success":false}`)) })
	if err := m.Activate(context.Background(), "BAD"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad key: %v", err)
	}
}
