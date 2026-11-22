package api

import (
	"context"
	"errors"
)

// Provider is a cloud storage service the user can link, as the UI sees it.
type Provider struct {
	ID   string `json:"id"` // e.g. "gdrive"
	Name string `json:"name"`
	// Available is false while the provider is not implemented in this build;
	// the UI shows it greyed out.
	Available bool   `json:"available"`
	Linked    bool   `json:"linked"`
	Pending   bool   `json:"pending"`           // consent page opened, waiting for the user
	Account   string `json:"account,omitempty"` // e.g. the account's email
	Error     string `json:"error,omitempty"`   // e.g. "access revoked, reconnect"
}

// LinkStart is returned when linking begins.
type LinkStart struct {
	// URL is the provider's consent page. The UI opens it in a new tab unless
	// the backend has already opened the system browser (then it is empty).
	URL string `json:"url,omitempty"`
}

// ErrProviderUnavailable means the provider isn't supported by this build yet.
var ErrProviderUnavailable = errors.New("this provider is not available yet")

// ErrUnknownProvider means the id is not one of the providers.
var ErrUnknownProvider = errors.New("unknown provider")

// Providers is what the API needs from the cloud-link layer. The real
// implementation (OAuth, keychain, store selection) lives outside this
// package; StubProviders stands in until it is wired up.
type Providers interface {
	List(ctx context.Context) []Provider
	// Link starts the OAuth flow. The UI polls List to see it complete.
	Link(ctx context.Context, id string) (LinkStart, error)
	// Unlink forgets the stored credentials. Cloud data is left alone.
	Unlink(ctx context.Context, id string) error
}

// StubProviders lists Google Drive as not yet available.
type StubProviders struct{}

func (StubProviders) List(context.Context) []Provider {
	return []Provider{
		{ID: "gdrive", Name: "Google Drive"},
	}
}

func (StubProviders) Link(_ context.Context, id string) (LinkStart, error) {
	return LinkStart{}, stubErr(id)
}

func (StubProviders) Unlink(_ context.Context, id string) error { return stubErr(id) }

func stubErr(id string) error {
	if id == "gdrive" {
		return ErrProviderUnavailable
	}
	return ErrUnknownProvider
}
