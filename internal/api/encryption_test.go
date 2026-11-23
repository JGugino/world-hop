package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/JGugino/world-hopper/internal/crypt"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/worlds"
)

func TestEncryptionAPI(t *testing.T) {
	dir := t.TempDir()
	pro := false
	m, err := crypt.NewManager(localdir.New(filepath.Join(dir, "shared")), dir, nil, func() bool { return pro })
	if err != nil {
		t.Fatal(err)
	}
	m.Params = crypt.Params{Time: 1, MemKiB: 64, Threads: 1}
	eng, _ := sync.New(m, filepath.Join(dir, "state"))
	svc := sync.NewRunner("t", eng, func() []worlds.World { return nil })
	h := (&Server{svc: svc, deps: Deps{Encryption: m}, assets: fstest.MapFS{"index.html": {Data: []byte("hi")}}}).Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:1"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const good = `{"passphrase":"correct horse battery","confirm":"correct horse battery","acknowledged":true}`
	if w := do("GET", "/api/encryption", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"keyInStore":false`) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/encryption/enable", good); w.Code != 402 {
		t.Fatalf("free user: %d %s", w.Code, w.Body)
	}
	pro = true
	if w := do("POST", "/api/encryption/enable", `{"passphrase":"correct horse battery","confirm":"nope","acknowledged":true}`); w.Code != 400 {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/encryption/enable", good); w.Code != 200 || !strings.Contains(w.Body.String(), `"unlocked":true`) {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/encryption/lock", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"unlocked":false`) {
		t.Fatalf("lock: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/encryption/unlock", `{"passphrase":"wrong wrong wrong"}`); w.Code != 401 {
		t.Fatalf("wrong passphrase: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/encryption/unlock", `{"passphrase":"correct horse battery"}`); w.Code != 200 {
		t.Fatalf("unlock: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/encryption/disable", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
}
