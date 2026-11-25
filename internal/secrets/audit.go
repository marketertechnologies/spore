package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/versality/spore/internal/redact"
)

type auditEntry struct {
	Time    time.Time `json:"time"`
	UID     int       `json:"uid"`
	PID     int       `json:"pid"`
	User    string    `json:"user"`
	Pilot   bool      `json:"pilot,omitempty"`
	Op      string    `json:"op"`
	Project string    `json:"project,omitempty"`
	Task    string    `json:"task,omitempty"`
	Profile string    `json:"profile,omitempty"`
	Command string    `json:"command,omitempty"`
	Args    []string  `json:"args,omitempty"`
	Branch  string    `json:"branch,omitempty"`
	Exit    int       `json:"exit"`
	Error   string    `json:"error,omitempty"`
}

// audit appends one JSON line per call to <state_dir>/audit.log.
// Arguments are redacted against the caller's project secrets: an
// agent could pass a value it guessed.
func (s *Server) audit(c Caller, req Request, resp Response) {
	r := s.redactor(req.Project)
	args := make([]string, len(req.Args))
	for i, a := range req.Args {
		args[i] = r.String(a)
	}
	e := auditEntry{
		Time: s.now().UTC(), UID: c.UID, PID: c.PID, User: c.Name, Pilot: c.Pilot,
		Op: req.Op, Project: req.Project, Task: req.Task, Profile: req.Profile,
		Command: req.Command, Args: args, Branch: req.Branch, Exit: resp.Exit, Error: r.String(resp.Error),
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.cfg.StateDir, "audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		s.logf("secrets: audit: %v", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
	if resp.Error != "" {
		s.logf("secrets: %s uid=%d op=%s profile=%s: %s", c.Name, c.UID, req.Op, req.Profile, e.Error)
	}
}

// redactor covers the global secrets plus project's own; an unknown
// project gets every project's, so nothing slips through on a typo.
func (s *Server) redactor(project string) *redact.Redactor {
	if _, ok := s.cfg.Projects[project]; ok {
		return redact.NewFromFiles(s.cfg.envFiles(project))
	}
	files := []string{filepath.Join(s.cfg.SecretsDir, "secrets.env")}
	for name := range s.cfg.Projects {
		files = append(files, filepath.Join(s.cfg.SecretsDir, name, "secrets.env"))
	}
	return redact.NewFromFiles(files)
}
