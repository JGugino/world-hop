package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JGugino/world-hopper/internal/config"
	"github.com/JGugino/world-hopper/internal/crypt"
	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/update"
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
	License   Licenser
	// Encryption is the client-side encryption settings. Nil when the build
	// has no encryption (the endpoints then report 501).
	Encryption Encrypter
	// ObjectStorage is the optional S3-compatible setup (Pro). Nil means this
	// build or mode has none.
	ObjectStorage ObjectStorage
	// Updates reports whether a newer release exists. Nil means no checking.
	Updates Updater
}

// Updater is the update notice the UI shows. *update.Checker implements it.
type Updater interface {
	Info() update.Info
}

var _ Updater = (*update.Checker)(nil)

// Encrypter is the encryption state the UI shows and edits. *crypt.Manager
// implements it.
type Encrypter interface {
	Info(ctx context.Context) crypt.Info
	Enable(ctx context.Context, pass, confirm string, acknowledged, remember bool) error
	Unlock(ctx context.Context, pass string, remember bool) error
	Lock() error
	Disable() error
}

var _ Encrypter = (*crypt.Manager)(nil)

// Licenser is the license state the UI shows and edits. *license.Manager
// implements it.
type Licenser interface {
	Info() license.Info
	Activate(ctx context.Context, key string) error
	Deactivate() error
	SetFreeWorld(id string) error
}

var _ Licenser = (*license.Manager)(nil)

func (s *Server) providers() Providers {
	if s.deps.Providers == nil {
		return StubProviders{}
	}
	return s.deps.Providers
}

type errBody struct {
	Error string `json:"error"`
	// Action and Fix are set for errors WorldHop can explain: what to do, and
	// the UI page where to do it. Detail is the raw technical message.
	Kind   string `json:"kind,omitempty"`
	Action string `json:"action,omitempty"`
	Fix    string `json:"fix,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(errBody{Error: err.Error()})
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
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Updates == nil {
			writeJSON(w, update.Info{})
			return
		}
		writeJSON(w, s.deps.Updates.Info())
	})
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

	s.encryptionRoutes(mux)

	mux.HandleFunc("GET /api/license", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.License == nil {
			writeJSON(w, license.Info{Tier: "free", FreeWorlds: license.FreeWorlds})
			return
		}
		writeJSON(w, s.deps.License.Info())
	})
	mux.HandleFunc("POST /api/license/activate", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.License == nil {
			writeErr(w, http.StatusNotImplemented, license.ErrUnavailable)
			return
		}
		var body struct {
			Key string `json:"key"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if err := s.deps.License.Activate(r.Context(), body.Key); err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, s.deps.License.Info())
	})
	mux.HandleFunc("POST /api/license/deactivate", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.License == nil {
			writeErr(w, http.StatusNotImplemented, license.ErrUnavailable)
			return
		}
		if err := s.deps.License.Deactivate(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, s.deps.License.Info())
	})
	mux.HandleFunc("POST /api/license/free-world", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.License == nil {
			writeErr(w, http.StatusNotImplemented, license.ErrUnavailable)
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if _, err := s.svc.FindWorld(body.ID); err != nil {
			writeServiceErr(w, err)
			return
		}
		if err := s.deps.License.SetFreeWorld(body.ID); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, s.deps.License.Info())
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
	mux.HandleFunc("GET /api/s3", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.ObjectStorage == nil {
			writeErr(w, http.StatusNotImplemented, ErrProviderUnavailable)
			return
		}
		writeJSON(w, s.deps.ObjectStorage.S3Config())
	})
	mux.HandleFunc("PUT /api/s3", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.ObjectStorage == nil {
			writeErr(w, http.StatusNotImplemented, ErrProviderUnavailable)
			return
		}
		var in S3Input
		if !readJSON(w, r, &in) {
			return
		}
		if err := s.deps.ObjectStorage.Save(in); err != nil {
			if errors.Is(err, license.ErrProRequired) {
				writeServiceErr(w, err)
			} else {
				writeErr(w, http.StatusBadRequest, err)
			}
			return
		}
		writeJSON(w, s.deps.ObjectStorage.S3Config())
	})
	// Test always answers 200 with {ok, error} so the form can show the reason;
	// only a missing licence is an HTTP error.
	mux.HandleFunc("POST /api/s3/test", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.ObjectStorage == nil {
			writeErr(w, http.StatusNotImplemented, ErrProviderUnavailable)
			return
		}
		var in S3Input
		if !readJSON(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		err := s.deps.ObjectStorage.Test(ctx, in)
		switch {
		case err == nil:
			writeJSON(w, map[string]any{"ok": true})
		case errors.Is(err, license.ErrProRequired):
			writeServiceErr(w, err)
		default:
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		}
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
	case errors.Is(err, crypt.ErrWrongPassphrase):
		writeErr(w, http.StatusUnauthorized, err)
	case errors.Is(err, crypt.ErrNotConfirmed), errors.Is(err, crypt.ErrWeakPassphrase):
		writeErr(w, http.StatusBadRequest, err)
	case errors.Is(err, crypt.ErrLocked):
		writeErr(w, http.StatusLocked, err)
	case errors.Is(err, license.ErrProRequired):
		writeErr(w, http.StatusPaymentRequired, err)
	case errors.Is(err, license.ErrWorldLimit):
		writeErr(w, http.StatusForbidden, err)
	case errors.Is(err, license.ErrUnavailable):
		writeErr(w, http.StatusNotImplemented, err)
	case errors.Is(err, license.ErrCheckFailed):
		writeErr(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, license.ErrInvalid):
		writeErr(w, http.StatusUnprocessableEntity, err)
	case errors.Is(err, sync.ErrNoStore), errors.Is(err, ErrProviderUnavailable):
		writeErr(w, http.StatusNotImplemented, err)
	case errors.Is(err, sync.ErrWorldOpen), errors.Is(err, sync.ErrDestExists), errors.As(err, &ce):
		writeErr(w, http.StatusConflict, err)
	default:
		p := sync.Explain(err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(errBody{Error: p.Title, Kind: p.Kind, Action: p.Action, Fix: p.Fix, Detail: p.Detail})
	}
}
