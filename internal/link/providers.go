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
		ap := api.Provider{ID: p.ID, Name: p.Name, Available: p.Configured, Linked: p.Linked, Pro: p.Pro, Locked: p.Locked}
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
	case errors.Is(err, ErrManualSetup):
		return api.LinkStart{}, errors.New("S3-compatible storage is set up with its settings form, not a sign-in")
	case errors.Is(err, ErrNotConfigured):
		// Keep the developer-facing message; it maps to 501 like ErrProviderUnavailable.
		return api.LinkStart{}, &unavailable{err}
	case err != nil:
		return api.LinkStart{}, err
	}
	// If the system browser was opened for the user, the UI needn't open a tab.
	if a.M.OpenURL != nil && a.M.Status().Error == "" {
		return api.LinkStart{}, nil
	}
	return api.LinkStart{URL: url}, nil
}

var _ api.ObjectStorage = API{}

func (a API) S3Config() api.S3Config {
	c := a.M.S3Config()
	return api.S3Config{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, Prefix: c.Prefix,
		PathStyle: c.PathStyle, AccessKey: c.AccessKey, HasSecret: c.HasSecret}
}

func s3Input(in api.S3Input) S3Input {
	return S3Input{
		S3Settings: S3Settings{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, PathStyle: in.PathStyle},
		AccessKey:  in.AccessKey, SecretKey: in.SecretKey,
	}
}

func (a API) Save(in api.S3Input) error { return a.M.SaveS3(s3Input(in)) }

func (a API) Test(ctx context.Context, in api.S3Input) error { return a.M.TestS3(ctx, s3Input(in)) }

func (a API) Unlink(_ context.Context, id string) error {
	err := a.M.Unlink(id)
	if errors.Is(err, ErrUnknownProvider) {
		return api.ErrUnknownProvider
	}
	return err
}

type unavailable struct{ error }

func (u *unavailable) Is(target error) bool { return target == api.ErrProviderUnavailable }
func (u *unavailable) Unwrap() error        { return u.error }
