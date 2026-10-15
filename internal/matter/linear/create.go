package linear

import (
	"fmt"
	"strings"
)

// CreatedIssue is the shape returned by CreateIssue: enough for a
// caller to print or hand back to the operator without exposing the
// internal Source.
type CreatedIssue struct {
	ID         string
	Identifier string
	URL        string
}

// CreateIssue mints a new Linear issue in the configured team, lands
// it in ReadyState, and delegates it to Rocky (s.actorID). This is the
// only authorised ticket-creation path: assigneeId is never settable,
// delegateId is not a parameter, both are hardcoded to enforce the
// "Rocky is the only ticket owner" invariant. Returns the issue's id,
// human identifier (e.g. ROC-42), and URL.
func (s *Source) CreateIssue(title, description string) (*CreatedIssue, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("matter.linear: title is required")
	}
	if err := s.loadStateIDs(); err != nil {
		return nil, err
	}
	readyID, ok := s.stateIDs[s.cfg.ReadyState]
	if !ok {
		return nil, fmt.Errorf("matter.linear: ready_state %q not found in team %s", s.cfg.ReadyState, s.cfg.Team)
	}
	if err := s.loadActorID(); err != nil {
		return nil, err
	}
	teamID, err := s.loadTeamID()
	if err != nil {
		return nil, err
	}
	const mutation = `mutation IssueCreate($teamId: String!, $title: String!, $description: String, $stateId: String!, $delegateId: String!) {
  issueCreate(input: {teamId: $teamId, title: $title, description: $description, stateId: $stateId, delegateId: $delegateId}) {
    success
    issue { id identifier url }
  }
}`
	vars := map[string]any{
		"teamId":      teamID,
		"title":       title,
		"description": description,
		"stateId":     readyID,
		"delegateId":  s.actorID,
	}
	var resp struct {
		Data struct {
			IssueCreate struct {
				Success bool `json:"success"`
				Issue   struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
					URL        string `json:"url"`
				} `json:"issue"`
			} `json:"issueCreate"`
		} `json:"data"`
	}
	if err := s.graphQL(mutation, vars, &resp); err != nil {
		return nil, err
	}
	if !resp.Data.IssueCreate.Success {
		return nil, fmt.Errorf("matter.linear: issueCreate returned success=false")
	}
	return &CreatedIssue{
		ID:         resp.Data.IssueCreate.Issue.ID,
		Identifier: resp.Data.IssueCreate.Issue.Identifier,
		URL:        resp.Data.IssueCreate.Issue.URL,
	}, nil
}

// loadTeamID resolves the team UUID for the configured team key. The
// issueCreate mutation needs the UUID (not the key); workflowStates
// already accepts the key, so we don't have the id cached from
// loadStateIDs and need a one-off lookup.
func (s *Source) loadTeamID() (string, error) {
	const q = `query Team($key: String!) {
  teams(filter: {key: {eq: $key}}) { nodes { id } }
}`
	var resp struct {
		Data struct {
			Teams struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"teams"`
		} `json:"data"`
	}
	if err := s.graphQL(q, map[string]any{"key": s.cfg.Team}, &resp); err != nil {
		return "", err
	}
	if len(resp.Data.Teams.Nodes) == 0 {
		return "", fmt.Errorf("matter.linear: team %q not found", s.cfg.Team)
	}
	return resp.Data.Teams.Nodes[0].ID, nil
}
