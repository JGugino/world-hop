package api

import (
	"errors"
	"net/http"

	"github.com/JGugino/world-hopper/internal/crypt"
)

var errNoEncryption = errors.New("encryption is not available in this build")

func (s *Server) encryptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/encryption", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Encryption == nil {
			writeJSON(w, crypt.Info{})
			return
		}
		writeJSON(w, s.deps.Encryption.Info(r.Context()))
	})
	mux.HandleFunc("POST /api/encryption/enable", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Encryption == nil {
			writeErr(w, http.StatusNotImplemented, errNoEncryption)
			return
		}
		var b struct {
			Passphrase   string `json:"passphrase"`
			Confirm      string `json:"confirm"`
			Acknowledged bool   `json:"acknowledged"`
			Remember     bool   `json:"remember"`
		}
		if !readJSON(w, r, &b) {
			return
		}
		if err := s.deps.Encryption.Enable(r.Context(), b.Passphrase, b.Confirm, b.Acknowledged, b.Remember); err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, s.deps.Encryption.Info(r.Context()))
	})
	mux.HandleFunc("POST /api/encryption/unlock", func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Encryption == nil {
			writeErr(w, http.StatusNotImplemented, errNoEncryption)
			return
		}
		var b struct {
			Passphrase string `json:"passphrase"`
			Remember   bool   `json:"remember"`
		}
		if !readJSON(w, r, &b) {
			return
		}
		if err := s.deps.Encryption.Unlock(r.Context(), b.Passphrase, b.Remember); err != nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, s.deps.Encryption.Info(r.Context()))
	})
	for _, name := range []string{"lock", "disable"} {
		name := name
		mux.HandleFunc("POST /api/encryption/"+name, func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Encryption == nil {
				writeErr(w, http.StatusNotImplemented, errNoEncryption)
				return
			}
			var err error
			if name == "lock" {
				err = s.deps.Encryption.Lock()
			} else {
				err = s.deps.Encryption.Disable()
			}
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, s.deps.Encryption.Info(r.Context()))
		})
	}
}
