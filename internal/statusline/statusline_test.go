package statusline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderRampBoundaries(t *testing.T) {
	cases := []struct {
		pctTarget int
		used      int
		wantColor string
	}{
		{0, 0, "#[fg=green]"},
		{49, 49000, "#[fg=green]"},
		{50, 50000, "#[fg=cyan]"},
		{74, 74000, "#[fg=cyan]"},
		{75, 75000, "#[fg=yellow]"},
		{84, 84000, "#[fg=yellow]"},
		{85, 85000, "#[fg=red]"},
		{94, 94000, "#[fg=red]"},
		{95, 95000, "#[fg=red,bold]"},
		{120, 120000, "#[fg=red,bold]"},
	}
	for _, tc := range cases {
		t.Run(tc.wantColor, func(t *testing.T) {
			path := writeTranscript(t, tc.used)
			got := Render(Config{TranscriptPath: path, Cap: 100000, Label: "coordinator"})
			if !strings.HasPrefix(got, tc.wantColor) {
				t.Errorf("used=%d: got %q, want color prefix %q", tc.used, got, tc.wantColor)
			}
			if !strings.Contains(got, "coordinator") {
				t.Errorf("missing label: %q", got)
			}
			if !strings.HasSuffix(got, "#[default]") {
				t.Errorf("missing reset suffix: %q", got)
			}
		})
	}
}

func TestRenderNoColor(t *testing.T) {
	path := writeTranscript(t, 30000)
	got := Render(Config{TranscriptPath: path, Cap: 100000, Label: "coordinator", NoColor: true})
	if strings.Contains(got, "#[") {
		t.Errorf("NoColor render still has tmux formatting: %q", got)
	}
	if got != "coordinator 30000/100000 tok 30%" {
		t.Errorf("unexpected render: %q", got)
	}
}

func TestRenderDefaults(t *testing.T) {
	got := Render(Config{NoColor: true})
	wantCap := DefaultCap
	if !strings.Contains(got, "0/"+itoa(wantCap)) {
		t.Errorf("empty transcript should render 0/<defaultcap>, got %q", got)
	}
}

func TestRenderNoLabel(t *testing.T) {
	path := writeTranscript(t, 10000)
	got := Render(Config{TranscriptPath: path, Cap: 100000, NoColor: true})
	if got != "10000/100000 tok 10%" {
		t.Errorf("no-label render = %q", got)
	}
}

// writeTranscript writes a minimal claude-shaped jsonl whose last
// assistant line reports `used` cache_read tokens.
func writeTranscript(t *testing.T, used int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	line := `{"role":"assistant","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":` + itoa(used-1) + `}}}`
	if used == 0 {
		line = `{"role":"user","content":"hi"}`
	}
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
