package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func coordInbox(t *testing.T) string {
	t.Helper()
	inbox := filepath.Join(t.TempDir(), "coordinator", "proj", "inbox")
	if err := ensureInbox(inbox); err != nil {
		t.Fatal(err)
	}
	return inbox
}

func TestCodexThreadFileKeyedBySession(t *testing.T) {
	inbox := "/state/coordinator/proj/inbox"
	got := CodexThreadFile(inbox, "spore/proj/coordinator")
	if got != "/state/coordinator/proj/codex-threads/spore_proj_coordinator.json" {
		t.Errorf("CodexThreadFile = %q", got)
	}
	if CodexThreadFile(inbox, "a") == CodexThreadFile(inbox, "b") {
		t.Error("two sessions on one inbox share a thread record")
	}
}

func TestRecordCodexThreadOverwritesOnNewSource(t *testing.T) {
	inbox := coordInbox(t)
	if _, err := ReadCodexThread(inbox, "k"); !errors.Is(err, ErrNoCodexThread) {
		t.Fatalf("ReadCodexThread before record: err = %v, want ErrNoCodexThread", err)
	}
	if err := RecordCodexThread(inbox, "k", Request{SessionID: "t1", Source: "startup"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordCodexThread(inbox, "k", Request{SessionID: "t2", Source: "clear"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordCodexThread(inbox, "k", Request{Source: "resume"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCodexThread(inbox, "k")
	if err != nil || got != "t2" {
		t.Fatalf("ReadCodexThread = %q, %v; want t2 (empty session_id must not clobber)", got, err)
	}
}

func TestCodexSessionKeyEnv(t *testing.T) {
	t.Setenv("SPORE_COORDINATOR_SESSION", "spore/p/coordinator")
	if got := CodexSessionKey(); got != "spore/p/coordinator" {
		t.Errorf("CodexSessionKey = %q", got)
	}
	t.Setenv("SPORE_COORDINATOR_SESSION", "")
	t.Setenv("TMUX", "")
	if got := CodexSessionKey(); got != "" {
		t.Errorf("CodexSessionKey outside tmux = %q, want empty", got)
	}
}

type queued struct{ thread, msg string }

type fakeQueue struct {
	mu    sync.Mutex
	calls []queued
	fail  int
}

func (f *fakeQueue) queue(_ context.Context, thread, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		return errors.New("daemon down")
	}
	f.calls = append(f.calls, queued{thread, msg})
	return nil
}

func testCodexOpts(q *fakeQueue, w inboxWaiter, stderr *bytes.Buffer) codexWatchOpts {
	return codexWatchOpts{
		initWatcher: func(string) (inboxWaiter, error) { return w, nil },
		sleep:       func(time.Duration) {},
		queue:       q.queue,
		stderr:      stderr,
	}
}

func pendingJSON(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	return m
}

func TestDeliverCodexWaitsForThreadWithoutClaiming(t *testing.T) {
	inbox := coordInbox(t)
	writeTell(t, filepath.Join(inbox, "1.json"), `{"ts":"t","source":"worker","body":"ready"}`)
	q := &fakeQueue{}
	err := deliverCodex(context.Background(), inbox, "k", testCodexOpts(q, nil, &bytes.Buffer{}))
	if !errors.Is(err, ErrNoCodexThread) {
		t.Fatalf("err = %v, want ErrNoCodexThread", err)
	}
	if len(pendingJSON(t, inbox)) != 1 {
		t.Error("envelope was claimed before a thread was known")
	}
}

func TestDeliverCodexQueuesRenderedBatchAndClaims(t *testing.T) {
	inbox := coordInbox(t)
	RecordCodexThread(inbox, "k", Request{SessionID: "thread-1"})
	writeTell(t, filepath.Join(inbox, "1.json"), `{"ts":"2026-10-21T00:00:00Z","source":"worker","body":"ready abc"}`)
	writeTell(t, filepath.Join(inbox, "2.json"), `{"slug":"coordinator","ts":"2026-10-21T00:00:01Z","msg":"hi"}`)
	q := &fakeQueue{}
	if err := deliverCodex(context.Background(), inbox, "k", testCodexOpts(q, nil, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 1 {
		t.Fatalf("queue calls = %d, want 1 batch", len(q.calls))
	}
	want := "[2026-10-21T00:00:00Z] worker: ready abc\n[2026-10-21T00:00:01Z] tell: hi"
	if q.calls[0].thread != "thread-1" || q.calls[0].msg != want {
		t.Errorf("queued %+v, want thread-1 / %q", q.calls[0], want)
	}
	if len(pendingJSON(t, inbox)) != 0 || len(pendingJSON(t, filepath.Join(inbox, "read"))) != 2 {
		t.Error("envelopes not moved to read/")
	}
}

func TestDeliverCodexFailureUnclaims(t *testing.T) {
	inbox := coordInbox(t)
	RecordCodexThread(inbox, "k", Request{SessionID: "thread-1"})
	writeTell(t, filepath.Join(inbox, "1.json"), `{"ts":"t","source":"s","body":"b"}`)
	q := &fakeQueue{fail: 1}
	if err := deliverCodex(context.Background(), inbox, "k", testCodexOpts(q, nil, &bytes.Buffer{})); err == nil {
		t.Fatal("want error from failed queue")
	}
	if len(pendingJSON(t, inbox)) != 1 || len(pendingJSON(t, filepath.Join(inbox, "read"))) != 0 {
		t.Fatal("failed delivery did not return the envelope to the inbox")
	}
	if err := deliverCodex(context.Background(), inbox, "k", testCodexOpts(q, nil, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 1 {
		t.Errorf("queue calls = %d, want 1 after retry", len(q.calls))
	}
}

func TestDeliverCodexSplitsOversizeBatches(t *testing.T) {
	inbox := coordInbox(t)
	RecordCodexThread(inbox, "k", Request{SessionID: "thread-1"})
	big := strings.Repeat("x", maxCodexMessage/2)
	for _, n := range []string{"1", "2", "3"} {
		writeTell(t, filepath.Join(inbox, n+".json"), `{"ts":"t","source":"s","body":"`+big+`"}`)
	}
	q := &fakeQueue{}
	if err := deliverCodex(context.Background(), inbox, "k", testCodexOpts(q, nil, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) < 2 {
		t.Fatalf("queue calls = %d, want the batch split", len(q.calls))
	}
	for _, c := range q.calls {
		if len(c.msg) > maxCodexMessage+64 {
			t.Errorf("message of %d bytes exceeds the argv cap", len(c.msg))
		}
	}
}

// Two drains racing on one inbox (Claude asyncRewake drain plus the
// codex sidecar, or two sidecars) must deliver each envelope once.
func TestDeliverCodexNoDoubleDelivery(t *testing.T) {
	inbox := coordInbox(t)
	RecordCodexThread(inbox, "a", Request{SessionID: "thread-a"})
	RecordCodexThread(inbox, "b", Request{SessionID: "thread-b"})
	for i := 0; i < 50; i++ {
		writeTell(t, filepath.Join(inbox, fmt.Sprintf("%03d.json", i)), `{"ts":"t","source":"s","body":"b"}`)
	}
	q := &fakeQueue{}
	var claude bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		deliverCodex(context.Background(), inbox, "a", testCodexOpts(q, nil, &bytes.Buffer{}))
	}()
	go func() {
		defer wg.Done()
		deliverCodex(context.Background(), inbox, "b", testCodexOpts(q, nil, &bytes.Buffer{}))
	}()
	go func() { defer wg.Done(); drainInbox(inbox, &claude) }()
	wg.Wait()

	total := strings.Count(claude.String(), "\n")
	for _, c := range q.calls {
		total += strings.Count(c.msg, "\n") + 1
	}
	if total != 50 {
		t.Errorf("delivered %d envelopes across drains, want exactly 50", total)
	}
}

func TestWatchInboxCodexLoopDeliversOnWakeAndStops(t *testing.T) {
	inbox := coordInbox(t)
	RecordCodexThread(inbox, "k", Request{SessionID: "thread-1"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := &fakeQueue{}
	waits := 0
	w := &fakeWaiter{woke: true}
	w.afterWake = func() {
		waits++
		if waits == 1 {
			writeTell(t, filepath.Join(inbox, "1.json"), `{"ts":"t","source":"s","body":"late"}`)
			return
		}
		cancel()
	}
	if err := watchInboxCodex(ctx, inbox, "k", testCodexOpts(q, w, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 1 || q.calls[0].msg != "[t] s: late" {
		t.Errorf("queue calls = %+v, want the envelope that landed after start", q.calls)
	}
	if !w.closed {
		t.Error("watcher not closed")
	}
}

func TestWatchInboxCodexExitsWhenParentGone(t *testing.T) {
	inbox := coordInbox(t)
	o := testCodexOpts(&fakeQueue{}, &fakeWaiter{}, &bytes.Buffer{})
	o.alive = func() bool { return false }
	done := make(chan error, 1)
	go func() { done <- watchInboxCodex(context.Background(), inbox, "k", o) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sidecar did not exit with its parent gone")
	}
}

func TestCodexQueueExecsInjectedBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argsFile + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPORE_CODEX_BIN", bin)
	if err := codexQueue(context.Background(), "thread-1", "[t] s: hello"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(argsFile)
	if string(got) != "queue\n--thread\nthread-1\n--message\n[t] s: hello\n" {
		t.Errorf("codex argv = %q", got)
	}

	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho no daemon >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := codexQueue(context.Background(), "thread-1", "m"); err == nil || !strings.Contains(err.Error(), "no daemon") {
		t.Errorf("codexQueue err = %v, want failure carrying stderr", err)
	}
}
