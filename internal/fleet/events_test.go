package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/redact"
	"github.com/versality/spore/internal/task"
)

func ledger(t *testing.T, projectRoot string) []events.Event {
	t.Helper()
	project, err := task.ProjectName(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := events.Read(project, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func countKind(evs []events.Event, kind string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func kindsOf(evs []events.Event) string {
	var ks []string
	for _, e := range evs {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, " ")
}

func TestLedgerWorkerSpawnRespawnCrashLoop(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	t.Setenv("SPORE_COORDINATOR_AGENT", "sleep 300")
	t.Setenv("SPORE_AGENT_BINARY", "true")
	now := fakeClock(t)
	writeTask(t, dirs.tasks, "crashy", "active")
	t.Cleanup(func() { killSporeSessions(dirs.project) })
	project, _ := task.ProjectName(dirs.project)
	session := fmt.Sprintf("spore/%s/crashy", project)

	for range MaxFastDeaths + 2 {
		if _, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}); err != nil {
			t.Fatal(err)
		}
		waitNoSession(t, session)
		*now = now.Add(5 * time.Second)
	}
	evs := ledger(t, dirs.project)
	if countKind(evs, events.WorkerSpawn) != 1 || countKind(evs, events.WorkerRespawn) != MaxFastDeaths-1 || countKind(evs, events.WorkerCrashLoop) != 1 {
		t.Fatalf("ledger kinds = %s; want 1 spawn, %d respawns, 1 crashloop", kindsOf(evs), MaxFastDeaths-1)
	}
	if countKind(evs, events.CoordinatorSpawn) != 1 {
		t.Errorf("ledger kinds = %s; want one coordinator.spawn", kindsOf(evs))
	}
	for _, e := range evs {
		if strings.HasPrefix(e.Kind, "worker.") && e.Slug != "crashy" {
			t.Errorf("worker event without slug: %+v", e)
		}
	}
	if events.ProjectRoot(project) == "" {
		t.Error("project root not noted next to the ledger")
	}
}

func TestLedgerCoordinatorRespawnAndCrashLoop(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	agent, _ := launchScript(t, "exit 1")
	t.Setenv("SPORE_COORDINATOR_AGENT", agent)
	now := fakeClock(t)
	t.Cleanup(func() { killSporeSessions(dirs.project) })

	for range MaxFastDeaths + 2 {
		if _, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(5 * time.Second)
	}
	evs := ledger(t, dirs.project)
	spawns := countKind(evs, events.CoordinatorSpawn) + countKind(evs, events.CoordinatorRespawn)
	if countKind(evs, events.CoordinatorCrash) != 1 || countKind(evs, events.CoordinatorRespawn) < 1 || spawns > MaxFastDeaths {
		t.Fatalf("ledger kinds = %s; want respawns then one coordinator.crashloop", kindsOf(evs))
	}
}

func TestLedgerReconcileSkippedThrottled(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	unlock, err := task.LockReconcile(dirs.project, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		r, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3})
		if err != nil || !r.Locked {
			t.Fatalf("Reconcile = %+v, %v; want locked", r, err)
		}
	}
	unlock()
	if err := Disable(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}); err != nil {
			t.Fatal(err)
		}
	}
	evs := ledger(t, dirs.project)
	if countKind(evs, events.ReconcileSkipped) != 2 {
		t.Fatalf("ledger kinds = %s; want one skip per reason (locked, disabled)", kindsOf(evs))
	}
}

func TestLedgerRoleRespawnAndCrashLoop(t *testing.T) {
	root, _ := livenessFixture(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := stubPanes(t)
	spec := EngineerSpec(root, "demo")
	session, _ := spec.SessionName()
	if err := ensureRolePane(spec, 0); err != nil {
		t.Fatal(err)
	}
	for range MaxRoleRespawns + 1 {
		f.now = f.now.Add(time.Minute)
		f.panes[session].Dead = true
		if err := ensureRolePane(spec, 0); err != nil {
			t.Fatal(err)
		}
	}
	evs := ledger(t, root)
	if countKind(evs, events.RoleRespawn) != MaxRoleRespawns || countKind(evs, events.RoleCrashLoop) != 1 {
		t.Fatalf("ledger kinds = %s; want %d role.respawn and 1 role.crashloop", kindsOf(evs), MaxRoleRespawns)
	}
	if evs[0].Fields["role"] != "engineer" || evs[0].Slug != "demo" {
		t.Errorf("first event = %+v", evs[0])
	}
}

func TestLedgerRolePhaseAndVerdictOnce(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	slug := "demo"
	if _, err := task.EnsureRoleTaskDir(root, slug); err != nil {
		t.Fatal(err)
	}
	snap := Snapshot{Phase: PhaseEngineerInitial}
	recordRoleProgress(root, slug, snap)
	recordRoleProgress(root, slug, snap)

	if err := task.WriteEngineerResponse(root, slug, 1, task.EngineerResponse{Notes: "r1"}); err != nil {
		t.Fatal(err)
	}
	mustWriteReview(t, root, slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictApprove, Summary: "ok"})
	snap = Snapshot{Phase: PhaseReviewB, EngineerRound: 1, CurrentReviewer: task.ReviewerB}
	for range 3 {
		recordRoleProgress(root, slug, snap)
	}
	evs := ledger(t, root)
	if countKind(evs, events.RolePhase) != 2 || countKind(evs, events.RoleVerdict) != 1 {
		t.Fatalf("ledger kinds = %s; want 2 role.phase, 1 role.verdict", kindsOf(evs))
	}
	for _, e := range evs {
		if e.Kind == events.RoleVerdict && (e.Fields["verdict"] != task.VerdictApprove || e.Fields["reviewer"] != string(task.ReviewerA) || e.Fields["round"] != "1") {
			t.Errorf("verdict event = %+v", e)
		}
		if e.Kind == events.RolePhase && e.Detail == string(PhaseReviewB) && e.Fields["from"] != string(PhaseEngineerInitial) {
			t.Errorf("phase event = %+v", e)
		}
	}
	if b, err := os.ReadFile(filepath.Join(task.RoleTaskDir(root, slug), "spec.md")); err == nil && strings.Contains(kindsOf(evs), string(b)) {
		t.Error("spec text in ledger")
	}
}

func TestLedgerPROpenedByMirror(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeEng(1)(t, f.root, f.slug)
	f.tick(t)
	f.tick(t)
	evs := ledger(t, f.root)
	if countKind(evs, events.PROpened) != 1 {
		t.Fatalf("ledger kinds = %s; want one pr.opened", kindsOf(evs))
	}
	for _, e := range evs {
		if e.Kind == events.PROpened && (e.Fields["url"] != "https://github.com/acme/widget/pull/12" || e.Slug != "demo") {
			t.Errorf("pr.opened = %+v", e)
		}
	}
}

// checkRedactionEvents: the mirror recorded a redaction event per
// pattern class, attributed to the task and the pr edit --title, and
// no event carries a redacted value.
func checkRedactionEvents(t *testing.T, root, canary string) {
	t.Helper()
	classes := map[string]bool{}
	var titleHit bool
	for _, e := range ledger(t, root) {
		if e.Kind != events.Redaction {
			continue
		}
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), canary) || strings.Contains(string(raw), "ghp_") || e.Slug != "demo" || e.Fields["via"] != "mirror" {
			t.Errorf("redaction event = %s", raw)
		}
		classes[e.Fields["class"]] = true
		titleHit = titleHit || (e.Fields["cmd"] == "pr edit" && e.Fields["where"] == "--title")
	}
	for _, c := range []string{redact.ClassSecretsEnv, redact.ClassToken, redact.ClassPEM, redact.ClassHomePath} {
		if !classes[c] {
			t.Errorf("no redaction event of class %s; got %v", c, classes)
		}
	}
	if !titleHit {
		t.Error("no redaction event for the pr edit --title")
	}
}
