package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/doctor"
	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/fleet"
	"github.com/versality/spore/internal/hooks"
	"github.com/versality/spore/internal/task"
)

const doctorUsage = `spore doctor - read-only drift checks for a spore host

Usage:
  spore doctor [--json] [--repo PATH] [--flake-lock PATH] [--sw DIR]
               [--dropin PATH] [--claude BIN]

Checks (one pass/warn/fail/skip line each):
  binary                  deployed spore (<sw>/bin/spore) vs this binary
  binary vs repo VERSION  deployed version vs a spore checkout's VERSION
  binary vs flake input   deployed commit vs the spore rev in flake.lock
  managed settings        live drop-in vs what this binary renders:
                          hooks, deny list, sandbox block (skipped when
                          not rendered), other keys
  claude code version     installed Claude Code vs the verified version
  fleet units             per project: reconcile timer and path units
                          active, last reconcile run succeeded
  secrets file modes      secrets.env, *.pem, *.key, github-token.json,
                          github.toml under ~/.config/spore and the spore
                          state dir are 0600 or stricter
  npmrc tokens            no literal registry token in ~/.npmrc
  bundle tokens           no GitHub token in ~/.bundle/config, a file
                          BUNDLE_CONFIG / BUNDLE_USER_CONFIG names, or
                          the cwd project's .bundle/config;
                          ~/.bundle/config is 0600 with no ACL
  kill switch             fleet-enabled flag state

Flags:
  --json        Print {"results": [...], "fail": N, "warn": N}.
  --repo        Spore checkout for the VERSION check. Default: the cwd's
                repo when it is a spore checkout.
  --flake-lock  Host flake lock. Default /etc/nixos/flake.lock.
  --sw          System profile. Default /run/current-system/sw.
  --dropin      Managed settings drop-in. Default
                /etc/claude-code/managed-settings.d/50-spore.json.
  --claude      Claude Code binary. Default "claude" on PATH.

Exits 1 when any check fails. See docs/doctor.md.
`

// doctorRun is the seam tests swap to fake systemctl and --version
// probes.
var doctorRun func(name string, args ...string) ([]byte, error)

func runDoctor(args []string, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "json output")
	repo := fs.String("repo", "", "spore checkout")
	lock := fs.String("flake-lock", "/etc/nixos/flake.lock", "host flake lock")
	sw := fs.String("sw", envOr("SPORE_DOCTOR_SW", "/run/current-system/sw"), "system profile")
	dropin := fs.String("dropin", envOr("SPORE_DOCTOR_DROPIN", hooks.DefaultManagedSettingsPath), "managed settings drop-in")
	claude := fs.String("claude", "claude", "claude code binary")
	help := fs.Bool("h", false, "help")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return 2, fmt.Errorf("%w\n\n%s", err, doctorUsage)
	}
	if *help {
		fmt.Fprint(stdout, doctorUsage)
		return 0, nil
	}
	if fs.NArg() != 0 {
		return 2, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *repo == "" {
		*repo = sporeCheckout()
	}
	home := os.Getenv("HOME")
	configDir, npmrc, bundleConfig := "", "", ""
	if home != "" {
		configDir = filepath.Join(home, ".config", "spore")
		npmrc = filepath.Join(home, ".npmrc")
		bundleConfig = filepath.Join(home, ".bundle", "config")
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		configDir = filepath.Join(d, "spore")
	}
	stateBase, _ := events.BaseDir()

	results := doctor.Check(doctor.Env{
		SW:            *sw,
		DropIn:        *dropin,
		RepoRoot:      *repo,
		FlakeLock:     *lock,
		ClaudeBin:     *claude,
		ConfigDir:     configDir,
		StateBase:     stateBase,
		Npmrc:         npmrc,
		BundleConfig:  bundleConfig,
		ProjectBundle: projectBundleConfigs(),
		SelfVersion:   spore.BuildVersion(),
		Run:           doctorRun,
		Render:        hooks.ManagedSettings,
		FleetEnabled:  fleet.Enabled,
	})
	fails, warns := doctor.Counts(results)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"results": results, "fail": fails, "warn": warns}); err != nil {
			return 1, err
		}
	} else {
		for _, r := range results {
			fmt.Fprintf(stdout, "%-5s %s: %s\n", strings.ToUpper(string(r.Status)), r.Name, r.Detail)
		}
		fmt.Fprintf(stdout, "\n%d fail, %d warn\n", fails, warns)
	}
	if fails > 0 {
		return 1, nil
	}
	return 0, nil
}

// projectBundleConfigs lists the bundler configs besides
// ~/.bundle/config that the sandbox leaves readable: files the
// BUNDLE_CONFIG / BUNDLE_USER_CONFIG env names, and the .bundle/config
// of the cwd's worktree and main checkout.
func projectBundleConfigs() []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, o := range out {
			if o == p {
				return
			}
		}
		out = append(out, p)
	}
	add(os.Getenv("BUNDLE_CONFIG"))
	add(os.Getenv("BUNDLE_USER_CONFIG"))
	if b, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if top := strings.TrimSpace(string(b)); top != "" {
			add(filepath.Join(top, ".bundle", "config"))
		}
	}
	if root, err := task.MainRepoRoot(""); err == nil {
		add(filepath.Join(root, ".bundle", "config"))
	}
	return out
}

// sporeCheckout returns the cwd's main repo when it is a spore
// checkout (go.mod declares the spore module), else "".
func sporeCheckout() string {
	root, err := task.MainRepoRoot("")
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.HasPrefix(string(b), "module github.com/versality/spore\n") {
		return ""
	}
	return root
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runDoctorMain(args []string) int {
	code, err := runDoctor(args, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore doctor:", err)
	}
	return code
}
