// Package license decides what the free tier and Pro allow.
//
// Free syncs one world per computer. A Gumroad license key unlocks every
// world. The key is verified against Gumroad's license API, remembered on
// disk, and re-checked periodically; if Gumroad cannot be reached, Pro keeps
// working for a grace period so a plane trip or an outage does not lock
// anyone out.
//
// Gating is enforced in the sync engine via Authorize, not in the UI.
package license

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Build-time settings, supplied with -ldflags (see the makefile). They are
// never committed. Without a product ID, activation is unavailable and the app
// stays on the free tier.
var (
	// GumroadProductID is the product's ID from Gumroad (not the permalink).
	GumroadProductID string
	// PurchaseURL is the product page the UI links to. Optional.
	PurchaseURL string
)

const (
	// FreeWorlds is how many worlds the free tier syncs.
	FreeWorlds = 1
	// RecheckEvery is how often a stored key is re-verified while online.
	RecheckEvery = 7 * 24 * time.Hour
	// OfflineGrace is how long Pro keeps working without a successful check.
	OfflineGrace = 30 * 24 * time.Hour

	verifyURL = "https://api.gumroad.com/v2/licenses/verify"
)

// ErrWorldLimit means the free tier already syncs its one world.
var ErrWorldLimit = errors.New("the free version syncs 1 world; upgrade to Pro to sync more")

// ErrUnavailable means this build cannot activate keys (no product ID).
var ErrUnavailable = errors.New("license activation is not available in this build")

// ErrInvalid means Gumroad rejected the key.
var ErrInvalid = errors.New("that license key was not accepted. Check it and try again")

// Info is what the UI shows.
type Info struct {
	Tier        string     `json:"tier"` // "free" | "pro"
	FreeWorlds  int        `json:"freeWorlds"`
	FreeWorld   string     `json:"freeWorld,omitempty"` // id of the world the free tier syncs
	Key         string     `json:"key,omitempty"`       // masked
	VerifiedAt  *time.Time `json:"verifiedAt,omitempty"`
	GraceEnds   *time.Time `json:"graceEnds,omitempty"` // set when Pro is running on offline grace
	CanActivate bool       `json:"canActivate"`         // build has a product ID
	PurchaseURL string     `json:"purchaseUrl,omitempty"`
	Error       string     `json:"error,omitempty"` // last re-check problem, e.g. "refunded"
}

type saved struct {
	Key        string    `json:"key,omitempty"`
	VerifiedAt time.Time `json:"verifiedAt,omitempty"`
	FreeWorld  string    `json:"freeWorld,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
}

// Manager holds license state. It is safe for concurrent use.
type Manager struct {
	path      string
	productID string
	endpoint  string
	client    *http.Client
	now       func() time.Time

	mu sync.Mutex
	s  saved
}

// New loads state from dir/license.json (missing is fine).
func New(dir string) (*Manager, error) {
	m := &Manager{
		path:      filepath.Join(dir, "license.json"),
		productID: GumroadProductID,
		endpoint:  verifyURL,
		client:    &http.Client{Timeout: 15 * time.Second},
		now:       time.Now,
	}
	b, err := os.ReadFile(m.path)
	if err == nil {
		if err := json.Unmarshal(b, &m.s); err != nil {
			// A damaged file must not lock the user out of the free tier.
			m.s = saved{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return m, nil
}

func (m *Manager) save() error {
	b, err := json.MarshalIndent(m.s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// proLocked reports whether Pro is currently in effect. m.mu must be held.
func (m *Manager) proLocked() bool {
	if m.s.Key == "" || m.s.VerifiedAt.IsZero() {
		return false
	}
	age := m.now().Sub(m.s.VerifiedAt)
	return age < OfflineGrace && age > -time.Hour // a clock set far back is not trusted
}

// Pro reports whether every world may sync.
func (m *Manager) Pro() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proLocked()
}

// Authorize is the engine's gate: nil if the world may sync. Free tier: the
// first world to sync claims the free slot; any other world is refused.
func (m *Manager) Authorize(worldID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proLocked() {
		return nil
	}
	switch m.s.FreeWorld {
	case worldID:
		return nil
	case "":
		m.s.FreeWorld = worldID
		if err := m.save(); err != nil {
			m.s.FreeWorld = ""
			return err
		}
		return nil
	}
	return ErrWorldLimit
}

// Limited reports whether worldID is currently refused, without claiming the
// free slot. It is for display; Authorize enforces.
func (m *Manager) Limited(worldID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.proLocked() && m.s.FreeWorld != "" && m.s.FreeWorld != worldID
}

// SetFreeWorld picks which world the free tier syncs.
func (m *Manager) SetFreeWorld(worldID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(worldID) == "" {
		return errors.New("choose a world")
	}
	old := m.s.FreeWorld
	m.s.FreeWorld = worldID
	if err := m.save(); err != nil {
		m.s.FreeWorld = old
		return err
	}
	return nil
}

// Info describes the current state for the UI.
func (m *Manager) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := Info{
		Tier:        "free",
		FreeWorlds:  FreeWorlds,
		FreeWorld:   m.s.FreeWorld,
		CanActivate: m.productID != "",
		PurchaseURL: PurchaseURL,
		Error:       m.s.LastError,
	}
	if m.s.Key != "" {
		in.Key = mask(m.s.Key)
	}
	if m.proLocked() {
		in.Tier = "pro"
		v := m.s.VerifiedAt
		in.VerifiedAt = &v
		if m.now().Sub(v) > RecheckEvery {
			g := v.Add(OfflineGrace)
			in.GraceEnds = &g
		}
	}
	return in
}

func mask(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("•", len(k))
	}
	return k[:4] + "…" + k[len(k)-4:]
}

// Activate verifies key with Gumroad and, if it is good, switches to Pro.
func (m *Manager) Activate(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if m.productID == "" {
		return ErrUnavailable
	}
	if key == "" {
		return errors.New("enter your license key")
	}
	ok, err := m.verify(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.s
	m.s.Key, m.s.VerifiedAt, m.s.LastError = key, m.now(), ""
	if err := m.save(); err != nil {
		m.s = old
		return err
	}
	return nil
}

// Deactivate forgets the key on this computer (back to the free tier).
func (m *Manager) Deactivate() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.s
	m.s.Key, m.s.VerifiedAt, m.s.LastError = "", time.Time{}, ""
	if err := m.save(); err != nil {
		m.s = old
		return err
	}
	return nil
}

// Refresh re-verifies a stored key if it is due. A key Gumroad now rejects
// (refunded, charged back) drops to free at once; a network failure changes
// nothing and the offline grace keeps running.
func (m *Manager) Refresh(ctx context.Context) {
	m.mu.Lock()
	key, due := m.s.Key, m.now().Sub(m.s.VerifiedAt) >= RecheckEvery
	m.mu.Unlock()
	if key == "" || !due || m.productID == "" {
		return
	}
	ok, err := m.verify(ctx, key)
	if err != nil {
		return // offline or Gumroad down: keep the grace period running
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.Key != key {
		return // changed while we were checking
	}
	if ok {
		m.s.VerifiedAt, m.s.LastError = m.now(), ""
	} else {
		m.s.Key, m.s.VerifiedAt = "", time.Time{}
		m.s.LastError = "This license is no longer valid (refunded or disputed), so WorldHop is back on the free version."
	}
	_ = m.save()
}

type verifyResp struct {
	Success  bool `json:"success"`
	Purchase struct {
		Refunded     bool `json:"refunded"`
		Chargebacked bool `json:"chargebacked"`
		Disputed     bool `json:"disputed"`
		DisputeWon   bool `json:"dispute_won"`
	} `json:"purchase"`
}

// verify asks Gumroad about key. ok=false with a nil error means Gumroad said
// the key is not valid; a non-nil error means we could not find out.
func (m *Manager) verify(ctx context.Context, key string) (ok bool, err error) {
	form := url.Values{
		"product_id":           {m.productID},
		"license_key":          {key},
		"increment_uses_count": {"false"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("could not reach the license server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return false, fmt.Errorf("the license server is unavailable (%d)", resp.StatusCode)
	}
	var v verifyResp
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<16)).Decode(&v); err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("unexpected license server reply (%d)", resp.StatusCode)
	}
	if !v.Success {
		return false, nil
	}
	p := v.Purchase
	if p.Refunded || p.Chargebacked || (p.Disputed && !p.DisputeWon) {
		return false, nil
	}
	return true, nil
}
