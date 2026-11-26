package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/fleet"
	"github.com/versality/spore/internal/hooks"
	"github.com/versality/spore/internal/report"
	"github.com/versality/spore/internal/task"
)

const reportUsage = `spore report - morning digest of the events ledger plus live state

Usage:
  spore report [--since 12h] [--project <name> | --all] [--full]

Flags:
  --since    Window to cover: a Go duration (12h, 90m) or days (2d).
             Default 12h.
  --project  Project to report on (the main repo's basename). Default:
             the cwd's repo.
  --all      Every project with a ledger under the spore state dir.
  --full     Also list every event in the window.

Reads <state dir>/spore/<project>/events.jsonl (and its rotated .1),
the project's tasks/ and its live tmux sessions. See docs/report.md.
`

// Seams for tests: live state touches tmux and the host flag file.
var (
	reportLive         = liveState
	reportFleetEnabled = fleet.Enabled
	reportAlerts       = hooks.ActiveAlerts
	reportNow          = time.Now
)

func runReport(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	since := fs.String("since", "12h", "window")
	project := fs.String("project", "", "project name")
	all := fs.Bool("all", false, "all projects")
	full := fs.Bool("full", false, "list every event")
	help := fs.Bool("h", false, "help")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, reportUsage)
	}
	if *help {
		fmt.Fprint(stdout, reportUsage)
		return nil
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *all && *project != "" {
		return errors.New("--project and --all are exclusive")
	}
	window, err := parseWindow(*since)
	if err != nil {
		return err
	}

	type target struct{ name, root string }
	var targets []target
	switch {
	case *all:
		names, err := events.Projects()
		if err != nil {
			return err
		}
		for _, n := range names {
			targets = append(targets, target{n, events.ProjectRoot(n)})
		}
	case *project != "":
		targets = append(targets, target{*project, events.ProjectRoot(*project)})
	default:
		root, err := task.MainRepoRoot("")
		if err != nil {
			return err
		}
		targets = append(targets, target{filepath.Base(root), root})
	}

	now := reportNow()
	h := report.Header{Since: now.Add(-window), Now: now, Alerts: reportAlerts()}
	if on, err := reportFleetEnabled(); err == nil {
		h.FleetEnabled = &on
	}
	var ps []report.Project
	for _, t := range targets {
		evs, err := events.Read(t.name, h.Since)
		if err != nil {
			return err
		}
		ps = append(ps, report.Project{Name: t.name, Events: evs, Live: reportLive(t.root)})
	}
	report.Render(stdout, h, ps, *full)
	return nil
}

// parseWindow accepts a Go duration or a whole number of days ("2d").
func parseWindow(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since %q: want a positive duration such as 12h or 2d", s)
	}
	return d, nil
}

func liveState(root string) *report.Live {
	if root == "" {
		return &report.Live{Err: "no project root recorded; run a reconcile pass once"}
	}
	metas, err := task.List(filepath.Join(root, "tasks"))
	if err != nil {
		return &report.Live{Err: err.Error()}
	}
	l := &report.Live{TasksByStatus: map[string][]string{}}
	for _, m := range metas {
		l.TasksByStatus[m.Status] = append(l.TasksByStatus[m.Status], m.Slug)
		if m.Loop != task.LoopRole || m.Status != "active" {
			continue
		}
		if esc, _ := task.IsEscalated(root, m.Slug); esc {
			l.Escalated = append(l.Escalated, m.Slug)
		}
		if ready, _ := task.IsReady(root, m.Slug); ready {
			l.Ready = append(l.Ready, m.Slug)
		}
	}
	l.Workers, l.RoleLoops, l.Coordinator = fleet.LiveUnits(root)
	return l
}

func runReportMain(args []string) int {
	if err := runReport(args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "spore report:", err)
		return 1
	}
	return 0
}
