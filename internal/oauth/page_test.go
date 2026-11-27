package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func startFlow(t *testing.T, p *fakeProvider) (*Pending, string) {
	t.Helper()
	f := &Flow{Name: "Google Drive", Config: p.cfg(), HTTP: p.Client()}
	pend, err := f.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pend.srv.Close() })
	// Visit the consent URL without following its redirect, so the fake provider
	// learns this attempt's challenge and redirect (as the real one would).
	c := *p.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if resp, err := c.Get(pend.AuthURL); err == nil {
		resp.Body.Close()
	}
	u, _ := url.Parse(pend.AuthURL)
	return pend, u.Query().Get("state")
}

func get(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func outcomeOf(t *testing.T, pend *Pending, state string) Outcome {
	t.Helper()
	code, body := get(t, pend.redirect[:strings.LastIndex(pend.redirect, "/")]+"/callback/result?state="+url.QueryEscape(state))
	if code != 200 {
		t.Fatalf("result status %d: %s", code, body)
	}
	var o Outcome
	if err := json.Unmarshal([]byte(body), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestSuccessPageWaitsForSaved(t *testing.T) {
	p := newFakeProvider(t)
	pend, state := startFlow(t, p)
	code, body := get(t, pend.redirect+"?code=thecode&state="+url.QueryEscape(state))
	if code != 200 || !strings.Contains(body, "Finishing sign-in") || !strings.Contains(body, `data-state="working"`) {
		t.Fatalf("first page = %d %.200s", code, body)
	}
	if _, err := pend.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Token in hand but not saved yet: still working, so a keychain failure can be shown.
	if o := outcomeOf(t, pend, state); o.State != "working" {
		t.Fatalf("before Saved: %+v", o)
	}
	pend.Saved(nil)
	o := outcomeOf(t, pend, state)
	if o.State != "success" || o.Kind != KindSuccess || !strings.Contains(o.Title, "Google Drive is connected") {
		t.Fatalf("after Saved: %+v", o)
	}
	// A reload of the redirect shows the final outcome straight away.
	if _, body := get(t, pend.redirect+"?code=thecode&state="+url.QueryEscape(state)); !strings.Contains(body, `data-state="success"`) {
		t.Fatalf("reload did not show success: %.300s", body)
	}
}

func TestEveryOutcomeHasItsPage(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		kind  Kind
	}{
		"denied":      {"error=access_denied", KindDenied},
		"unavailable": {"error=temporarily_unavailable", KindUnavailable},
		"blocked":     {"error=admin_policy_enforced", KindBlocked},
		"setup":       {"error=invalid_client", KindSetup},
		"other":       {"error=weird&error_description=something+odd", KindProvider},
		"no code":     {"", KindIncomplete},
		"permission":  {"code=thecode&scope=s1", KindPermission},
	} {
		t.Run(name, func(t *testing.T) {
			p := newFakeProvider(t)
			pend, state := startFlow(t, p)
			q := "state=" + url.QueryEscape(state)
			if tc.query != "" {
				q += "&" + tc.query
			}
			code, body := get(t, pend.redirect+"?"+q)
			if code != 200 || !strings.Contains(body, `data-state="error"`) || !strings.Contains(body, `data-kind="`+string(tc.kind)+`"`) {
				t.Fatalf("page = %d %.400s", code, body)
			}
			if _, err := pend.Wait(context.Background()); err == nil {
				t.Fatal("Wait should fail")
			}
			if o := outcomeOf(t, pend, state); o.Kind != tc.kind {
				t.Fatalf("result = %+v", o)
			}
		})
	}
}

func TestPermissionNeedsOnlyTheMissingScope(t *testing.T) {
	if got := missingScopes([]string{"s1", "s2"}, "s1 s2 extra"); len(got) != 0 {
		t.Fatalf("complete grant flagged: %v", got)
	}
	if got := missingScopes([]string{"s1", "s2"}, ""); len(got) != 0 {
		t.Fatalf("no scope parameter must pass: %v", got)
	}
	if got := missingScopes([]string{"s1", "s2"}, "s2"); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("missing = %v", got)
	}
}

func TestStaleLinkGetsAPageAndDoesNotEndTheFlow(t *testing.T) {
	p := newFakeProvider(t)
	pend, state := startFlow(t, p)
	code, body := get(t, pend.redirect+"?code=x&state=nope")
	if code != 400 || !strings.Contains(body, "not valid") {
		t.Fatalf("stale = %d %.300s", code, body)
	}
	if o := outcomeOf(t, pend, state); o.State != "working" {
		t.Fatalf("a stray request ended the flow: %+v", o)
	}
	if code, _ := get(t, pend.redirect[:strings.LastIndex(pend.redirect, "/")]+"/callback/result?state=nope"); code != 403 {
		t.Fatalf("result with a wrong state = %d, want 403", code)
	}
}

func TestExchangeFailuresAreClassified(t *testing.T) {
	// The fake rejects a code other than "thecode" with invalid_grant.
	p := newFakeProvider(t)
	pend, state := startFlow(t, p)
	get(t, pend.redirect+"?code=old&state="+url.QueryEscape(state))
	if _, err := pend.Wait(context.Background()); !errors.Is(err, ErrReauth) {
		t.Fatalf("err = %v", err)
	}
	if o := outcomeOf(t, pend, state); o.Kind != KindExpired {
		t.Fatalf("result = %+v", o)
	}

}

func TestKindOf(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		kind Kind
	}{
		"network":   {&url.Error{Op: "Post", URL: "https://x", Err: errors.New("connection refused")}, KindNetwork},
		"deadline":  {context.DeadlineExceeded, KindTimeout},
		"cancelled": {context.Canceled, KindCancelled},
		"expired":   {ErrReauth, KindExpired},
		"refused":   {errors.New("oauth: token endpoint 400: invalid_request"), KindExchange},
	} {
		if got, _ := kindOf(tc.err); got != tc.kind {
			t.Errorf("%s: kind = %q, want %q", name, got, tc.kind)
		}
	}
}

func TestStorageFailureIsShown(t *testing.T) {
	p := newFakeProvider(t)
	pend, state := startFlow(t, p)
	get(t, pend.redirect+"?code=thecode&state="+url.QueryEscape(state))
	if _, err := pend.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	pend.Saved(errors.New("secret service is locked"))
	o := outcomeOf(t, pend, state)
	if o.Kind != KindStorage || !strings.Contains(o.Detail, "secret service is locked") {
		t.Fatalf("result = %+v", o)
	}
}

func TestCancelAfterTheBrowserArrived(t *testing.T) {
	p := newFakeProvider(t)
	pend, state := startFlow(t, p)
	get(t, pend.redirect+"?code=thecode&state="+url.QueryEscape(state))
	pend.Cancel()
	if o := outcomeOf(t, pend, state); o.Kind != KindCancelled {
		t.Fatalf("result = %+v", o)
	}
}

func TestTimeoutClosesTheListener(t *testing.T) {
	p := newFakeProvider(t)
	f := &Flow{Name: "Google Drive", Config: p.cfg(), HTTP: p.Client(), Timeout: 50 * time.Millisecond}
	pend, _ := f.Start()
	if _, err := pend.Wait(context.Background()); err == nil {
		t.Fatal("expected timeout")
	}
	// No browser ever arrived, so nothing needs the page: the listener is gone at once.
	if _, err := http.Get(pend.redirect + "?state=x"); err == nil {
		t.Fatal("listener should be closed")
	}
}

func TestProviderTextIsEscaped(t *testing.T) {
	p := newFakeProvider(t)
	pend, state := startFlow(t, p)
	_, body := get(t, pend.redirect+"?state="+url.QueryEscape(state)+"&error=weird&error_description="+url.QueryEscape(`<script>alert(1)</script>`))
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatalf("provider text was not escaped: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)") {
		t.Fatalf("expected the escaped text in the page: %.600s", body)
	}
}

func TestPageAssetsAreServed(t *testing.T) {
	p := newFakeProvider(t)
	pend, _ := startFlow(t, p)
	base := pend.redirect[:strings.LastIndex(pend.redirect, "/")]
	for _, path := range []string{"/callback/assets/font.woff2", "/callback/assets/icon.svg"} {
		if code, body := get(t, base+path); code != 200 || len(body) < 100 {
			t.Fatalf("%s = %d (%d bytes)", path, code, len(body))
		}
	}
}
