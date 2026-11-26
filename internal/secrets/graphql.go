package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// PRActivityQuery is the role loop's PR feedback read
// (fleet.NewGitHubPRFeedbackSource). It takes the latest 100 of each
// list and the latest 50 comments per thread; older feedback predates
// any useful cutoff on a PR the loop already cycled.
const PRActivityQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      timelineItems(last: 50, itemTypes: [READY_FOR_REVIEW_EVENT]) {
        nodes { ... on ReadyForReviewEvent { createdAt actor { __typename login } } }
      }
      reviews(last: 100) {
        nodes { state body submittedAt createdAt author { __typename login } }
      }
      reviewThreads(last: 100) {
        nodes {
          isResolved path line originalLine
          comments(last: 50) { nodes { body diffHunk createdAt author { __typename login } } }
        }
      }
      comments(last: 100) {
        nodes { body createdAt author { __typename login } }
      }
    }
  }
}`

// graphqlQueries are the only GraphQL documents the broker runs, each
// with its variables. GitHub's schema reaches other repositories from
// nearly any object (owner.repositories, headRepository, node ids,
// cross-reference events), so no filter on field names is sound. Every
// query here must take the repo as $owner and $name, which
// checkGraphQL pins to the project's repo.
var graphqlQueries = []struct {
	doc  []string
	vars map[string]*regexp.Regexp
}{
	{mustTokens(PRActivityQuery), map[string]*regexp.Regexp{"owner": nil, "name": nil, "number": regexp.MustCompile(`^[0-9]{1,9}$`)}},
}

func mustTokens(doc string) []string {
	toks, err := gqlTokens(doc)
	if err != nil {
		panic(err)
	}
	return toks
}

var errGraphQLMutation = errors.New("graphql mutations are not allowed")

// checkGraphQL checks one `gh api graphql` call: query is the document,
// vars the other fields (or the body's variables), ownerRepo the
// project's OWNER/REPO.
func checkGraphQL(query string, vars map[string]string, ownerRepo string) error {
	toks, err := gqlTokens(query)
	if err != nil {
		return fmt.Errorf("graphql: %w", err)
	}
	if err := gqlReadOnly(toks); err != nil {
		return err
	}
	owner, name, _ := strings.Cut(ownerRepo, "/")
	for _, q := range graphqlQueries {
		if !slices.Equal(toks, q.doc) {
			continue
		}
		for k, v := range vars {
			re, ok := q.vars[k]
			switch {
			case !ok:
				return fmt.Errorf("graphql variable %q is not allowed", k)
			case k == "owner" && !strings.EqualFold(v, owner), k == "name" && !strings.EqualFold(v, name):
				return fmt.Errorf("graphql %s %q is not the project's repo", k, v)
			case re != nil && !re.MatchString(v):
				return fmt.Errorf("graphql variable %s=%q is not allowed", k, v)
			}
		}
		for k := range q.vars {
			if _, ok := vars[k]; !ok {
				return fmt.Errorf("graphql variable %q is missing", k)
			}
		}
		return nil
	}
	return fmt.Errorf("graphql: only the queries spore itself sends are allowed")
}

// graphqlBody reads a `--input -` body: {"query", "variables"}. Keys
// match exactly and once: encoding/json folds case and keeps the last
// duplicate, which GitHub's parser need not agree with.
func graphqlBody(stdin string) (string, map[string]string, error) {
	body, err := jsonObject([]byte(stdin))
	if err != nil {
		return "", nil, fmt.Errorf("graphql body: %w", err)
	}
	var query string
	vars := map[string]string{}
	for k, raw := range body {
		switch k {
		case "query":
			if err := json.Unmarshal(raw, &query); err != nil {
				return "", nil, fmt.Errorf("graphql body query: %w", err)
			}
		case "variables":
			obj, err := jsonObject(raw)
			if err != nil {
				return "", nil, fmt.Errorf("graphql body variables: %w", err)
			}
			for vk, vraw := range obj {
				var s string
				var n json.Number
				switch {
				case json.Unmarshal(vraw, &s) == nil:
					vars[vk] = s
				case json.Unmarshal(vraw, &n) == nil:
					vars[vk] = n.String()
				default:
					return "", nil, fmt.Errorf("graphql variable %q must be a string or number", vk)
				}
			}
		default:
			return "", nil, fmt.Errorf("graphql body key %q is not allowed", k)
		}
	}
	if _, ok := body["query"]; !ok {
		return "", nil, fmt.Errorf("graphql body has no query")
	}
	return query, vars, nil
}

// jsonObject decodes one JSON object, refusing a repeated key and any
// trailing data.
func jsonObject(b []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("want a JSON object")
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("key %q is repeated", k)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after the object")
	}
	return out, nil
}

// gqlReadOnly refuses a document with any definition other than a
// query or fragment. A top-level name `mutation` or `subscription`
// is refused wherever it sits (an operation named `mutation` too).
func gqlReadOnly(toks []string) error {
	depth, start := 0, true
	for _, t := range toks {
		if depth == 0 && (t == "mutation" || t == "subscription") {
			return errGraphQLMutation
		}
		if start {
			if t != "query" && t != "fragment" && t != "{" {
				return fmt.Errorf("graphql definition %q is not a query", t)
			}
			start = false
		}
		switch t {
		case "{", "(", "[":
			depth++
		case "}", ")", "]":
			if depth--; depth < 0 {
				return fmt.Errorf("graphql: unbalanced %q", t)
			}
			if depth == 0 && t == "}" {
				start = true
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("graphql: unbalanced document")
	}
	return nil
}

// gqlTokens lexes a GraphQL document per the spec's lexical grammar,
// dropping ignored tokens (whitespace, commas, comments, BOM). Strings
// keep their quotes so two documents compare token for token.
func gqlTokens(doc string) ([]string, error) {
	var toks []string
	for i := 0; i < len(doc); {
		c := doc[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case strings.HasPrefix(doc[i:], "\uFEFF"):
			i += len("\uFEFF")
		case c == '#':
			for i < len(doc) && doc[i] != '\n' && doc[i] != '\r' {
				i++
			}
		case strings.HasPrefix(doc[i:], "..."):
			toks = append(toks, "...")
			i += 3
		case strings.IndexByte("!$&():=@[]{}|", c) >= 0:
			toks = append(toks, string(c))
			i++
		case c == '_' || isAlpha(c):
			j := i + 1
			for j < len(doc) && (doc[j] == '_' || isAlpha(doc[j]) || isDigit(doc[j])) {
				j++
			}
			toks = append(toks, doc[i:j])
			i = j
		case c == '-' || isDigit(c):
			j := i + 1
			for j < len(doc) && (isDigit(doc[j]) || strings.IndexByte(".eE+-", doc[j]) >= 0) {
				j++
			}
			toks = append(toks, doc[i:j])
			i = j
		case strings.HasPrefix(doc[i:], `"""`):
			j := i + 3
			for {
				if j >= len(doc) {
					return nil, fmt.Errorf("unterminated block string")
				}
				if strings.HasPrefix(doc[j:], `\"""`) {
					j += 4
					continue
				}
				if strings.HasPrefix(doc[j:], `"""`) {
					j += 3
					break
				}
				j++
			}
			toks = append(toks, doc[i:j])
			i = j
		case c == '"':
			j := i + 1
			for {
				if j >= len(doc) || doc[j] == '\n' || doc[j] == '\r' {
					return nil, fmt.Errorf("unterminated string")
				}
				if doc[j] == '\\' {
					j += 2
					continue
				}
				if doc[j] == '"' {
					j++
					break
				}
				j++
			}
			toks = append(toks, doc[i:j])
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return toks, nil
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
