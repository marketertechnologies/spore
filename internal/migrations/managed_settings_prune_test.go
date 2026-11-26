package migrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pruneUserSettings = `{
  "hooks": {
    "SessionStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "/home/spore/.claude/hooks/load-state-md.pl", "timeout": 5}]}
    ],
    "Stop": [
      {"matcher": "", "hooks": [
        {"type": "command", "command": "/run/current-system/sw/bin/spore fleet replenish-hook", "timeout": 30},
        {"type": "command", "command": "bin/my-stop", "timeout": 5}
      ]}
    ]
  },
  "permissions": {"deny": ["SendMessage"]},
  "theme": "dark"
}
`

func buildSpore(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(bin, "spore"), "github.com/versality/spore/cmd/spore")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build spore: %v\n%s", err, out)
	}
	return bin
}

// noBrokerEnv is the environment for an exec'd spore binary. Only a
// test binary skips the host broker socket on its own (see
// secrets.SocketPath), so a built spore needs SPORE_SECRETS_SOCKET
// pointed at an absent path.
func noBrokerEnv(t *testing.T) []string {
	return append(os.Environ(), "SPORE_SECRETS_SOCKET="+filepath.Join(t.TempDir(), "no-broker.sock"))
}

func runPrune(t *testing.T, home, bin, managed string) string {
	t.Helper()
	script, err := filepath.Abs("../../bootstrap/migrations/004-managed-settings-prune.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-eu", script)
	cmd.Dir = home
	cmd.Env = append(noBrokerEnv(t),
		"HOME="+home,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SPORE_MANAGED_SETTINGS="+managed)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("migration 004: %v\n%s", err, out)
	}
	return string(out)
}

func TestManagedSettingsPrune(t *testing.T) {
	bin := buildSpore(t)
	home := t.TempDir()
	managed := filepath.Join(home, "etc/50-spore.json")
	gen := exec.Command(filepath.Join(bin, "spore"), "hooks", "settings", "--managed",
		"--spore", "/nix/store/x-spore/bin/spore", "--hooks-dir", "/nix/store/x-shims/share/spore/hooks")
	gen.Env = noBrokerEnv(t)
	body, err := gen.Output()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, managed, string(body))
	target := filepath.Join(home, ".claude/settings.json")
	writeFile(t, target, pruneUserSettings)

	out := runPrune(t, home, bin, managed)
	if !strings.Contains(out, "backed up to "+target+".spore-bak-") {
		t.Errorf("first run output:\n%s", out)
	}
	got := readFile(t, target)
	for _, gone := range []string{"load-state-md", "replenish-hook", "SendMessage", "permissions"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived the prune:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"bin/my-stop", `"theme": "dark"`} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q lost:\n%s", kept, got)
		}
	}

	out = runPrune(t, home, bin, managed)
	if strings.TrimSpace(out) != target+": nothing to prune" {
		t.Errorf("second run not a no-op:\n%s", out)
	}
	if readFile(t, target) != got {
		t.Error("second run changed settings.json")
	}
	backups, _ := filepath.Glob(target + ".spore-bak-*")
	if len(backups) != 1 || readFile(t, backups[0]) != pruneUserSettings {
		t.Errorf("want one backup holding the original, got %v", backups)
	}
}

func TestManagedSettingsPrune_noManagedFile(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".claude/settings.json")
	writeFile(t, target, pruneUserSettings)
	out := runPrune(t, home, t.TempDir(), filepath.Join(home, "absent.json"))
	if !strings.Contains(out, "absent; "+target+" left as is") {
		t.Errorf("output = %q", out)
	}
	if readFile(t, target) != pruneUserSettings {
		t.Error("settings.json changed without a managed file")
	}
}

func TestManagedSettingsPrune_symlinkLeftAlone(t *testing.T) {
	bin := buildSpore(t)
	home := t.TempDir()
	managed := filepath.Join(home, "etc/50-spore.json")
	writeFile(t, managed, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/nix/store/x/bin/spore fleet replenish-hook"}]}]}}`)
	source := filepath.Join(home, "hm/settings.json")
	writeFile(t, source, pruneUserSettings)
	target := filepath.Join(home, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
	out := runPrune(t, home, bin, managed)
	if !strings.Contains(out, target+" is a symlink, managed elsewhere") {
		t.Errorf("output = %q", out)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("settings.json is no longer a symlink")
	}
	if readFile(t, source) != pruneUserSettings {
		t.Error("symlink source changed")
	}
}
