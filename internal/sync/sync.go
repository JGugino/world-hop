// Package sync holds the sync engine entry points. For now these are stubs;
// the real engine (chunking, manifests, stores) is built in later steps.
package sync

import (
	"context"
	stdsync "sync"
	"time"
)

type Status struct {
	State      string     `json:"state"` // "idle" | "syncing"
	LastSync   *time.Time `json:"lastSync"`
	LastResult string     `json:"lastResult"`
	Version    string     `json:"version"`
}

type Service struct {
	mu     stdsync.Mutex
	status Status
	delay  time.Duration
}

func New(version string) *Service {
	return &Service{
		status: Status{State: "idle", LastResult: "No sync has run yet.", Version: version},
		delay:  500 * time.Millisecond,
	}
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// ForceSync runs a (stubbed) sync. If one is already running it returns the
// current status without starting another.
func (s *Service) ForceSync(ctx context.Context) Status {
	s.mu.Lock()
	if s.status.State == "syncing" {
		st := s.status
		s.mu.Unlock()
		return st
	}
	s.status.State = "syncing"
	s.mu.Unlock()

	// TODO: pull/push via internal/store once implemented.
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.status.State = "idle"
	s.status.LastSync = &now
	s.status.LastResult = "Stub sync completed (no worlds configured yet)."
	return s.status
}
