package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTaskMergeForceMergeRedRequiresReason(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"flag with no follow-up", []string{"demo", "--force-merge-red"}},
		{"flag followed by empty string", []string{"demo", "--force-merge-red", ""}},
		{"flag with empty equals", []string{"demo", "--force-merge-red="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runTaskMerge(tc.args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "requires a <reason>") {
				t.Errorf("error = %q should mention requires a <reason>", err)
			}
		})
	}
}

func TestRunTaskMergeUnknownFlag(t *testing.T) {
	err := runTaskMerge([]string{"demo", "--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error = %q should mention unknown flag", err)
	}
}

func TestRunTaskMergeMissingSlug(t *testing.T) {
	err := runTaskMerge(nil)
	if err == nil {
		t.Fatal("expected error for missing slug, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("error = %q should be a usage hint", err)
	}
}

func TestRefuseTaskNewWhenMatterEnabled(t *testing.T) {
	cases := []struct {
		name      string
		toml      string
		wantBlock bool
		wantNames []string
	}{
		{
			name:      "no spore.toml",
			toml:      "",
			wantBlock: false,
		},
		{
			name:      "matter section disabled",
			toml:      "[matter.linear]\nenabled = false\nteam = \"X\"\n",
			wantBlock: false,
		},
		{
			name:      "matter.linear enabled",
			toml:      "[matter.linear]\nenabled = true\nteam = \"X\"\n",
			wantBlock: true,
			wantNames: []string{"linear"},
		},
		{
			name:      "two matters enabled",
			toml:      "[matter.linear]\nenabled = true\n\n[matter.github-issues]\nenabled = true\n",
			wantBlock: true,
			wantNames: []string{"github-issues", "linear"},
		},
		{
			name:      "non-matter section ignored",
			toml:      "[fleet]\nmax_workers = 1\n",
			wantBlock: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.toml != "" {
				if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte(tc.toml), 0o644); err != nil {
					t.Fatalf("write spore.toml: %v", err)
				}
			}
			err := refuseTaskNewWhenMatterEnabled(dir)
			if tc.wantBlock {
				if err == nil {
					t.Fatal("expected refusal error, got nil")
				}
				if !strings.Contains(err.Error(), "'spore task new' is disabled") {
					t.Errorf("error = %q should mention disabled", err)
				}
				for _, n := range tc.wantNames {
					if !strings.Contains(err.Error(), n) {
						t.Errorf("error = %q missing matter name %q", err, n)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
