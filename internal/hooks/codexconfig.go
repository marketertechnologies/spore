package hooks

import (
	"encoding/json"
	"fmt"
	"sort"
)

// CodexHooks emits a deterministic hooks.json for Codex from the same
// HookBin map Settings consumes. Codex reads top-level {"hooks": ...}
// with no $schema and no permissions. AsyncRewake is rejected: Codex
// ignores the key and runs the hook synchronously, so a week-long
// watch-inbox would hold every Stop open.
func CodexHooks(events map[string][]HookBin) ([]byte, error) {
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)

	hooksMap := make(map[string][]hookGroup)
	for _, name := range names {
		for _, b := range events[name] {
			if b.AsyncRewake {
				return nil, fmt.Errorf("hook %q: codex has no asyncRewake", b.BinPath)
			}
		}
		groups, err := consolidate(events[name])
		if err != nil {
			return nil, err
		}
		if len(groups) > 0 {
			hooksMap[name] = groups
		}
	}

	b, err := json.MarshalIndent(codexTop{Hooks: hooksMap}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal codex hooks: %w", err)
	}
	return append(b, '\n'), nil
}

type codexTop struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

// CodexUserEvents is the user-level ($CODEX_HOME/hooks.json) binding
// set: the Claude host Stop hooks plus the SessionStart thread record
// the inbox sidecar needs. The Claude watch-inbox asyncRewake hook is
// absent on purpose; the launch shim's sidecar replaces it. Every
// command self-gates on its env, so the file is safe in sessions spore
// did not spawn.
func CodexUserEvents(spore string) map[string][]HookBin {
	return map[string][]HookBin{
		"SessionStart": {
			{Name: "codex-session-start", BinPath: spore + " hooks codex-session-start", Timeout: 10},
		},
		"Stop": {
			{Name: "coordinator-token-monitor", BinPath: spore + " coordinator token-monitor", Timeout: 10},
			{Name: "worker-token-monitor", BinPath: spore + " worker token-monitor", Timeout: 10},
			{Name: "replenish", BinPath: spore + " fleet replenish-hook", Timeout: 30},
		},
	}
}
