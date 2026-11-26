package hooks

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/versality/spore/internal/sandbox"
)

// HookBin describes one hook binary entry for claude-code's
// settings.json. Name is a human label (not emitted into JSON).
// BinPath is the shell command to run. Matcher is an optional tool
// name regex (used for PreToolUse/PostToolUse); leave empty for
// Stop/Notification hooks.
type HookBin struct {
	Name        string
	BinPath     string
	Matcher     string
	Timeout     int  // seconds; 0 omits the field (claude-code default)
	Async       bool // run without blocking the agent
	AsyncRewake bool // long-running hook that wakes the agent on exit 2
	// ContextLimit sets Codex's additionalContextLimit (approx tokens
	// before the context spills to a temp file; 0 disables spilling).
	// Nil omits the key. Claude ignores it.
	ContextLimit *int
}

// Settings is SettingsWith under DefaultPermissions.
func Settings(events map[string][]HookBin) ([]byte, error) {
	return SettingsWith(events, DefaultPermissions())
}

// SettingsWith emits a complete, deterministic settings.json blob for
// claude-code. The events map keys are hook event names (Stop,
// Notification, PostToolUse, UserPromptSubmit, PreToolUse, ...).
// Empty slices are omitted. Keys are sorted at every level.
// Hooks with the same Matcher within one event are consolidated
// into a single group. permissions.deny is perms.DenyRules().
func SettingsWith(events map[string][]HookBin, perms Permissions) ([]byte, error) {
	top, err := buildTop(events, perms)
	if err != nil {
		return nil, err
	}
	return marshalTop(top)
}

func buildTop(events map[string][]HookBin, perms Permissions) (settingsTop, error) {
	hooksMap := make(map[string][]hookGroup)

	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		groups, err := consolidate(events[name])
		if err != nil {
			return settingsTop{}, err
		}
		if len(groups) > 0 {
			hooksMap[name] = groups
		}
	}

	top := settingsTop{
		Schema:      "https://json.schemastore.org/claude-code-settings.json",
		Permissions: &settingsPermissions{Deny: perms.DenyRules()},
	}
	if len(hooksMap) > 0 {
		top.Hooks = hooksMap
	}
	return top, nil
}

func marshalTop(top settingsTop) ([]byte, error) {
	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	return append(b, '\n'), nil
}

// settingsTop field order is alphabetical by JSON key for
// deterministic output.
type settingsTop struct {
	Schema                 string                      `json:"$schema"`
	EnabledPlugins         map[string]bool             `json:"enabledPlugins,omitempty"`
	ExtraKnownMarketplaces map[string]marketplaceEntry `json:"extraKnownMarketplaces,omitempty"`
	Hooks                  map[string][]hookGroup      `json:"hooks,omitempty"`
	Permissions            *settingsPermissions        `json:"permissions,omitempty"`
	Sandbox                *sandbox.Managed            `json:"sandbox,omitempty"`
}

type settingsPermissions struct {
	Deny []string `json:"deny"`
}

type hookGroup struct {
	Hooks   []hookEntry `json:"hooks"`
	Matcher string      `json:"matcher,omitempty"`
}

// hookEntry field order is alphabetical for deterministic JSON output.
type hookEntry struct {
	ContextLimit *int   `json:"additionalContextLimit,omitempty"`
	Async        bool   `json:"async,omitempty"`
	AsyncRewake  bool   `json:"asyncRewake,omitempty"`
	Command      string `json:"command"`
	Timeout      int    `json:"timeout,omitempty"`
	Type         string `json:"type"`
}

// consolidate merges HookBins with the same Matcher into a single
// hookGroup, preserving insertion order of both groups and entries.
func consolidate(bins []HookBin) ([]hookGroup, error) {
	if len(bins) == 0 {
		return nil, nil
	}

	type accum struct {
		matcher string
		entries []hookEntry
	}
	seen := make(map[string]int)
	var groups []accum

	for _, b := range bins {
		if b.BinPath == "" {
			return nil, fmt.Errorf("hook %q: empty BinPath", b.Name)
		}
		entry := hookEntry{
			ContextLimit: b.ContextLimit,
			Async:        b.Async,
			AsyncRewake:  b.AsyncRewake,
			Command:      b.BinPath,
			Timeout:      b.Timeout,
			Type:         "command",
		}
		idx, ok := seen[b.Matcher]
		if !ok {
			idx = len(groups)
			seen[b.Matcher] = idx
			groups = append(groups, accum{matcher: b.Matcher})
		}
		groups[idx].entries = append(groups[idx].entries, entry)
	}

	result := make([]hookGroup, len(groups))
	for i, g := range groups {
		result[i] = hookGroup{
			Hooks:   g.entries,
			Matcher: g.matcher,
		}
	}
	return result, nil
}
