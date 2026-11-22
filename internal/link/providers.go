package link

import (
	"context"
	"errors"

	"github.com/JGugino/world-hopper/internal/api"
)

// API adapts a Manager to the web UI's api.Providers interface.
type API struct{ M *Manager }

var _ api.Providers = API{}

func (a API) List(context.Context) []api.Provider {
	st := a.M.Status()
	var out []api.Provider
	for _, p := range st.Providers {
		ap := api.Provider{ID: p.ID, Name: p.Name, Available: p.Configured, Linked: p.Linked}
		if p.ID == st.Chosen {
			switch st.State {
			case StateLinking:
				ap.Pending = true
			case StateReconnect:
				ap.Linked = false
				ap.Error = "access revoked or expired, reconnect"
			}
			ap.Error = firstNonEmpty(ap.Error, st.Error)
		}
		out = append(out, ap)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (a API) Link(_ context.Context, id string) (api.LinkStart, error) {
	url, err := a.M.StartLink(id)
	switch {
	case errors.Is(err, ErrUnknownProvider):
		return api.LinkStart{}, api.ErrUnknownProvider
	case errors.Is(err, ErrNotConfigured):
		return api.LinkStart{}, api.ErrProviderUnavailable
	case err != nil:
		return api.LinkStart{}, err
	}
	// If the system browser was opened for the user, the UI needn't open a tab.
	if a.M.OpenURL != nil && a.M.Status().Error == "" {
		return api.LinkStart{}, nil
	}
	return api.LinkStart{URL: url}, nil
}

func (a API) Unlink(_ context.Context, id string) error {
	err := a.M.Unlink(id)
	if errors.Is(err, ErrUnknownProvider) {
		return api.ErrUnknownProvider
	}
	return err
}
