package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/versality/spore/internal/task"
)

// DefaultManagedSettingsPath is where the spore-fleet NixOS module
// installs the host-wide Claude Code managed settings drop-in.
const DefaultManagedSettingsPath = "/etc/claude-code/managed-settings.d/50-spore.json"

// PluginName names both the spore plugin and the marketplace that
// carries it (.claude-plugin/ at the repo root), so the enabledPlugins
// key is "spore@spore".
const PluginName = "spore"

// hostStopBins is the Stop set every host agent shares. Each command
// self-gates on $SPORE_TASK_INBOX, so it is safe in sessions spore did
// not spawn.
func hostStopBins(spore string) []HookBin {
	return []HookBin{
		{Name: "coordinator-token-monitor", BinPath: spore + " coordinator token-monitor", Timeout: 10},
		{Name: "worker-token-monitor", BinPath: spore + " worker token-monitor", Timeout: 10},
		{Name: "replenish", BinPath: spore + " fleet replenish-hook", Timeout: 30},
	}
}

// HostEvents is the host-wide Claude Code binding set. It is the one
// source for the managed settings drop-in and for
// bootstrap/handover/settings.json (pinned by a test). hooksDir holds
// block-bg-bash.pl and load-state-md.pl.
func HostEvents(spore, hooksDir string) map[string][]HookBin {
	stop := append(hostStopBins(spore),
		HookBin{Name: "engineer-check", BinPath: engineerCheckCommand(spore), Timeout: task.EngineerCheckHookTimeout},
		HookBin{Name: "watch-inbox", BinPath: spore + " hooks watch-inbox", Timeout: 604800, AsyncRewake: true})
	return map[string][]HookBin{
		"Notification": {
			{Name: "notify-coordinator", BinPath: spore + " hooks notify-coordinator", Timeout: 5, Async: true},
		},
		"PreToolUse": {
			{Name: "block-bg-bash", BinPath: filepath.Join(hooksDir, "block-bg-bash.pl"), Matcher: "Bash", Timeout: 5},
			{Name: "pretooluse", BinPath: spore + " hooks pretooluse", Matcher: "Bash", Timeout: 5},
		},
		"SessionStart": {
			{Name: "load-state-md", BinPath: filepath.Join(hooksDir, "load-state-md.pl"), Timeout: 5},
		},
		"Stop": stop,
	}
}

// engineerCheckCommand skips the hook outside engineer panes and on a
// binary without the subcommand: an older spore exits 2 on an unknown
// subcommand, which Claude Code reads as a blocked stop.
func engineerCheckCommand(spore string) string {
	return `[ "$SPORE_ROLE" = engineer ] || exit 0; ` + spore + ` hooks 2>&1 | grep -q engineer-check || exit 0; exec ` + spore + ` hooks engineer-check`
}

// ManagedSettings renders the managed settings drop-in: HostEvents plus
// the default permissions.deny set. A project's spore.toml
// [permissions] cannot lift a managed deny rule. A non-empty marketplaceDir also registers that
// directory as the "spore" marketplace and force-enables its plugin.
func ManagedSettings(spore, hooksDir, marketplaceDir string) ([]byte, error) {
	top, err := buildTop(HostEvents(spore, hooksDir), DefaultPermissions())
	if err != nil {
		return nil, err
	}
	if marketplaceDir != "" {
		top.ExtraKnownMarketplaces = map[string]marketplaceEntry{
			PluginName: {Source: marketplaceSource{Source: "directory", Path: marketplaceDir}},
		}
		top.EnabledPlugins = map[string]bool{PluginName + "@" + PluginName: true}
	}
	return marshalTop(top)
}

type marketplaceEntry struct {
	Source marketplaceSource `json:"source"`
}

type marketplaceSource struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

// ManagedSet is what a managed settings file provides: hook commands
// keyed by event, group matcher and normalised command, plus deny rules.
type ManagedSet struct {
	commands map[string]bool
	deny     map[string]bool
}

// LoadManagedSet reads a managed settings file. A missing file yields
// an empty set and ok=false.
func LoadManagedSet(path string) (set ManagedSet, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ManagedSet{}, false, nil
	}
	if err != nil {
		return ManagedSet{}, false, err
	}
	set, err = ParseManagedSet(raw)
	if err != nil {
		return ManagedSet{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return set, true, nil
}

// ParseManagedSet extracts the hook commands and deny rules from a
// settings blob.
func ParseManagedSet(raw []byte) (ManagedSet, error) {
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ManagedSet{}, fmt.Errorf("parse managed settings: %w", err)
	}
	set := ManagedSet{commands: map[string]bool{}, deny: map[string]bool{}}
	for event, groups := range doc.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				set.commands[managedKey(event, g.Matcher, h.Command)] = true
			}
		}
	}
	for _, d := range doc.Permissions.Deny {
		set.deny[d] = true
	}
	return set, nil
}

// Provides reports whether the managed set already runs command on
// event under matcher. Commands match on the program's base name plus
// its arguments, so /run/current-system/sw/bin/spore, a Nix store path
// and a bare `spore` all count as the same hook. The matcher must match
// too: a hook bound to `Bash|Edit` is not replaced by one bound to
// `Bash`, so pruning it would drop the Edit coverage.
func (m ManagedSet) Provides(event, matcher, command string) bool {
	return m.commands[managedKey(event, matcher, command)]
}

func (m ManagedSet) Denies(rule string) bool {
	return m.deny[rule]
}

// managedKey treats an empty matcher and "*" as the same match-all.
func managedKey(event, matcher, command string) string {
	if matcher == "*" {
		matcher = ""
	}
	return event + "\x00" + matcher + "\x00" + NormalizeCommand(command)
}

// NormalizeCommand reduces every program in a hook command to its base
// name, keeping arguments. A program is the first word or any word
// after `;`, `|`, `||`, `&&` or `exec`, so compound commands such as
// engineerCheckCommand compare equal across spore paths.
func NormalizeCommand(command string) string {
	fields := strings.Fields(command)
	cmdPos := true
	for i, f := range fields {
		if cmdPos {
			fields[i] = filepath.Base(f)
		}
		switch f {
		case "|", "||", "&&", "exec":
			cmdPos = true
		default:
			cmdPos = strings.HasSuffix(f, ";")
		}
	}
	return strings.Join(fields, " ")
}

// FilterManaged drops every bin the managed set already provides and
// returns the names of the dropped commands.
func FilterManaged(events map[string][]HookBin, m ManagedSet) (map[string][]HookBin, []string) {
	out := make(map[string][]HookBin, len(events))
	var dropped []string
	for event, bins := range events {
		for _, b := range bins {
			if m.Provides(event, b.Matcher, b.BinPath) {
				dropped = append(dropped, event+": "+b.BinPath)
				continue
			}
			out[event] = append(out[event], b)
		}
	}
	sort.Strings(dropped)
	return out, dropped
}
