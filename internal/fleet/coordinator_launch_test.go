package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runLaunchShim runs spore-coordinator-launch.sh with a stub claude
// that records its argv, one arg per line. The shim prepends the nix
// system profile to PATH, so the copy points that prefix at the stub.
func runLaunchShim(t *testing.T, env ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	raw, err := os.ReadFile("../../bootstrap/handover/spore-coordinator-launch.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "argv")
	stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + out + "\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	tmuxStub := "#!/usr/bin/env bash\necho spore/myproj/coordinator\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(tmuxStub), 0o755); err != nil {
		t.Fatal(err)
	}
	shim := strings.ReplaceAll(string(raw), "/run/current-system/sw/bin", bin)
	script := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(script, []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	role := filepath.Join(dir, "role.md")
	if err := os.WriteFile(role, []byte("ROLE"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "myproj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = proj
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"SPORE_COORDINATOR_ROLE=" + role,
		"SPORE_COORDINATOR_ROLE_SHARED=" + filepath.Join(dir, "none"),
	}, env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch shim: %v\n%s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
}

func TestCoordinatorLaunchShimRemoteControl(t *testing.T) {
	got := runLaunchShim(t, "SPORE_COORDINATOR_MODEL=opus")
	want := []string{"--dangerously-skip-permissions", "--model", "opus",
		"--remote-control", "spore-myproj-coordinator", "ROLE"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestCoordinatorLaunchShimRemoteControlTmuxName(t *testing.T) {
	got := runLaunchShim(t, "TMUX=/tmp/sock,1,0", "TMUX_PANE=%0")
	want := []string{"--dangerously-skip-permissions",
		"--remote-control", "spore/myproj/coordinator", "ROLE"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestCoordinatorLaunchShimRemoteControlOptOut(t *testing.T) {
	got := runLaunchShim(t, "SPORE_COORDINATOR_REMOTE_CONTROL=0")
	want := []string{"--dangerously-skip-permissions", "ROLE"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("argv = %q, want %q", got, want)
	}
}
