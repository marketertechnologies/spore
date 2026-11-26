package secrets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const gqlRepo = "github.com/acme/alpha"

// prArgs is the call fleet's PR feedback source sends.
func prArgs(query string, vars ...string) []string {
	args := []string{"api", "graphql", "--hostname", "github.com", "-f", "query=" + query}
	for _, v := range vars {
		args = append(args, "-f", v)
	}
	return args
}

func gqlBody(t *testing.T, query string, vars map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGraphQLAllowsSporeQueries(t *testing.T) {
	reflowed := "# PR feedback\n" + strings.Join(strings.Fields(PRActivityQuery), " ")
	repoVars := map[string]any{"owner": "acme", "name": "alpha", "number": 7}
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"fleet call", []string{"api", "graphql", "--hostname", "github.com", "-f", "query=" + PRActivityQuery, "-f", "owner=acme", "-f", "name=alpha", "-F", "number=7"}, ""},
		{"inline flags", []string{"api", "graphql", "--raw-field=query=" + PRActivityQuery, "--field=owner=acme", "--field", "name=alpha", "-F", "number=7"}, ""},
		{"reflowed with comment", prArgs(reflowed, "owner=acme", "name=alpha", "number=7"), ""},
		{"repo case differs", prArgs(PRActivityQuery, "owner=Acme", "name=Alpha", "number=7"), ""},
		{"input stdin", []string{"api", "graphql", "--input", "-"}, gqlBody(t, PRActivityQuery, repoVars)},
		{"input= stdin", []string{"api", "graphql", "--input=-", "--jq", ".data"}, gqlBody(t, PRActivityQuery, repoVars)},
	}
	for _, c := range cases {
		if err := checkGHArgs(c.args, c.stdin, gqlRepo); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// Each of these passed the -f/--raw-field-only regex the broker had.
func TestGraphQLRefusesMutations(t *testing.T) {
	const m = "mutation { mergePullRequest(input: {pullRequestId: \"PR_1\"}) { clientMutationId } }"
	argCases := map[string][]string{
		"-F":                  {"api", "graphql", "-F", "query=" + m},
		"--field":             {"api", "graphql", "--field", "query=" + m},
		"--field=":            {"api", "graphql", "--field=query=" + m},
		"--raw-field":         {"api", "graphql", "--raw-field", "query=" + m},
		"-f":                  {"api", "graphql", "-f", "query=" + m},
		"-F file":             {"api", "graphql", "-F", "query=@/tmp/m.graphql"},
		"comment first":       prArgs("# read only\nmutation { deleteRef(input: {refId: \"x\"}) { clientMutationId } }"),
		"no space":            prArgs("mutation{updateRefs(input:{repositoryId:\"x\",refUpdates:[{name:\"refs/heads/main\",afterOid:\"0\",force:true}]}){clientMutationId}}"),
		"named":               prArgs("mutation Sneaky($id: ID!) { deleteRef(input: {refId: $id}) { clientMutationId } }"),
		"query then mutation": prArgs("query A { viewer { login } } mutation B { createRef(input: {}) { clientMutationId } }"),
		"shorthand then":      prArgs("{ viewer { login } }\nmutation { createCommitOnBranch(input: {}) { clientMutationId } }"),
		"after fragment":      prArgs("fragment F on User { login } mutation { x }"),
		"uppercase":           prArgs("MUTATION { x }"),
		"mixed case":          prArgs("Mutation { x }"),
		"subscription":        prArgs("subscription { x }"),
		"op named mutation":   prArgs("query mutation { viewer { login } }"),
		"bom and commas":      prArgs("\uFEFF,,mutation { x }"),
		"spore query + mut":   prArgs(PRActivityQuery+"\nmutation { x }", "owner=acme", "name=alpha", "number=7"),
	}
	for name, a := range argCases {
		if err := checkGHArgs(a, "", gqlRepo); err == nil {
			t.Errorf("%s: %q accepted", name, a)
		}
	}
	stdinCases := map[string]string{
		"--input -":         `{"query":"mutation { mergePullRequest(input: {}) { clientMutationId } }"}`,
		"case-folded key":   `{"query":"mutation { x }","Query":` + strconvQuote(PRActivityQuery) + `,"variables":{"owner":"acme","name":"alpha","number":7}}`,
		"duplicate key":     `{"query":` + strconvQuote(PRActivityQuery) + `,"query":"mutation { x }","variables":{"owner":"acme","name":"alpha","number":7}}`,
		"operationName":     `{"query":"mutation M { x } query Q { y }","operationName":"M"}`,
		"trailing object":   gqlBody(t, PRActivityQuery, map[string]any{"owner": "acme", "name": "alpha", "number": 7}) + `{"query":"mutation { x }"}`,
		"not json":          `mutation { x }`,
		"no query":          `{"variables":{}}`,
		"variables not obj": `{"query":"{ a }","variables":[1]}`,
		"empty":             ``,
	}
	for name, body := range stdinCases {
		for _, a := range [][]string{{"api", "graphql", "--input", "-"}, {"api", "graphql", "--input=-"}} {
			if err := checkGHArgs(a, body, gqlRepo); err == nil {
				t.Errorf("%s: %q with stdin %q accepted", name, a, body)
			}
		}
	}
}

// From project alpha, a GraphQL read reached another repo of the org.
func TestGraphQLScopedToProjectRepo(t *testing.T) {
	cases := map[string][]string{
		"other repo name":     prArgs(PRActivityQuery, "owner=acme", "name=secret", "number=7"),
		"other owner":         prArgs(PRActivityQuery, "owner=evil", "name=alpha", "number=7"),
		"literal repository":  prArgs(`query { repository(owner: "acme", name: "secret") { object(expression: "HEAD:README.md") { ... on Blob { text } } } }`),
		"organization":        prArgs(`query { organization(login: "acme") { repositories(first: 100) { nodes { name } } } }`),
		"owner hop":           prArgs(`query { repository(owner: "acme", name: "alpha") { owner { repository(name: "secret") { id } } } }`),
		"search":              prArgs(`query { search(query: "org:acme", type: REPOSITORY, first: 10) { nodes { ... on Repository { name } } } }`),
		"node":                prArgs(`query { node(id: "R_kgDOsecret") { ... on Repository { name } } }`),
		"nodes":               prArgs(`query { nodes(ids: ["R_1"]) { id } }`),
		"viewer repositories": prArgs(`query { viewer { repositories(first: 10) { nodes { name } } } }`),
		"viewer login":        prArgs(`query { viewer { login } }`),
		"extra variable":      prArgs(PRActivityQuery, "owner=acme", "name=alpha", "number=7", "after=x"),
		"operationName":       prArgs(PRActivityQuery, "owner=acme", "name=alpha", "number=7", "operationName=x"),
		"missing number":      prArgs(PRActivityQuery, "owner=acme", "name=alpha"),
		"missing owner":       prArgs(PRActivityQuery, "name=alpha", "number=7"),
		"bad number":          prArgs(PRActivityQuery, "owner=acme", "name=alpha", "number=7 } }"),
		"repeated owner":      prArgs(PRActivityQuery, "owner=acme", "owner=evil", "name=alpha", "number=7"),
		"repeated query":      append(prArgs(PRActivityQuery, "owner=acme", "name=alpha", "number=7"), "-f", "query={ viewer { login } }"),
		"field without =":     prArgs(PRActivityQuery, "owner"),
		"no query":            {"api", "graphql", "-f", "owner=acme"},
		"input plus fields":   {"api", "graphql", "--input", "-", "-f", "owner=evil"},
		"unterminated string": prArgs(`query { repository(owner: "acme) { id } }`),
	}
	for name, a := range cases {
		if err := checkGHArgs(a, "", gqlRepo); err == nil {
			t.Errorf("%s: %q accepted", name, a)
		}
	}
	body := gqlBody(t, PRActivityQuery, map[string]any{"owner": "acme", "name": "secret", "number": 7})
	if err := checkGHArgs([]string{"api", "graphql", "--input", "-"}, body, gqlRepo); err == nil {
		t.Error("--input body for another repo accepted")
	}
}

func TestGQLTokens(t *testing.T) {
	got, err := gqlTokens("# c\nquery Q($a: [Int!] = [1, -2.5e3]) { f(s: \"x\\\"y\", b: \"\"\"bl\\\"\"\"ock\"\"\") ...on T { g } }")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"query", "Q", "(", "$", "a", ":", "[", "Int", "!", "]", "=", "[", "1", "-2.5e3", "]", ")", "{", "f", "(", "s", ":", `"x\"y"`, "b", ":", `"""bl\"""ock"""`, ")", "...", "on", "T", "{", "g", "}", "}"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("tokens:\n got %q\nwant %q", got, want)
	}
}

// The broker checks what gh will read: --input - goes to the check
// as the request's stdin.
func TestGHRefusesMutationOnStdin(t *testing.T) {
	c := testConfig(t)
	s := startServer(t, c)
	s.mint = func(context.Context, string) (string, error) { return "ghs_testtoken", nil }
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\ncat\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	resp, err := GHRaw("alpha", `{"query":"mutation { mergePullRequest(input: {}) { clientMutationId } }"}`, []string{"api", "graphql", "--input", "-"}, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "graphql mutations are not allowed") || resp.Stdout != "" {
		t.Fatalf("mutation on stdin: %+v, %v", resp, err)
	}
	ok := gqlBody(t, PRActivityQuery, map[string]any{"owner": "acme", "name": "alpha", "number": 7})
	resp, err = GHRaw("alpha", ok, []string{"api", "graphql", "--input", "-"}, 10*time.Second)
	if err != nil || resp.Error != "" || resp.Exit != 0 || !strings.Contains(resp.Stdout, "pullRequest(number: $number)") {
		t.Fatalf("spore's query on stdin: %+v, %v", resp, err)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
