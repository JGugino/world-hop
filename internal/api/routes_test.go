package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/JGugino/world-hopper/internal/config"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/worlds"
)

func TestSettingsAPI(t *testing.T) {
	dir := t.TempDir()
	saves := filepath.Join(dir, "saves")
	wp := filepath.Join(saves, "W1")
	os.MkdirAll(wp, 0o755)
	os.WriteFile(filepath.Join(wp, "level.dat"), []byte("data"), 0o644)

	cfg, _, _ := config.Load(filepath.Join(dir, "cfg.json"))
	eng, err := sync.New(localdir.New(filepath.Join(dir, "shared")), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	svc := sync.NewRunner("t", eng, func() []worlds.World {
		ws, _ := worlds.Resolve(cfg.Sources())
		return ws
	})
	h := (&Server{svc: svc, deps: Deps{Sources: cfg}, assets: fstest.MapFS{"index.html": {Data: []byte("hi")}}}).Handler()

	do := func(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:1"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do("POST", "/api/sources", `{"kind":"saves","path":"`+saves+`"}`); w.Code != 200 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	} else if !strings.Contains(w.Body.String(), `"worlds":1`) {
		t.Fatalf("add body: %s", w.Body)
	}
	if w := do("POST", "/api/sources", `{"kind":"world","path":"`+saves+`"}`); w.Code != 400 {
		t.Fatalf("bad world add: %d", w.Code)
	}
	if w := do("POST", "/api/sources", `{"kind":"saves","path":"`+saves+`"}`, "Origin", "http://evil.example"); w.Code != 403 {
		t.Fatalf("cross-origin post: %d", w.Code)
	}

	var ws []sync.WorldInfo
	w := do("GET", "/api/worlds", "")
	json.Unmarshal(w.Body.Bytes(), &ws)
	if len(ws) != 1 || ws[0].Status != "local-ahead" {
		t.Fatalf("worlds: %s", w.Body)
	}
	if w := do("POST", "/api/worlds/W1/sync", ""); w.Code != 200 {
		t.Fatalf("sync world: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/worlds/nope/sync", ""); w.Code != 404 {
		t.Fatalf("unknown world: %d", w.Code)
	}
	if w := do("POST", "/api/worlds/W1/resolve", `{"strategy":"bogus"}`); w.Code != 400 {
		t.Fatalf("bad strategy: %d", w.Code)
	}

	if w := do("GET", "/api/worlds/W1/snapshots", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("snapshots: %d %s", w.Code, w.Body)
	}

	if w := do("GET", "/api/providers", ""); !strings.Contains(w.Body.String(), `"dropbox"`) {
		t.Fatalf("providers: %s", w.Body)
	}
	if w := do("POST", "/api/providers/gdrive/link", ""); w.Code != http.StatusNotImplemented {
		t.Fatalf("stub link: %d", w.Code)
	}
	if w := do("POST", "/api/providers/zzz/link", ""); w.Code != 404 {
		t.Fatalf("unknown provider: %d", w.Code)
	}

	if w := do("POST", "/api/sources/remove", `{"kind":"saves","path":"`+saves+`"}`); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
}
