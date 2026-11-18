package task

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// liveResult is how the live isolation check wants the test to end.
type liveResult int

const (
	// liveSkip: the mechanism cannot build a namespace on this host (e.g.
	// no usable userns), so there is nothing to assert here.
	liveSkip liveResult = iota
	// livePass: isolation held; every worker bound the port.
	livePass
	// liveFail: isolation did not hold (a worker errored or never bound).
	liveFail
)

// classifyNetnsLive decides the outcome of the live isolation check. It
// first runs a cheap probe (a trivial wrapped command) to see whether this
// host can build a namespace at all; if the probe errors the mechanism is
// not runnable here (a CI runner without userns is not a spore bug) so it
// returns liveSkip. Only after the probe succeeds does it launch the two
// parallel workers that each bind the SAME loopback port: with isolation
// each gets a private netns and both bind, so once the probe has passed any
// worker error means a genuine isolation breach and returns liveFail. run
// executes a wrapped command and returns its combined output; it is a
// parameter so tests drive each scenario without spawning pasta.
func classifyNetnsLive(run func(wrapped string) ([]byte, error), probeWrapped, workerWrapped, port string) (liveResult, string) {
	if out, err := run(probeWrapped); err != nil {
		return liveSkip, fmt.Sprintf(
			"network isolation cannot build a namespace on this host "+
				"(userns likely unavailable): %v\noutput: %s", err, out)
	}

	var wg sync.WaitGroup
	outs := make([]string, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := run(workerWrapped)
			outs[i], errs[i] = string(out), err
		}(i)
	}
	wg.Wait()

	for i := 0; i < 2; i++ {
		if errs[i] != nil {
			return liveFail, fmt.Sprintf(
				"worker %d failed after the probe succeeded, so the "+
					"namespace built but isolation did not hold "+
					"(breach): %v\noutput: %s", i, errs[i], outs[i])
		}
		if !strings.Contains(outs[i], "BOUND "+port) {
			return liveFail, fmt.Sprintf("worker %d did not bind :%s: %s", i, port, outs[i])
		}
	}
	return livePass, ""
}

// TestNetnsIsolationLive proves the real wrapForIsolation output gives
// two workers their own netns: both bind the SAME loopback port in
// parallel without EADDRINUSE, which is only possible if each command
// runs in a private network namespace. Self-skips where the mechanism
// is not installed (pasta / python3 missing) or not runnable (no usable
// userns, e.g. a GitHub Actions runner) so `just check` stays green
// there; runs for real on the flake-configured host (passt in
// systemPackages).
func TestNetnsIsolationLive(t *testing.T) {
	if _, err := exec.LookPath("pasta"); err != nil {
		t.Skip("pasta not on PATH; skipping live netns isolation check")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH; skipping live netns isolation check")
	}

	// A worker that binds the fixed port on loopback and reports. With
	// isolation the port is per-netns, so two of these collide only if
	// isolation failed.
	const port = "3000"
	bind := python + ` -c 'import socket; s=socket.socket(); ` +
		`s.bind(("127.0.0.1",` + port + `)); print("BOUND ` + port + `")'`

	worker, err := wrapForIsolation("sh -c "+shellSingleQuoteLocal(bind), true)
	if err != nil {
		t.Fatalf("wrapForIsolation: %v", err)
	}
	// The probe is the same wrapper on a trivial command: it exercises
	// namespace setup without asserting isolation, so a setup failure
	// (no userns) is cleanly separated from a real isolation breach.
	probe, err := wrapForIsolation("true", true)
	if err != nil {
		t.Fatalf("wrapForIsolation probe: %v", err)
	}

	run := func(wrapped string) ([]byte, error) {
		return exec.Command("sh", "-c", wrapped).CombinedOutput()
	}

	result, reason := classifyNetnsLive(run, probe, worker, port)
	switch result {
	case liveSkip:
		t.Skip(reason)
	case liveFail:
		t.Fatal(reason)
	}
}

// shellSingleQuoteLocal mirrors the shell single-quote escaping used
// elsewhere in the kernel, kept test-local to avoid exporting a helper
// solely for this file.
func shellSingleQuoteLocal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
