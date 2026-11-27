// page.go: serving the end-of-sign-in page from the loopback listener, and how the
// outcome is recorded. The page is embedded (page/), styled like the web view, and
// shows the final result only after the credentials are actually saved.

package oauth

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"time"
)

//go:embed page/callback.html page/font.woff2 page/icon.svg
var pageFS embed.FS

var callbackTmpl = template.Must(template.ParseFS(pageFS, "page/callback.html"))

// outcomeGrace is how long the loopback server stays up after the outcome is
// known, so the open browser page can fetch it. The server is only ever
// reachable from this computer, and only for the length of one sign-in.
var outcomeGrace = 30 * time.Second

// savedFallback bounds how long the page waits for the caller to report that
// the credentials were saved (Pending.Saved) once the token exchange worked.
var savedFallback = 2 * time.Minute

// pageData is what the page template is filled with: the outcome plus a one-time
// nonce that allows the page's own script under its Content-Security-Policy.
type pageData struct {
	Outcome
	Nonce string
}

// writePage renders the page with strict security headers: no caching, no referrer,
// and a Content-Security-Policy that allows only this page's own script and assets.
func (p *Pending) writePage(w http.ResponseWriter, status int, o Outcome) {
	nonce := randString(12)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; "+
		"img-src 'self'; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'")
	w.WriteHeader(status)
	_ = callbackTmpl.Execute(w, pageData{Outcome: o, Nonce: nonce})
}

// writeResult answers the page's polling request with the current outcome as JSON.
func (p *Pending) writeResult(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(p.outcome())
}

// serveAsset returns a handler that serves one embedded file (the font or icon).
func serveAsset(name, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := pageFS.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write(b)
	}
}

// outcome is what the page shows right now.
func (p *Pending) outcome() Outcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.final {
		return p.out
	}
	return describe(KindWorking, p.f.Name, "")
}

// finalize records how the attempt ended. The first outcome wins.
func (p *Pending) finalize(kind Kind, detail string) {
	p.mu.Lock()
	if p.final {
		p.mu.Unlock()
		return
	}
	p.final = true
	p.out = describe(kind, p.f.Name, detail)
	seen := p.seen
	if p.fallback != nil {
		p.fallback.Stop()
	}
	p.mu.Unlock()
	if seen {
		time.AfterFunc(outcomeGrace, func() { p.srv.Close() })
	} else {
		p.srv.Close()
	}
}

// Saved reports whether the credentials were stored once Wait returned a token:
// nil shows the success page, an error the "could not save" page. Call it once
// after Wait succeeds; without it the page waits a while, then shows success.
func (p *Pending) Saved(err error) {
	if err != nil {
		p.finalize(KindStorage, err.Error())
		return
	}
	p.finalize(KindSuccess, "")
}
