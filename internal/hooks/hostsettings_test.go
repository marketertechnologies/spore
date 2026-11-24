package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	goldenSpore    = "/nix/store/0000000000000000000000000000000-spore/bin/spore"
	goldenHooksDir = "/nix/store/0000000000000000000000000000000-spore-shims/share/spore/hooks"
	goldenMarket   = "/nix/store/0000000000000000000000000000000-spore-claude-marketplace"
)

func TestManagedSettings_Golden(t *testing.T) {
	for _, tc := range []struct {
		name, market, golden string
	}{
		{"hooks only", "", "testdata/managed-settings.golden.json"},
		{"with plugin", goldenMarket, "testdata/managed-settings-plugin.golden.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ManagedSettings(goldenSpore, goldenHooksDir, tc.market)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("ManagedSettings differs from %s.\ngot:\n%s", tc.golden, got)
			}
		})
	}
}

func TestManagedSettings_CoversCodexStopSet(t *testing.T) {
	host := HostEvents("spore", "/h")["Stop"]
	codex := CodexUserEvents("spore")["Stop"]
	for _, c := range codex {
		found := false
		for _, h := range host {
			if h.BinPath == c.BinPath {
				found = true
			}
		}
		if !found {
			t.Errorf("codex Stop hook %q missing from host set", c.BinPath)
		}
	}
}

func TestNormalizeCommand(t *testing.T) {
	for in, want := range map[string]string{
		"/run/current-system/sw/bin/spore hooks watch-inbox": "spore hooks watch-inbox",
		"spore  hooks   watch-inbox":                         "spore hooks watch-inbox",
		"/home/spore/.claude/hooks/load-state-md.pl":         "load-state-md.pl",
		"": "",
	} {
		if got := NormalizeCommand(in); got != want {
			t.Errorf("NormalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func managedFixture(t *testing.T) ManagedSet {
	t.Helper()
	raw, err := ManagedSettings(goldenSpore, goldenHooksDir, "")
	if err != nil {
		t.Fatal(err)
	}
	set, err := ParseManagedSet(raw)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestFilterManaged_DropsProjectStopDuplicates(t *testing.T) {
	events := map[string][]HookBin{
		"Stop": {
			{BinPath: "spore coordinator token-monitor"},
			{BinPath: "spore hooks watch-inbox", AsyncRewake: true},
			{BinPath: "bin/project-stop"},
		},
		"SessionStart": {{BinPath: "bin/spore-worker-init"}},
		"Notification": {{BinPath: "spore coordinator token-monitor"}},
	}
	got, dropped := FilterManaged(events, managedFixture(t))
	if len(dropped) != 2 {
		t.Errorf("dropped = %v, want the two spore Stop hooks", dropped)
	}
	if len(got["Stop"]) != 1 || got["Stop"][0].BinPath != "bin/project-stop" {
		t.Errorf("Stop kept = %+v", got["Stop"])
	}
	if len(got["SessionStart"]) != 1 {
		t.Errorf("SessionStart dropped: %+v", got)
	}
	if len(got["Notification"]) != 1 {
		t.Error("matching is per event: a Stop command under Notification must stay")
	}
}

const userSettingsFixture = `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "hooks": {
    "Notification": [
      {"matcher": "", "hooks": [{"type": "command", "command": "spore hooks notify-coordinator", "timeout": 5}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [
        {"type": "command", "command": "/home/spore/.claude/hooks/block-bg-bash.pl", "timeout": 5},
        {"type": "command", "command": "my-guard > /dev/null 2>&1 && echo ok", "timeout": 5}
      ]}
    ],
    "SessionStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "/home/spore/.claude/hooks/load-state-md.pl", "timeout": 5}]}
    ],
    "Stop": [
      {"matcher": "", "hooks": [
        {"type": "command", "command": "/run/current-system/sw/bin/spore coordinator token-monitor", "timeout": 10},
        {"type": "command", "command": "/run/current-system/sw/bin/spore worker token-monitor", "timeout": 10},
        {"type": "command", "command": "/run/current-system/sw/bin/spore fleet replenish-hook", "timeout": 30},
        {"type": "command", "command": "/run/current-system/sw/bin/spore hooks watch-inbox", "timeout": 604800, "asyncRewake": true}
      ]}
    ]
  },
  "permissions": {"allow": ["Bash(ls:*)"], "deny": ["SendMessage", "WebFetch"]},
  "statusLine": {"type": "command", "command": "~/.claude/statusline.sh"},
  "skipDangerousModePermissionPrompt": true
}
`

const userSettingsPruned = `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "hooks": {
    "PreToolUse": [
      {
        "hooks": [
          {
            "command": "my-guard > /dev/null 2>&1 && echo ok",
            "timeout": 5,
            "type": "command"
          }
        ],
        "matcher": "Bash"
      }
    ]
  },
  "permissions": {
    "allow": [
      "Bash(ls:*)"
    ],
    "deny": [
      "WebFetch"
    ]
  },
  "skipDangerousModePermissionPrompt": true,
  "statusLine": {
    "type": "command",
    "command": "~/.claude/statusline.sh"
  }
}
`

func TestPruneSettings_KeepsUnrelated(t *testing.T) {
	out, removed, err := PruneSettings([]byte(userSettingsFixture), managedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != userSettingsPruned {
		t.Fatalf("pruned settings:\n%s", out)
	}
	if len(removed) != 8 {
		t.Errorf("removed %d entries, want 7 hooks + 1 deny: %v", len(removed), removed)
	}
}

func TestPruneSettings_AllSporeOnlyDropsEmptyBlocks(t *testing.T) {
	in := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"spore fleet replenish-hook"}]}]},"permissions":{"deny":["SendMessage"]},"theme":"dark"}`
	out, _, err := PruneSettings([]byte(in), managedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"theme\": \"dark\"\n}\n"; string(out) != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestPruneSettingsFile_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(userSettingsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	removed, backup, err := PruneSettingsFile(path, managedFixture(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 || backup != path+".spore-bak-20261124T100000Z" {
		t.Fatalf("first run: removed=%v backup=%q", removed, backup)
	}
	if b, _ := os.ReadFile(backup); string(b) != userSettingsFixture {
		t.Error("backup is not the original file")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", info.Mode().Perm())
	}

	removed, backup, err = PruneSettingsFile(path, managedFixture(t), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 || backup != "" {
		t.Fatalf("second run not a no-op: removed=%v backup=%q", removed, backup)
	}
	if b, _ := os.ReadFile(path); string(b) != userSettingsPruned {
		t.Error("second run changed the file")
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"settings.json", "settings.json.spore-bak-20261124T100000Z"}; !reflect.DeepEqual(names, want) {
		t.Errorf("dir = %v, want %v", names, want)
	}
}

func TestPruneSettingsFile_Missing(t *testing.T) {
	removed, backup, err := PruneSettingsFile(filepath.Join(t.TempDir(), "nope.json"), managedFixture(t), time.Now())
	if err != nil || removed != nil || backup != "" {
		t.Fatalf("missing file: removed=%v backup=%q err=%v", removed, backup, err)
	}
}

func TestPruneSettings_BadJSON(t *testing.T) {
	_, _, err := PruneSettings([]byte(`{"hooks": [`), managedFixture(t))
	if err == nil || !strings.Contains(err.Error(), "parse settings") {
		t.Fatalf("err = %v", err)
	}
}

func TestPruneSettings_MatcherMustMatch(t *testing.T) {
	in := `{"hooks":{"PreToolUse":[` +
		`{"matcher":"Bash|Edit","hooks":[{"type":"command","command":"/home/u/.claude/hooks/block-bg-bash.pl"}]},` +
		`{"matcher":"Bash","hooks":[{"type":"command","command":"/home/u/.claude/hooks/block-bg-bash.pl"}]}` +
		`],"Stop":[{"matcher":"*","hooks":[{"type":"command","command":"spore fleet replenish-hook"}]}]}}`
	out, removed, err := PruneSettings([]byte(in), managedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("removed = %v, want the Bash-only binding and the match-all Stop hook", removed)
	}
	if !strings.Contains(string(out), `"matcher": "Bash|Edit"`) || strings.Contains(string(out), `"matcher": "Bash"`+"\n") {
		t.Errorf("want only the Bash|Edit group kept:\n%s", out)
	}
	if strings.Contains(string(out), "replenish-hook") {
		t.Errorf("\"*\" matcher should count as match-all:\n%s", out)
	}
}

func TestFilterManaged_MatcherMustMatch(t *testing.T) {
	events := map[string][]HookBin{
		"PreToolUse": {
			{BinPath: "/x/block-bg-bash.pl", Matcher: "Bash"},
			{BinPath: "/x/block-bg-bash.pl", Matcher: "Edit"},
		},
	}
	got, dropped := FilterManaged(events, managedFixture(t))
	if len(dropped) != 1 || len(got["PreToolUse"]) != 1 || got["PreToolUse"][0].Matcher != "Edit" {
		t.Errorf("kept %+v, dropped %v", got["PreToolUse"], dropped)
	}
}

func TestPruneSettingsFile_SymlinkLeftAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hm-settings.json")
	if err := os.WriteFile(target, []byte(userSettingsFixture), 0o444); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	removed, backup, err := PruneSettingsFile(link, managedFixture(t), time.Now())
	if !errors.Is(err, ErrSymlinkSettings) || removed != nil || backup != "" {
		t.Fatalf("removed=%v backup=%q err=%v", removed, backup, err)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced")
	}
	if b, _ := os.ReadFile(target); string(b) != userSettingsFixture {
		t.Error("link target changed")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("unexpected files: %v", entries)
	}
}

func TestPruneSettingsFile_BackupNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, []byte(userSettingsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	_, first, err := PruneSettingsFile(path, managedFixture(t), now)
	if err != nil {
		t.Fatal(err)
	}
	second := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"spore fleet replenish-hook"}]}]}}`
	if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	_, backup, err := PruneSettingsFile(path, managedFixture(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if backup != first+".1" {
		t.Errorf("second backup = %q, want %q", backup, first+".1")
	}
	if b, _ := os.ReadFile(first); string(b) != userSettingsFixture {
		t.Error("first backup overwritten")
	}
	if b, _ := os.ReadFile(backup); string(b) != second {
		t.Error("second backup does not hold the second original")
	}
}

func TestFilterManaged_DroppedSorted(t *testing.T) {
	events := map[string][]HookBin{
		"Stop":         {{BinPath: "spore worker token-monitor"}, {BinPath: "spore coordinator token-monitor"}},
		"Notification": {{BinPath: "spore hooks notify-coordinator"}},
	}
	for i := 0; i < 20; i++ {
		_, dropped := FilterManaged(events, managedFixture(t))
		if !sort.StringsAreSorted(dropped) || len(dropped) != 3 {
			t.Fatalf("dropped = %v", dropped)
		}
	}
}
