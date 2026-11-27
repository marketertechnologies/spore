package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotifyCoordinator_WritesPoke(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)

	slug := "myproject"
	if err := NotifyCoordinator(slug); err != nil {
		t.Fatalf("NotifyCoordinator: %v", err)
	}

	inbox := filepath.Join(state, slug, "inbox")
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	var jsonFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	if len(jsonFiles) != 1 {
		t.Fatalf("expected 1 json file, got %d: %v", len(jsonFiles), jsonFiles)
	}

	b, err := os.ReadFile(filepath.Join(inbox, jsonFiles[0]))
	if err != nil {
		t.Fatal(err)
	}
	var ev tellEvent
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("unmarshal poke: %v", err)
	}
	if ev.Ts == "" {
		t.Error("ts is empty")
	}
	if ev.Source != "notification" {
		t.Errorf("source=%q, want notification", ev.Source)
	}
	if ev.Body != "poke" {
		t.Errorf("body=%q, want poke", ev.Body)
	}
}

func TestNotifyCoordinator_AtomicWrite(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)

	if err := NotifyCoordinator("proj"); err != nil {
		t.Fatal(err)
	}

	tmpDir := filepath.Join(state, "proj", "inbox", ".tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("leftover in .tmp: %s", e.Name())
		}
	}
}

func TestNotifyCoordinator_CreatesInboxDirs(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)

	if err := NotifyCoordinator("fresh"); err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"", ".tmp", "read"} {
		p := filepath.Join(state, "fresh", "inbox", sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing dir %s: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}
}

const permissionPayload = `{"session_id":"abc","hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`

func setWorkerEnv(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)
	t.Setenv("WT_PROJECT", "myproject")
	t.Setenv("SPORE_ROLE", "")
	t.Setenv("SPORE_TASK_SLUG", "fix-thing")
	t.Setenv("SPORE_TASK_INBOX", filepath.Join(t.TempDir(), "fix-thing", "inbox"))
	return filepath.Join(state, "myproject", "inbox")
}

func inboxEvents(t *testing.T, inbox string) []tellEvent {
	t.Helper()
	entries, err := os.ReadDir(inbox)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	var out []tellEvent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(inbox, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var ev tellEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatalf("unmarshal %s: %v", e.Name(), err)
		}
		out = append(out, ev)
	}
	return out
}

func TestNotifyCoordinatorEnv_NoProjectIsNoop(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)
	t.Setenv("WT_PROJECT", "")
	t.Setenv("SPORE_TASK_INBOX", "")

	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatalf("NotifyCoordinatorEnv: %v", err)
	}
	entries, _ := os.ReadDir(state)
	if len(entries) != 0 {
		t.Errorf("expected no project dirs created, got %d", len(entries))
	}
}

func TestNotifyCoordinatorEnv_WorkerPermissionPromptForwarded(t *testing.T) {
	inbox := setWorkerEnv(t)

	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatalf("NotifyCoordinatorEnv: %v", err)
	}
	evs := inboxEvents(t, inbox)
	if len(evs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(evs))
	}
	want := "worker fix-thing: permission_prompt: Claude needs your permission to use Bash"
	if evs[0].Source != "notification" || evs[0].Body != want {
		t.Errorf("got source=%q body=%q, want notification / %q", evs[0].Source, evs[0].Body, want)
	}
}

func TestNotifyCoordinatorEnv_RolePaneIsNoop(t *testing.T) {
	inbox := setWorkerEnv(t)
	t.Setenv("SPORE_ROLE", "engineer")

	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatalf("NotifyCoordinatorEnv: %v", err)
	}
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Errorf("role pane wrote to the coordinator inbox (err=%v)", err)
	}
}

func TestNotifyCoordinatorEnv_SkipsInfoOnly(t *testing.T) {
	for _, payload := range []string{
		`{"message":"Claude is waiting for your input","notification_type":"idle_prompt"}`,
		`{"message":"Login successful","notification_type":"auth_success"}`,
		`{"message":"Claude is waiting for your input"}`,
		``,
		`not json`,
		`{}`,
	} {
		t.Run(payload, func(t *testing.T) {
			inbox := setWorkerEnv(t)
			if err := NotifyCoordinatorEnv(strings.NewReader(payload)); err != nil {
				t.Fatalf("NotifyCoordinatorEnv: %v", err)
			}
			if evs := inboxEvents(t, inbox); len(evs) != 0 {
				t.Errorf("payload %q forwarded: %+v", payload, evs)
			}
		})
	}
}

func TestNotifyCoordinatorEnv_ForwardsOtherTypes(t *testing.T) {
	for _, payload := range []string{
		`{"message":"Server wants input","notification_type":"elicitation_dialog"}`,
		`{"message":"something new","notification_type":"brand_new_type"}`,
		`{"message":"Claude needs your permission"}`,
	} {
		t.Run(payload, func(t *testing.T) {
			inbox := setWorkerEnv(t)
			if err := NotifyCoordinatorEnv(strings.NewReader(payload)); err != nil {
				t.Fatalf("NotifyCoordinatorEnv: %v", err)
			}
			if evs := inboxEvents(t, inbox); len(evs) != 1 {
				t.Errorf("payload %q: got %d messages, want 1", payload, len(evs))
			}
		})
	}
}

func TestNotifyCoordinatorEnv_DedupsBurstPerSlugTypeAndMessage(t *testing.T) {
	inbox := setWorkerEnv(t)

	for i := 0; i < 3; i++ {
		if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(inboxEvents(t, inbox)); n != 1 {
		t.Fatalf("burst of 3 identical permission prompts: got %d messages, want 1", n)
	}

	otherPrompt := `{"message":"Claude needs your permission to use Edit","notification_type":"permission_prompt"}`
	if err := NotifyCoordinatorEnv(strings.NewReader(otherPrompt)); err != nil {
		t.Fatal(err)
	}
	if n := len(inboxEvents(t, inbox)); n != 2 {
		t.Fatalf("a different permission prompt must pass: got %d messages, want 2", n)
	}

	other := `{"message":"Server wants input","notification_type":"elicitation_dialog"}`
	if err := NotifyCoordinatorEnv(strings.NewReader(other)); err != nil {
		t.Fatal(err)
	}
	if n := len(inboxEvents(t, inbox)); n != 3 {
		t.Fatalf("a different type must pass: got %d messages, want 3", n)
	}

	t.Setenv("SPORE_TASK_SLUG", "other-task")
	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatal(err)
	}
	if n := len(inboxEvents(t, inbox)); n != 4 {
		t.Fatalf("a different worker must pass: got %d messages, want 4", n)
	}
}

func TestNotifyCoordinatorEnv_SameBlockingPromptPassesAfterBurstWindow(t *testing.T) {
	inbox := setWorkerEnv(t)
	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatal(err)
	}
	stamp := notifyStamp(filepath.Dir(inbox), "fix-thing", "permission_prompt", "Claude needs your permission to use Bash")
	past := time.Now().Add(-NotifyBurstWindow - time.Second)
	if err := os.Chtimes(stamp, past, past); err != nil {
		t.Fatalf("stamp not written where expected: %v", err)
	}
	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatal(err)
	}
	if n := len(inboxEvents(t, inbox)); n != 2 {
		t.Fatalf("second Bash approval after the burst window: got %d messages, want 2", n)
	}
}

func TestDedupWindow(t *testing.T) {
	if got := dedupWindow("permission_prompt"); got != NotifyBurstWindow {
		t.Errorf("permission_prompt window = %v, want %v", got, NotifyBurstWindow)
	}
	if got := dedupWindow("auth_storage_failure"); got != NotifyDedupWindow {
		t.Errorf("auth_storage_failure window = %v, want %v", got, NotifyDedupWindow)
	}
}

func TestNotifyCoordinatorEnv_FailedWriteDoesNotStamp(t *testing.T) {
	inbox := setWorkerEnv(t)
	if err := os.MkdirAll(filepath.Dir(inbox), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err == nil {
		t.Fatal("expected an error writing into a file-shaped inbox")
	}
	if err := os.Remove(inbox); err != nil {
		t.Fatal(err)
	}
	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatal(err)
	}
	if n := len(inboxEvents(t, inbox)); n != 1 {
		t.Fatalf("retry after a failed write: got %d messages, want 1", n)
	}
}

func TestNotifyCoordinatorEnv_NilStdinIsNoop(t *testing.T) {
	inbox := setWorkerEnv(t)
	if err := NotifyCoordinatorEnv(nil); err != nil {
		t.Fatal(err)
	}
	if evs := inboxEvents(t, inbox); len(evs) != 0 {
		t.Errorf("nil stdin forwarded: %+v", evs)
	}
}

func TestNotifyStamp_WindowExpiresAndPrunes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stamp := notifyStamp(dir, "w", "permission_prompt", "msg")
	if sentRecently(stamp, now, NotifyDedupWindow) {
		t.Fatal("unsent stamp reported recent")
	}
	markSent(stamp, now)
	if !sentRecently(stamp, now.Add(NotifyDedupWindow-time.Second), NotifyDedupWindow) {
		t.Fatal("send inside the window not suppressed")
	}
	if sentRecently(stamp, now.Add(NotifyDedupWindow+time.Second), NotifyDedupWindow) {
		t.Fatal("send after the window suppressed")
	}
	if notifyStamp(dir, "w", "permission_prompt", "other") == stamp {
		t.Fatal("different messages share a stamp")
	}

	later := now.Add(notifyStampMaxAge + time.Minute)
	fresh := notifyStamp(dir, "w", "permission_prompt", "other")
	markSent(fresh, later)
	if _, err := os.Stat(stamp); !os.IsNotExist(err) {
		t.Errorf("stale stamp not pruned (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh stamp missing: %v", err)
	}
}

func TestNotifyCoordinatorEnv_SelfPokeIsNoop(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)
	t.Setenv("WT_PROJECT", "myproject")
	t.Setenv("SPORE_ROLE", "")
	inbox := filepath.Join(state, "myproject", "inbox")
	t.Setenv("SPORE_TASK_INBOX", inbox)

	if err := NotifyCoordinatorEnv(strings.NewReader(permissionPayload)); err != nil {
		t.Fatalf("NotifyCoordinatorEnv: %v", err)
	}
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Errorf("expected inbox not created (self-poke skipped), got err=%v", err)
	}
}

func TestNotifyCoordinator_HonorsXDGStateHome(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", xdg)
	if err := NotifyCoordinator("proj"); err != nil {
		t.Fatal(err)
	}
	if got := pendingJSON(t, filepath.Join(xdg, "spore", "coordinator", "proj", "inbox")); len(got) != 1 {
		t.Errorf("XDG inbox holds %v, want one notice", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Errorf("notice touched HOME/.local (err %v)", err)
	}
}

func TestNotifyCoordinator_NoStateBaseIsAnError(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if err := NotifyCoordinator("proj"); err == nil {
		t.Error("NotifyCoordinator with no HOME and no XDG_STATE_HOME: err = nil")
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("notice written under the cwd: %v", entries)
	}
}
