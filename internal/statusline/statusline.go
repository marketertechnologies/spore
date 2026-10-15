// Package statusline renders a tmux status-right string showing the
// live token usage of the agent running in a pane, with a colour ramp
// that warns the operator before the token-cap wrap fires. It is the
// in-pane twin of the coordinator token-monitor: both read the same
// transcript and the same cap so the indicator's danger zone matches
// when a wrap actually happens.
package statusline

import (
	"fmt"

	"github.com/versality/spore/internal/transcript"
)

// DefaultCap is the fallback wrap cap when the caller supplies none. It
// matches the coordinator token-monitor's soft cap so the red zone of
// the ramp lines up with the wrap reminder.
const DefaultCap = 250000

// Config drives a single Render call.
type Config struct {
	// TranscriptPath is the agent transcript to read the used-token
	// total from. Empty renders a best-effort 0-used line.
	TranscriptPath string
	// Cap is the wrap threshold the percentage is measured against.
	// Zero falls back to DefaultCap.
	Cap int
	// Label prefixes the counter (e.g. "coordinator", "opus",
	// "codex:medium"). Empty omits the prefix.
	Label string
	// NoColor drops the tmux `#[fg=...]` formatting for plain-text
	// smoke runs and NO_COLOR environments.
	NoColor bool
}

// Render returns one line of tmux-formatted text:
//
//	#[fg=<color>[,bold]]<label> <used>/<cap> tok <pct>%#[default]
//
// Errors are swallowed: a missing or unparseable transcript renders 0
// used rather than leaving the status slot empty (an empty slot reads
// as "wiring broken" when in fact the parser just has nothing yet).
func Render(cfg Config) string {
	cap := cfg.Cap
	if cap <= 0 {
		cap = DefaultCap
	}
	used := 0
	if cfg.TranscriptPath != "" {
		used = transcript.SumContextTokens(cfg.TranscriptPath)
	}
	pct := 0
	if cap > 0 {
		pct = used * 100 / cap
	}

	body := fmt.Sprintf("%d/%d tok %d%%", used, cap, pct)
	if cfg.Label != "" {
		body = cfg.Label + " " + body
	}
	if cfg.NoColor {
		return body
	}
	return colorPrefix(pct) + body + "#[default]"
}

// colorPrefix maps a usage percentage to a tmux colour directive. The
// five-step ramp gives the operator increasing warning as the context
// fills: green well below the cap, cyan past halfway, yellow as wrap
// approaches, red in the danger zone, red+bold once wrap is imminent.
func colorPrefix(pct int) string {
	switch {
	case pct >= 95:
		return "#[fg=red,bold]"
	case pct >= 85:
		return "#[fg=red]"
	case pct >= 75:
		return "#[fg=yellow]"
	case pct >= 50:
		return "#[fg=cyan]"
	default:
		return "#[fg=green]"
	}
}
