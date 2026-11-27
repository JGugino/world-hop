package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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

func TestChangePassphraseAPI(t *testing.T) {
	dir := t.TempDir()
	m, err := crypt.NewManager(localdir.New(filepath.Join(dir, "shared")), dir, nil, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	m.Params = crypt.Params{Time: 1, MemKiB: 64, Threads: 1}
	eng, _ := sync.New(m, filepath.Join(dir, "state"))
	svc := sync.NewRunner("t", eng, func() []worlds.World { return nil })
	h := (&Server{svc: svc, deps: Deps{Encryption: m}, assets: fstest.MapFS{"index.html": {Data: []byte("hi")}}}).Handler()
	do := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Host = "127.0.0.1:1"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const change = "/api/encryption/change-passphrase"
	body := func(cur, next, confirm string, ack bool) string {
		return `{"current":"` + cur + `","passphrase":"` + next + `","confirm":"` + confirm + `","acknowledged":` + map[bool]string{true: "true", false: "false"}[ack] + `}`
	}
	if w := do(change, body("correct horse battery", "another long passphrase", "another long passphrase", true)); w.Code != 400 {
		t.Fatalf("not enabled: %d %s", w.Code, w.Body)
	}
	do("/api/encryption/enable", `{"passphrase":"correct horse battery","confirm":"correct horse battery","acknowledged":true}`)
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"wrong current":  {body("wrong wrong wrong", "another long passphrase", "another long passphrase", true), 401},
		"mismatch":       {body("correct horse battery", "another long passphrase", "typo typo typo", true), 400},
		"unacknowledged": {body("correct horse battery", "another long passphrase", "another long passphrase", false), 400},
		"same":           {body("correct horse battery", "correct horse battery", "correct horse battery", true), 400},
		"weak":           {body("correct horse battery", "short", "short", true), 400},
	} {
		if w := do(change, tc.body); w.Code != tc.code {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, tc.code)
		}
	}
	w := do(change, body("correct horse battery", "another long passphrase", "another long passphrase", true))
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"rekey"`) {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	m.WaitChangePassphrase(context.Background())
	if w := do("/api/encryption/change-passphrase/dismiss", ""); w.Code != 200 || strings.Contains(w.Body.String(), `"rekey"`) {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	if w := do("/api/encryption/lock", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := do("/api/encryption/unlock", `{"passphrase":"correct horse battery"}`); w.Code != 401 {
		t.Fatalf("old passphrase after the change: %d", w.Code)
	}
	if w := do("/api/encryption/unlock", `{"passphrase":"another long passphrase"}`); w.Code != 200 {
		t.Fatalf("new passphrase: %d %s", w.Code, w.Body)
	}
}

func TestKeyRollbackAPI(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	m, err := crypt.NewManager(localdir.New(shared), dir, nil, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	m.Params = crypt.Params{Time: 1, MemKiB: 64, Threads: 1}
	now := time.Now()
	m.Now = func() time.Time { return now }
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
	do("POST", "/api/encryption/enable", `{"passphrase":"correct horse battery","confirm":"correct horse battery","acknowledged":true}`)
	hdr := filepath.Join(shared, "worlds", "_worldhop-key", "manifest-000000000001-key.json")
	os.Remove(hdr)
	now = now.Add(time.Hour)
	if _, err := m.ListWorlds(context.Background()); err == nil {
		t.Fatal("rollback not noticed")
	}
	if w := do("GET", "/api/encryption", ""); !strings.Contains(w.Body.String(), `"keyRollback":true`) {
		t.Fatalf("info: %s", w.Body)
	}
	if w := do("POST", "/api/encryption/unlock", `{"passphrase":"correct horse battery"}`); w.Code != 423 {
		t.Fatalf("unlock while rolled back: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/encryption/restore-key", ""); w.Code != 200 || strings.Contains(w.Body.String(), `"keyRollback":true`) {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(hdr); err != nil {
		t.Fatal("the header was not put back")
	}
	os.Remove(hdr)
	now = now.Add(time.Hour)
	m.ListWorlds(context.Background())
	if w := do("POST", "/api/encryption/accept-key-rollback", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("accept: %d %s", w.Code, w.Body)
	}
}
