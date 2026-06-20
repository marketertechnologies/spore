package tokenstatus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	// Deterministic tier for tests: never shell out to live credentials.
	activeTierFn = func() string { return "" }
}

func writeTee(t *testing.T, path string, v tee) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTranscript(t *testing.T, dir string, ctx int) string {
	t.Helper()
	path := filepath.Join(dir, "session.jsonl")
	line := `{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":` +
		itoa(ctx) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	return strings.TrimSpace(string(jsonNum(n)))
}

func jsonNum(n int) []byte {
	b, _ := json.Marshal(n)
	return b
}

// isolateEnv points the worker/coordinator dirs at a temp tree and clears
// the spawn env so detection is deterministic.
func isolateEnv(t *testing.T, root string) {
	t.Helper()
	t.Setenv("SPORE_WORKER_TOKEN_DIR", filepath.Join(root, "worker-token"))
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", filepath.Join(root, "coord"))
	t.Setenv("SPORE_TASK_INBOX", "")
	t.Setenv("SPORE_ACCOUNT_TIER", "")
	t.Setenv("NO_COLOR", "")
}

func runMode(t *testing.T, args []string, stdin string) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errb)
	return out.String(), code
}

func TestStatuslineLiveTranscript(t *testing.T) {
	root := t.TempDir()
	isolateEnv(t, root)
	t.Setenv("SPORE_ACCOUNT_TIER", "max")

	wt := filepath.Join(root, ".worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	tr := writeTranscript(t, root, 95000)
	payload := `{"cwd":"` + wt + `","session_id":"sid1","transcript_path":"` + tr + `"}`

	out, code := runMode(t, []string{"--statusline", "--no-color"}, payload)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	// worker max cap default is 180k -> 95k is 52%.
	want := "ctx 95k / 180k (52%) max"
	if strings.TrimSpace(out) != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestStatuslineTeeFallback(t *testing.T) {
	root := t.TempDir()
	isolateEnv(t, root)

	writeTee(t, filepath.Join(root, "worker-token", "feat-y.json"), tee{
		Slug: "feat-y", SessionID: "sid2", Role: "worker", Tier: "sub",
		Ctx: 60000, CapSoft: 120000, CapHard: 120000, Pct: 50,
	})
	// No transcript_path -> live parse fails, tee by session_id wins.
	payload := `{"cwd":"/tmp/whatever","session_id":"sid2"}`

	out, _ := runMode(t, []string{"--statusline", "--no-color"}, payload)
	want := "ctx 60k / 120k (50%) sub"
	if strings.TrimSpace(out) != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestStatuslineZeroRender(t *testing.T) {
	root := t.TempDir()
	isolateEnv(t, root)

	wt := filepath.Join(root, ".worktrees", "fresh")
	payload := `{"cwd":"` + wt + `","session_id":"sid3"}`

	out, _ := runMode(t, []string{"--statusline", "--no-color"}, payload)
	// Fresh worker, sub tier default 120k.
	want := "ctx 0 / 120k (0%) unknown"
	if strings.TrimSpace(out) != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestStatuslineCtxUnknown(t *testing.T) {
	root := t.TempDir()
	isolateEnv(t, root)
	out, _ := runMode(t, []string{"--statusline", "--no-color"}, `{}`)
	if strings.TrimSpace(out) != "ctx ?" {
		t.Fatalf("got %q want %q", out, "ctx ?")
	}
}

func TestStatuslineColorEscalation(t *testing.T) {
	root := t.TempDir()
	isolateEnv(t, root)
	tr := writeTranscript(t, root, 115000) // 115k/120k sub = 95%
	wt := filepath.Join(root, ".worktrees", "hot")
	payload := `{"cwd":"` + wt + `","session_id":"s","transcript_path":"` + tr + `"}`

	out, _ := runMode(t, []string{"--statusline"}, payload)
	if !strings.Contains(out, ansiRed) {
		t.Fatalf("expected red escalation, got %q", out)
	}
}

func TestFleetAggregation(t *testing.T) {
	root := t.TempDir()
	isolateEnv(t, root)

	writeTee(t, filepath.Join(root, "coord", "token.json"), tee{
		Slug: "coordinator", Role: "coordinator", Tier: "max",
		Ctx: 100000, CapSoft: 150000, CapHard: 190000, Pct: 52,
	})
	writeTee(t, filepath.Join(root, "worker-token", "a.json"), tee{
		Slug: "a", Role: "worker", Ctx: 110000, CapHard: 120000, Pct: 91,
	})
	writeTee(t, filepath.Join(root, "worker-token", "b.json"), tee{
		Slug: "b", Role: "worker", Ctx: 12000, CapHard: 120000, Pct: 10,
	})

	out, _ := runMode(t, []string{"--fleet", "--no-color"}, "")
	out = strings.TrimSpace(out)
	// Sorted by pct desc: a (91!), coordinator (52), b (10).
	want := "a 110k/120k! | coordinator 100k/190k | b 12k/120k"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestFleetSkipsStale(t *testing.T) {
	root := t.TempDir()
	isolateEnv(t, root)
	p := filepath.Join(root, "worker-token", "old.json")
	writeTee(t, p, tee{Slug: "old", Ctx: 1000, CapHard: 120000})
	stale := time.Now().Add(-FleetStaleAfter - time.Minute)
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}
	out, _ := runMode(t, []string{"--fleet", "--no-color"}, "")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1k", 87543: "87k", 190000: "190k"}
	for in, want := range cases {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d)=%q want %q", in, got, want)
		}
	}
}

func TestUnknownArg(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"--bogus"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("exit %d want 2", code)
	}
}
