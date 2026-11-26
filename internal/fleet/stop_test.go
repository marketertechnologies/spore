package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/hooks"
	"github.com/versality/spore/internal/secrets"
	"github.com/versality/spore/internal/task"
)

func TestManagedSession(t *testing.T) {
	for _, tc := range []struct {
		name, project string
		wantProj      string
		wantSlug      string
	}{
		{"spore/acme/coordinator", "", "acme", "coordinator"},
		{"spore/acme/fix-bug_2.1", "", "acme", "fix-bug_2.1"},
		{"spore-role/acme/feat/engineer", "", "acme", "feat"},
		{"spore-role/acme/feat/reviewer-A", "", "acme", "feat"},
		{"spore/acme/feat", "acme", "acme", "feat"},
		{"spore/acme/feat", "other", "", ""},
		{"spore/pilot/andrei", "", "", ""},
		{"spore/pilot/andrei", "pilot", "", ""},
		{"spore/acme", "", "", ""},
		{"spore/acme/feat/extra", "", "", ""},
		{"spore/acme/feat x", "", "", ""},
		{"spore/acme/", "", "", ""},
		{"spore-role/acme/feat/shell", "", "", ""},
		{"spore-role/acme/feat/reviewer-", "", "", ""},
		{"spore-role/acme/feat", "", "", ""},
		{"sporex/acme/feat", "", "", ""},
		{"x spore/acme/feat", "", "", ""},
		{"keepalive", "", "", ""},
	} {
		proj, slug, ok := ManagedSession(tc.name, tc.project)
		if ok != (tc.wantProj != "") || proj != tc.wantProj || slug != tc.wantSlug {
			t.Errorf("ManagedSession(%q, %q) = %q, %q, %v; want %q, %q", tc.name, tc.project, proj, slug, ok, tc.wantProj, tc.wantSlug)
		}
	}
}

func newTmuxSession(t *testing.T, name string, env ...string) {
	t.Helper()
	cmd := exec.Command("tmux", "-L", testTmuxSocket, "new-session", "-d", "-s", name)
	for _, kv := range env {
		cmd.Args = append(cmd.Args, "-e", kv)
	}
	cmd.Args = append(cmd.Args, "sleep 300")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session %s: %v: %s", name, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", testTmuxSocket, "kill-session", "-t", "="+name).Run() })
}

func agentEnv(project, slug string) []string {
	return []string{"SPORE_TASK_SLUG=" + slug, "WT_PROJECT=" + project}
}

// stopFixture starts one session of every kind for projects a and b,
// plus sessions spore did not spawn.
func stopFixture(t *testing.T, a, b string) {
	t.Helper()
	newTmuxSession(t, "spore/"+a+"/coordinator", agentEnv(a, "coordinator")...)
	newTmuxSession(t, "spore/"+a+"/w1", agentEnv(a, "w1")...)
	newTmuxSession(t, "spore-role/"+a+"/r1/engineer", agentEnv(a, "r1")...)
	newTmuxSession(t, "spore-role/"+a+"/r1/reviewer-A", agentEnv(a, "r1")...)
	newTmuxSession(t, "spore/"+b+"/w2", agentEnv(b, "w2")...)
	// The operator's shells, a session that borrows the name shape, and
	// one whose env names another task.
	newTmuxSession(t, "spore/pilot/op", agentEnv("pilot", "op")...)
	newTmuxSession(t, "spore/"+a+"/scratch")
	newTmuxSession(t, "spore/"+a+"/w3", agentEnv(a, "other")...)
}

func liveSessions(t *testing.T) []string {
	t.Helper()
	names, err := task.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestStopKillsAgentSessionsOnly(t *testing.T) {
	requireToolchain(t)
	mustEnable(t)
	stopFixture(t, "stopa", "stopb")

	res, err := Stop("stopa")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if on, _ := Enabled(); on {
		t.Error("flag still present after Stop")
	}
	want := []string{"spore-role/stopa/r1/engineer", "spore-role/stopa/r1/reviewer-A", "spore/stopa/coordinator", "spore/stopa/w1"}
	got := slices.Clone(res.Killed)
	slices.Sort(got)
	if !slices.Equal(got, want) || len(res.Failed) != 0 {
		t.Errorf("Killed = %v, Failed = %v; want %v", res.Killed, res.Failed, want)
	}
	live := liveSessions(t)
	for _, s := range want {
		if slices.Contains(live, s) {
			t.Errorf("%s still alive", s)
		}
	}
	for _, s := range []string{"spore/stopb/w2", "spore/pilot/op", "spore/stopa/scratch", "spore/stopa/w3", "keepalive"} {
		if !slices.Contains(live, s) {
			t.Errorf("%s was killed", s)
		}
	}

	res, err = Stop("")
	if err != nil {
		t.Fatalf("Stop all: %v", err)
	}
	if !slices.Equal(res.Killed, []string{"spore/stopb/w2"}) {
		t.Errorf("host-wide Killed = %v, want [spore/stopb/w2]", res.Killed)
	}
	live = liveSessions(t)
	for _, s := range []string{"spore/pilot/op", "spore/stopa/scratch", "spore/stopa/w3", "keepalive"} {
		if !slices.Contains(live, s) {
			t.Errorf("host-wide stop killed %s", s)
		}
	}
}

// A flag Stop cannot remove still lets it kill the sessions, and says
// so in its error.
func TestStopKillsWhenFlagStays(t *testing.T) {
	requireToolchain(t)
	mustEnable(t)
	p, _ := FlagPath()
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if os.Remove(p) == nil {
		t.Skip("directory permissions not enforced (running as root?)")
	}
	newTmuxSession(t, "spore/stopc/w1", agentEnv("stopc", "w1")...)

	res, err := Stop("stopc")
	if err == nil || !strings.Contains(err.Error(), "flag not removed") {
		t.Errorf("err = %v, want flag not removed", err)
	}
	if !slices.Equal(res.Killed, []string{"spore/stopc/w1"}) {
		t.Errorf("Killed = %v", res.Killed)
	}
}

func TestViaBrokerRoutesReadOnlyAndPermission(t *testing.T) {
	var calls []bool
	prev := brokerCall
	brokerCall = func(enable bool) error { calls = append(calls, enable); return nil }
	t.Cleanup(func() { brokerCall = prev })

	erofs := &fs.PathError{Op: "remove", Path: "/var/lib/spore-fleet/fleet-enabled", Err: syscall.EROFS}
	if err := viaBroker(erofs, false); err != nil {
		t.Errorf("EROFS: %v", err)
	}
	if err := viaBroker(&fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}, true); err != nil {
		t.Errorf("EACCES: %v", err)
	}
	other := &fs.PathError{Op: "remove", Path: "x", Err: errors.New("i/o error")}
	if err := viaBroker(other, false); err != other {
		t.Errorf("other error = %v, want it returned as is", err)
	}
	if !slices.Equal(calls, []bool{false, true}) {
		t.Errorf("broker calls = %v, want [false true]", calls)
	}

	brokerCall = func(bool) error { return secrets.ErrUnavailable }
	if err := viaBroker(erofs, false); err != erofs {
		t.Errorf("no broker: err = %v, want the original", err)
	}
	refusal := &secrets.BrokerError{Msg: "refused"}
	brokerCall = func(bool) error { return refusal }
	if err := viaBroker(erofs, true); err != refusal {
		t.Errorf("refusal: err = %v", err)
	}
}

// With the host config pinning the flag, SPORE_FLEET_FLAG pointing at
// an agent's own file neither enables the fleet nor lets a pass run.
func TestFlagPathHostConfigBeatsEnv(t *testing.T) {
	dir := t.TempDir()
	hostFlag := filepath.Join(dir, "host", "fleet-enabled")
	cfg := filepath.Join(dir, "secrets-broker.json")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(`{"fleet_flag":%q}`, hostFlag)), 0o644); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, "own-flag")
	if err := os.WriteFile(own, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPORE_FLEET_FLAG", own)

	if p, _ := FlagPath(); p != own {
		t.Fatalf("without host config FlagPath = %s, want the env %s", p, own)
	}
	prev := HostConfigPath
	HostConfigPath = cfg
	t.Cleanup(func() { HostConfigPath = prev })

	if p, _ := FlagPath(); p != hostFlag {
		t.Errorf("FlagPath = %s, want %s", p, hostFlag)
	}
	if on, _ := Enabled(); on {
		t.Error("Enabled = true through the env override")
	}
	dirs := newTestDirs(t)
	res, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 1})
	if err != nil || !res.Disabled {
		t.Errorf("Reconcile = %+v, %v; want Disabled", res, err)
	}
}

// A failed tmux listing must not read as "every session died": the
// pass is skipped, so a healthy worker gets no false respawn or crash
// loop.
func TestReconcileSkipsPassWhenTmuxListFails(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	t.Setenv("SPORE_COORDINATOR_AGENT", "sleep 300")
	t.Setenv("SPORE_AGENT_BINARY", "sleep 300")
	writeTask(t, dirs.tasks, "w1", "active")
	t.Cleanup(func() { killSporeSessions(dirs.project) })
	cfg := Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}

	if r, err := Reconcile(cfg); err != nil || !slices.Equal(r.Spawned, []string{"w1"}) {
		t.Fatalf("first pass = %+v, %v", r, err)
	}
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shim := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = list-sessions ]; then echo 'server exited unexpectedly' >&2; exit 1; fi\nexec %s \"$@\"\n", realTmux)
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+path)
	for pass := range MaxFastDeaths + 1 {
		r, err := Reconcile(cfg)
		if err == nil || len(r.Spawned)+len(r.Crashed) != 0 {
			t.Fatalf("pass %d with a failing listing = %+v, %v; want an error and no spawn", pass, r, err)
		}
	}
	t.Setenv("PATH", path)

	r, err := Reconcile(cfg)
	if err != nil || !slices.Equal(r.Kept, []string{"w1"}) || len(r.Spawned) != 0 {
		t.Errorf("pass after recovery = %+v, %v; want w1 kept", r, err)
	}
	evs := ledger(t, dirs.project)
	if n := countKind(evs, events.WorkerRespawn) + countKind(evs, events.WorkerCrashLoop); n != 0 {
		t.Errorf("false respawn/crash events: %s", kindsOf(evs))
	}
	if n := len(fleetNotices(t, dirs.project)); n != 0 {
		t.Errorf("coordinator got %d fleet notices, want 0", n)
	}
}

// A role loop in PhaseDone (PR ready, no panes, waiting for the
// merge) takes no MaxWorkers slot.
func TestReconcileDoneRoleLoopTakesNoSlot(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	t.Setenv("SPORE_COORDINATOR_AGENT", "sleep 300")
	t.Setenv("SPORE_AGENT_BINARY", "sleep 300")
	t.Cleanup(func() { killSporeSessions(dirs.project); killRoleSessions(dirs.project) })
	for _, slug := range []string{"a-done", "b-done"} {
		writeRoleLoopTask(t, dirs.tasks, slug, "active")
		if err := os.MkdirAll(filepath.Join(dirs.project, ".worktrees", slug), 0o755); err != nil {
			t.Fatal(err)
		}
		writeEng(1)(t, dirs.project, slug)
		writeRev(task.ReviewerA, 1, task.VerdictApprove)(t, dirs.project, slug)
		writeRev(task.ReviewerB, 1, task.VerdictApprove)(t, dirs.project, slug)
	}
	writeTask(t, dirs.tasks, "c-work", "active")

	r, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Spawned, []string{"c-work"}) || len(r.Skipped) != 0 {
		t.Errorf("Spawned = %v, Skipped = %v; want c-work spawned past the done loops", r.Spawned, r.Skipped)
	}
}

// The coordinator crash-loop notice reaches the operator: an alert
// `spore fleet status` lists, cleared once the coordinator runs again.
func TestCoordinatorCrashLoopRaisesOperatorAlert(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	agent, _ := launchScript(t, "exit 1")
	t.Setenv("SPORE_COORDINATOR_AGENT", agent)
	now := fakeClock(t)
	t.Cleanup(func() { killSporeSessions(dirs.project) })
	project, _ := task.ProjectName(dirs.project)
	cfg := Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 1}

	for range MaxFastDeaths + 2 {
		if _, err := Reconcile(cfg); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(5 * time.Second)
	}
	var crash []string
	for _, a := range hooks.ActiveAlerts() {
		if strings.Contains(a, "["+project+"] coordinator:") {
			crash = append(crash, a)
		}
	}
	if len(crash) != 1 || !strings.Contains(crash[0], "respawn stopped") {
		t.Fatalf("alerts = %q, want one coordinator crash alert", hooks.ActiveAlerts())
	}

	if err := Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	for _, a := range hooks.ActiveAlerts() {
		if strings.Contains(a, "["+project+"]") {
			t.Errorf("alert not cleared by a disabled pass: %q", a)
		}
	}
}

// A pass re-reads the flag before each spawn, so a stop that lands
// mid-pass ends it before it spawns more.
func TestStoppedRereadsFlag(t *testing.T) {
	mustEnable(t)
	var res Result
	if stopped(&res) || res.Disabled {
		t.Fatal("stopped with the flag present")
	}
	if err := Disable(); err != nil {
		t.Fatal(err)
	}
	if !stopped(&res) || !res.Disabled || len(res.Warnings) != 1 {
		t.Errorf("stopped after Disable = %+v", res)
	}
}

// Run from inside an agent pane (the coordinator, on request), Stop
// leaves the caller's own session for last instead of killing the
// process mid-sweep.
func TestStopLeavesOwnSessionForLast(t *testing.T) {
	requireToolchain(t)
	mustEnable(t)
	newTmuxSession(t, "spore/stopd/coordinator", agentEnv("stopd", "coordinator")...)
	newTmuxSession(t, "spore/stopd/w1", agentEnv("stopd", "w1")...)
	prev := stopSelf
	stopSelf = func() string { return "spore/stopd/coordinator" }
	t.Cleanup(func() { stopSelf = prev })

	res, err := Stop("stopd")
	if err != nil {
		t.Fatal(err)
	}
	if res.Self != "spore/stopd/coordinator" || !slices.Equal(res.Killed, []string{"spore/stopd/w1"}) {
		t.Errorf("res = %+v", res)
	}
	if !slices.Contains(liveSessions(t), "spore/stopd/coordinator") {
		t.Error("own session killed by Stop")
	}
}
