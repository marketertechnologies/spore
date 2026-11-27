package secrets

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/versality/spore/internal/ghapp"
)

// Server is the broker. One process serves every project on the host.
type Server struct {
	cfg *Config
	// Now defaults to time.Now; tests pin it.
	Now func() time.Time
	// Log receives one line per refused or failed call (the journal
	// under systemd). Defaults to os.Stderr.
	Log io.Writer

	agentUID int
	pilotGID string

	mu     sync.Mutex
	tokens map[string]ghapp.Token
	gitMu  map[string]*sync.Mutex

	// mint and remote replace the App token and the https remote in
	// tests; ghSocket points brokered gh calls at a stub API.
	mint     func(ctx context.Context, project string) (string, error)
	remote   func(repo string) string
	ghSocket string
}

// NewServer prepares a broker for cfg. The agent user and pilot group
// are resolved once, at start.
func NewServer(cfg *Config) (*Server, error) {
	s := &Server{cfg: cfg, tokens: map[string]ghapp.Token{}, gitMu: map[string]*sync.Mutex{}}
	s.agentUID = os.Getuid()
	if cfg.AgentUser != "" {
		u, err := user.Lookup(cfg.AgentUser)
		if err != nil {
			return nil, fmt.Errorf("secrets: agent_user: %w", err)
		}
		if s.agentUID, err = strconv.Atoi(u.Uid); err != nil {
			return nil, err
		}
	}
	if cfg.PilotGroup != "" {
		g, err := user.LookupGroup(cfg.PilotGroup)
		if err != nil {
			return nil, fmt.Errorf("secrets: pilot_group: %w", err)
		}
		s.pilotGID = g.Gid
		if u, err := user.LookupId(strconv.Itoa(s.agentUID)); err == nil {
			if gids, err := u.GroupIds(); err == nil && slices.Contains(gids, g.Gid) {
				return nil, fmt.Errorf("secrets: agent user %s is in pilot group %s; agents could grant themselves profiles", u.Username, cfg.PilotGroup)
			}
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o750); err != nil {
		return nil, fmt.Errorf("secrets: state dir: %w", err)
	}
	return s, nil
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) logf(format string, a ...any) {
	w := s.Log
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format+"\n", a...)
}

// Caller is the peer of one connection.
type Caller struct {
	UID, PID int
	// Pilot is root or a member of the pilot group: a human.
	Pilot bool
	// Agent is the agent user (and not a pilot).
	Agent bool
	Name  string
}

func (s *Server) caller(conn net.Conn) (Caller, error) {
	uid, pid, err := peerCred(conn)
	if err != nil {
		return Caller{}, err
	}
	c := Caller{UID: uid, PID: pid, Name: strconv.Itoa(uid)}
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		c.Name = u.Username
		if s.pilotGID != "" {
			if gids, err := u.GroupIds(); err == nil && slices.Contains(gids, s.pilotGID) {
				c.Pilot = true
			}
		}
	}
	if uid == 0 {
		c.Pilot = true
	}
	c.Agent = !c.Pilot && uid == s.agentUID
	if !c.Pilot && !c.Agent {
		return c, fmt.Errorf("uid %d is neither the agent user nor a pilot", uid)
	}
	return c, nil
}

// Listen returns the socket-activated listener when systemd passed
// one (LISTEN_FDS), else listens on path.
func Listen(path string) (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") == "1" {
		f := os.NewFile(3, "spore-secrets.socket")
		l, err := net.FileListener(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("secrets: socket activation: %w", err)
		}
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Serve accepts connections until l is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Minute))
	c, cerr := s.caller(conn)
	br := bufio.NewReader(conn)
	var req Request
	if err := readLine(br, &req); err != nil {
		s.logf("secrets: uid %d: bad request: %v", c.UID, err)
		return
	}
	var payload []byte
	if req.PayloadSize > 0 {
		if req.PayloadSize > maxPayload {
			s.reply(conn, c, req, Response{Error: "payload too large"}, nil)
			return
		}
		payload = make([]byte, req.PayloadSize)
		if _, err := io.ReadFull(br, payload); err != nil {
			s.logf("secrets: uid %d: short payload: %v", c.UID, err)
			return
		}
	}
	if cerr != nil {
		s.reply(conn, c, req, Response{Error: "refused: " + cerr.Error()}, nil)
		return
	}
	resp, out := s.dispatch(c, req, payload)
	s.reply(conn, c, req, resp, out)
}

func (s *Server) reply(conn net.Conn, c Caller, req Request, resp Response, payload []byte) {
	resp.PayloadSize = int64(len(payload))
	s.audit(c, req, resp)
	var r io.Reader
	if len(payload) > 0 {
		r = bytes.NewReader(payload)
	}
	if err := writeFrame(conn, resp, r, resp.PayloadSize); err != nil {
		s.logf("secrets: uid %d: write reply: %v", c.UID, err)
	}
}

func (s *Server) dispatch(c Caller, req Request, payload []byte) (Response, []byte) {
	fail := func(format string, a ...any) (Response, []byte) {
		return Response{Error: fmt.Sprintf(format, a...)}, nil
	}
	switch req.Op {
	case OpList:
		return s.list(c, req)
	case OpRun:
		return s.run(c, req), nil
	case OpGrant:
		return s.grant(c, req), nil
	case OpRevoke:
		return s.revoke(c, req), nil
	case OpGrants:
		return s.grants()
	case OpGH:
		return s.gh(c, req), nil
	case OpPush:
		return s.push(c, req, payload), nil
	case OpFetch:
		return s.fetch(c, req)
	case OpFleet:
		return s.fleet(c, req), nil
	}
	return fail("unknown op %q", req.Op)
}

// ProfileInfo is what `spore secrets list` shows: never a value.
type ProfileInfo struct {
	Name        string   `json:"name"`
	Project     string   `json:"project"`
	Description string   `json:"description,omitempty"`
	Commands    []string `json:"commands"`
	Vars        []string `json:"vars"`
	Granted     bool     `json:"granted,omitempty"`
}

func (s *Server) list(c Caller, req Request) (Response, []byte) {
	var out []ProfileInfo
	for _, name := range s.cfg.ProfileNames() {
		ok, granted := s.allowed(c, req, name)
		if !ok {
			continue
		}
		p := s.cfg.Profiles[name]
		info := ProfileInfo{Name: name, Project: p.Project, Description: p.Description, Granted: granted}
		for k := range p.Commands {
			info.Commands = append(info.Commands, k)
		}
		for k := range p.Vars {
			info.Vars = append(info.Vars, k)
		}
		slices.Sort(info.Commands)
		slices.Sort(info.Vars)
		out = append(out, info)
	}
	b, _ := json.Marshal(out)
	return Response{Data: b}, nil
}

// allowed applies the scoping rules: pilots and a caller with no task
// see every profile; any agent with a task (coordinators claim the task
// "coordinator") sees its project's profiles plus live grants.
func (s *Server) allowed(c Caller, req Request, profile string) (ok, viaGrant bool) {
	p, exists := s.cfg.Profiles[profile]
	if !exists {
		return false, false
	}
	if c.Pilot || req.Task == "" {
		return true, false
	}
	if p.Project == req.Project {
		return true, false
	}
	if s.granted(req.Project, req.Task, profile) {
		return true, true
	}
	return false, false
}

func (s *Server) grant(c Caller, req Request) Response {
	if !c.Pilot {
		return Response{Error: "refused: only root or the pilot group may grant profiles"}
	}
	if _, ok := s.cfg.Profiles[req.Profile]; !ok {
		return Response{Error: fmt.Sprintf("unknown profile %q", req.Profile)}
	}
	if req.Task == "" || req.Project == "" {
		return Response{Error: "grant needs a project and a task"}
	}
	d, err := time.ParseDuration(req.For)
	if err != nil || d <= 0 || d > MaxGrant {
		return Response{Error: fmt.Sprintf("--for %q: want a duration up to %s", req.For, MaxGrant)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gs, err := s.liveGrants()
	if err != nil {
		return Response{Error: "read grants: " + err.Error()}
	}
	now := s.now()
	g := Grant{Project: req.Project, Task: req.Task, Profile: req.Profile, Expires: now.Add(d), By: c.Name, Created: now}
	gs = slices.DeleteFunc(gs, func(o Grant) bool {
		return o.Project == g.Project && o.Task == g.Task && o.Profile == g.Profile
	})
	gs = append(gs, g)
	if err := s.saveGrants(gs); err != nil {
		return Response{Error: "write grants: " + err.Error()}
	}
	b, _ := json.Marshal(g)
	return Response{Data: b}
}

func (s *Server) revoke(c Caller, req Request) Response {
	if !c.Pilot {
		return Response{Error: "refused: only root or the pilot group may revoke grants"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gs, err := s.liveGrants()
	if err != nil {
		return Response{Error: "read grants: " + err.Error()}
	}
	n := len(gs)
	gs = slices.DeleteFunc(gs, func(o Grant) bool {
		return o.Task == req.Task && (req.Project == "" || o.Project == req.Project) && (req.Profile == "" || o.Profile == req.Profile)
	})
	if err := s.saveGrants(gs); err != nil {
		return Response{Error: "write grants: " + err.Error()}
	}
	return Response{Stdout: fmt.Sprintf("revoked %d grant(s)\n", n-len(gs))}
}

func (s *Server) grants() (Response, []byte) {
	s.mu.Lock()
	gs, err := s.liveGrants()
	s.mu.Unlock()
	if err != nil {
		return Response{Error: "read grants: " + err.Error()}, nil
	}
	b, _ := json.Marshal(gs)
	return Response{Data: b}, nil
}

// fleet flips the kill switch. Anyone allowed to call may stop the
// fleet; only a pilot may start it again.
func (s *Server) fleet(c Caller, req Request) Response {
	if s.cfg.FleetFlag == "" {
		return Response{Error: "fleet_flag is not configured"}
	}
	if !req.Enable {
		if err := os.Remove(s.cfg.FleetFlag); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Response{Error: err.Error()}
		}
		return Response{Stdout: "disabled\n"}
	}
	if !c.Pilot {
		return Response{Error: "refused: only root or the pilot group may re-enable the fleet (sudo spore fleet enable)"}
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.FleetFlag), 0o755); err != nil {
		return Response{Error: err.Error()}
	}
	f, err := os.OpenFile(s.cfg.FleetFlag, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Response{Error: err.Error()}
	}
	f.Close()
	return Response{Stdout: "enabled\n"}
}
