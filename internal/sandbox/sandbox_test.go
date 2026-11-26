package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeToml(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadDefaultsOn(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.AllowDomains != nil || c.AllowWrite != nil {
		t.Fatalf("Load without spore.toml = %+v, want enabled with no extras", c)
	}
}

func TestLoadSection(t *testing.T) {
	dir := writeToml(t, `[sandbox]
enabled = true
allow_domains = ["example.com", "*.example.org"]
allow_write = ["~/.bundle"]
excluded_commands = ["docker *"]
`)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Enabled: true, AllowDomains: []string{"example.com", "*.example.org"}, AllowWrite: []string{"~/.bundle"}, ExcludedCommands: []string{"docker *"}}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("Load = %+v, want %+v", c, want)
	}
}

func TestLoadRejectsBadSection(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key": "[sandbox]\nallow = true\n",
		"bad bool":    "[sandbox]\nenabled = yes\n",
		"duplicate":   "[sandbox]\nenabled = true\nenabled = false\n",
		"bad array":   "[sandbox]\nallow_domains = example.com\n",
	} {
		if _, err := Load(writeToml(t, body)); err == nil {
			t.Errorf("%s: Load accepted %q", name, body)
		}
	}
}

func TestPaneSettings(t *testing.T) {
	js, err := PaneSettings(Config{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if js != `{"sandbox":{"enabled":true,"failIfUnavailable":true}}` {
		t.Fatalf("bare pane settings = %s", js)
	}
	js, err = PaneSettings(Config{Enabled: true, AllowDomains: []string{"a.test"}, AllowWrite: []string{"~/x"}, ExcludedCommands: []string{"docker *"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sandbox":{"enabled":true,"excludedCommands":["docker *"],"failIfUnavailable":true,"filesystem":{"allowWrite":["~/x"]},"network":{"allowedDomains":["a.test"]}}}`
	if js != want {
		t.Fatalf("pane settings =\n%s\nwant\n%s", js, want)
	}
	if js, _ := PaneSettings(Config{Enabled: false, AllowDomains: []string{"a.test"}}); js != "" {
		t.Fatalf("opted-out project rendered %s", js)
	}
}

func TestPaneEnv(t *testing.T) {
	env, err := PaneEnv(writeToml(t, "[sandbox]\nenabled = false\n"))
	if err != nil || env != nil {
		t.Fatalf("opt-out: env %q err %v, want none", env, err)
	}
	env, err = PaneEnv(writeToml(t, "[sandbox]\nenabled = maybe\n"))
	if err == nil {
		t.Fatal("broken section: want the load error reported")
	}
	if len(env) != 2 || env[0] != "-e" || env[1] != EnvVar+`={"sandbox":{"enabled":true,"failIfUnavailable":true}}` {
		t.Fatalf("broken section must fail closed, got %q", env)
	}
}

func TestManagedBlockShape(t *testing.T) {
	b, err := json.Marshal(ManagedBlock())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["enabled"]; ok {
		t.Fatal("managed block must not set enabled: it would block the per-project opt-out")
	}
	if m["allowUnsandboxedCommands"] != false {
		t.Fatalf("allowUnsandboxedCommands = %v, want an explicit false", m["allowUnsandboxedCommands"])
	}
	network := m["network"].(map[string]any)
	if network["strictAllowlist"] != true {
		t.Fatal("strictAllowlist must be on: bypass mode answers the network prompt with allow")
	}
	for _, p := range []string{"~/.config/spore", "~/.claude/.credentials.json", "~/.ssh"} {
		if !strings.Contains(string(b), `"`+p+`"`) {
			t.Errorf("denyRead lacks %s", p)
		}
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func TestPrepareCheckout(t *testing.T) {
	dir := gitInit(t)
	exclude := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.WriteFile(exclude, []byte("# own\n*.log"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := PrepareCheckout(dir); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.HasPrefix(got, "# own\n*.log\n"+excludeBegin+"\n/.bash_profile\n") {
		t.Fatalf("exclude does not keep the user's lines then the block:\n%s", got)
	}
	if strings.Count(got, excludeBegin) != 1 || !strings.HasSuffix(got, "/.claude/workflows\n"+excludeEnd+"\n") {
		t.Fatalf("exclude block not idempotent:\n%s", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".gitmodules")); err != nil || fi.Size() != 0 {
		t.Fatalf(".gitmodules: %v", err)
	}
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("placeholder files still untracked: %s", out)
	}
}

func TestPrepareCheckoutKeepsGitmodules(t *testing.T) {
	dir := gitInit(t)
	gm := filepath.Join(dir, ".gitmodules")
	if err := os.WriteFile(gm, []byte("[submodule \"x\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCheckout(dir); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(gm); string(raw) != "[submodule \"x\"]\n" {
		t.Fatalf(".gitmodules overwritten: %q", raw)
	}
}

func TestPrepareCheckoutWorktreeUsesCommonDir(t *testing.T) {
	dir := gitInit(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@e.invalid", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(dir, ".worktrees", "w")
	run("worktree", "add", "-q", wt, "-b", "wt/w")
	if err := PrepareCheckout(wt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(raw), excludeBegin) {
		t.Fatalf("worktree exclude not in the common dir: %v %q", err, raw)
	}
}
