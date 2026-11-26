// Package fleet implements the worker-fleet reconciler: a short-
// lived pass that brings tmux sessions in line with the on-disk
// task queue. For each tasks/<slug>.md with status=active that has
// no live session, Reconcile spawns one (worktree + branch + tmux);
// for each session whose task no longer has status=active, it kills
// the session. Idempotent and exit-on-done; no daemon main loop.
//
// A user-level kill switch lives at the FlagPath
// (~/.local/state/spore/fleet-enabled, honouring XDG_STATE_HOME).
// Empty/missing means paused: Reconcile returns immediately without
// touching tmux. Present (any contents) means enabled.
package fleet

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/versality/spore/internal/callbudget"
	"github.com/versality/spore/internal/editor"
	"github.com/versality/spore/internal/matter"
	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

// DefaultMaxWorkers is the fallback concurrency cap when no override
// is set via flag, env, or spore.toml.
const DefaultMaxWorkers = 3

// Config drives a single reconcile pass.
type Config struct {
	TasksDir    string
	ProjectRoot string
	// MaxWorkers caps concurrent agent units: each worker session and
	// each role loop with a live pane takes one slot.
	MaxWorkers int
}

// Result is the outcome of one reconcile pass. Slug lists are
// sorted; sets are disjoint by construction (a slug appears in at most
// one of Spawned / Kept / Skipped / Crashed).
type Result struct {
	// Disabled is true when the kill-switch flag is missing and
	// Reconcile short-circuited. The slug lists are empty in that
	// case; the sessions already running (if any) are NOT reaped.
	Disabled bool

	// Locked is true when another pass or `spore task start` held the
	// project's reconcile lock and this pass did nothing.
	Locked bool

	Active  []string
	Spawned []string
	Reaped  []string
	Kept    []string
	Skipped []string
	// Crashed lists active tasks whose worker died fast MaxFastDeaths
	// times in a row; they are not respawned until a healthy run or
	// the task leaves active.
	Crashed []string

	// Matter is the per-matter sync outcome from the prelude pass.
	// Empty when no matters are configured. Errors do not abort
	// reconciliation: the worker pass still runs against whatever
	// tasks are on disk.
	Matter []MatterResult

	// Warnings are non-fatal problems found during the pass (bad task
	// files, a task that failed to spawn or reap), also printed to
	// stderr as they are found.
	Warnings []string
}

// MatterResult records one matter's sync outcome for the pass.
type MatterResult struct {
	Name    string
	Created int
	Updated int
	Err     error
}

// Reconcile runs a single pass: list active tasks, list spore-prefix
// tmux sessions, reap stale sessions, then spawn missing ones up to
// the MaxWorkers cap. Honours the kill-switch flag at FlagPath. The
// singleton coordinator session is ensured alongside the worker fleet
// when the flag is on, and reaped when the flag goes off.
func Reconcile(cfg Config) (Result, error) {
	// Two passes (timer, tasks/ path unit, Stop hook) or a pass and
	// `spore task start` racing on one task both run `git worktree
	// add` for wt/<slug>; the loser fails. The holder covers it.
	unlock, err := task.LockReconcile(cfg.ProjectRoot, 0)
	if errors.Is(err, task.ErrLockHeld) {
		return Result{Locked: true}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("reconcile lock: %w", err)
	}
	defer unlock()

	enabled, err := Enabled()
	if err != nil {
		return Result{}, err
	}
	if !enabled {
		// Worker sessions are kept alive on flag-disable so the
		// operator stays attached to in-flight work. The coordinator
		// is a kernel singleton with no operator-attached state worth
		// preserving, so we tear it down with the flag.
		ReapCoordinator(cfg.ProjectRoot)
		clearGuard(coordinatorGuardPath(cfg.ProjectRoot))
		return Result{Disabled: true}, nil
	}

	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = DefaultMaxWorkers
	}

	coordinatorWarning := superviseCoordinator(cfg.ProjectRoot)

	matterRes := syncMatters(cfg.ProjectRoot)

	metas, badTasks, err := scanTasks(cfg.TasksDir)
	if err != nil {
		return Result{}, err
	}
	statusBySlug := map[string]string{}
	activeSet := map[string]bool{}
	sortOrderBySlug := map[string]float64{}
	for _, m := range metas {
		statusBySlug[m.Slug] = m.Status
		if m.Status == "active" {
			activeSet[m.Slug] = true
		}
		if v := m.Extra[matter.MatterSortOrderKey]; v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				sortOrderBySlug[m.Slug] = f
			}
		}
	}

	running, err := task.SpawnedSlugs(cfg.ProjectRoot)
	if err != nil {
		return Result{}, err
	}
	// The coordinator shares the spore-session prefix but is not a
	// worker; filter it out before the reap loop and the cap math so
	// EnsureCoordinator above stays the sole owner of its lifecycle.
	var workerSlugs []string
	runningSet := map[string]bool{}
	for _, s := range running {
		if s == CoordinatorSlug {
			continue
		}
		workerSlugs = append(workerSlugs, s)
		runningSet[s] = true
	}

	res := Result{Matter: matterRes}
	if coordinatorWarning != "" {
		res.warn(coordinatorWarning)
	}
	res.Warnings = append(res.Warnings, noticeBadTasks(cfg.ProjectRoot, badTasks)...)

	// Reap first so freed slots count toward the cap on spawn.
	// Sessions for tasks the operator paused or blocked are kept
	// alive deliberately (the per-status semantics of pause/block).
	// Sessions whose task is done, missing, or in an unknown state
	// are reaped.
	for _, slug := range workerSlugs {
		switch statusBySlug[slug] {
		case "active", "paused", "blocked":
			res.Kept = append(res.Kept, slug)
			continue
		}
		if err := task.Reap(cfg.TasksDir, cfg.ProjectRoot, slug); err != nil {
			res.warn(fmt.Sprintf("reap %s: %v", slug, err))
			continue
		}
		res.Reaped = append(res.Reaped, slug)
		delete(runningSet, slug)
	}
	// Role panes live under spore-role/, outside SpawnedSlugs. Reap
	// them by the same status rule so a task flipped to done by hand
	// (not through `spore task done`) does not leak its panes.
	roleSlugs, err := task.SpawnedRoleSlugs(cfg.ProjectRoot)
	if err != nil {
		return res, err
	}
	for _, slug := range roleSlugs {
		switch statusBySlug[slug] {
		case "active", "paused", "blocked":
			continue
		}
		if err := task.Reap(cfg.TasksDir, cfg.ProjectRoot, slug); err != nil {
			return res, fmt.Errorf("reap %s: %w", slug, err)
		}
		if !slices.Contains(res.Reaped, slug) {
			res.Reaped = append(res.Reaped, slug)
		}
	}

	// Stable iteration: sort by matter_sort_order ascending (lower
	// spawns first; tasks without a stamp sort after stamped ones), then
	// by slug. Stamps come from matter adapters such as linear's
	// kanban sortOrder, so an upstream reorder changes which active
	// task spawns next when MaxWorkers clips the active set.
	var actives []string
	for slug := range activeSet {
		actives = append(actives, slug)
	}
	sort.SliceStable(actives, func(i, j int) bool {
		ai, aiOK := sortOrderBySlug[actives[i]]
		aj, ajOK := sortOrderBySlug[actives[j]]
		switch {
		case aiOK && ajOK:
			if ai != aj {
				return ai < aj
			}
		case aiOK:
			return true
		case ajOK:
			return false
		}
		return actives[i] < actives[j]
	})
	res.Active = actives

	workersCfg, err := LoadWorkersConfig(cfg.ProjectRoot)
	if err != nil {
		return res, err
	}
	agentCounts := agentCountsFromMetas(metas, runningSet)

	// A task with `loop: role` in its frontmatter is opted into the
	// role-loop and owned solely by DriveRoleLoop. The homogeneous
	// spawn below must skip it: otherwise Reconcile would mint a
	// generic worker at spore/<project>/<slug> (invisible to
	// runningSet, which only tracks the spore-role/<project>/ prefix)
	// on top of the role panes that edit the same wt/<slug> branch.
	// The key rides in the same file write that flips status to
	// active, so the watcher-triggered pass already sees it. The key is
	// the only opt-in: `.spore/<slug>/` presence is not, because
	// coordinators stage briefs there for generic tasks too.
	roleLooped := map[string]bool{}
	for _, m := range metas {
		switch {
		case m.Loop == task.LoopRole:
			roleLooped[m.Slug] = true
		case m.Loop != "" && m.Status == "active":
			res.warn(fmt.Sprintf("task %s: unknown loop: %q (want %q or empty); treating as a generic worker task", m.Slug, m.Loop, task.LoopRole))
		}
	}

	// One slot per agent unit: a worker session, or a role loop with
	// any live pane (engineer plus reviewers count once). Running units
	// hold their slot; new ones fill free slots in actives order.
	roleRunning := runningRoleLoops(cfg.ProjectRoot)
	used := len(runningSet) + len(roleRunning)
	var drive []string

	guards := workerGuards(cfg.ProjectRoot, activeSet)
	now := nowFunc()
	for _, slug := range actives {
		if roleLooped[slug] {
			switch {
			case roleRunning[slug]:
				drive = append(drive, slug)
			case used >= cfg.MaxWorkers:
				res.Skipped = append(res.Skipped, slug)
			default:
				drive = append(drive, slug)
				used++
			}
			continue
		}
		g := guards(slug)
		if runningSet[slug] {
			g.alive(now)
			saveGuard(g)
			continue
		}
		g.dead()
		if !g.mayRespawn() {
			if g.capOnce() {
				notifyFleet(cfg.ProjectRoot, fmt.Sprintf(
					"%s: worker died within %s of spawn %d times in a row; respawn stopped. Inspect the task, then pause and restart it to retry.",
					slug, FastDeathWindow, g.Deaths))
			}
			saveGuard(g)
			res.Crashed = append(res.Crashed, slug)
			continue
		}
		saveGuard(g)
		if used >= cfg.MaxWorkers {
			res.Skipped = append(res.Skipped, slug)
			continue
		}
		picked, err := assignAgent(cfg.TasksDir, slug, workersCfg, agentCounts)
		if err != nil {
			res.warn(fmt.Sprintf("assign agent %s: %v", slug, err))
			continue
		}
		if _, err := task.Ensure(cfg.TasksDir, slug); err != nil {
			res.warn(fmt.Sprintf("ensure %s: %v", slug, err))
			continue
		}
		g.spawned(now)
		saveGuard(g)
		used++
		// Fire OnSpawn now that the session is up: this is the
		// rover-claim signal matter adapters bind their upstream
		// "in progress" mirror to (e.g. Linear's Ready -> In
		// Progress flip). Errors from a matter push do not roll
		// back the spawn; the worker is real, the kanban catches
		// up on the next reconcile pass.
		task.NotifyMatterSpawn(cfg.ProjectRoot, cfg.TasksDir, slug, os.Stderr)
		res.Spawned = append(res.Spawned, slug)
		runningSet[slug] = true
		agentCounts[picked]++
	}

	// Advance the role-looped tasks that hold a slot. DriveRoleLoop
	// owns the worktree precondition for these slugs (see the
	// homogeneous-spawn skip above) and creates `.spore/<slug>/` itself
	// on the first tick, so a task starts moving on the same pass that
	// classified it.
	for _, slug := range drive {
		if _, err := DriveRoleLoop(cfg.ProjectRoot, cfg.TasksDir, slug); err != nil {
			fmt.Fprintf(os.Stderr, "role-loop drive %s: %v\n", slug, err)
		}
	}

	if _, err := editor.RefreshWorkspace(cfg.ProjectRoot); err != nil {
		fmt.Fprintf(os.Stderr, "code workspace refresh: %v\n", err)
	}

	sort.Strings(res.Spawned)
	sort.Strings(res.Reaped)
	sort.Strings(res.Kept)
	sort.Strings(res.Skipped)
	sort.Strings(res.Crashed)
	return res, nil
}

// runningRoleLoops returns the slugs with at least one live role pane
// (spore-role/<project>/<slug>/<role>).
func runningRoleLoops(projectRoot string) map[string]bool {
	out := map[string]bool{}
	project, err := task.ProjectName(projectRoot)
	if err != nil {
		return out
	}
	lines, err := callbudget.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return out
	}
	prefix := "spore-role/" + project + "/"
	for _, line := range strings.Split(string(lines), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !ok {
			continue
		}
		if slug, _, ok := strings.Cut(rest, "/"); ok && slug != "" {
			out[slug] = true
		}
	}
	return out
}

// workerGuards returns a loader for per-slug crash-loop records under
// the project state dir, after dropping the records of tasks that are
// no longer active (pausing a task re-arms its cap).
func workerGuards(projectRoot string, active map[string]bool) func(string) *spawnGuard {
	dir, err := fleetStateDir(projectRoot, "workers")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fleet: worker crash records: %v\n", err)
		return func(string) *spawnGuard { return &spawnGuard{} }
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if slug, ok := strings.CutSuffix(e.Name(), ".json"); !ok || !active[slug] {
			clearGuard(filepath.Join(dir, e.Name()))
		}
	}
	return func(slug string) *spawnGuard { return loadGuard(filepath.Join(dir, slug+".json")) }
}

func saveGuard(g *spawnGuard) {
	if g.path == "" {
		return
	}
	if err := g.save(); err != nil {
		fmt.Fprintf(os.Stderr, "fleet: crash record: %v\n", err)
	}
}

// warn records a per-task failure that must not abort the pass.
func (r *Result) warn(w string) {
	fmt.Fprintln(os.Stderr, w)
	r.Warnings = append(r.Warnings, w)
}

// agentCountsFromMetas tallies the agent name carried in the
// frontmatter of every running worker, plus an empty-key bucket for
// running workers whose task has no `agent:` set yet (so the ratio
// balancer never overcounts a known agent based on a task spawned
// before this commit landed). The empty-key bucket is included in the
// totals selectByRatio sees so an unassigned legacy worker does not
// inflate its counterpart's deficit.
func agentCountsFromMetas(metas []frontmatter.Meta, running map[string]bool) map[string]int {
	out := map[string]int{}
	for _, m := range metas {
		if !running[m.Slug] {
			continue
		}
		out[m.Agent]++
	}
	return out
}

// assignAgent applies SelectAgent to the task at <tasksDir>/<slug>.md,
// then writes the chosen agent back into the file's frontmatter when
// it would change. Returns the agent name. A task whose `agent:` is
// already set short-circuits without touching the file.
func assignAgent(tasksDir, slug string, cfg WorkersConfig, counts map[string]int) (string, error) {
	path := filepath.Join(tasksDir, slug+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m, body, err := frontmatter.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	picked := SelectAgent(m, cfg, counts)
	if m.Agent == picked {
		return picked, nil
	}
	m.Agent = picked
	if err := os.WriteFile(path, frontmatter.Write(m, body), 0o644); err != nil {
		return "", err
	}
	return picked, nil
}

// syncMatters runs Sync against every enabled matter under
// projectRoot, returning one MatterResult per attempted matter (in
// the order LoadFromProject returned them, i.e. sorted by name).
// Returns nil when no matters are configured. Errors are captured
// per-matter rather than bubbled so reconciliation continues even
// when one upstream is down.
func syncMatters(projectRoot string) []MatterResult {
	configs, err := matter.LoadFromProject(projectRoot)
	if err != nil {
		return []MatterResult{{Name: "", Err: fmt.Errorf("matter: load: %w", err)}}
	}
	if len(configs) == 0 {
		return nil
	}
	matters, err := matter.FromConfig(configs)
	if err != nil {
		return []MatterResult{{Name: "", Err: fmt.Errorf("matter: instantiate: %w", err)}}
	}
	if len(matters) == 0 {
		return nil
	}
	out := make([]MatterResult, 0, len(matters))
	for _, m := range matters {
		ctx, cancel := callbudget.Context()
		c, u, err := m.Sync(ctx, projectRoot)
		cancel()
		out = append(out, MatterResult{Name: m.Name(), Created: c, Updated: u, Err: err})
	}
	return out
}

// LoadMaxWorkers reads `[fleet] max_workers = N` from a spore.toml
// at projectRoot, falling back to DefaultMaxWorkers when missing.
// Mirrors the tiny TOML subset accepted by `internal/align`.
func LoadMaxWorkers(projectRoot string) (int, error) {
	tomlPath := filepath.Join(projectRoot, "spore.toml")
	b, err := os.ReadFile(tomlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultMaxWorkers, nil
		}
		return DefaultMaxWorkers, err
	}
	overrides, err := parseFleetTOML(string(b))
	if err != nil {
		return DefaultMaxWorkers, fmt.Errorf("fleet: parse %s: %w", tomlPath, err)
	}
	if v, ok := overrides["max_workers"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return DefaultMaxWorkers, fmt.Errorf("fleet: max_workers: want integer, got %q", v)
		}
		if n < 1 {
			return DefaultMaxWorkers, fmt.Errorf("fleet: max_workers must be >= 1, got %d", n)
		}
		return n, nil
	}
	return DefaultMaxWorkers, nil
}

// parseFleetTOML collects the scalar entries of the [fleet] section as
// raw strings (comments stripped, quotes kept for the caller to strip)
// so integer knobs (max_workers) and string knobs (account_tier) can
// share the section.
func parseFleetTOML(content string) (map[string]string, error) {
	out := map[string]string{}
	inFleet := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inFleet = strings.TrimSpace(line[1:len(line)-1]) == "fleet"
			continue
		}
		if !inFleet {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: malformed entry %q", lineNum, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if i := strings.IndexByte(val, '#'); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		out[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FlagPath returns the kill-switch path:
// `<XDG_STATE_HOME>/spore/fleet-enabled`, falling back to
// `$HOME/.local/state/spore/fleet-enabled` when XDG_STATE_HOME is
// unset. Reconcile short-circuits when this path is missing.
func FlagPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", errors.New("fleet: HOME and XDG_STATE_HOME both unset")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "spore", "fleet-enabled"), nil
}

// Enable creates the kill-switch flag (along with parent dirs).
// Idempotent.
func Enable() error {
	p, err := FlagPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Disable removes the kill-switch flag. Idempotent (missing flag is
// a no-op).
func Disable() error {
	p, err := FlagPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Enabled reports whether the kill-switch flag is present.
func Enabled() (bool, error) {
	p, err := FlagPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
