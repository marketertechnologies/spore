package fleet

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/task"
)

func TestResolveRoleBodyProjectOverride(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	writeRoleBodies(t, root)
	got, err := ResolveRoleBody(root, "reviewer.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "bootstrap", "roles", "reviewer.md"); got != want {
		t.Errorf("path = %q, want project file %q", got, want)
	}
}

func TestResolveRoleBodyBundled(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := newGitRepoFor(t, "demo-project")
	for _, name := range []string{"engineer.md", "reviewer.md", "reviewer-code-review.md"} {
		want, err := spore.BundledRoles.ReadFile("bootstrap/roles/" + name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ResolveRoleBody(root, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if dir := filepath.Join(state, "spore", "demo-project", "roles"); filepath.Dir(filepath.Dir(got)) != dir || filepath.Base(got) != name {
			t.Errorf("%s path = %q, want %s/<sha>/%s", name, got, dir, name)
		}
		b, err := os.ReadFile(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != string(want) {
			t.Errorf("%s content differs from the bundled body", name)
		}

		if err := os.WriteFile(got, []byte("stale\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveRoleBody(root, name); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(got); string(b) != string(want) {
			t.Errorf("%s: stale copy not refreshed", name)
		}
	}
}

func TestResolveRoleBodyUnknown(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	if _, err := ResolveRoleBody(root, "qa.md"); err == nil {
		t.Fatal("expected error for a body in neither source")
	}
}

func TestResolveRoleBodyPathTracksContent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	eng, err := ResolveRoleBody(root, "engineer.md")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := ResolveRoleBody(root, "reviewer.md")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(eng) == filepath.Dir(rev) {
		t.Errorf("bodies with different content share dir %s", filepath.Dir(eng))
	}
}

func TestResolveRoleBodyBrokenOverride(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	dir := filepath.Join(root, "bootstrap", "roles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gone.md"), filepath.Join(dir, "reviewer.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoleBody(root, "reviewer.md"); err == nil || !strings.Contains(err.Error(), "dangling") {
		t.Errorf("dangling override: err = %v, want dangling symlink error", err)
	}
	if err := os.Symlink("engineer.md", filepath.Join(dir, "engineer.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoleBody(root, "engineer.md"); err == nil {
		t.Error("looping override: want error, got bundled fallback")
	}
}

func TestResolveRoleBodyDanglingDirSymlink(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	if err := os.MkdirAll(filepath.Join(root, "bootstrap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "moved-checkout", "roles"), filepath.Join(root, "bootstrap", "roles")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoleBody(root, "reviewer.md"); err == nil || !strings.Contains(err.Error(), "dangling") {
		t.Errorf("err = %v, want dangling symlink error for the roles dir", err)
	}
}

func TestResolveRoleBodyNotRegular(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	if err := os.MkdirAll(filepath.Join(root, "bootstrap", "roles", "reviewer.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoleBody(root, "reviewer.md"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("err = %v, want not a regular file", err)
	}
}

// An override that exists but is unreadable or empty fails the
// resolve with a *RoleBodyError; it never falls back to the bundled
// body or a bare agent.
func TestResolveRoleBodyUnreadableOrEmpty(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
		mode             os.FileMode
	}{
		{"empty", "", "is empty", 0o644},
		{"whitespace", " \n\t\n", "is empty", 0o644},
		{"unreadable", "role body", "is unreadable", 0o000},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.mode == 0 && os.Geteuid() == 0 {
				t.Skip("root reads mode 000 files")
			}
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			root := newGitRepoFor(t, "demo-project")
			dir := filepath.Join(root, "bootstrap", "roles")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "engineer.md"), []byte(c.body), c.mode); err != nil {
				t.Fatal(err)
			}
			_, err := ResolveRoleBody(root, "engineer.md")
			var be *RoleBodyError
			if !errors.As(err, &be) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want *RoleBodyError containing %q", err, c.want)
			}
			if _, err := EngineerSpec(root, "x").TmuxArgs(); !errors.As(err, &be) {
				t.Errorf("TmuxArgs err = %v, want the role body error", err)
			}
		})
	}
}

// A roles dir the spawner cannot search fails the stat itself; the
// error keeps the OS cause and names the path once.
func TestResolveRoleBodyStatErrorKeepsCause(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches mode 000 dirs")
	}
	root := newGitRepoFor(t, "demo-project")
	dir := filepath.Join(root, "bootstrap", "roles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "engineer.md"), []byte("role body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := ResolveRoleBody(root, "engineer.md")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v, want it to wrap fs.ErrPermission", err)
	}
	p := filepath.Join(dir, "engineer.md")
	want := "rolespawn: project role body " + p + " cannot be read: permission denied"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// A drive whose engineer override is empty spawns nothing and tells
// the coordinator once; fixing the body spawns the pane and re-arms
// the notice.
func TestDriveRoleLoopNotifiesBrokenRoleBody(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	stateDir := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", stateDir)
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	body := filepath.Join(root, "bootstrap", "roles", "engineer.md")
	if err := os.MkdirAll(filepath.Dir(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(body, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	panes := stubPanes(t)
	spawnRole = func(spec RoleSpawnSpec) (string, bool, error) {
		if _, err := spec.TmuxArgs(); err != nil {
			return "", false, err
		}
		s, _ := spec.SessionName()
		panes.spawns = append(panes.spawns, s)
		panes.panes[s] = &paneProbe{Exists: true, Command: "claude", Created: panes.now}
		return s, true, nil
	}
	inbox := filepath.Join(stateDir, "demo-project", "inbox")
	for range 2 {
		if _, err := DriveRoleLoop(root, tasksDir, slug); err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("drive err = %v, want the role body error", err)
		}
	}
	if len(panes.spawns) != 0 {
		t.Fatalf("spawned %v with an empty role body", panes.spawns)
	}
	envs := readInboxEnvelopes(t, inbox)
	if len(envs) != 1 || !strings.Contains(envs[0].Body, "demo: engineer not spawned") || !strings.Contains(envs[0].Body, "engineer.md is empty") {
		t.Fatalf("envelopes = %+v, want one role body notice", envs)
	}
	if err := os.WriteFile(body, []byte("engineer body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DriveRoleLoop(root, tasksDir, slug); err != nil {
		t.Fatal(err)
	}
	if len(panes.spawns) != 1 {
		t.Fatalf("spawns after fix = %v", panes.spawns)
	}
	matches, _ := filepath.Glob(filepath.Join(task.RoleTaskDir(root, slug), "state", roleBodyNoticeStem+"*"))
	if len(matches) != 0 {
		t.Errorf("notice markers left after a clean spawn: %v", matches)
	}
}

func TestWarnRoleOnce(t *testing.T) {
	var warn strings.Builder
	defer func(w io.Writer) { roleWarnings = w }(roleWarnings)
	roleWarnings = &warn
	msg := "rolespawn: " + t.Name() + "\n"
	warnRoleOnce("%s", msg)
	warnRoleOnce("%s", msg)
	if warn.String() != msg {
		t.Errorf("warnings = %q, want one %q", warn.String(), msg)
	}
}

func TestResolveRoleBodyBootstrapScript(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	if err := os.WriteFile(filepath.Join(root, "bootstrap"), []byte("#!/bin/sh\nautoreconf -i\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRoleBody(root, "reviewer.md")
	if err != nil {
		t.Fatalf("bootstrap script blocked the bundled fallback: %v", err)
	}
	if strings.HasPrefix(got, root) {
		t.Errorf("path = %q, want the bundled body", got)
	}
	if _, err := EngineerSpec(root, "feature-x").TmuxArgs(); err != nil {
		t.Errorf("engineer TmuxArgs: %v", err)
	}
}

// The bundled bodies carry the comment-ID contract the driver and the
// validator rely on.
func TestBundledRoleBodiesCommentIDContract(t *testing.T) {
	want := map[string][]string{
		"reviewer.md": {
			"$SPORE_REVIEWER_PREFIX", "$SPORE_REVIEWER_LABEL", "`<prefix><round>.<n>`",
			"then `medium`, then `low`,\nthen `question`", "By default slot A is this\nrequirements review",
			"Check the `pr` title and body",
		},
		"reviewer-code-review.md": {
			"$SPORE_REVIEWER_PREFIX", "`<prefix><round>.<n>`", "authoritative numbered list",
			"you run last\n(slot B)",
			"`$SPORE_REVIEW_EFFORT <pr_number>`. Do not pass `--comment`",
			"| `file:line`        | `where`     |", "| `failure_scenario` | `trigger`   |", "or a finding\nwith no `category`, maps to the closest listed one",
		},
		"engineer.md": {
			"`<prefix><round>.<n>`", "`R1.3` is `\"comment\": 2`", "`detail`", "`sha`", "- `pr`: the PR title and description",
		},
	}
	for name, phrases := range want {
		b, err := spore.BundledRoles.ReadFile("bootstrap/roles/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range phrases {
			if !strings.Contains(string(b), p) {
				t.Errorf("%s lacks %q", name, p)
			}
		}
	}
}

// Reviewer B must not post the skill's unnumbered inline comments: the
// mirror posts the graded ones, anchored and led by their IDs.
func TestBundledCodeReviewBodyDoesNotPost(t *testing.T) {
	b, err := spore.BundledRoles.ReadFile("bootstrap/roles/reviewer-code-review.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "<pr_number> --comment") {
		t.Error("reviewer-code-review.md still invokes the skill with --comment")
	}
}
