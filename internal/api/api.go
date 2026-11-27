// Package api serves the local HTTP API and embedded UI on 127.0.0.1.
//
// The web view (the SvelteKit app in ui/) is the only user interface, and it talks to
// the rest of the program through this package. Nothing here makes sync decisions: each
// handler checks its input, calls the sync Runner or one of the managers (cloud
// account, encryption, license, sources, updates) and returns JSON. The server is
// loopback-only, rejects requests with a foreign Host or Origin (so a web page the
// user happens to visit can't drive it), and only exists while the web view is open
// (see lazy.go).
//
// Files: api.go (the server and its guards), routes.go (the endpoints and the
// interfaces they depend on), encryption.go (the Encryption tab's endpoints),
// providers.go (the Cloud account tab's types), lazy.go (start and stop on demand),
// picker.go (the native folder dialog). docs/technical.md section 2.4 lists every
// endpoint.
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	stdsync "sync/atomic"
	"time"

	"github.com/JGugino/world-hopper/internal/sync"
)

// Server is one running instance of the local API. svc is the sync Runner, deps the
// managers the settings endpoints use, and assets the built UI. It records when the
// last request arrived so Lazy can stop it when it has been idle.
type Server struct {
	svc    *sync.Runner
	deps   Deps
	assets fs.FS
	ln     net.Listener
	srv    *http.Server
	last   stdsync.Int64 // unix nanos of the last request

	// OnClosed, if set, is called when the UI reports its page is closing
	// (POST /api/bye). The page may just be reloading, so callers should wait
	// a grace period and check LastActivity before acting on it.
	OnClosed func()

	// Pick opens a native folder dialog. Nil uses the operating system's own;
	// tests replace it.
	Pick func(ctx context.Context, title string) (string, error)
}

// New listens on 127.0.0.1 (port 0 picks a free port unless addr says otherwise).
func New(addr string, svc *sync.Runner, assets fs.FS, deps Deps) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{svc: svc, deps: deps, assets: assets, ln: ln}
	s.last.Store(time.Now().UnixNano())
	h := s.Handler()
	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.last.Store(time.Now().UnixNano())
		h.ServeHTTP(w, r)
	})}
	return s, nil
}

// Handler builds the request handler: the status and sync endpoints, the settings
// routes, the folder picker, and the embedded UI for everything else, all wrapped in
// hostGuard.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.svc.Status())
	})
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.svc.ForceSync(r.Context()))
	})
	mux.HandleFunc("POST /api/bye", func(w http.ResponseWriter, r *http.Request) {
		if s.OnClosed != nil {
			s.OnClosed()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.routes(mux)
	s.registerPicker(mux)
	mux.Handle("/", s.static())
	return hostGuard(mux)
}

// hostGuard rejects requests whose Host isn't loopback, blocking DNS-rebinding
// attacks against the local API.
func hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// State-changing requests must come from our own page: a cross-origin
		// page could otherwise POST to the loopback port.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// static serves the embedded UI. Anything that is not a file falls back to
// index.html so the single-page app's client-side routes (/cloud, /license...) work
// when reloaded or opened directly. If the UI was never built it says how to build it.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if _, err := fs.Stat(s.assets, "index.html"); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<h1>WorldHop</h1><p>UI not built. Run <code>npm run build</code> in <code>ui/</code> and rebuild.</p>"))
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(s.assets, p); err != nil {
				r.URL.Path = "/" // SPA fallback
			}
		}
		files.ServeHTTP(w, r)
	})
}

// URL is the address the server listens on, for opening in the browser.
func (s *Server) URL() string { return "http://" + s.ln.Addr().String() + "/" }

// Serve runs the server until Close.
func (s *Server) Serve() error { return s.srv.Serve(s.ln) }

// LastActivity reports when the server last received a request (or started).
func (s *Server) LastActivity() time.Time { return time.Unix(0, s.last.Load()) }

// Close stops the server immediately.
func (s *Server) Close() error { return s.srv.Close() }

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
