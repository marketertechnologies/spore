package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/hooks"
)

type fakeHost struct {
	versions map[string]string
	units    string
	byName   string
}

func (f *fakeHost) run(name string, args ...string) ([]byte, error) {
	if name == "systemctl" {
		if strings.HasSuffix(args[len(args)-1], "*") {
			return []byte(f.units), nil
		}
		return []byte(f.byName), nil
	}
	if v, ok := f.versions[name]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("not found")
}

func find(t *testing.T, rs []Result, name string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no result %q in %+v", name, rs)
	return Result{}
}

const unitBlock = `Id=%s
LoadState=loaded
ActiveState=%s
SubState=waiting
Result=%s
`

func unitsFor(states map[string][2]string) string {
	var b []string
	for id, st := range states {
		b = append(b, fmt.Sprintf(unitBlock, id, st[0], st[1]))
	}
	return strings.Join(b, "\n")
}

func newEnv(t *testing.T) (Env, *fakeHost) {
	t.Helper()
	dir := t.TempDir()
	sw := filepath.Join(dir, "sw")
	if err := os.MkdirAll(filepath.Join(sw, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sw, "bin", "spore"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	_ = os.MkdirAll(repo, 0o755)
	_ = os.WriteFile(filepath.Join(repo, "VERSION"), []byte("0.54.0\n"), 0o644)
	lock := filepath.Join(dir, "flake.lock")
	_ = os.WriteFile(lock, []byte(`{"nodes":{"root":{},"spore":{"locked":{"owner":"o","repo":"spore","rev":"abcdef1234567890"}}}}`), 0o644)
	dropin := filepath.Join(dir, "50-spore.json")
	rendered, err := hooks.ManagedSettings(filepath.Join(sw, "bin", "spore"), filepath.Join(sw, "share", "spore", "hooks"), "")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(dropin, rendered, 0o644)

	ok := map[string][2]string{}
	for _, id := range []string{"spore-fleet-reconcile-p.timer", "spore-fleet-reconcile-flag-p.path", "spore-fleet-reconcile-tasks-p.path"} {
		ok[id] = [2]string{"active", "success"}
	}
	byName := map[string][2]string{"spore-fleet-reconcile-p.service": {"inactive", "success"}}
	for k, v := range ok {
		byName[k] = v
	}
	f := &fakeHost{
		versions: map[string]string{
			filepath.Join(sw, "bin", "spore"): "0.54.0 (abcdef1234567890)\n",
			"claude":                          VerifiedClaudeCode + " (Claude Code)\n",
		},
		units:  unitsFor(ok),
		byName: unitsFor(byName),
	}
	env := Env{
		SW: sw, DropIn: dropin, RepoRoot: repo, FlakeLock: lock, ClaudeBin: "claude",
		ConfigDir: filepath.Join(dir, "config"), StateBase: filepath.Join(dir, "state"),
		Npmrc:        filepath.Join(dir, "home", ".npmrc"),
		SelfVersion:  "0.54.0 (abcdef1234567890)",
		Run:          f.run,
		Render:       hooks.ManagedSettings,
		FleetEnabled: func() (bool, error) { return true, nil },
	}
	return env, f
}

func TestCheckAllPassOnCleanHost(t *testing.T) {
	env, _ := newEnv(t)
	rs := Check(env)
	if fails, warns := Counts(rs); fails != 0 || warns != 0 {
		t.Fatalf("clean host: %d fail, %d warn: %+v", fails, warns, rs)
	}
	if r := find(t, rs, "managed settings: sandbox"); r.Status != Pass {
		t.Errorf("sandbox = %+v, want pass: the drop-in carries the rendered sandbox floor", r)
	}
	if r := find(t, rs, "fleet units: p"); r.Status != Pass {
		t.Errorf("units = %+v", r)
	}
}

func TestCheckBinaryDrift(t *testing.T) {
	env, f := newEnv(t)
	f.versions[filepath.Join(env.SW, "bin", "spore")] = "0.53.0 (fedcba9876543210)\n"
	rs := Check(env)
	if r := find(t, rs, "binary vs flake input"); r.Status != Fail || !strings.Contains(r.Detail, "fedcba987654") {
		t.Errorf("flake = %+v, want fail naming the deployed commit", r)
	}
	if r := find(t, rs, "binary vs repo VERSION"); r.Status != Warn || !strings.Contains(r.Detail, "deploy pending") {
		t.Errorf("repo = %+v", r)
	}
	if r := find(t, rs, "binary"); r.Status != Warn {
		t.Errorf("binary = %+v, want warn: this binary differs", r)
	}

	delete(f.versions, filepath.Join(env.SW, "bin", "spore"))
	if r := find(t, Check(env), "binary"); r.Status != Fail {
		t.Errorf("missing binary = %+v, want fail", r)
	}
}

func TestCheckManagedSettingsDrift(t *testing.T) {
	env, _ := newEnv(t)
	raw, _ := os.ReadFile(env.DropIn)
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	delete(top["hooks"].(map[string]any), "Stop")
	perms := top["permissions"].(map[string]any)
	deny := perms["deny"].([]any)
	perms["deny"] = append(deny[1:], "Bash(extra *)")
	top["sandbox"] = map[string]any{"enabled": true}
	b, _ := json.Marshal(top)
	_ = os.WriteFile(env.DropIn, b, 0o644)

	rs := Check(env)
	if r := find(t, rs, "managed settings: hooks"); r.Status != Fail || !strings.Contains(r.Detail, "Stop missing") {
		t.Errorf("hooks = %+v", r)
	}
	if r := find(t, rs, "managed settings: deny"); r.Status != Fail || !strings.Contains(r.Detail, "1 missing") || !strings.Contains(r.Detail, "Bash(extra *)") {
		t.Errorf("deny = %+v", r)
	}
	if r := find(t, rs, "managed settings: sandbox"); r.Status != Fail {
		t.Errorf("sandbox = %+v, want fail when the live block differs from the rendered floor", r)
	}

	_ = os.Remove(env.DropIn)
	if r := find(t, Check(env), "managed settings"); r.Status != Fail {
		t.Errorf("missing drop-in = %+v", r)
	}
}

func TestCheckClaudeCodeVersion(t *testing.T) {
	env, f := newEnv(t)
	f.versions["claude"] = "9.0.0 (Claude Code)\n"
	if r := find(t, Check(env), "claude code version"); r.Status != Warn || !strings.Contains(r.Detail, "newer than the verified") {
		t.Errorf("newer = %+v", r)
	}
	delete(f.versions, "claude")
	if r := find(t, Check(env), "claude code version"); r.Status != Warn {
		t.Errorf("missing = %+v", r)
	}
}

func TestCheckFleetUnits(t *testing.T) {
	env, f := newEnv(t)
	f.byName = unitsFor(map[string][2]string{
		"spore-fleet-reconcile-p.timer":      {"inactive", "success"},
		"spore-fleet-reconcile-flag-p.path":  {"active", "success"},
		"spore-fleet-reconcile-tasks-p.path": {"active", "success"},
		"spore-fleet-reconcile-p.service":    {"failed", "exit-code"},
	})
	r := find(t, Check(env), "fleet units: p")
	if r.Status != Fail || !strings.Contains(r.Detail, "timer inactive") || !strings.Contains(r.Detail, "exit-code") {
		t.Errorf("units = %+v", r)
	}
	f.units = ""
	if r := find(t, Check(env), "fleet units"); r.Status != Warn {
		t.Errorf("no units = %+v", r)
	}
}

func TestCheckSecretModes(t *testing.T) {
	env, _ := newEnv(t)
	proj := filepath.Join(env.ConfigDir, "proj")
	_ = os.MkdirAll(proj, 0o700)
	_ = os.WriteFile(filepath.Join(env.ConfigDir, "secrets.env"), []byte("X=1"), 0o600)
	_ = os.WriteFile(filepath.Join(proj, "app.pem"), []byte("k"), 0o600)
	_ = os.WriteFile(filepath.Join(proj, "notes.txt"), []byte("k"), 0o644)
	if r := find(t, Check(env), "secrets file modes"); r.Status != Pass || !strings.Contains(r.Detail, "2 secret") {
		t.Errorf("tight modes = %+v", r)
	}
	st := filepath.Join(env.StateBase, "proj")
	_ = os.MkdirAll(st, 0o755)
	_ = os.WriteFile(filepath.Join(st, "github.toml"), []byte("x"), 0o640)
	if err := os.Chmod(filepath.Join(st, "github.toml"), 0o640); err != nil {
		t.Fatal(err)
	}
	if r := find(t, Check(env), "secrets file modes"); r.Status != Fail || !strings.Contains(r.Detail, "github.toml is 0640") {
		t.Errorf("loose mode = %+v", r)
	}
}

func TestCheckNpmrc(t *testing.T) {
	env, _ := newEnv(t)
	if r := find(t, Check(env), "npmrc tokens"); r.Status != Pass {
		t.Errorf("absent = %+v", r)
	}
	if err := os.MkdirAll(filepath.Dir(env.Npmrc), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := "registry=https://registry.npmjs.org/\n" +
		"//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}\n" +
		"# _authToken=CANARY-COMMENT\n" +
		"//r.example/:_authToken=${CI_TOKEN} # ci\n" +
		"//q.example/:_authToken=${Q_TOKEN?} ; optional\n" +
		"//s.example/:_password=\"${S_PW}\"\n" +
		"always-auth=true\n"
	if err := os.WriteFile(env.Npmrc, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := find(t, Check(env), "npmrc tokens"); r.Status != Pass {
		t.Errorf("env reference = %+v", r)
	}
	for _, tc := range []struct{ body, line string }{
		{"//registry.npmjs.org/:_authToken=CANARY-TOKEN\n", "line 1 "},
		{"registry=https://r.example/\n//r.example/:_auth = \"CANARY-B64\"\n", "line 2 "},
		{"_password=CANARY-PW\n", "line 1 "},
		{"//r.example/:_authToken=pre${X}CANARY\n", "line 1 "},
		{"//r.example/:_authToken=${X}CANARY}\n", "line 1 "},
		{"//r.example/:_authToken=\"CANARY#x\"\n", "line 1 "},
	} {
		if err := os.WriteFile(env.Npmrc, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		r := find(t, Check(env), "npmrc tokens")
		if r.Status != Warn || !strings.Contains(r.Detail, tc.line) || !strings.Contains(r.Detail, "NPM_CONFIG_USERCONFIG") {
			t.Errorf("%q = %+v, want warn naming %q", tc.body, r, tc.line)
		}
		if strings.Contains(r.Detail, "CANARY") {
			t.Errorf("%q: detail leaks the value: %q", tc.body, r.Detail)
		}
	}
}

func TestCheckKillSwitch(t *testing.T) {
	env, _ := newEnv(t)
	env.FleetEnabled = func() (bool, error) { return false, nil }
	if r := find(t, Check(env), "kill switch"); r.Status != Warn {
		t.Errorf("disabled = %+v", r)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"0.40.0", "0.40.9", -1}, {"0.54.0", "0.54.0", 0}, {"2.1.330", "2.1.329", 1}, {"1.10.0", "1.9.9", 1}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
