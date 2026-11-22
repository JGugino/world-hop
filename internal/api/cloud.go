package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/JGugino/world-hopper/internal/link"
)

// Cloud is the account-linking surface the UI talks to; *link.Manager
// implements it.
type Cloud interface {
	Status() link.Status
	Choose(provider string) error
	StartLink(provider string) (consentURL string, err error)
	CancelLink()
	Unlink(provider string) error
}

// cloudRoutes registers /api/cloud/*. Handlers answer 503 until s.Cloud is set.
//
//	GET  /api/cloud          -> link.Status
//	POST /api/cloud/choose   {"provider":"gdrive"|"dropbox"} -> link.Status
//	POST /api/cloud/link     {"provider":...} -> {"url": consentURL, "status": link.Status}
//	POST /api/cloud/cancel   -> link.Status
//	POST /api/cloud/unlink   {"provider":...} -> link.Status
//
// Errors are {"error": "..."} with 400 (bad request/provider), 409 (provider
// not configured) or 500.
func (s *Server) cloudRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cloud", s.cloud(func(c Cloud, _ string) (any, error) {
		return c.Status(), nil
	}, false))
	mux.HandleFunc("POST /api/cloud/choose", s.cloud(func(c Cloud, p string) (any, error) {
		if err := c.Choose(p); err != nil {
			return nil, err
		}
		return c.Status(), nil
	}, true))
	mux.HandleFunc("POST /api/cloud/link", s.cloud(func(c Cloud, p string) (any, error) {
		u, err := c.StartLink(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": u, "status": c.Status()}, nil
	}, true))
	mux.HandleFunc("POST /api/cloud/cancel", s.cloud(func(c Cloud, _ string) (any, error) {
		c.CancelLink()
		return c.Status(), nil
	}, false))
	mux.HandleFunc("POST /api/cloud/unlink", s.cloud(func(c Cloud, p string) (any, error) {
		if err := c.Unlink(p); err != nil {
			return nil, err
		}
		return c.Status(), nil
	}, true))
}

func (s *Server) cloud(fn func(c Cloud, provider string) (any, error), wantProvider bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Cloud == nil {
			writeErr(w, http.StatusServiceUnavailable, "cloud accounts are not available")
			return
		}
		if r.Method == http.MethodPost && !sameOriginJSON(r) {
			writeErr(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		provider := ""
		if wantProvider {
			var body struct {
				Provider string `json:"provider"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
				writeErr(w, http.StatusBadRequest, "expected {\"provider\": ...}")
				return
			}
			provider = body.Provider
		}
		out, err := fn(s.Cloud, provider)
		switch {
		case errors.Is(err, link.ErrUnknownProvider):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, link.ErrNotConfigured):
			writeErr(w, http.StatusConflict, err.Error())
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, out)
		}
	}
}

// sameOriginJSON guards state-changing calls against other websites posting to
// the loopback port: it requires a JSON content type (which forces a CORS
// preflight we never grant) and, when an Origin header is sent, that it is us.
func sameOriginJSON(r *http.Request) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
