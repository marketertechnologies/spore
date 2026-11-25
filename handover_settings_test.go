package spore

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/versality/spore/internal/hooks"
	"github.com/versality/spore/internal/task"
)

func TestHandoverSettingsWireCommunicationHooks(t *testing.T) {
	raw, err := os.ReadFile("bootstrap/handover/settings.json")
	if err != nil {
		t.Fatalf("read handover settings: %v", err)
	}
	var settings struct {
		Hooks       map[string][]handoverHookGroup `json:"hooks"`
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("parse handover settings: %v", err)
	}

	if !hasCommand(settings.Hooks["PreToolUse"], "/home/spore/.claude/hooks/block-bg-bash.pl") {
		t.Fatal("handover settings lost block-bg-bash PreToolUse hook")
	}
	if !hasCommand(settings.Hooks["PreToolUse"], "/run/current-system/sw/bin/spore hooks pretooluse") {
		t.Fatal("handover settings missing spore hooks pretooluse guard")
	}
	if !reflect.DeepEqual(settings.Permissions.Deny, hooks.DefaultDeny()) {
		t.Fatalf("handover permissions.deny drifted from hooks.DefaultDeny():\ngot  %q\nwant %q", settings.Permissions.Deny, hooks.DefaultDeny())
	}
	for _, rule := range []string{"Read(~/.claude/.credentials.json)", "Edit(~/.claude/.credentials.json)", "Bash(*.claude/.credentials.json*)"} {
		if !slices.Contains(settings.Permissions.Deny, rule) {
			t.Fatalf("handover permissions.deny lacks %s", rule)
		}
	}
	if !hasCommand(settings.Hooks["SessionStart"], "/home/spore/.claude/hooks/load-state-md.pl") {
		t.Fatal("handover settings lost load-state-md SessionStart hook")
	}
	if !hasAsync(settings.Hooks["Notification"], "/run/current-system/sw/bin/spore hooks notify-coordinator") {
		t.Fatal("handover settings missing async notify-coordinator Notification hook")
	}
	if !hasAsyncRewake(settings.Hooks["Stop"], "/run/current-system/sw/bin/spore hooks watch-inbox") {
		t.Fatal("handover settings missing asyncRewake watch-inbox Stop hook")
	}
	if !hasCommand(settings.Hooks["Stop"], "/run/current-system/sw/bin/spore coordinator token-monitor") {
		t.Fatal("handover settings missing coordinator token-monitor Stop hook")
	}
	if !hasCommand(settings.Hooks["Stop"], "/run/current-system/sw/bin/spore worker token-monitor") {
		t.Fatal("handover settings missing worker token-monitor Stop hook")
	}
	if !hasCommand(settings.Hooks["Stop"], "/run/current-system/sw/bin/spore fleet replenish-hook") {
		t.Fatal("handover settings missing fleet replenish-hook Stop hook")
	}
	if !hasTimeout(settings.Hooks["Stop"], handoverEngineerCheck, task.EngineerCheckHookTimeout) {
		t.Fatalf("handover settings missing engineer-check Stop hook with timeout %d", task.EngineerCheckHookTimeout)
	}
}

// handoverEngineerCheck skips the hook outside engineer panes and on a
// binary without the subcommand: an older spore exits 2 on an unknown
// subcommand, which Claude Code reads as a blocked stop.
const handoverEngineerCheck = "[ \"$SPORE_ROLE\" = engineer ] || exit 0; /run/current-system/sw/bin/spore hooks 2>&1 | grep -q engineer-check || exit 0; exec /run/current-system/sw/bin/spore hooks engineer-check"

func hasTimeout(groups []handoverHookGroup, command string, timeout int) bool {
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == command && hook.Timeout == timeout {
				return true
			}
		}
	}
	return false
}

func TestHandoverSettingsMatchHostEvents(t *testing.T) {
	raw, err := os.ReadFile("bootstrap/handover/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := hooks.ManagedSettings("/run/current-system/sw/bin/spore", "/home/spore/.claude/hooks", "")
	if err != nil {
		t.Fatal(err)
	}
	type doc struct {
		Hooks map[string][]struct {
			Matcher string           `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
		Permissions map[string]any `json:"permissions"`
	}
	var got, gen doc
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &gen); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, gen) {
		t.Fatalf("bootstrap/handover/settings.json drifted from hooks.HostEvents; regenerate its hooks and permissions from:\n%s", want)
	}
}

type handoverHookGroup struct {
	Hooks []handoverHook `json:"hooks"`
}

type handoverHook struct {
	Command     string `json:"command"`
	Async       bool   `json:"async,omitempty"`
	AsyncRewake bool   `json:"asyncRewake,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

func hasCommand(groups []handoverHookGroup, command string) bool {
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == command {
				return true
			}
		}
	}
	return false
}

func hasAsync(groups []handoverHookGroup, command string) bool {
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == command && hook.Async {
				return true
			}
		}
	}
	return false
}

func hasAsyncRewake(groups []handoverHookGroup, command string) bool {
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == command && hook.AsyncRewake {
				return true
			}
		}
	}
	return false
}
