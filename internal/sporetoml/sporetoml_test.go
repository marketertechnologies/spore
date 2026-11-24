package sporetoml

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSection(t *testing.T) {
	content := `
# top
[coordinator]
driver = "claude"

[editor]
auto_connect_ide = true # trailing
label = 'a # b'

[editor.sub]
ignored = 1
`
	got, err := ParseSection(content, "editor")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Line: 7, Key: "auto_connect_ide", Value: "true"},
		{Line: 8, Key: "label", Value: "a # b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseSectionMalformed(t *testing.T) {
	if _, err := ParseSection("[editor]\nauto_connect_ide\n", "editor"); err == nil {
		t.Error("want error on entry without =")
	}
	if _, err := ParseSection("[other]\nnot an entry\n", "editor"); err != nil {
		t.Errorf("malformed line outside the section: %v", err)
	}
}

func TestReadSectionMissingFile(t *testing.T) {
	got, err := ReadSection(t.TempDir(), "editor")
	if err != nil || got != nil {
		t.Errorf("got %v, %v; want nil, nil", got, err)
	}
}

func TestReadSection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[fleet]\naccount_tier = \"max\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSection(root, "fleet")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "max" {
		t.Errorf("got %+v", got)
	}
}

func TestEntryBool(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "false": false} {
		got, err := Entry{Key: "k", Value: v}.Bool()
		if err != nil || got != want {
			t.Errorf("%s: got %v, %v", v, got, err)
		}
	}
	for _, v := range []string{"1", "yes", "True", ""} {
		if _, err := (Entry{Key: "k", Value: v}).Bool(); err == nil {
			t.Errorf("%q: want error", v)
		}
	}
}

func TestEntryStrings(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`[]`, []string{}},
		{`[ ]`, []string{}},
		{`["a"]`, []string{"a"}},
		{`["Bash(rm -rf *)", 'Read(~/x/**)',]`, []string{"Bash(rm -rf *)", "Read(~/x/**)"}},
		{`["a, b", "c\"d", "e\\f"]`, []string{"a, b", `c"d`, `e\f`}},
		{`['lit\"']`, []string{`lit\"`}},
	}
	for _, tc := range cases {
		got, err := Entry{Key: "deny", Value: tc.in}.Strings()
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{`"a"`, `[`, `[a]`, `["a" "b"]`, `["a`, `["a",,]`} {
		if _, err := (Entry{Key: "deny", Value: in}).Strings(); err == nil {
			t.Errorf("%s: want error", in)
		}
	}
}

func TestParseSectionStringArray(t *testing.T) {
	got, err := ParseSection("[permissions]\ndeny = [\"Bash(a # b)\", \"c\"] # note\n", "permissions")
	if err != nil {
		t.Fatal(err)
	}
	v, err := got[0].Strings()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, []string{"Bash(a # b)", "c"}) {
		t.Errorf("got %q", v)
	}
}
