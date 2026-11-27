package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsCheck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, profiles string) string {
		p := filepath.Join(dir, name)
		cfg := `{"secrets_dir":"` + dir + `/secrets","state_dir":"` + dir + `/state",` +
			`"projects":{"alpha":{"repo":"","push_branch":""}},"profiles":` + profiles + `}`
		if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name, profiles, wantErr string
	}{
		{"ok", `{"alpha-api":{"project":"alpha","vars":{"T":"API_TOKEN"},"commands":{"get":{"argv":["curl","-H","Authorization: Bearer ${T}"],"args":["https://api\\.example\\.com/v1(/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*"]}}}}`, ""},
		{"bad-regexp", `{"alpha-api":{"project":"alpha","vars":{"T":"API_TOKEN"},"commands":{"get":{"argv":["curl"],"args":["https://api\\.example\\.com/v1("]}}}}`, "args:"},
		{"undeclared-var", `{"alpha-api":{"project":"alpha","commands":{"get":{"argv":["curl","-H","X: ${T}"]}}}}`, "${T} is not in vars"},
		{"unknown-project", `{"beta-api":{"project":"beta","commands":{"get":{"argv":["curl"]}}}}`, `unknown project "beta"`},
		{"no-commands", `{"alpha-api":{"project":"alpha"}}`, "no commands"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runSecretsCheck([]string{"--config", write(tc.name+".json", tc.profiles)})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
