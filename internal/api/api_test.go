package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/JGugino/world-hopper/internal/sync"
)

func TestAPI(t *testing.T) {
	h := (&Server{svc: sync.NewRunner("t", nil, nil), assets: fstest.MapFS{"index.html": {Data: []byte("hi")}}}).Handler()

	get := func(method, path, host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get("GET", "/api/status", "127.0.0.1:1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"idle"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if w := get("POST", "/api/sync", "127.0.0.1:1"); w.Code != 200 || !strings.Contains(w.Body.String(), "No sync store configured") {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	if w := get("GET", "/some/route", "localhost:1"); w.Body.String() != "hi" {
		t.Fatalf("fallback: %s", w.Body)
	}
	if w := get("GET", "/api/status", "evil.com"); w.Code != http.StatusForbidden {
		t.Fatalf("host guard: %d", w.Code)
	}
}

func TestUpdateEndpoint(t *testing.T) {
	h := (&Server{svc: sync.NewRunner("t", nil, nil), assets: fstest.MapFS{"index.html": {Data: []byte("hi")}}}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1/api/update", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
