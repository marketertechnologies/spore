package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
)

// Claim fills req.Project and req.Task from the caller's spore
// environment: SPORE_PROJECT_ROOT (else the cwd repo) and
// SPORE_TASK_SLUG. An explicit project wins.
func Claim(req *Request) {
	if req.Project == "" {
		if root, err := task.MainRepoRoot(os.Getenv("SPORE_PROJECT_ROOT")); err == nil {
			if name, err := task.ProjectName(root); err == nil {
				req.Project = name
			}
		}
	}
	if req.Task == "" {
		req.Task = os.Getenv("SPORE_TASK_SLUG")
	}
}

// GH runs one gh call through the broker as project's App and returns
// stdout. A non-zero exit is an error carrying stderr.
func GH(project, stdin string, args []string, timeout time.Duration) (string, error) {
	resp, _, err := Call(Request{Op: OpGH, Project: project, Args: args, Stdin: stdin}, nil, timeout)
	if err != nil {
		return "", err
	}
	if resp.Exit != 0 {
		return "", &ExitError{Code: resp.Exit, Stderr: strings.TrimSpace(resp.Stderr)}
	}
	return strings.TrimSpace(resp.Stdout), nil
}

// ExitError is a command the broker ran that exited non-zero.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Push sends branch at sha from the repo at dir to origin through the
// broker. A thin bundle goes first; if the broker lacks its base, the
// whole branch follows.
func Push(project, dir, branch, sha, lease string, timeout time.Duration) error {
	tmp, err := os.MkdirTemp("", "spore-push-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	ref := "refs/heads/" + branch
	thin := filepath.Join(tmp, "thin.bundle")
	full := filepath.Join(tmp, "full.bundle")
	send := func(path string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		req := Request{Op: OpPush, Project: project, Branch: branch, SHA: sha, Lease: lease, PayloadSize: int64(len(b))}
		_, _, err = Call(req, bytes.NewReader(b), timeout)
		return err
	}
	if bundle(dir, thin, ref, "--not", "--remotes=origin") == nil {
		err := send(thin)
		var be *BrokerError
		if err == nil || !errors.As(err, &be) || !be.NeedFull {
			return err
		}
	}
	if err := bundle(dir, full, ref); err != nil {
		return err
	}
	return send(full)
}

func bundle(dir, out string, revs ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir, "bundle", "create", "--quiet", out}, revs...)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git bundle: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// Fetch updates refs/remotes/origin/<branch> in the repo at dir from
// origin through the broker.
func Fetch(project, dir, branch string, timeout time.Duration) error {
	ref := "refs/remotes/origin/" + branch
	have := ""
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref).Output(); err == nil {
		have = strings.TrimSpace(string(out))
	}
	resp, body, err := Call(Request{Op: OpFetch, Project: project, Branch: branch, Have: have}, nil, timeout)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		var d struct{ SHA string }
		if json.Unmarshal(resp.Data, &d) == nil && d.SHA == have {
			return nil
		}
		return fmt.Errorf("secrets broker: fetch returned no bundle")
	}
	tmp, err := os.CreateTemp("", "spore-fetch-*.bundle")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	cmd := exec.Command("git", "-C", dir, "fetch", "--quiet", "--no-tags", tmp.Name(), "+"+ref+":"+ref)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git fetch bundle: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}
