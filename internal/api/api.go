// Package api serves the local HTTP API and embedded UI on 127.0.0.1.
package api

import (
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

func (s *Server) URL() string  { return "http://" + s.ln.Addr().String() + "/" }
func (s *Server) Serve() error { return s.srv.Serve(s.ln) }

// LastActivity reports when the server last received a request (or started).
func (s *Server) LastActivity() time.Time { return time.Unix(0, s.last.Load()) }
func (s *Server) Close() error            { return s.srv.Close() }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
