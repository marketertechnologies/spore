package hooks

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCodexHooks_UserGolden(t *testing.T) {
	got, err := CodexHooks(CodexUserEvents("/run/current-system/sw/bin/spore"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/codex-hooks.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(golden) {
		t.Fatalf("CodexHooks output differs from golden.\ngot:\n%s\nwant:\n%s", got, golden)
	}
}

// codexHooksFile mirrors the fields codex-rs config/src/hook_config.rs
// reads; HooksFile there is deny_unknown_fields at the top level.
type codexHooksFile struct {
	Hooks map[string][]struct {
		Matcher *string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
			Async   bool   `json:"async"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func TestCodexHooks_UserShape(t *testing.T) {
	raw, err := CodexHooks(CodexUserEvents("spore"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top["hooks"] == nil {
		t.Fatalf("top-level keys must be exactly [hooks]: %s", raw)
	}
	if strings.Contains(string(raw), "asyncRewake") || strings.Contains(string(raw), "watch-inbox") {
		t.Fatalf("codex config must not carry the claude watch-inbox rewake hook:\n%s", raw)
	}

	var f codexHooksFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	commands := func(event string) []string {
		var out []string
		for _, g := range f.Hooks[event] {
			for _, h := range g.Hooks {
				if h.Type != "command" || h.Timeout <= 0 {
					t.Errorf("%s %q: type=%q timeout=%d", event, h.Command, h.Type, h.Timeout)
				}
				out = append(out, h.Command)
			}
		}
		return out
	}
	if got := commands("SessionStart"); len(got) != 1 || got[0] != "spore hooks codex-session-start" {
		t.Errorf("SessionStart = %q, want the codex-session-start thread record", got)
	}
	want := []string{"spore coordinator token-monitor", "spore worker token-monitor", "spore fleet replenish-hook"}
	if got := commands("Stop"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Stop = %q, want %q", got, want)
	}
	if len(f.Hooks) != 2 {
		t.Errorf("events = %d, want SessionStart and Stop only", len(f.Hooks))
	}
}

func TestCodexHooks_RejectsAsyncRewake(t *testing.T) {
	_, err := CodexHooks(map[string][]HookBin{
		"Stop": {{BinPath: "spore hooks watch-inbox", AsyncRewake: true}},
	})
	if err == nil {
		t.Fatal("expected asyncRewake to be rejected")
	}
}

func TestCodexHooks_EmptyKeepsHooksKey(t *testing.T) {
	got, err := CodexHooks(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\n  \"hooks\": {}\n}\n" {
		t.Fatalf("got %q", got)
	}
}
