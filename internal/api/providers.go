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
	Available bool `json:"available"`
	// Note says why the provider is not available (shown instead of a bare
	// "coming soon"), e.g. that this build has no OAuth client ID.
	Note    string `json:"note,omitempty"`
	Linked  bool   `json:"linked"`
	Pending bool   `json:"pending"`           // consent page opened, waiting for the user
	Account string `json:"account,omitempty"` // e.g. the account's email
	Error   string `json:"error,omitempty"`   // e.g. "access revoked, reconnect"
	// Pro marks a provider that needs a Pro licence; Locked means this
	// computer has none, so the UI shows it as an upgrade rather than a form.
	Pro    bool `json:"pro,omitempty"`
	Locked bool `json:"locked,omitempty"`
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
		{ID: "gdrive", Name: "Google Drive",
			Note: "Cloud accounts are off because WorldHop was started with -store (a shared folder). Start it without -store to connect Google Drive."},
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

// S3Config is the saved S3-compatible storage setup as the UI sees it. The
// secret key is never returned, only whether one is stored.
type S3Config struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	PathStyle bool   `json:"pathStyle"`
	AccessKey string `json:"accessKey"`
	HasSecret bool   `json:"hasSecret"`
}

// S3Input is what the settings form submits. An empty SecretKey keeps the
// stored one.
type S3Input struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	PathStyle bool   `json:"pathStyle"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// ObjectStorage is the setup form behind the S3-compatible provider (Pro).
// Save and Test return license.ErrProRequired without a Pro licence.
type ObjectStorage interface {
	S3Config() S3Config
	// Save stores the settings (secret in the keychain) and selects S3.
	Save(in S3Input) error
	// Test tries the bucket with the given settings, saving nothing. A nil
	// error means the credentials can list the bucket.
	Test(ctx context.Context, in S3Input) error
}
