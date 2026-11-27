package hooks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

// P1.13: N Stop-hook watchers (each acks inflight/ on start, then
// drains) race M parallel tells at settle 0. A watcher's ack must never
// take a message another watcher claimed but has not printed yet.
func TestWatchInbox_ConcurrentWatchersAckOnlyDelivered(t *testing.T) {
	// Watchers of one session share an inflight dir, so only the claim
	// dir keeps one's ack off another's unprinted claim; own sessions
	// cover the per-session split.
	t.Run("shared session", func(t *testing.T) { concurrentWatchers(t, func(int) string { return "shared" }) })
	t.Run("no session", func(t *testing.T) { concurrentWatchers(t, func(int) string { return "" }) })
	t.Run("own sessions", func(t *testing.T) { concurrentWatchers(t, func(w int) string { return fmt.Sprintf("session-%d", w) }) })
}

func concurrentWatchers(t *testing.T, session func(int) string) {
	_, _, inbox := setupInbox(t)
	const watchers, tells = 4, 400
	opts := watchOpts{
		timeout: 0, settle: 0,
		initWatcher: func(string) (inboxWaiter, error) { return nil, errors.New("no inotify in test") },
		sleep:       func(time.Duration) {},
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	outs := make([]bytes.Buffer, watchers)
	for w := 0; w < watchers; w++ {
		opts.session = session(w)
		wg.Add(1)
		go func(buf *bytes.Buffer, opts watchOpts) {
			defer wg.Done()
			for last := false; !last; {
				select {
				case <-done:
					last = true
				default:
				}
				if err := watchInboxAt(inbox, buf, io.Discard, opts); err != nil && !errors.Is(err, ErrWake) {
					t.Error(err)
					return
				}
			}
		}(&outs[w], opts)
	}
	var tw sync.WaitGroup
	for i := 0; i < tells; i++ {
		tw.Add(1)
		go func(i int) {
			defer tw.Done()
			if err := task.TellDir(inbox, "x", fmt.Sprintf("m-%d-%s", i, strings.Repeat("b", 2048))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	tw.Wait()
	close(done)
	wg.Wait()

	seen := map[string]int{}
	for i := range outs {
		for _, line := range strings.Split(strings.TrimRight(outs[i].String(), "\n"), "\n") {
			if line == "" {
				continue
			}
			_, body, ok := strings.Cut(line, "unverified): ")
			if !ok || body == "" {
				t.Errorf("bad line: %.200q", line)
				continue
			}
			seen[body]++
		}
	}
	for i := 0; i < tells; i++ {
		if m := fmt.Sprintf("m-%d-%s", i, strings.Repeat("b", 2048)); seen[m] != 1 {
			t.Errorf("m-%d delivered %d times", i, seen[m])
		}
	}
	if left := pendingJSON(t, inbox); len(left) != 0 {
		t.Errorf("undelivered: %v", left)
	}
	if left, _ := os.ReadDir(filepath.Join(inbox, claimingDir)); len(left) != 0 {
		t.Errorf("claim dirs left behind: %d", len(left))
	}
}

// A watcher killed between its claim and its print leaves an unlocked
// claim dir; the next watcher puts those messages back and delivers
// them. A locked claim dir is a live drain and stays untouched.
func TestWatchInbox_RecoversDeadWatcherClaim(t *testing.T) {
	_, _, inbox := setupInbox(t)
	dead := filepath.Join(inbox, claimingDir, "999999999-abc")
	if err := os.MkdirAll(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTell(t, filepath.Join(dead, "100.json"), `{"body":"stranded"}`)
	live := filepath.Join(inbox, claimingDir, fmt.Sprintf("%d-abc", os.Getpid()))
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTell(t, filepath.Join(live, "200.json"), `{"body":"mid-claim"}`)
	lock, err := lockClaim(live)
	if err != nil || lock == nil {
		t.Fatalf("lockClaim: %v", err)
	}
	defer lock.Close()

	opts := watchOpts{
		timeout: 0, settle: 0,
		initWatcher: func(string) (inboxWaiter, error) { return nil, errors.New("no inotify in test") },
		sleep:       func(time.Duration) {},
	}
	var out bytes.Buffer
	if err := watchInboxAt(inbox, &out, io.Discard, opts); !errors.Is(err, ErrWake) {
		t.Fatalf("err=%v, want ErrWake", err)
	}
	if !strings.Contains(out.String(), "stranded") || strings.Contains(out.String(), "mid-claim") {
		t.Errorf("out = %q, want the dead claim only", out.String())
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("dead claim dir kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(live, "200.json")); err != nil {
		t.Errorf("live claim touched: %v", err)
	}
}

// Two agents on one inbox: each Stop acks only its own session's turn
// and each StopFailure requeues only its own, so a failed turn of one
// neither loses its redelivery to the other's ack nor redelivers the
// other's acked message.
func TestWatchInbox_SessionsAckAndRequeueOnlyTheirOwnTurn(t *testing.T) {
	_, _, inbox := setupInbox(t)
	opts := func(session string) watchOpts {
		return watchOpts{
			session: session, timeout: 0, settle: 0,
			initWatcher: func(string) (inboxWaiter, error) { return nil, errors.New("no inotify in test") },
			sleep:       func(time.Duration) {},
		}
	}
	writeTell(t, filepath.Join(inbox, "100.json"), `{"body":"m1"}`)
	var w1 bytes.Buffer
	if err := watchInboxAt(inbox, &w1, io.Discard, opts("s1")); !errors.Is(err, ErrWake) || !strings.Contains(w1.String(), "m1") {
		t.Fatalf("s1 drain: err=%v out=%q", err, w1.String())
	}
	writeTell(t, filepath.Join(inbox, "200.json"), `{"body":"m2"}`)
	var w2 bytes.Buffer
	if err := watchInboxAt(inbox, &w2, io.Discard, opts("s2")); !errors.Is(err, ErrWake) || !strings.Contains(w2.String(), "m2") {
		t.Fatalf("s2 drain: err=%v out=%q", err, w2.String())
	}
	if got := pendingJSON(t, filepath.Join(inbox, "read")); len(got) != 0 {
		t.Errorf("s2's start acked another session's turn: %v", got)
	}

	// s1's turn hits an API error; s2's turn succeeded.
	if err := RequeueInflight(inbox, "s1"); err != nil {
		t.Fatal(err)
	}
	if got := pendingJSON(t, inbox); len(got) != 1 || filepath.Base(got[0]) != "redeliver-100.json" {
		t.Errorf("inbox after s1's failed turn: %v, want m1 only", got)
	}
	var w2b bytes.Buffer
	if err := watchInboxAt(inbox, &w2b, io.Discard, opts("s2")); !errors.Is(err, ErrWake) {
		t.Fatalf("s2 second drain: err=%v", err)
	}
	if out := w2b.String(); !strings.Contains(out, "m1") || strings.Contains(out, "m2") {
		t.Errorf("redelivery: %q, want m1 once and no m2", out)
	}
	if got := pendingJSON(t, filepath.Join(inbox, "read")); len(got) != 1 || filepath.Base(got[0]) != "200.json" {
		t.Errorf("read/: %v, want s2's acked m2", got)
	}
}

// Loose files in inflight/ (a build before per-session dirs) are acked
// by the next session's Stop instead of staying there forever.
func TestWatchInbox_SessionAcksLegacyInflight(t *testing.T) {
	_, _, inbox := setupInbox(t)
	if err := os.MkdirAll(filepath.Join(inbox, "inflight"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTell(t, filepath.Join(inbox, "inflight", "100.json"), `{"body":"old"}`)
	opts := watchOpts{
		session: "s1", timeout: 0, settle: 0,
		initWatcher: func(string) (inboxWaiter, error) { return nil, errors.New("no inotify in test") },
		sleep:       func(time.Duration) {},
	}
	if err := watchInboxAt(inbox, io.Discard, io.Discard, opts); err != nil {
		t.Fatal(err)
	}
	if got := pendingJSON(t, filepath.Join(inbox, "read")); len(got) != 1 {
		t.Errorf("read/: %v, want the legacy file", got)
	}
}

func TestInflightDirKeepsSessionInside(t *testing.T) {
	for _, s := range []string{"../x", "..", ".", "a/b", strings.Repeat("a", 200)} {
		d := inflightDir("/in", s)
		if filepath.Dir(d) != "/in/inflight" || strings.HasPrefix(filepath.Base(d), ".") {
			t.Errorf("inflightDir(%q) = %q", s, d)
		}
	}
	if d := inflightDir("/in", "0f2c-uuid_1"); d != "/in/inflight/0f2c-uuid_1" {
		t.Errorf("plain session: %q", d)
	}
}

// Liveness is the claim dir's lock, not its pid: an unlocked dir named
// after a live pid (pid reuse, or a watcher in another pid namespace
// that died) is recovered.
func TestRecoverClaims_UnlockedDirOfLivePidIsDead(t *testing.T) {
	_, _, inbox := setupInbox(t)
	dir := filepath.Join(inbox, claimingDir, fmt.Sprintf("%d-old", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTell(t, filepath.Join(dir, "100.json"), `{"body":"stranded"}`)
	if err := recoverClaims(inbox); err != nil {
		t.Fatal(err)
	}
	if got := pendingJSON(t, inbox); len(got) != 1 {
		t.Errorf("inbox after recovery: %v, want the stranded message", got)
	}
}

// A claim dir removed by recoverClaims before its owner locks it yields
// no lock, so the owner claims nothing into a dir it does not hold.
func TestLockClaim_RemovedDirYieldsNoLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "1-x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if f, err := lockClaim(dir); f != nil || err != nil {
		t.Errorf("lockClaim on removed dir = %v, %v", f, err)
	}
}

// A message that was printed but could not be moved to inflight still
// wakes the agent, and stays in the claim dir for recovery.
func TestDrainInbox_PrintedButUnmovedStillWakes(t *testing.T) {
	_, _, inbox := setupInbox(t)
	writeTell(t, filepath.Join(inbox, "100.json"), `{"body":"seen"}`)
	// A regular file where inflight/ should be makes every move fail.
	writeTell(t, filepath.Join(inbox, "inflight"), "")
	var out, errOut bytes.Buffer
	opts := watchOpts{
		session: "s1", timeout: 0, settle: 0,
		initWatcher: func(string) (inboxWaiter, error) { return nil, errors.New("no inotify in test") },
		sleep:       func(time.Duration) {},
	}
	if n, err := drainInbox(inbox, opts.session, &out, &errOut); n != 1 || err != nil {
		t.Fatalf("drain: n=%d err=%v", n, err)
	}
	if !strings.Contains(out.String(), "seen") || !strings.Contains(errOut.String(), "not moved to inflight") {
		t.Errorf("out=%q stderr=%q", out.String(), errOut.String())
	}
	if left, _ := filepath.Glob(filepath.Join(inbox, claimingDir, "*", "*.json")); len(left) != 1 {
		t.Errorf("claim dir holds %v, want the unmoved message", left)
	}
}

// Another session's inflight dir is acked once it is older than any
// turn (the session is gone); a fresh one is left alone.
func TestAckInflight_SweepsStaleSessionDirs(t *testing.T) {
	_, _, inbox := setupInbox(t)
	for _, s := range []string{"gone", "busy"} {
		dir := inflightDir(inbox, s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTell(t, filepath.Join(dir, s+".json"), `{"body":"x"}`)
	}
	old := time.Now().Add(-2 * staleInflight)
	if err := os.Chtimes(inflightDir(inbox, "gone"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := ackInflight(inbox, "me"); err != nil {
		t.Fatal(err)
	}
	if got := pendingJSON(t, filepath.Join(inbox, "read")); len(got) != 1 || filepath.Base(got[0]) != "gone.json" {
		t.Errorf("read/: %v, want the stale session's message", got)
	}
	if _, err := os.Stat(inflightDir(inbox, "gone")); !os.IsNotExist(err) {
		t.Errorf("stale session dir kept: %v", err)
	}
	if got := pendingJSON(t, inflightDir(inbox, "busy")); len(got) != 1 {
		t.Errorf("fresh session dir touched: %v", got)
	}
}

// A session's Stop never acks the bucket of a watcher run without a
// session, so that watcher's failed turn can still be requeued.
func TestAckInflight_LeavesNoSessionBucket(t *testing.T) {
	_, _, inbox := setupInbox(t)
	if _, err := claimInbox(inbox, filepath.Join("inflight", noSession)); err != nil {
		t.Fatal(err)
	}
	writeTell(t, filepath.Join(inbox, "100.json"), `{"body":"x"}`)
	if _, err := claimInbox(inbox, filepath.Join("inflight", noSession)); err != nil {
		t.Fatal(err)
	}
	if err := ackInflight(inbox, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := RequeueInflight(inbox, ""); err != nil {
		t.Fatal(err)
	}
	if got := pendingJSON(t, inbox); len(got) != 1 {
		t.Errorf("requeued: %v, want the session-less turn's message", got)
	}
}
