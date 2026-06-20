package recipes

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestList_sortsAndStripsPrefix(t *testing.T) {
	fsys := fstest.MapFS{
		"bootstrap/recipes/sentry.md": {Data: []byte("# Recipe: Sentry HTTP API\nbody\n")},
		"bootstrap/recipes/jira.md":   {Data: []byte("# Recipe: Atlassian Jira REST API\nbody\n")},
		"bootstrap/recipes/README.md": {Data: []byte("# Recipes\nindex page\n")},
	}
	got, err := List(fsys, "bootstrap/recipes")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(got))
	}
	if got[0].Name != "jira" || got[0].Title != "Atlassian Jira REST API" {
		t.Fatalf("got[0] = %+v, want jira / Atlassian Jira REST API", got[0])
	}
	if got[1].Name != "sentry" || got[1].Title != "Sentry HTTP API" {
		t.Fatalf("got[1] = %+v, want sentry / Sentry HTTP API", got[1])
	}
}

func TestList_handlesMissingTitle(t *testing.T) {
	fsys := fstest.MapFS{
		"bootstrap/recipes/notitle.md": {Data: []byte("no header here, just body\n")},
	}
	got, err := List(fsys, "bootstrap/recipes")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "notitle" || got[0].Title != "" {
		t.Fatalf("got = %+v, want notitle / empty title", got)
	}
}

func TestList_skipsDotfiles(t *testing.T) {
	fsys := fstest.MapFS{
		"bootstrap/recipes/.hidden.md": {Data: []byte("# Recipe: Hidden\n")},
		"bootstrap/recipes/visible.md": {Data: []byte("# Recipe: Visible\n")},
	}
	got, err := List(fsys, "bootstrap/recipes")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "visible" {
		t.Fatalf("got = %+v, want only visible", got)
	}
}

func TestGet_returnsBody(t *testing.T) {
	fsys := fstest.MapFS{
		"bootstrap/recipes/jira.md": {Data: []byte("# Recipe: Atlassian Jira REST API\nthe body\n")},
	}
	body, err := Get(fsys, "bootstrap/recipes", "jira")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != "# Recipe: Atlassian Jira REST API\nthe body\n" {
		t.Fatalf("Get body = %q", body)
	}
}

func TestGet_returnsErrNotFound(t *testing.T) {
	fsys := fstest.MapFS{}
	_, err := Get(fsys, "bootstrap/recipes", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_rejectsTraversalAttempts(t *testing.T) {
	fsys := fstest.MapFS{
		"bootstrap/recipes/jira.md": {Data: []byte("# Recipe: Jira\n")},
	}
	for _, bad := range []string{"", "../etc/passwd", "sub/jira", "jira\\..\\etc"} {
		if _, err := Get(fsys, "bootstrap/recipes", bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q) err = %v, want ErrNotFound", bad, err)
		}
	}
}
