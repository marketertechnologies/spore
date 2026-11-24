package spore

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/versality/spore/internal/hooks"
)

func TestHandoverSettingsWireCommunicationHooks(t *testing.T) {
	raw, err := os.ReadFile("bootstrap/handover/settings.json")
	if err != nil {
		t.Fatalf("read handover settings: %v", err)
	}
	var settings struct {
		Hooks map[string][]handoverHookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("parse handover settings: %v", err)
	}

	if !hasCommand(settings.Hooks["PreToolUse"], "/home/spore/.claude/hooks/block-bg-bash.pl") {
		t.Fatal("handover settings lost block-bg-bash PreToolUse hook")
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
