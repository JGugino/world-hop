package oauth

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// Token is an OAuth token set.
type Token struct {
	AccessToken  string    `json:"a"`
	RefreshToken string    `json:"r"`
	Expiry       time.Time `json:"e"`
}

// ErrNoToken means nothing is stored under the key.
var ErrNoToken = errors.New("oauth: no stored token")

// TokenStore persists tokens. The real implementation is the OS keychain; tokens
// are never written to plain files.
type TokenStore interface {
	Get(key string) (Token, error) // ErrNoToken if absent
	Set(key string, t Token) error
	Delete(key string) error
}

// KeyringStore keeps tokens in the OS keychain (Keychain, Credential Manager,
// Secret Service).
type KeyringStore struct{ Service string }

func (k KeyringStore) Get(key string) (Token, error) {
	s, err := keyring.Get(k.Service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return Token{}, ErrNoToken
	}
	if err != nil {
		return Token{}, err
	}
	var t Token
	if err := json.Unmarshal([]byte(s), &t); err != nil {
		return Token{}, err
	}
	return t, nil
}

func (k KeyringStore) Set(key string, t Token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return keyring.Set(k.Service, key, string(b))
}

func (k KeyringStore) Delete(key string) error {
	err := keyring.Delete(k.Service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// MemoryStore is an in-memory TokenStore for tests.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Token
}

func (s *MemoryStore) Get(key string) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[key]
	if !ok {
		return Token{}, ErrNoToken
	}
	return t, nil
}

func (s *MemoryStore) Set(key string, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]Token{}
	}
	s.m[key] = t
	return nil
}

func (s *MemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
