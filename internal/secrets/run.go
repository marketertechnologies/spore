package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/versality/spore/internal/redact"
)

// maxOutput caps each of stdout and stderr returned to the caller.
const maxOutput = 4 << 20

// loadVars reads the layered secrets files for project into a map.
func (s *Server) loadVars(project string) (map[string]string, error) {
	vars := map[string]string{}
	for _, f := range s.cfg.envFiles(project) {
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, kv := range redact.ParseEnv(b) {
			vars[kv[0]] = kv[1]
		}
	}
	return vars, nil
}

func (s *Server) run(c Caller, req Request) Response {
	ok, _ := s.allowed(c, req, req.Profile)
	if !ok {
		return Response{Error: fmt.Sprintf("refused: profile %q is not available to this caller (spore secrets list)", req.Profile)}
	}
	p := s.cfg.Profiles[req.Profile]
	cmd, found := p.Commands[req.Command]
	if !found {
		return Response{Error: fmt.Sprintf("refused: profile %q has no command %q (have %s)", req.Profile, req.Command, strings.Join(commandNames(p), ", "))}
	}
	if err := cmd.checkArgs(req.Args); err != nil {
		return Response{Error: fmt.Sprintf("refused: %s %s: %v", req.Profile, req.Command, err)}
	}
	if req.Stdin != "" && !cmd.Stdin {
		return Response{Error: fmt.Sprintf("refused: %s %s does not take stdin", req.Profile, req.Command)}
	}
	all, err := s.loadVars(p.Project)
	if err != nil {
		return Response{Error: "load secrets: " + err.Error()}
	}
	env := map[string]string{}
	for name, key := range p.Vars {
		v, ok := all[key]
		if !ok {
			return Response{Error: fmt.Sprintf("profile %q: %s is not set in the %s secrets", req.Profile, key, p.Project)}
		}
		env[name] = v
	}
	argv := make([]string, 0, len(cmd.Argv)+len(req.Args))
	for _, a := range cmd.Argv {
		argv = append(argv, varRE.ReplaceAllStringFunc(a, func(m string) string {
			return env[m[2:len(m)-1]]
		}))
	}
	argv = append(argv, req.Args...)
	res := s.exec(argv, env, req.Stdin, cmd.timeout)
	r := redact.NewFromFiles(s.cfg.envFiles(p.Project))
	res.Stdout, res.Stderr = r.String(res.Stdout), r.String(res.Stderr)
	return res
}

func commandNames(p Profile) []string {
	var out []string
	for k := range p.Commands {
		out = append(out, k)
	}
	return out
}

// exec runs argv in a scratch dir with only PATH, a scratch HOME and
// env. Output is bounded; a longer stream is cut and flagged.
func (s *Server) exec(argv []string, env map[string]string, stdin string, timeout time.Duration) Response {
	dir, err := os.MkdirTemp("", "spore-secrets-run-")
	if err != nil {
		return Response{Error: err.Error()}
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "LANG=C.UTF-8"}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, stderr := &capBuffer{max: maxOutput}, &capBuffer{max: maxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	resp := Response{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		switch {
		case ctx.Err() != nil:
			resp.Exit = 124
			resp.Error = fmt.Sprintf("timed out after %s", timeout)
		case errors.As(err, &exit):
			resp.Exit = exit.ExitCode()
		default:
			resp.Exit = 127
			resp.Error = err.Error()
		}
	}
	resp.Stdout, resp.Stderr = stdout.String(), stderr.String()
	resp.Truncated = stdout.cut || stderr.cut
	return resp
}

type capBuffer struct {
	bytes.Buffer
	max int
	cut bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		b.cut = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
