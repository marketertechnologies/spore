package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/versality/spore/internal/statusline"
	"github.com/versality/spore/internal/transcript"
)

const statuslineUsage = `spore statusline - render the agent token-usage status line

Usage:
  spore statusline [--transcript PATH] [--cap N] [--label S] [--no-color]

Prints a tmux status-right string with the live token usage of the
agent in the current pane and a colour ramp that warns before the wrap
cap. Transcript precedence: --transcript > $CLAUDE_TRANSCRIPT_PATH >
newest *.jsonl under the cwd's claude projects dir. Wire it via:

  tmux set-option -t <session> status-interval 5
  tmux set-option -t <session> status-right \
    "#(CLAUDE_PROJECT_DIR='#{pane_current_path}' spore statusline --label coordinator)"
`

func runStatusline(args []string) int {
	fs := flag.NewFlagSet("statusline", flag.ContinueOnError)
	transcriptPath := fs.String("transcript", "", "agent transcript path")
	cap := fs.Int("cap", 0, "wrap cap (default "+itoa(statusline.DefaultCap)+")")
	labelStr := fs.String("label", "", "counter label prefix")
	noColor := fs.Bool("no-color", false, "drop tmux colour formatting")
	help := fs.Bool("h", false, "show help")
	helpLong := fs.Bool("help", false, "show help")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "spore statusline:", err)
		return 2
	}
	if *help || *helpLong {
		fmt.Print(statuslineUsage)
		return 0
	}

	path := *transcriptPath
	if path == "" {
		path = os.Getenv("CLAUDE_TRANSCRIPT_PATH")
	}
	if path == "" {
		path = transcript.FindFallbackTranscript()
	}

	out := statusline.Render(statusline.Config{
		TranscriptPath: path,
		Cap:            *cap,
		Label:          *labelStr,
		NoColor:        *noColor || os.Getenv("NO_COLOR") != "",
	})
	fmt.Print(out)
	return 0
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
