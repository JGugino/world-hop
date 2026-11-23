package link

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/s3"
	"github.com/zalando/go-keyring"
)

// S3 is the provider id of the S3-compatible store (a Pro feature).
const S3 = "s3"

// ErrManualSetup is returned by StartLink for providers set up with a form
// rather than a browser sign-in.
var ErrManualSetup = errors.New("link: this provider is set up with its settings form")

// SecretStore keeps small secrets. The real implementation is the OS keychain;
// secrets are never written to plain files.
type SecretStore interface {
	Get(key string) (string, error) // ErrNoSecret if absent
	Set(key, value string) error
	Delete(key string) error
}

// ErrNoSecret means nothing is stored under the key.
var ErrNoSecret = errors.New("link: no stored secret")

// KeyringSecrets keeps secrets in the OS keychain.
type KeyringSecrets struct{ Service string }

func (k KeyringSecrets) Get(key string) (string, error) {
	v, err := keyring.Get(k.Service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoSecret
	}
	return v, err
}

func (k KeyringSecrets) Set(key, v string) error { return keyring.Set(k.Service, key, v) }

func (k KeyringSecrets) Delete(key string) error {
	if err := keyring.Delete(k.Service, key); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// MemorySecrets is an in-memory SecretStore for tests.
type MemorySecrets map[string]string

func (m MemorySecrets) Get(k string) (string, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return "", ErrNoSecret
}
func (m MemorySecrets) Set(k, v string) error { m[k] = v; return nil }
func (m MemorySecrets) Delete(k string) error { delete(m, k); return nil }

// S3Settings are the non-secret connection settings, kept in s3.json.
type S3Settings struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	PathStyle bool   `json:"pathStyle"`
}

// S3Input is what the user enters. An empty SecretKey on save keeps the stored one.
type S3Input struct {
	S3Settings
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// S3Config is what the UI is shown: the settings, the access key id, and
// whether a secret is stored. The secret itself is never returned.
type S3Config struct {
	S3Settings
	AccessKey string `json:"accessKey"`
	HasSecret bool   `json:"hasSecret"`
}

type s3Creds struct {
	AccessKey string `json:"a"`
	SecretKey string `json:"s"`
}

// EnableS3 turns on the S3 provider. Secrets go to the given SecretStore (the
// OS keychain in production) and use is gated on the licence.
func (m *Manager) EnableS3(secrets SecretStore, lic license.Checker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets, m.license = secrets, lic
	m.providers[S3] = &provider{id: S3, name: "S3-compatible storage", configured: true, manual: true, pro: true}
	m.chosen = m.loadChoice()
}

func (m *Manager) s3File() string { return filepath.Join(m.dir, "s3.json") }

func (m *Manager) loadS3Settings() (S3Settings, bool) {
	b, err := os.ReadFile(m.s3File())
	if err != nil {
		return S3Settings{}, false
	}
	var s S3Settings
	if json.Unmarshal(b, &s) != nil || s.Bucket == "" {
		return S3Settings{}, false
	}
	return s, true
}

func (m *Manager) loadS3Creds() (s3Creds, error) {
	v, err := m.secrets.Get(S3)
	if err != nil {
		return s3Creds{}, err
	}
	var c s3Creds
	if err := json.Unmarshal([]byte(v), &c); err != nil || c.SecretKey == "" {
		return s3Creds{}, ErrNoSecret
	}
	return c, nil
}

// s3Linked reports whether both settings and credentials are saved.
func (m *Manager) s3Linked() bool {
	if m.secrets == nil {
		return false
	}
	if _, ok := m.loadS3Settings(); !ok {
		return false
	}
	_, err := m.loadS3Creds()
	return err == nil
}

// Pro reports whether Pro features are unlocked.
func (m *Manager) Pro() bool { return m.license != nil && m.license.Pro() }

// S3Config returns the saved settings for the UI.
func (m *Manager) S3Config() S3Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out S3Config
	if m.secrets == nil {
		return out
	}
	out.S3Settings, _ = m.loadS3Settings()
	if c, err := m.loadS3Creds(); err == nil {
		out.AccessKey, out.HasSecret = c.AccessKey, true
	}
	return out
}

// resolveS3 merges input with the stored secret and normalises it.
func (m *Manager) resolveS3(in S3Input) (s3.Config, error) {
	if in.SecretKey == "" {
		if c, err := m.loadS3Creds(); err == nil && (in.AccessKey == "" || in.AccessKey == c.AccessKey) {
			in.AccessKey, in.SecretKey = c.AccessKey, c.SecretKey
		}
	}
	return s3.Config{
		Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix,
		PathStyle: in.PathStyle, AccessKey: in.AccessKey, SecretKey: in.SecretKey,
	}.Normalize()
}

// TestS3 checks the entered (or, for blank fields, saved) settings against the
// bucket without saving anything.
func (m *Manager) TestS3(ctx context.Context, in S3Input) error {
	if err := license.Require(m.license); err != nil {
		return err
	}
	m.mu.Lock()
	cfg, err := m.resolveS3(in)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	s, err := s3.New(cfg, m.HTTP)
	if err != nil {
		return err
	}
	return s.Test(ctx)
}

// SaveS3 stores the settings and credentials and makes S3 the chosen provider.
// It does not contact the bucket; the UI offers a separate test.
func (m *Manager) SaveS3(in S3Input) error {
	if err := license.Require(m.license); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.resolveS3(in)
	if err != nil {
		return err
	}
	creds, _ := json.Marshal(s3Creds{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey})
	if err := m.secrets.Set(S3, string(creds)); err != nil {
		return errors.New("could not save the secret key to the system keychain: " + err.Error())
	}
	set := S3Settings{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, Prefix: cfg.Prefix, PathStyle: cfg.PathStyle}
	b, _ := json.Marshal(set)
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	tmp := m.s3File() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.s3File()); err != nil {
		return err
	}
	if err := m.saveChoice(S3); err != nil {
		return err
	}
	m.chosen, m.lastErr = S3, ""
	m.resetProvider(S3)
	m.changed()
	return nil
}

// forgetS3 deletes the saved settings and credentials.
func (m *Manager) forgetS3() error {
	if m.secrets != nil {
		if err := m.secrets.Delete(S3); err != nil {
			return err
		}
	}
	if err := os.Remove(m.s3File()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *Manager) newS3Store() (store.Store, error) {
	if err := license.Require(m.license); err != nil {
		return nil, err
	}
	set, ok := m.loadS3Settings()
	if !ok {
		return nil, ErrNotLinked
	}
	c, err := m.loadS3Creds()
	if err != nil {
		return nil, ErrNotLinked
	}
	return s3.New(s3.Config{
		Endpoint: set.Endpoint, Region: set.Region, Bucket: set.Bucket, Prefix: set.Prefix,
		PathStyle: set.PathStyle, AccessKey: c.AccessKey, SecretKey: c.SecretKey,
	}, m.HTTP)
}
