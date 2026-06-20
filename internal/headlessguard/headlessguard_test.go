package headlessguard

import (
	"os"
	"strings"
	"testing"
)

func TestAllowed(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want bool
	}{
		{"inside tmux", Env{TMUX: "/tmp/tmux-1000/default,123,0"}, true},
		{"interactive tty", Env{StdinIsTTY: true}, true},
		{"tmux and tty", Env{TMUX: "x", StdinIsTTY: true}, true},
		{"headless", Env{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.Allowed(); got != tc.want {
				t.Errorf("Allowed(%+v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestRefusalMessageNamesBinary(t *testing.T) {
	msg := RefusalMessage("codex")
	if !strings.Contains(msg, "codex") {
		t.Errorf("refusal message missing binary name: %q", msg)
	}
	if !strings.Contains(msg, "REFUSING") {
		t.Errorf("refusal message missing loud refusal: %q", msg)
	}
}

func TestBackstopAccountTier(t *testing.T) {
	t.Run("seeds from WT when unset", func(t *testing.T) {
		t.Setenv("SPORE_ACCOUNT_TIER", "")
		t.Setenv("WT_ACCOUNT_TIER", "max")
		BackstopAccountTier()
		if got := os.Getenv("SPORE_ACCOUNT_TIER"); got != "max" {
			t.Errorf("SPORE_ACCOUNT_TIER = %q, want max", got)
		}
	})
	t.Run("preserves existing", func(t *testing.T) {
		t.Setenv("SPORE_ACCOUNT_TIER", "pro")
		t.Setenv("WT_ACCOUNT_TIER", "max")
		BackstopAccountTier()
		if got := os.Getenv("SPORE_ACCOUNT_TIER"); got != "pro" {
			t.Errorf("SPORE_ACCOUNT_TIER = %q, want pro (preserved)", got)
		}
	})
	t.Run("no-op when WT unset", func(t *testing.T) {
		t.Setenv("SPORE_ACCOUNT_TIER", "")
		t.Setenv("WT_ACCOUNT_TIER", "")
		BackstopAccountTier()
		if got := os.Getenv("SPORE_ACCOUNT_TIER"); got != "" {
			t.Errorf("SPORE_ACCOUNT_TIER = %q, want empty", got)
		}
	})
}
