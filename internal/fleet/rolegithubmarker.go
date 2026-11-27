package fleet

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// mirrorMarker tags a post with its event so a repost after a lost
// save can be detected. It renders as nothing on GitHub. The cycle
// keeps a reopened loop's reused round names apart.
func mirrorMarker(reopen *reopenState, name string) string {
	cycle := 1
	if reopen != nil {
		cycle = reopen.Cycle
	}
	return fmt.Sprintf("<!-- spore-mirror: cycle %d %s -->", cycle, name)
}

// alreadyPosted reports whether e's post is on the PR: a review or
// issue comment whose body carries the event's marker. Events that
// post nothing (round 1, ready) are idempotent and report false.
func (m *githubMirror) alreadyPosted(e mirrorEvent) (bool, error) {
	if m.state.PRNumber == 0 {
		return false, nil
	}
	list := "issues/%d/comments"
	switch {
	case e.kind == eventReview:
		list = "pulls/%d/reviews"
	case e.kind == eventReady, e.kind == eventEngineer && e.round == 1:
		return false, nil
	}
	host, ownerRepo := m.repoParts()
	out, err := m.gh("", "api", fmt.Sprintf("repos/%s/"+list, ownerRepo, m.state.PRNumber),
		"--hostname", host, "--paginate")
	if err != nil {
		return false, err
	}
	// --paginate prints one JSON array per page, back to back.
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var page []struct {
			Body string `json:"body"`
		}
		if err := dec.Decode(&page); err == io.EOF {
			return false, nil
		} else if err != nil {
			return false, fmt.Errorf("decode %s: %w", fmt.Sprintf(list, m.state.PRNumber), err)
		}
		for _, p := range page {
			if strings.Contains(p.Body, m.marker) {
				return true, nil
			}
		}
	}
}

// withMarker ends body with the event marker, when there is one.
func withMarker(body, marker string) string {
	if marker == "" {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\n" + marker + "\n"
}
