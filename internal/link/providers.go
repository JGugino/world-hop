// providers.go: adapts the link Manager to the interfaces the api package defines for
// the Cloud account page, so the api package never imports link.

package link

import (
	"context"
	"errors"

	"github.com/JGugino/world-hopper/internal/api"
)

// API adapts a Manager to the web UI's api.Providers interface.
type API struct{ M *Manager }

var _ api.Providers = API{}

// List describes each provider for the Cloud account tab: whether it is available in
// this build, whether it is connected, whether it is mid sign-in, and the last error.
func (a API) List(context.Context) []api.Provider {
	st := a.M.Status()
	var out []api.Provider
	for _, p := range st.Providers {
		ap := api.Provider{ID: p.ID, Name: p.Name, Available: p.Configured, Linked: p.Linked, Pro: p.Pro, Locked: p.Locked}
		if !p.Configured {
			ap.Note = "Google sign-in isn't set up in this build (no OAuth client ID was compiled in). Release builds include it; for a development build, put GOOGLE_CLIENT_ID in .env and run `make dev`."
		}
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

// firstNonEmpty returns a if it is not empty, otherwise b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Link starts connecting a provider. When this process opened the system browser
// itself there is nothing for the UI to do; otherwise the consent URL is returned for
// the UI to open. A provider with no OAuth client compiled in is reported as
// unavailable
// (a developer-facing message).
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

// S3Config returns the saved S3 settings (never the secret key) for the form.
func (a API) S3Config() api.S3Config {
	c := a.M.S3Config()
	return api.S3Config{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, Prefix: c.Prefix,
		PathStyle: c.PathStyle, AccessKey: c.AccessKey, HasSecret: c.HasSecret}
}

// s3Input converts the form's input into the link package's shape.
func s3Input(in api.S3Input) S3Input {
	return S3Input{
		S3Settings: S3Settings{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, PathStyle: in.PathStyle},
		AccessKey:  in.AccessKey, SecretKey: in.SecretKey,
	}
}

// Save stores the S3 settings and credentials and makes S3 the chosen provider.
func (a API) Save(in api.S3Input) error { return a.M.SaveS3(s3Input(in)) }

// Test checks the entered S3 settings against the bucket without saving anything.
func (a API) Test(ctx context.Context, in api.S3Input) error { return a.M.TestS3(ctx, s3Input(in)) }

// Unlink forgets a provider's stored credentials and asks the provider to end
// WorldHop's access (cloud data is left alone). If the provider could not be
// reached the result carries a warning saying where to remove the access by hand.
func (a API) Unlink(_ context.Context, id string) (api.UnlinkResult, error) {
	rev, err := a.M.Unlink(id)
	if errors.Is(err, ErrUnknownProvider) {
		return api.UnlinkResult{}, api.ErrUnknownProvider
	}
	if err != nil {
		return api.UnlinkResult{}, err
	}
	res := api.UnlinkResult{Revoked: rev.Revoked}
	if rev.ManualKey != "" {
		res.Warning = "Disconnected on this computer, but WorldHop can't revoke S3 access keys: access key " +
			rev.ManualKey + " stays valid with your storage service. Delete or rotate it in the service's console (under access keys or API tokens) if you no longer want it to work."
	}
	if rev.Err != nil {
		res.Warning = "Disconnected on this computer, but " + a.M.providers[id].name +
			" could not be reached to end WorldHop's access. Remove WorldHop under your account's third-party access settings (for Google: myaccount.google.com/permissions)."
	}
	return res, nil
}

// unavailable wraps an error so it matches api.ErrProviderUnavailable, which the API
// answers with 501.
type unavailable struct{ error }

// Is makes errors.Is(err, api.ErrProviderUnavailable) true for this error.
func (u *unavailable) Is(target error) bool { return target == api.ErrProviderUnavailable }

// Unwrap returns the underlying error.
func (u *unavailable) Unwrap() error { return u.error }
