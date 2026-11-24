package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeSporeToml(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadPermissions_MissingFileIsDefault(t *testing.T) {
	p, err := LoadPermissions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, DefaultPermissions()) {
		t.Fatalf("got %+v", p)
	}
	if !reflect.DeepEqual(p.DenyRules(), DefaultDeny()) {
		t.Fatalf("deny = %q, want %q", p.DenyRules(), DefaultDeny())
	}
}

func TestLoadPermissions_MergeAndDedupe(t *testing.T) {
	root := writeSporeToml(t, `[permissions]
deny = ["Bash(terraform apply *)", "SendMessage", "Bash(git push -f *)", "Bash(terraform apply *)", ""]
`)
	p, err := LoadPermissions(root)
	if err != nil {
		t.Fatal(err)
	}
	want := append(DefaultDeny(), "Bash(terraform apply *)")
	if got := p.DenyRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("deny = %q, want %q", got, want)
	}
}

func TestLoadPermissions_OptOutKeepsKernelAndProject(t *testing.T) {
	root := writeSporeToml(t, `[permissions]
default_deny = false
deny = ["Read(./.env)"]
`)
	p, err := LoadPermissions(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SendMessage", "Read(./.env)"}
	if got := p.DenyRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("deny = %q, want %q", got, want)
	}
}

func TestLoadPermissions_Errors(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":  "[permissions]\nallow = [\"x\"]\n",
		"bad bool":     "[permissions]\ndefault_deny = no\n",
		"not an array": "[permissions]\ndeny = \"Bash(x *)\"\n",
		"multi-line":   "[permissions]\ndeny = [\n\"Bash(x *)\",\n]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadPermissions(writeSporeToml(t, body)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestSettingsWith_RendersMergedDeny(t *testing.T) {
	perms := Permissions{DefaultDeny: true, Deny: []string{"Bash(nixos-rebuild *)"}}
	got, err := SettingsWith(nil, perms)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("rendered settings are not JSON: %v\n%s", err, got)
	}
	want := append(DefaultDeny(), "Bash(nixos-rebuild *)")
	if !reflect.DeepEqual(parsed.Permissions.Deny, want) {
		t.Fatalf("deny = %q, want %q", parsed.Permissions.Deny, want)
	}
}
