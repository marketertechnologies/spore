package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractContextFromLine(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[],"usage":{"input_tokens":100000,"output_tokens":5000,"cache_creation_input_tokens":20000,"cache_read_input_tokens":30000}}}`
	got := ExtractContextFromLine(line)
	want := 150000
	if got != want {
		t.Errorf("ExtractContextFromLine = %d, want %d", got, want)
	}
}

func TestExtractContextFromLineNoUsage(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[]}}`
	if got := ExtractContextFromLine(line); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestExtractContextFromLineMultipleUsageBlocks(t *testing.T) {
	// Streaming transcripts can include intermediate usage blocks; the
	// last one is the final accounting and is what we sum.
	line := `{"role":"assistant","usage":{"input_tokens":1,"output_tokens":1},"content":[],"usage":{"input_tokens":50000,"cache_read_input_tokens":50000}}`
	if got := ExtractContextFromLine(line); got != 100000 {
		t.Errorf("expected 100000, got %d", got)
	}
}

func TestSumContextTokens(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "transcript.jsonl")

	lines := []string{
		`{"type":"human","message":{"role":"user"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[],"usage":{"input_tokens":50000,"output_tokens":1000,"cache_creation_input_tokens":10000,"cache_read_input_tokens":5000}}}`,
		`{"type":"human","message":{"role":"user"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[],"usage":{"input_tokens":80000,"output_tokens":2000,"cache_creation_input_tokens":15000,"cache_read_input_tokens":25000}}}`,
	}
	var content []byte
	for _, l := range lines {
		content = append(content, []byte(l+"\n")...)
	}
	if err := os.WriteFile(f, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := SumContextTokens(f), 120000; got != want {
		t.Errorf("SumContextTokens = %d, want %d", got, want)
	}
}

func TestSumContextTokensMissingFile(t *testing.T) {
	if got := SumContextTokens(filepath.Join(t.TempDir(), "missing.jsonl")); got != 0 {
		t.Errorf("expected 0 for missing file, got %d", got)
	}
}

func TestFindFallbackTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := "/tmp/some/project"
	t.Setenv("SPORE_PROJECT_ROOT", "")
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)

	encoded := "-tmp-some-project"
	projDir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	older := filepath.Join(projDir, "older.jsonl")
	newer := filepath.Join(projDir, "newer.jsonl")
	if err := os.WriteFile(older, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, future, future); err != nil {
		t.Fatal(err)
	}

	if got := FindFallbackTranscript(); got != newer {
		t.Errorf("FindFallbackTranscript = %q, want %q", got, newer)
	}
}

func TestFindFallbackTranscriptMissingDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPORE_PROJECT_ROOT", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "/nope")
	if got := FindFallbackTranscript(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// Shapes copied from a live codex-cli 0.162.1 rollout.
const (
	codexMeta     = `{"timestamp":"2026-10-21T12:32:55.101Z","ordinal":0,"type":"session_meta","payload":{"id":"01a15e73-0000-7000-8000-000000000001","cwd":"/p","originator":"codex_cli_rs","cli_version":"0.162.1","context_window":272000}}`
	codexAssist   = `{"timestamp":"2026-10-21T12:32:58.000Z","ordinal":3,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"see \"usage\":{\"input_tokens\":999999}"}]}}`
	codexNullInfo = `{"timestamp":"2026-10-21T12:32:56.000Z","ordinal":1,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":1.0}}}}`
)

func codexTokenCount(last, total int) string {
	return fmt.Sprintf(`{"timestamp":"2026-10-21T12:33:00.000Z","ordinal":4,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%[2]d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":%[2]d},"last_token_usage":{"input_tokens":%[1]d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":%[1]d},"model_context_window":258400},"rate_limits":{}}}`, last, total)
}

func writeLines(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSumContextTokensCodexLatestLastUsage(t *testing.T) {
	path := writeLines(t, "rollout-2026-10-21T12-32-55-x.jsonl",
		codexMeta, codexNullInfo, codexTokenCount(40000, 40000), codexAssist,
		codexTokenCount(120000, 160000), codexNullInfo)
	if got := SumContextTokens(path); got != 120000 {
		t.Errorf("SumContextTokens(codex) = %d, want 120000 (latest last_token_usage, not cumulative)", got)
	}
}

func TestSumContextTokensCodexNoTokenCountIgnoresAssistantText(t *testing.T) {
	path := writeLines(t, "rollout.jsonl", codexMeta, codexAssist)
	if got := SumContextTokens(path); got != 0 {
		t.Errorf("SumContextTokens(codex without token_count) = %d, want 0", got)
	}
}

func TestSumContextTokensCodexCompressedSkipped(t *testing.T) {
	path := writeLines(t, "rollout.jsonl.zst", codexMeta, codexTokenCount(1, 1))
	if got := SumContextTokens(path); got != 0 {
		t.Errorf("SumContextTokens(.zst) = %d, want 0", got)
	}
}

func TestSumContextTokensClaudeMentioningTokenCount(t *testing.T) {
	path := writeLines(t, "claude.jsonl",
		`{"type":"user","message":{"role":"user","content":"grep token_count"}}`,
		`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`)
	if got := SumContextTokens(path); got != 7 {
		t.Errorf("SumContextTokens(claude) = %d, want 7", got)
	}
}

func TestProjectDirOrder(t *testing.T) {
	t.Setenv("SPORE_PROJECT_ROOT", "/spore/root")
	t.Setenv("CLAUDE_PROJECT_DIR", "/claude/dir")
	if got := ProjectDir(); got != "/spore/root" {
		t.Errorf("ProjectDir = %q, want SPORE_PROJECT_ROOT", got)
	}
	t.Setenv("SPORE_PROJECT_ROOT", "")
	if got := ProjectDir(); got != "/claude/dir" {
		t.Errorf("ProjectDir = %q, want CLAUDE_PROJECT_DIR", got)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	wd, _ := os.Getwd()
	if got := ProjectDir(); got != wd {
		t.Errorf("ProjectDir = %q, want cwd %q", got, wd)
	}
}

func TestFindFallbackTranscriptPrefersSporeProjectRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPORE_PROJECT_ROOT", "/srv/proj")
	t.Setenv("CLAUDE_PROJECT_DIR", "/elsewhere")
	projDir := filepath.Join(home, ".claude", "projects", "-srv-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(projDir, "s.jsonl")
	if err := os.WriteFile(want, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindFallbackTranscript(); got != want {
		t.Errorf("FindFallbackTranscript = %q, want %q", got, want)
	}
}
