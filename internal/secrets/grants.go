package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Grant gives one task one more profile until Expires.
type Grant struct {
	Project string    `json:"project"`
	Task    string    `json:"task"`
	Profile string    `json:"profile"`
	Expires time.Time `json:"expires"`
	By      string    `json:"by"`
	Created time.Time `json:"created"`
}

// MaxGrant caps a grant's lifetime.
const MaxGrant = 7 * 24 * time.Hour

func (s *Server) grantsPath() string { return filepath.Join(s.cfg.StateDir, "grants.json") }

// liveGrants returns unexpired grants; the caller holds s.mu.
func (s *Server) liveGrants() ([]Grant, error) {
	b, err := os.ReadFile(s.grantsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Grant
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	now := s.now()
	out := all[:0]
	for _, g := range all {
		if g.Expires.After(now) {
			out = append(out, g)
		}
	}
	return out, nil
}

// saveGrants writes gs atomically; the caller holds s.mu.
func (s *Server) saveGrants(gs []Grant) error {
	if gs == nil {
		gs = []Grant{}
	}
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.grantsPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.grantsPath())
}

func (s *Server) granted(project, task, profile string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	gs, err := s.liveGrants()
	if err != nil {
		return false
	}
	for _, g := range gs {
		if g.Project == project && g.Task == task && g.Profile == profile {
			return true
		}
	}
	return false
}
