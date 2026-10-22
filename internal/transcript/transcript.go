// Package transcript reads agent session JSONL transcripts (claude-code
// sessions and Codex rollouts) to extract token-usage signals. Token
// monitors (coordinator and worker) share these helpers.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SumContextTokens returns the current context size of the transcript
// at path, detecting the record shape per file. For claude-code it sums
// input_tokens + cache_creation_input_tokens + cache_read_input_tokens
// from the last assistant message's usage block. For a Codex rollout it
// returns last_token_usage.total_tokens of the latest token_count event
// (total_token_usage is cumulative, not the live context). Returns 0 on
// any error, when no usage is found, or for a .zst-compressed rollout.
func SumContextTokens(path string) int {
	if strings.HasSuffix(path, ".zst") {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	var lastLine string
	codex, codexCtx := false, 0
	first := true
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if first && strings.TrimSpace(line) != "" {
			first = false
			codex = isCodexRecord(line)
		}
		if strings.Contains(line, `"token_count"`) {
			if n, ok := codexContextFromLine(line); ok {
				codex, codexCtx = true, n
				continue
			}
		}
		if !codex && strings.Contains(line, `"role":"assistant"`) {
			lastLine = line
		}
	}
	if codex {
		return codexCtx
	}
	if lastLine == "" {
		return 0
	}
	return ExtractContextFromLine(lastLine)
}

// isCodexRecord reports whether line has the Codex rollout envelope
// {"timestamp","type","payload"} with a session_meta header.
func isCodexRecord(line string) bool {
	var rec struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal([]byte(line), &rec) != nil {
		return false
	}
	return rec.Type == "session_meta" && len(rec.Payload) > 0
}

// codexContextFromLine parses an event_msg/token_count rollout record.
// A token_count with a null info (rate-limit-only update) is not a
// usage sample and reports ok=false.
func codexContextFromLine(line string) (int, bool) {
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Info *struct {
				LastTokenUsage *struct {
					TotalTokens int `json:"total_tokens"`
				} `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal([]byte(line), &rec) != nil {
		return 0, false
	}
	if rec.Type != "event_msg" || rec.Payload.Type != "token_count" {
		return 0, false
	}
	if rec.Payload.Info == nil || rec.Payload.Info.LastTokenUsage == nil {
		return 0, false
	}
	return rec.Payload.Info.LastTokenUsage.TotalTokens, true
}

// ExtractContextFromLine pulls the last "usage" block from a single
// JSONL line and sums the three context-token counters.
func ExtractContextFromLine(line string) int {
	usageRE := regexp.MustCompile(`"usage"\s*:\s*\{`)
	locs := usageRE.FindAllStringIndex(line, -1)
	if len(locs) == 0 {
		return 0
	}
	lastLoc := locs[len(locs)-1]
	start := lastLoc[1] - 1

	depth := 0
	end := start
	for i := start; i < len(line); i++ {
		switch line[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				goto done
			}
		}
	}
done:
	if end <= start {
		return 0
	}

	block := line[start:end]

	var usage map[string]json.RawMessage
	if json.Unmarshal([]byte(block), &usage) != nil {
		return 0
	}

	sum := 0
	for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		if raw, ok := usage[key]; ok {
			var n int
			if json.Unmarshal(raw, &n) == nil {
				sum += n
			}
		}
	}
	return sum
}

// ProjectDir resolves the agent's project root for hook paths:
// SPORE_PROJECT_ROOT (set by spore on every session it spawns, any
// agent), then CLAUDE_PROJECT_DIR (claude-code only), then cwd.
func ProjectDir() string {
	for _, k := range []string{"SPORE_PROJECT_ROOT", "CLAUDE_PROJECT_DIR"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

// FindFallbackTranscript locates the newest *.jsonl under
// ~/.claude/projects/<encoded ProjectDir>/ when the hook payload omits
// transcript_path. Returns "" when nothing is found.
func FindFallbackTranscript() string {
	cwd := ProjectDir()
	encoded := strings.ReplaceAll(cwd, "/", "-")
	home, _ := os.UserHomeDir()
	projDir := filepath.Join(home, ".claude", "projects", encoded)

	entries, err := os.ReadDir(projDir)
	if err != nil {
		return ""
	}

	var newest string
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || fi.ModTime().After(newestTime) {
			newest = filepath.Join(projDir, e.Name())
			newestTime = fi.ModTime()
		}
	}
	return newest
}
