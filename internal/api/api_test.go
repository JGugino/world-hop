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
	h := (&Server{svc: sync.New("t"), assets: fstest.MapFS{"index.html": {Data: []byte("hi")}}}).Handler()

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
	if w := get("POST", "/api/sync", "127.0.0.1:1"); w.Code != 200 || !strings.Contains(w.Body.String(), "Stub sync") {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	if w := get("GET", "/some/route", "localhost:1"); w.Body.String() != "hi" {
		t.Fatalf("fallback: %s", w.Body)
	}
	if w := get("GET", "/api/status", "evil.com"); w.Code != http.StatusForbidden {
		t.Fatalf("host guard: %d", w.Code)
	}
}
