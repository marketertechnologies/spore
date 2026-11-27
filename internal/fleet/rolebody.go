package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/callbudget"
	"github.com/versality/spore/internal/task"
)

var roleWarnings io.Writer = os.Stderr

var warnedRole sync.Map

// warnRoleOnce reports msg once per process: the drive loop spawns
// every round, and a config warning is not news the second time.
func warnRoleOnce(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if _, dup := warnedRole.LoadOrStore(msg, true); !dup {
		fmt.Fprint(roleWarnings, msg)
	}
}

// ResolveRoleBody returns the path of role body name (e.g.
// "reviewer.md"): <projectRoot>/bootstrap/roles/<name> when present,
// else the bundled body materialized under
// <StateDirForProject>/roles/<sha>/<name>. The pane reads the body
// with cat at exec time, so the bundled copy must exist on disk; the
// content-hash directory keeps binaries of different releases
// spawning for one project from rewriting each other's body.
func ResolveRoleBody(projectRoot, name string) (string, error) {
	dir := filepath.Join(projectRoot, "bootstrap", "roles")
	project, err := projectRoleBody(projectRoot, name)
	if err != nil || project != "" {
		return project, err
	}
	body, err := fs.ReadFile(spore.BundledRoles, "bootstrap/roles/"+name)
	if err != nil {
		return "", fmt.Errorf("rolespawn: role body %s: not in %s and not bundled: %w", name, dir, err)
	}
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		warnRoleOnce("rolespawn: %s has no %s; using the bundled body alongside project overrides\n", dir, name)
	}
	state, err := task.StateDirForProject(projectRoot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	path := filepath.Join(state, "roles", hex.EncodeToString(sum[:6]), name)
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, body) {
		return path, nil
	}
	if err := writePrivateFile(path, string(body)); err != nil {
		return "", fmt.Errorf("rolespawn: materialize role body %s: %w", name, err)
	}
	return path, nil
}

// RoleBodySource describes where ResolveRoleBody takes body name from,
// without materializing the bundled copy: the override path with the
// branch its checkout is on (role bodies are read from the project
// checkout, not the task worktree, so a checkout on the wrong branch
// shows here), the bundled body with the spore version, or the error
// that fails the spawn.
func RoleBodySource(projectRoot, name string) string {
	p, err := projectRoleBody(projectRoot, name)
	if err != nil {
		return "error: " + err.Error()
	}
	if p != "" {
		return fmt.Sprintf("%s (project override, checkout %s)", p, checkoutRef(projectRoot))
	}
	if _, err := fs.Stat(spore.BundledRoles, "bootstrap/roles/"+name); err != nil {
		return fmt.Sprintf("error: role body %s not in %s and not bundled", name, filepath.Join(projectRoot, "bootstrap", "roles"))
	}
	return fmt.Sprintf("bundled %s (spore %s)", name, spore.Version())
}

// checkoutRef names the branch dir's checkout is on, or its short
// commit when detached.
func checkoutRef(dir string) string {
	if out, err := callbudget.Command("git", "-C", dir, "symbolic-ref", "-q", "--short", "HEAD").Output(); err == nil {
		return "on branch " + strings.TrimSpace(string(out))
	}
	if out, err := callbudget.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output(); err == nil {
		return "detached at " + strings.TrimSpace(string(out))
	}
	return "on an unknown branch"
}

// RoleBodyError is a project role body override that exists but
// cannot be used. The spawn fails on it instead of falling back, and
// the drive tells the coordinator (see notifyRoleBodyError).
type RoleBodyError struct {
	Path, Reason string
	Err          error
}

func (e *RoleBodyError) Unwrap() error { return e.Err }

// unusable keeps err as the cause but drops a *fs.PathError's own
// op and path from the message: Path already names the file.
func unusable(p, reason string, err error) *RoleBodyError {
	msg := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		msg = pe.Err.Error()
	}
	return &RoleBodyError{Path: p, Reason: reason + ": " + msg, Err: err}
}

func (e *RoleBodyError) Error() string {
	return fmt.Sprintf("rolespawn: project role body %s %s", e.Path, e.Reason)
}

// projectRoleBody returns the project override for name, or "" when
// a path component is absent or a parent is not a directory. A
// component that exists but is broken (dangling symlink, unreadable,
// a body that is not a regular file or holds only whitespace) is a
// *RoleBodyError, not an absence: falling back would silently drop a
// customized body.
func projectRoleBody(projectRoot, name string) (string, error) {
	p := projectRoot
	parts := []string{"bootstrap", "roles", name}
	for i, part := range parts {
		p = filepath.Join(p, part)
		_, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", unusable(p, "cannot be read", err)
		}
		fi, err := os.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", &RoleBodyError{Path: p, Reason: "is a dangling symlink"}
		case err != nil:
			return "", unusable(p, "cannot be read", err)
		case i < len(parts)-1 && !fi.IsDir():
			// e.g. an autotools-style ./bootstrap script: not an override.
			return "", nil
		case i == len(parts)-1 && !fi.Mode().IsRegular():
			return "", &RoleBodyError{Path: p, Reason: "is not a regular file"}
		}
	}
	// The pane reads the body with cat at exec time; reading it here
	// turns a body the pane could not use into a spawn error.
	body, err := os.ReadFile(p)
	if err != nil {
		return "", unusable(p, "is unreadable", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", &RoleBodyError{Path: p, Reason: "is empty"}
	}
	return p, nil
}
