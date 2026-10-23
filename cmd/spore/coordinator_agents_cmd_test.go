package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestWriteCoordinatorAgents(t *testing.T) {
	onlyClaude := func(name string) (string, error) {
		if name == "claude" {
			return "/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	both := func(name string) (string, error) { return "/bin/" + name, nil }
	cases := []struct {
		name, current, model string
		look                 func(string) (string, error)
		want                 string
	}{
		{"default claude, codex missing", "", "", onlyClaude, "claude\tcurrent\ncodex\tmissing\n"},
		{"codex current with model", "codex", "gpt-5", onlyClaude, "claude\tavailable\ncodex\tcurrent\tgpt-5\n"},
		{"claude current, codex available", "claude", "", both, "claude\tcurrent\ncodex\tavailable\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			writeCoordinatorAgents(&b, tc.current, tc.model, tc.look)
			if b.String() != tc.want {
				t.Fatalf("got %q, want %q", b.String(), tc.want)
			}
		})
	}
}
