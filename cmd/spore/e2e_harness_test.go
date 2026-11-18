package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	sharedBinOnce sync.Once
	sharedBin     string
	sharedBinErr  error
)

// sharedSporeBin builds the CLI once per test process. The binary lands
// in TMUX_TMPDIR, which TestMain creates and removes, so no build output
// outlives the run.
func sharedSporeBin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go not available: %v", err)
	}
	sharedBinOnce.Do(func() {
		dir := os.Getenv("TMUX_TMPDIR")
		if dir == "" {
			dir = os.TempDir()
		}
		sharedBin = filepath.Join(dir, "spore-e2e")
		if out, err := exec.Command("go", "build", "-o", sharedBin, ".").CombinedOutput(); err != nil {
			sharedBinErr = errors.New(string(out))
		}
	})
	if sharedBinErr != nil {
		t.Fatalf("go build: %v", sharedBinErr)
	}
	return sharedBin
}

// e2eEnv is a scrubbed environment for a CLI run: private HOME and XDG
// dirs, no inherited SPORE_/WT_/CLAUDE_ identity, no tmux pane. TMUX_TMPDIR
// stays, so tmux calls land on the per-process test server.
func e2eEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "SPORE_"), strings.HasPrefix(k, "WT_"),
			strings.HasPrefix(k, "CLAUDE"), strings.HasPrefix(k, "AGENT_BUDGET_"),
			strings.HasPrefix(k, "XDG_"), k == "HOME", k == "TMUX", k == "TMUX_PANE",
			k == "HTTPS_PROXY", k == "https_proxy", k == "HTTP_PROXY", k == "http_proxy",
			k == "NO_PROXY", k == "no_proxy", k == "SKYHELM_STATE_DIR":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_RUNTIME_DIR="+filepath.Join(home, "run"),
	)
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func runCLI(t *testing.T, bin, dir string, env []string, stdin string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := cliResult{stdout: out.String(), stderr: errb.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.code = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("run %v: %v", args, err)
	}
	return res
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
