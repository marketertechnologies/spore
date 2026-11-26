package spore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSpore answers --version and --help, and runs doctor with the
// exit code in $FAKE_DOCTOR_EXIT.
const fakeSpore = `#!/bin/sh
case "$1" in
  --version) echo "0.54.0 (abc)" ;;
  --help) printf 'Commands:\n  doctor     Drift checks.\n' ;;
  doctor)
    echo "$@" > "$FAKE_DOCTOR_ARGS"
    if [ "$FAKE_DOCTOR_EXIT" = 0 ]; then printf 'PASS  binary: ok\n\n0 fail, 0 warn\n'; exit 0; fi
    printf 'FAIL  managed settings: hooks: differs\n\n1 fail, 0 warn\n'; exit 1 ;;
esac
`

func runSmoke(t *testing.T, doctorExit string) (string, string, error) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	sw := filepath.Join(dir, "sw")
	if err := os.MkdirAll(filepath.Join(sw, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sw, "bin", "spore"), []byte(fakeSpore), 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "doctor-args")
	cmd := exec.Command("bash", "scripts/smoke-post-upgrade.sh", "-", filepath.Join(dir, "no-repo"))
	cmd.Env = []string{
		"PATH=" + filepath.Join(sw, "bin") + ":" + os.Getenv("PATH"),
		"HOME=" + dir,
		"CODEX_HOME=" + filepath.Join(dir, "codex"),
		"SPORE_SMOKE_SW=" + sw,
		// No drop-in, so the tmux pane scan (step 6) never runs.
		"SPORE_SMOKE_DROPIN=" + filepath.Join(dir, "none.json"),
		"FAKE_DOCTOR_EXIT=" + doctorExit,
		"FAKE_DOCTOR_ARGS=" + argsFile,
	}
	out, err := cmd.CombinedOutput()
	args, _ := os.ReadFile(argsFile)
	return string(out), string(args), err
}

func TestSmokePostUpgradeRunsDoctor(t *testing.T) {
	out, args, err := runSmoke(t, "1")
	if err == nil {
		t.Fatalf("smoke passed with a failing doctor:\n%s", out)
	}
	if !strings.Contains(out, "FAIL  spore doctor: 1 fail, 0 warn") || !strings.Contains(out, "doctor: FAIL  managed settings: hooks") {
		t.Errorf("doctor failure not reported:\n%s", out)
	}
	if !strings.Contains(args, "doctor --sw ") || !strings.Contains(args, "--dropin ") {
		t.Errorf("doctor args = %q, want --sw and --dropin passed through", args)
	}

	out, _, _ = runSmoke(t, "0")
	if !strings.Contains(out, "PASS  spore doctor: 0 fail, 0 warn") {
		t.Errorf("passing doctor not reported:\n%s", out)
	}
}
