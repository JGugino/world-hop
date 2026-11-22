package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JGugino/world-hopper/internal/config"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// SourceStore holds the user's sync sources. *config.Config implements it.
type SourceStore interface {
	Sources() []worlds.Source
	Add(worlds.Source) (worlds.Source, error)
	Remove(kind, path string) error
}

var _ SourceStore = (*config.Config)(nil)

// Deps are the collaborators the settings endpoints need. Nil fields fall back
// to inert behaviour (no sources, stub providers).
type Deps struct {
	Sources   SourceStore
	Providers Providers
}

func (s *Server) providers() Providers {
	if s.deps.Providers == nil {
		return StubProviders{}
	}
	return s.deps.Providers
}

type errBody struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(errBody{err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad request body"))
		return false
	}
	return true
}

// SourceInfo is a source plus what it currently resolves to.
type SourceInfo struct {
	worlds.Source
	Worlds int    `json:"worlds"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) sourceInfos() []SourceInfo {
	out := []SourceInfo{}
	if s.deps.Sources == nil {
		return out
	}
	for _, src := range s.deps.Sources.Sources() {
		ws, problems := worlds.Resolve([]worlds.Source{src})
		info := SourceInfo{Source: src, Worlds: len(ws)}
		if len(problems) > 0 {
			info.Error = problems[0].Error()
		}
		out = append(out, info)
	}
	return out
}

// SnapshotDTO is a local snapshot as the UI sees it.
type SnapshotDTO struct {
	ID        string     `json:"id"`
	Reason    string     `json:"reason"`
	CreatedAt *time.Time `json:"createdAt"`
}

func snapshotDTO(si sync.SnapshotInfo) SnapshotDTO {
	d := SnapshotDTO{ID: si.ID}
	// ids look like 20060102T150405.000000Z-reason
	if stamp, reason, ok := strings.Cut(si.ID, "-"); ok {
		d.Reason = reason
		if t, err := time.Parse("20060102T150405.000000Z", stamp); err == nil {
			d.CreatedAt = &t
		}
	}
	return d
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.sourceInfos())
	})
	mux.HandleFunc("POST /api/sources", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Sources == nil {
			writeErr(w, http.StatusNotImplemented, errors.New("sources are not configurable"))
			return
		}
		var src worlds.Source
		if !readJSON(w, r, &src) {
			return
		}
		src.Launcher = ""
		if _, err := s.deps.Sources.Add(src); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, s.sourceInfos())
	})
	mux.HandleFunc("POST /api/sources/remove", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Sources == nil {
			writeErr(w, http.StatusNotImplemented, errors.New("sources are not configurable"))
			return
		}
		var src worlds.Source
		if !readJSON(w, r, &src) {
			return
		}
		if err := s.deps.Sources.Remove(src.Kind, src.Path); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, s.sourceInfos())
	})

	mux.HandleFunc("GET /api/worlds", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.svc.Worlds(r.Context()))
	})
	mux.HandleFunc("POST /api/worlds/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.svc.SyncWorld(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("POST /api/worlds/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Strategy sync.Strategy `json:"strategy"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		res, err := s.svc.Resolve(r.Context(), r.PathValue("id"), body.Strategy)
		if err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"used": res.Used, "copy": res.Copy})
	})
	mux.HandleFunc("GET /api/worlds/{id}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		snaps, err := s.svc.Snapshots(r.PathValue("id"))
		if err != nil {
			writeServiceErr(w, err)
			return
		}
		out := make([]SnapshotDTO, 0, len(snaps))
		for i := len(snaps) - 1; i >= 0; i-- { // newest first
			out = append(out, snapshotDTO(snaps[i]))
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/worlds/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Snapshot string `json:"snapshot"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if err := s.svc.Restore(r.PathValue("id"), body.Snapshot); err != nil {
			writeServiceErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/cloud-worlds", func(w http.ResponseWriter, r *http.Request) {
		cw, err := s.svc.CloudWorlds(r.Context())
		if err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, cw)
	})
	mux.HandleFunc("POST /api/cloud-worlds/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Saves string `json:"saves"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		res, err := s.svc.DownloadWorld(r.Context(), r.PathValue("id"), body.Saves)
		if err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"id": r.PathValue("id"), "snapshot": res.Snapshot})
	})

	mux.HandleFunc("GET /api/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.providers().List(r.Context()))
	})
	mux.HandleFunc("POST /api/providers/{id}/link", func(w http.ResponseWriter, r *http.Request) {
		start, err := s.providers().Link(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, start)
	})
	mux.HandleFunc("POST /api/providers/{id}/unlink", func(w http.ResponseWriter, r *http.Request) {
		if err := s.providers().Unlink(r.Context(), r.PathValue("id")); err != nil {
			writeServiceErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func writeServiceErr(w http.ResponseWriter, err error) {
	var ce *sync.ConflictError
	switch {
	case errors.Is(err, sync.ErrUnknownWorld), errors.Is(err, sync.ErrNotInStore), errors.Is(err, ErrUnknownProvider), errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, sync.ErrBadStrategy), errors.Is(err, sync.ErrBadTarget):
		writeErr(w, http.StatusBadRequest, err)
	case errors.Is(err, sync.ErrNoStore), errors.Is(err, ErrProviderUnavailable):
		writeErr(w, http.StatusNotImplemented, err)
	case errors.Is(err, sync.ErrWorldOpen), errors.Is(err, sync.ErrDestExists), errors.As(err, &ce):
		writeErr(w, http.StatusConflict, err)
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}
