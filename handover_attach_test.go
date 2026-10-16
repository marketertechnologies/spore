package spore

import (
	"os/exec"
	"strings"
	"testing"
)

func runAttach(t *testing.T, cmd string, flags ...string) (string, string, int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	args := append([]string{"bootstrap/handover/spore-attach.sh"}, flags...)
	c := exec.Command(bash, append(args, "-c", cmd)...)
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

func TestSporeAttachSkipsLoginShellFlags(t *testing.T) {
	for _, flags := range [][]string{{"-i", "-l"}, {"-l"}, {"-il"}, {"--login"}} {
		stdout, stderr, code := runAttach(t, `echo hi`, flags...)
		if code != 0 || !strings.HasSuffix(stdout, "hi\n") {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q; want trailing hi", flags, code, stdout, stderr)
		}
	}

	_, stderr, code := runAttach(t, "spore-attach bogus", "-i", "-l")
	if code != 2 || !strings.Contains(stderr, "unknown mode: bogus") {
		t.Fatalf("attach behind flags: exit %d, stderr %q; want exit 2 and unknown mode: bogus", code, stderr)
	}

	_, stderr, code = runAttach(t, "echo hi", "-x")
	if code != 2 || !strings.Contains(stderr, "unknown mode: -x") {
		t.Fatalf("unknown flag: exit %d, stderr %q; want exit 2", code, stderr)
	}
}

func TestSporeAttachPipedLoginRunsStdin(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	c := exec.Command(bash, "bootstrap/handover/spore-attach.sh")
	c.Stdin = strings.NewReader("echo ok\n")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("run spore-attach: %v, output: %s", err, out)
	}
	if !strings.HasSuffix(string(out), "ok\n") {
		t.Fatalf("output = %q, want trailing %q", out, "ok\n")
	}
}
