// Package recipes serves the embedded recipe library that ships with
// the spore CLI. Recipes are reusable how-to documents for talking to
// external systems (Jira, Sentry, Notion, etc.) from a coordinator or
// worker pane. Each recipe is a markdown file under
// bootstrap/recipes/; the filename (sans .md) is the canonical name.
//
// Discovery and read access are exposed via `spore recipes ls` and
// `spore recipes show <name>`. The coordinator role mentions both
// commands so any coordinator session can find the library on its
// first turn.
package recipes

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Recipe is one entry in the embedded library.
type Recipe struct {
	// Name is the filename without the .md suffix, e.g. "jira".
	Name string
	// Title is the first H1 of the file with the "Recipe: " prefix
	// stripped, e.g. "Atlassian Jira REST API". Empty when the file
	// has no H1.
	Title string
}

// List enumerates every recipe under prefix in fsys, sorted by name.
func List(fsys fs.FS, prefix string) ([]Recipe, error) {
	var out []Recipe
	err := fs.WalkDir(fsys, prefix, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := path.Base(p)
		if !strings.HasSuffix(name, ".md") {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if name == "README.md" {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		out = append(out, Recipe{
			Name:  strings.TrimSuffix(name, ".md"),
			Title: extractTitle(body),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ErrNotFound is returned by Get when no recipe matches the requested
// name.
var ErrNotFound = errors.New("recipe not found")

// Get returns the raw markdown body of the named recipe.
func Get(fsys fs.FS, prefix, name string) ([]byte, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, ErrNotFound
	}
	p := path.Join(prefix, name+".md")
	body, err := fs.ReadFile(fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return body, err
}

// extractTitle returns the first H1 of body with any "Recipe: "
// prefix stripped. Returns "" when no H1 is present.
func extractTitle(body []byte) string {
	s := bufio.NewScanner(strings.NewReader(string(body)))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(line, "# "))
		title = strings.TrimPrefix(title, "Recipe: ")
		return title
	}
	return ""
}
