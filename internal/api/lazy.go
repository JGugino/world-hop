package api

import (
	"io/fs"
	"log"
	"sync"
	"time"

	wsync "github.com/JGugino/world-hopper/internal/sync"
)

// Lazy starts a Server only when asked and shuts it down after it has been idle,
// so the HTTP listener doesn't exist while the UI isn't in use.
// closeGrace is how long after the page reports closing before the server stops.
const closeGrace = 5 * time.Second

type Lazy struct {
	addr   string
	svc    *wsync.Runner
	assets fs.FS
	idle   time.Duration
	tick   time.Duration
	grace  time.Duration // wait after the UI says it closed, to survive reloads

	mu  sync.Mutex
	cur *Server
}

func NewLazy(addr string, svc *wsync.Runner, assets fs.FS, idle time.Duration) *Lazy {
	tick := idle / 4
	if tick <= 0 {
		tick = time.Second
	}
	return &Lazy{addr: addr, svc: svc, assets: assets, idle: idle, tick: tick, grace: closeGrace}
}

// Ensure returns the URL of the running server, starting one (on a free
// loopback port by default) if none is running.
func (l *Lazy) Ensure() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil {
		return l.cur.URL(), nil
	}
	srv, err := New(l.addr, l.svc, l.assets)
	if err != nil {
		return "", err
	}
	l.cur = srv
	srv.OnClosed = func() { l.closeSoon(srv) }
	go func() {
		if err := srv.Serve(); err != nil && err.Error() != "http: Server closed" {
			log.Printf("server stopped: %v", err)
		}
	}()
	go l.watch(srv)
	log.Printf("web server started at %s", srv.URL())
	return srv.URL(), nil
}

// Running reports whether a server is currently up.
func (l *Lazy) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cur != nil
}

func (l *Lazy) watch(srv *Server) {
	t := time.NewTicker(l.tick)
	defer t.Stop()
	for range t.C {
		l.mu.Lock()
		if l.cur != srv {
			l.mu.Unlock()
			return
		}
		if time.Since(srv.LastActivity()) >= l.idle {
			l.cur = nil
			l.mu.Unlock()
			srv.Close()
			log.Printf("web server stopped after idle")
			return
		}
		l.mu.Unlock()
	}
}

// closeSoon stops srv after the grace period unless it sees any request in the
// meantime (a reload's new page, or another open view polling).
func (l *Lazy) closeSoon(srv *Server) {
	seen := srv.LastActivity()
	time.AfterFunc(l.grace, func() {
		l.mu.Lock()
		if l.cur != srv || srv.LastActivity().After(seen) {
			l.mu.Unlock()
			return
		}
		l.cur = nil
		l.mu.Unlock()
		srv.Close()
		log.Printf("web server stopped after view closed")
	})
}

// CloseNow stops the server immediately, e.g. when a native window closes.
func (l *Lazy) CloseNow() { l.Close() }

// Close stops the server if it is running.
func (l *Lazy) Close() {
	l.mu.Lock()
	srv := l.cur
	l.cur = nil
	l.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}
