package spore

import (
	"os/exec"
	"strings"
	"testing"
)

func runAttach(t *testing.T, cmd string) (string, string, int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	c := exec.Command(bash, "bootstrap/handover/spore-attach.sh", "-c", cmd)
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	err = c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run spore-attach: %v", err)
	}
	return stdout.String(), stderr.String(), code
}

func TestSporeAttachFallsThroughToBash(t *testing.T) {
	stdout, stderr, code := runAttach(t, `echo hi`)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.HasSuffix(stdout, "hi\n") {
		t.Fatalf("stdout = %q, want trailing %q", stdout, "hi\n")
	}

	stdout, _, _ = runAttach(t, `printf '%s\n' "a  b"`)
	if !strings.HasSuffix(stdout, "a  b\n") {
		t.Fatalf("quoted command mangled: stdout = %q", stdout)
	}
}

func TestSporeAttachKeepsAttachModes(t *testing.T) {
	for _, cmd := range []string{
		"/usr/local/bin/spore-attach bogus",
		"  spore-attach.sh bogus",
	} {
		_, stderr, code := runAttach(t, cmd)
		if code != 2 || !strings.Contains(stderr, "unknown mode") {
			t.Fatalf("%q: exit %d, stderr %q; want exit 2 and unknown mode", cmd, code, stderr)
		}
	}
}
