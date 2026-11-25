package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

// Two runs on one round overlap (the Stop hook and a manual run): the
// second waits on the lock and takes the first one's pass.
func TestEngineerCheckConcurrentRunsSerialise(t *testing.T) {
	in, runs := checkFixture(t, "sleep 1")
	writeResponse(t, in.ProjectRoot, 1)
	var wg sync.WaitGroup
	results := make([]EngineerCheckResult, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := EngineerCheck(in)
			if err != nil {
				t.Error(err)
			}
			results[i] = res
		}()
	}
	wg.Wait()
	if n := checkRuns(t, runs); n != 1 {
		t.Fatalf("check ran %d times, want 1", n)
	}
	for i, r := range results {
		if r.Code != 0 {
			t.Errorf("run %d: %+v, want pass", i, r)
		}
	}
}

// A run that waited on a failing one reports that failure instead of
// rerunning the check.
func TestEngineerCheckWaiterReusesConcurrentFailure(t *testing.T) {
	in, runs := checkFixture(t, "exit 1")
	writeResponse(t, in.ProjectRoot, 1)
	sha := headOf(t, in.ProjectRoot)
	unlock, _, err := lockCheckRun(task.CheckLockPath(in.ProjectRoot, "demo", 1), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan EngineerCheckResult)
	go func() {
		res, err := EngineerCheck(in)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	time.Sleep(3 * checkLockPoll)
	if err := task.WriteCheckAttempt(in.ProjectRoot, "demo", 1, sha, "exit: 1\nother run tail\n"); err != nil {
		t.Fatal(err)
	}
	unlock()
	res := <-done
	if res.Code != 2 || !strings.Contains(res.Stderr, "concurrent run") || !strings.Contains(res.Stderr, "other run tail") {
		t.Fatalf("%+v, want the concurrent failure", res)
	}
	if n := checkRuns(t, runs); n != 0 {
		t.Errorf("check ran %d times, want 0", n)
	}
}

// A holder that outlives the check timeout leaves the second run a
// clean exit: the stop goes through and nothing runs.
func TestEngineerCheckLockTimeoutExitsCleanly(t *testing.T) {
	in, runs := checkFixture(t, "true")
	if err := os.WriteFile(filepath.Join(in.ProjectRoot, "spore.toml"),
		[]byte("[fleet.roles.engineer]\ncheck = 'echo x >> "+runs+"'\ncheck_timeout_seconds = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	grace := checkLockGrace
	checkLockGrace = 0
	t.Cleanup(func() { checkLockGrace = grace })
	writeResponse(t, in.ProjectRoot, 1)
	unlock, _, err := lockCheckRun(task.CheckLockPath(in.ProjectRoot, "demo", 1), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	res := mustCheck(t, in)
	if res.Code != 0 || !strings.Contains(res.Stderr, "still holds") {
		t.Fatalf("%+v, want a clean exit naming the held lock", res)
	}
	if n := checkRuns(t, runs); n != 0 {
		t.Errorf("check ran %d times, want 0", n)
	}
}
