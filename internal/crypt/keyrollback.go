// keyrollback.go: what happens when the cloud shows an older key header than this
// computer has used, or none.

package crypt

import (
	"context"
	"errors"
	"time"

	"github.com/JGugino/world-hopper/internal/store"
)

// The key header lives in the cloud account like everything else, so a provider
// (or an accident) can roll it back to an older one or remove it. This computer
// remembers the newest header it has used (it holds no secret), notices when
// the cloud shows an older one, and stops. Two ways forward, both the person's
// to choose.

// RestoreKeyHeader writes this computer's saved copy of the newest key header
// back to the cloud, undoing a rollback or deletion of it. Nothing else is
// touched. It does not help if the provider is hiding the header rather than
// having lost it; the answer then is still "older", and nothing is changed.
func (m *Manager) RestoreKeyHeader(ctx context.Context) error {
	m.mu.Lock()
	saved, gen := m.cfg.Header, m.cfg.KeyGen
	m.mu.Unlock()
	if saved == nil || gen < 1 {
		return ErrNoSavedHeader
	}
	h := *saved
	h.gen = gen
	if _, err := h.validate(); err != nil {
		return err
	}
	if err := WriteHeader(ctx, m.Inner, &h); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	cur, err := ReadHeader(ctx, m.Inner)
	if err != nil {
		return err
	}
	if cur.Generation() < gen {
		return ErrKeyRollback // still not shown
	}
	m.mu.Lock()
	m.rollback = false
	m.checked = time.Time{}
	m.mu.Unlock()
	return nil
}

// AcceptKeyRollback takes the cloud as it is: this computer forgets the newer
// key (and the copy in the keychain) and goes by whatever header the cloud shows
// now. If it shows none, encryption is turned off on this computer so a new key
// can be set up. Either way the person then enters a passphrase again. Data
// written under the newer key stays where it is and is not readable here.
func (m *Manager) AcceptKeyRollback(ctx context.Context) error {
	_, err := ReadHeader(ctx, m.Inner)
	if err != nil && !errors.Is(err, ErrNoKey) {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rekey != nil && m.rekey.State == RekeyRunning {
		return ErrRekeying
	}
	if m.Keys != nil {
		if m.hdr != nil {
			m.Keys.Delete(m.hdr.ID())
		}
		if m.cfg.KeyID != "" {
			m.Keys.Delete(m.cfg.KeyID)
		}
	}
	m.key, m.enc, m.hdr = nil, nil, nil
	m.rollback = false
	if errors.Is(err, ErrNoKey) {
		m.cfg = settings{}
	} else {
		m.cfg = settings{Enabled: true}
	}
	return m.save()
}
