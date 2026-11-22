package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JGugino/world-hopper/internal/link"
)

type fakeCloud struct {
	chosen, linked string
	calls          []string
}

func (f *fakeCloud) Status() link.Status { return link.Status{Chosen: f.chosen, State: link.StateNone} }
func (f *fakeCloud) Choose(p string) error {
	if p != "gdrive" {
		return link.ErrUnknownProvider
	}
	f.chosen = p
	return nil
}
func (f *fakeCloud) StartLink(p string) (string, error) {
	if p == "dropbox" {
		return "", link.ErrNotConfigured
	}
	if p == "boom" {
		return "", errors.New("boom")
	}
	f.calls = append(f.calls, "link:"+p)
	return "https://consent.example/x", nil
}
func (f *fakeCloud) CancelLink()           { f.calls = append(f.calls, "cancel") }
func (f *fakeCloud) Unlink(p string) error { f.calls = append(f.calls, "unlink:"+p); return nil }

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCloudEndpoints(t *testing.T) {
	fc := &fakeCloud{}
	s := &Server{Cloud: fc}
	h := s.Handler()

	if w := do(h, "GET", "/api/cloud", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"none"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/cloud/choose", `{"provider":"gdrive"}`, nil); w.Code != 200 || fc.chosen != "gdrive" {
		t.Fatalf("choose: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/cloud/choose", `{"provider":"x"}`, nil); w.Code != 400 {
		t.Fatalf("unknown: %d", w.Code)
	}
	w := do(h, "POST", "/api/cloud/link", `{"provider":"gdrive"}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"url":"https://consent.example/x"`) {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/cloud/link", `{"provider":"dropbox"}`, nil); w.Code != 409 {
		t.Fatalf("not configured: %d", w.Code)
	}
	if w := do(h, "POST", "/api/cloud/link", `{"provider":"boom"}`, nil); w.Code != 500 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("500: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/cloud/cancel", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := do(h, "POST", "/api/cloud/unlink", `{"provider":"gdrive"}`, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if got := strings.Join(fc.calls, ","); got != "link:gdrive,cancel,unlink:gdrive" {
		t.Fatalf("calls = %s", got)
	}
	if w := do(h, "POST", "/api/cloud/choose", `nope`, nil); w.Code != 400 {
		t.Fatalf("bad body: %d", w.Code)
	}
}

func TestCloudRejectsCrossSiteAndFormPosts(t *testing.T) {
	fc := &fakeCloud{}
	h := (&Server{Cloud: fc}).Handler()
	if w := do(h, "POST", "/api/cloud/unlink", `{"provider":"gdrive"}`, map[string]string{"Origin": "https://evil.example"}); w.Code != 403 {
		t.Fatalf("cross-origin: %d", w.Code)
	}
	if w := do(h, "POST", "/api/cloud/unlink", `provider=gdrive`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); w.Code != 403 {
		t.Fatalf("form post: %d", w.Code)
	}
	if w := do(h, "POST", "/api/cloud/unlink", `{"provider":"gdrive"}`, map[string]string{"Origin": "http://127.0.0.1:1234"}); w.Code != 200 {
		t.Fatalf("same origin: %d", w.Code)
	}
	if len(fc.calls) != 1 {
		t.Fatalf("calls = %v", fc.calls)
	}
}

func TestCloudUnavailableWithoutManager(t *testing.T) {
	h := (&Server{}).Handler()
	if w := do(h, "GET", "/api/cloud", "", nil); w.Code != 503 {
		t.Fatalf("code = %d", w.Code)
	}
}
