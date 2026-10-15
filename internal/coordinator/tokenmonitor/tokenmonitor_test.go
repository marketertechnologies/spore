package tokenmonitor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIsCoordinator(t *testing.T) {
	cases := []struct {
		inbox    string
		stateDir string
		want     bool
	}{
		{"/state/coord", "/state/coord", true},
		{"/state/coord/myproj/inbox", "/state/coord", true},
		{"/state/wt/slug/inbox", "/state/coord", false},
		{"", "/state/coord", false},
	}
	for _, tc := range cases {
		cfg := Config{Inbox: tc.inbox, StateDir: tc.stateDir}
		if got := cfg.IsCoordinator(); got != tc.want {
			t.Errorf("IsCoordinator(%q, %q) = %v, want %v", tc.inbox, tc.stateDir, got, tc.want)
		}
	}
}

func TestCheckSkipsNonCoordinator(t *testing.T) {
	cfg := Config{
		Inbox:    "/some/other/path",
		StateDir: "/state/coord",
	}
	result := Check(cfg, HookPayload{})
	if result.Level != "skip" {
		t.Errorf("expected skip, got %s", result.Level)
	}
}

func TestCheckHardCap(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcript")
	os.MkdirAll(transcriptDir, 0o700)
	f := filepath.Join(transcriptDir, "session.jsonl")

	line := `{"type":"assistant","message":{"role":"assistant","content":[],"usage":{"input_tokens":195000,"output_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	os.WriteFile(f, []byte(line+"\n"), 0o644)

	stateDir := filepath.Join(dir, "state")
	cfg := Config{
		SoftCap:  150000,
		HardCap:  190000,
		StateDir: stateDir,
		Inbox:    stateDir,
	}

	result := Check(cfg, HookPayload{SessionID: "test", TranscriptPath: f})
	if result.Level != "hard" {
		t.Errorf("expected hard, got %s", result.Level)
	}
	if !result.ShouldFire {
		t.Error("expected ShouldFire = true")
	}
	if result.Ctx != 195000 {
		t.Errorf("Ctx = %d, want 195000", result.Ctx)
	}
}

func TestCheckSoftCap(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcript")
	os.MkdirAll(transcriptDir, 0o700)
	f := filepath.Join(transcriptDir, "session.jsonl")

	line := `{"type":"assistant","message":{"role":"assistant","content":[],"usage":{"input_tokens":160000,"output_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	os.WriteFile(f, []byte(line+"\n"), 0o644)

	stateDir := filepath.Join(dir, "state")
	cfg := Config{
		SoftCap:  150000,
		HardCap:  190000,
		StateDir: stateDir,
		Inbox:    stateDir,
	}

	result := Check(cfg, HookPayload{SessionID: "test-soft", TranscriptPath: f})
	if result.Level != "soft" {
		t.Errorf("expected soft, got %s", result.Level)
	}
	if !result.ShouldFire {
		t.Error("expected ShouldFire = true on first soft crossing")
	}

	result2 := Check(cfg, HookPayload{SessionID: "test-soft", TranscriptPath: f})
	if result2.Level != "ok" {
		t.Errorf("expected ok on second check (soft marker exists), got %s", result2.Level)
	}
	if result2.ShouldFire {
		t.Error("expected ShouldFire = false on second soft check")
	}
}

func TestCheckOk(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcript")
	os.MkdirAll(transcriptDir, 0o700)
	f := filepath.Join(transcriptDir, "session.jsonl")

	line := `{"type":"assistant","message":{"role":"assistant","content":[],"usage":{"input_tokens":50000,"output_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	os.WriteFile(f, []byte(line+"\n"), 0o644)

	stateDir := filepath.Join(dir, "state")
	cfg := Config{
		SoftCap:  150000,
		HardCap:  190000,
		StateDir: stateDir,
		Inbox:    stateDir,
	}

	result := Check(cfg, HookPayload{SessionID: "test-ok", TranscriptPath: f})
	if result.Level != "ok" {
		t.Errorf("expected ok, got %s", result.Level)
	}
}

func TestDriverKillCommandScope(t *testing.T) {
	// Supervise mode must spare the pane root (the respawn loop) and kill
	// only its child driver, else the loop dies, the session is torn down,
	// and an attached operator is detached. Single-exec mode kills the
	// tty, ending the driver and the session as designed.
	sup := DriverKillCommand(true)
	if !strings.Contains(sup, "pane_pid") || !strings.Contains(sup, "-P") {
		t.Errorf("supervise kill must be pane-pid-scoped (driver only): %q", sup)
	}
	if strings.Contains(sup, "pane_tty") {
		t.Errorf("supervise kill must not be tty-scoped (would kill the loop): %q", sup)
	}
	single := DriverKillCommand(false)
	if !strings.Contains(single, "pane_tty") || !strings.Contains(single, "-t") {
		t.Errorf("single-exec kill must be tty-scoped: %q", single)
	}
	if strings.Contains(single, "pane_pid") {
		t.Errorf("single-exec kill must not target pane_pid (driver is the pane root): %q", single)
	}
}

func TestCheckEmbedsSuperviseAwareKill(t *testing.T) {
	mkTranscript := func(t *testing.T, tokens int) (string, string) {
		t.Helper()
		dir := t.TempDir()
		tdir := filepath.Join(dir, "transcript")
		os.MkdirAll(tdir, 0o700)
		f := filepath.Join(tdir, "session.jsonl")
		line := `{"type":"assistant","message":{"role":"assistant","content":[],"usage":{"input_tokens":` +
			itoa(tokens) + `,"output_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
		os.WriteFile(f, []byte(line+"\n"), 0o644)
		return filepath.Join(dir, "state"), f
	}

	t.Run("hard supervise", func(t *testing.T) {
		stateDir, f := mkTranscript(t, 195000)
		cfg := Config{SoftCap: 150000, HardCap: 190000, StateDir: stateDir, Inbox: stateDir, Supervise: true}
		res := Check(cfg, HookPayload{SessionID: "h-sup", TranscriptPath: f})
		if res.Level != "hard" {
			t.Fatalf("level = %s, want hard", res.Level)
		}
		if !strings.Contains(res.Message, "pane_pid") {
			t.Errorf("supervise hard message must carry pane-pid kill:\n%s", res.Message)
		}
	})

	t.Run("hard single-exec", func(t *testing.T) {
		stateDir, f := mkTranscript(t, 195000)
		cfg := Config{SoftCap: 150000, HardCap: 190000, StateDir: stateDir, Inbox: stateDir, Supervise: false}
		res := Check(cfg, HookPayload{SessionID: "h-single", TranscriptPath: f})
		if res.Level != "hard" {
			t.Fatalf("level = %s, want hard", res.Level)
		}
		if !strings.Contains(res.Message, "pane_tty") {
			t.Errorf("single-exec hard message must carry tty kill:\n%s", res.Message)
		}
	})
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func TestAppendLedger(t *testing.T) {
	dir := t.TempDir()
	ledgerFile := filepath.Join(dir, "ledger.jsonl")
	cfg := Config{
		StateDir:   dir,
		LedgerFile: ledgerFile,
	}
	cfg = cfg.Defaults()
	appendLedger(cfg, "sess1", 100000, false, false)

	content, err := os.ReadFile(ledgerFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) == 0 {
		t.Error("expected ledger content")
	}
}
