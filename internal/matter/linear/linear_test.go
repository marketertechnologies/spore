package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/matter"
	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

// stubLinear emulates the Linear GraphQL endpoint just well enough to
// drive the Sync paths. State and issues are mutable so a test can
// verify a transition mutation actually moved the issue. comments is
// keyed by both UUID and human identifier so respondIssueComments can
// resolve whichever the production code passes.
type stubLinear struct {
	t        *testing.T
	team     string
	states   map[string]string // name -> id
	issues   map[string]*stubIssue
	comments map[string][]*stubComment
	calls    int
}

type stubIssue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	URL         string
	StateID     string
	SortOrder   float64
}

type stubComment struct {
	ID        string
	Body      string
	URL       string
	CreatedAt time.Time
	Author    string
}

func newStub(t *testing.T) *stubLinear {
	return &stubLinear{
		t:    t,
		team: "MAR",
		states: map[string]string{
			"Backlog":     "state-backlog",
			"Ready":       "state-ready",
			"In Progress": "state-doing",
			"Done":        "state-done",
		},
		issues:   map[string]*stubIssue{},
		comments: map[string][]*stubComment{},
	}
}

// addComment registers a comment against both the UUID and the human
// identifier so respondIssueComments can answer whichever the caller
// passes in via vars["id"].
func (s *stubLinear) addComment(uuidOrIdent string, c *stubComment) {
	iss, ok := s.issues[uuidOrIdent]
	if !ok {
		iss = s.findByIdentifier(uuidOrIdent)
	}
	if iss == nil {
		s.t.Fatalf("addComment: unknown issue %q", uuidOrIdent)
	}
	s.comments[iss.ID] = append(s.comments[iss.ID], c)
	s.comments[iss.Identifier] = append(s.comments[iss.Identifier], c)
}

func (s *stubLinear) addReady(id, identifier, title, desc string) *stubIssue {
	iss := &stubIssue{
		ID: id, Identifier: identifier, Title: title,
		Description: desc, URL: "https://linear.app/team/issue/" + identifier,
		StateID: s.states["Ready"],
	}
	s.issues[id] = iss
	return iss
}

// findByIdentifier returns the stubIssue matching the human
// identifier (e.g. "MAR-12"). issueUpdate is called with the
// identifier in adoptIssue->transitionIssue and OnDone, but the stub
// keys its map by ID; the lookup walks the map.
func (s *stubLinear) findByIdentifier(ident string) *stubIssue {
	for _, iss := range s.issues {
		if iss.Identifier == ident {
			return iss
		}
	}
	return nil
}

func (s *stubLinear) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		if got := r.Header.Get("Authorization"); got != "lin_test" {
			s.t.Errorf("Authorization = %q, want lin_test", got)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			s.t.Errorf("Content-Type = %q", got)
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch {
		case strings.Contains(body.Query, "workflowStates"):
			s.respondStates(w)
		case strings.Contains(body.Query, "issueUpdate"):
			s.respondIssueUpdate(w, body.Variables)
		case strings.Contains(body.Query, "issues("):
			s.respondIssues(w, body.Variables)
		case strings.Contains(body.Query, "IssueComments"):
			s.respondIssueComments(w, body.Variables)
		default:
			http.Error(w, "unrecognised query: "+body.Query, http.StatusBadRequest)
		}
	}
}

func (s *stubLinear) respondStates(w http.ResponseWriter) {
	type node struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var nodes []node
	for name, id := range s.states {
		nodes = append(nodes, node{ID: id, Name: name})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	resp := map[string]any{
		"data": map[string]any{
			"workflowStates": map[string]any{"nodes": nodes},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *stubLinear) respondIssues(w http.ResponseWriter, vars map[string]any) {
	stateID, _ := vars["stateId"].(string)
	type node struct {
		ID          string  `json:"id"`
		Identifier  string  `json:"identifier"`
		Title       string  `json:"title"`
		Description string  `json:"description"`
		URL         string  `json:"url"`
		SortOrder   float64 `json:"sortOrder"`
	}
	var nodes []node
	for _, iss := range s.issues {
		if iss.StateID != stateID {
			continue
		}
		nodes = append(nodes, node{
			ID: iss.ID, Identifier: iss.Identifier, Title: iss.Title,
			Description: iss.Description, URL: iss.URL,
			SortOrder: iss.SortOrder,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Identifier < nodes[j].Identifier })
	resp := map[string]any{
		"data": map[string]any{
			"issues": map[string]any{"nodes": nodes},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *stubLinear) respondIssueComments(w http.ResponseWriter, vars map[string]any) {
	id, _ := vars["id"].(string)
	cs := s.comments[id]
	type userPayload struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	type node struct {
		ID        string       `json:"id"`
		Body      string       `json:"body"`
		URL       string       `json:"url"`
		CreatedAt string       `json:"createdAt"`
		User      *userPayload `json:"user"`
	}
	var nodes []node
	for _, c := range cs {
		var u *userPayload
		if c.Author != "" {
			u = &userPayload{Name: c.Author, DisplayName: c.Author}
		}
		nodes = append(nodes, node{
			ID:        c.ID,
			Body:      c.Body,
			URL:       c.URL,
			CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano),
			User:      u,
		})
	}
	resp := map[string]any{
		"data": map[string]any{
			"issue": map[string]any{
				"comments": map[string]any{"nodes": nodes},
			},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *stubLinear) respondIssueUpdate(w http.ResponseWriter, vars map[string]any) {
	id, _ := vars["id"].(string)
	stateID, _ := vars["stateId"].(string)
	iss, ok := s.issues[id]
	if !ok {
		// adoptIssue passes the human identifier (e.g. MAR-12) when
		// transitioning new issues to in_progress, so fall back to
		// an identifier lookup before erroring.
		iss = s.findByIdentifier(id)
	}
	if iss == nil {
		http.Error(w, "no such issue: "+id, http.StatusNotFound)
		return
	}
	iss.StateID = stateID
	resp := map[string]any{
		"data": map[string]any{
			"issueUpdate": map[string]any{"success": true},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func newSource(t *testing.T, srvURL string) *Source {
	t.Helper()
	t.Setenv("LINEAR_API_KEY", "lin_test")
	src, err := NewFromConfig(Config{
		Team:            "MAR",
		ReadyState:      "Ready",
		InProgressState: "In Progress",
		DoneState:       "Done",
		APIKeyEnv:       "LINEAR_API_KEY",
		Endpoint:        srvURL,
	})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	return src
}

func TestSyncCreatesTasksAndPushesReady(t *testing.T) {
	stub := newStub(t)
	stub.addReady("issue-uuid-1", "MAR-12", "Wire up onboarding email", "Send welcome email on signup.")
	stub.addReady("issue-uuid-2", "MAR-13", "Crash on empty cart", "Repro: open cart without items.")

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	src := newSource(t, srv.URL)

	created, updated, err := src.Sync(context.Background(), root)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if created != 2 || updated != 0 {
		t.Errorf("Sync = (created=%d, updated=%d), want (2, 0)", created, updated)
	}

	for id, iss := range stub.issues {
		if iss.StateID != stub.states["In Progress"] {
			t.Errorf("issue %s state = %s, want In Progress", id, iss.StateID)
		}
	}

	tasksDir := filepath.Join(root, "tasks")
	files, err := os.ReadDir(tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 task files, got %d", len(files))
	}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(tasksDir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m, body, err := frontmatter.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", f.Name(), err)
		}
		if m.Status != "active" {
			t.Errorf("%s: status = %q, want active", f.Name(), m.Status)
		}
		if m.Extra[matter.MatterKey] != "linear" {
			t.Errorf("%s: matter = %q, want linear", f.Name(), m.Extra[matter.MatterKey])
		}
		if !strings.HasPrefix(m.Extra[matter.MatterIDKey], "MAR-") {
			t.Errorf("%s: matter_id = %q", f.Name(), m.Extra[matter.MatterIDKey])
		}
		if !strings.HasPrefix(m.Extra[matter.MatterURLKey], "https://linear.app/") {
			t.Errorf("%s: matter_url = %q", f.Name(), m.Extra[matter.MatterURLKey])
		}
		if !strings.Contains(string(body), "Linear: https://linear.app/") {
			t.Errorf("%s: body missing Linear URL: %s", f.Name(), body)
		}
	}

	c2, u2, err := src.Sync(context.Background(), root)
	if err != nil {
		t.Fatalf("Sync (idempotent pass): %v", err)
	}
	if c2 != 0 || u2 != 0 {
		t.Errorf("expected no-op second pass, got (created=%d, updated=%d)", c2, u2)
	}
}

func TestSyncProjectsInLinearSortOrder(t *testing.T) {
	stub := newStub(t)
	// Identifier order is MAR-50 < MAR-51 < MAR-52, but sortOrder
	// inverts it: MAR-52 sits at the top of the kanban column. The stub
	// responds in identifier order; the production code must re-sort
	// client-side before iterating in adoptIssue.
	stub.addReady("u-50", "MAR-50", "Bottom of Ready", "").SortOrder = 99.0
	stub.addReady("u-51", "MAR-51", "Middle of Ready", "").SortOrder = 50.0
	stub.addReady("u-52", "MAR-52", "Top of Ready", "").SortOrder = -10.0

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	src := newSource(t, srv.URL)

	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	tasksDir := filepath.Join(root, "tasks")
	files, err := os.ReadDir(tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	type stamped struct {
		identifier string
		sortOrder  string
	}
	var got []stamped
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(tasksDir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m, _, err := frontmatter.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", f.Name(), err)
		}
		got = append(got, stamped{
			identifier: m.Extra[matter.MatterIDKey],
			sortOrder:  m.Extra[matter.MatterSortOrderKey],
		})
	}

	wantOrder := map[string]string{
		"MAR-52": "-10",
		"MAR-51": "50",
		"MAR-50": "99",
	}
	for _, g := range got {
		want, ok := wantOrder[g.identifier]
		if !ok {
			t.Errorf("unexpected identifier %q stamped", g.identifier)
			continue
		}
		if g.sortOrder != want {
			t.Errorf("%s: matter_sort_order = %q, want %q", g.identifier, g.sortOrder, want)
		}
	}
}

func TestSyncPushesDoneTasks(t *testing.T) {
	stub := newStub(t)
	iss := stub.addReady("issue-uuid-7", "MAR-21", "Ship checkout", "")
	iss.StateID = stub.states["In Progress"]

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := frontmatter.Meta{
		Status: "done", Slug: "ship-checkout", Title: "Ship checkout",
		Extra: map[string]string{
			matter.MatterKey:   "linear",
			matter.MatterIDKey: "MAR-21",
		},
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "ship-checkout.md"),
		frontmatter.Write(m, []byte("\nbody\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	src := newSource(t, srv.URL)
	created, updated, err := src.Sync(context.Background(), root)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if created != 0 || updated != 1 {
		t.Errorf("Sync = (created=%d, updated=%d), want (0, 1)", created, updated)
	}
	if iss.StateID != stub.states["Done"] {
		t.Errorf("issue state = %q, want Done", iss.StateID)
	}

	raw, err := os.ReadFile(filepath.Join(tasksDir, "ship-checkout.md"))
	if err != nil {
		t.Fatal(err)
	}
	m2, _, err := frontmatter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Extra["linear_done"] != "yes" {
		t.Errorf("linear_done = %q, want yes", m2.Extra["linear_done"])
	}

	c2, u2, err := src.Sync(context.Background(), root)
	if err != nil {
		t.Fatalf("Sync second pass: %v", err)
	}
	if c2 != 0 || u2 != 0 {
		t.Errorf("second pass should not push again, got (created=%d, updated=%d)", c2, u2)
	}
}

func TestSyncRecognisesLegacyLinearKey(t *testing.T) {
	stub := newStub(t)
	iss := stub.addReady("issue-uuid-8", "MAR-30", "Legacy task", "")
	iss.StateID = stub.states["In Progress"]

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-rename frontmatter shape: only the legacy `linear:` key.
	m := frontmatter.Meta{
		Status: "done", Slug: "legacy-task", Title: "Legacy task",
		Extra: map[string]string{"linear": "MAR-30"},
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "legacy-task.md"),
		frontmatter.Write(m, []byte("\nbody\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	src := newSource(t, srv.URL)
	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if iss.StateID != stub.states["Done"] {
		t.Errorf("legacy task should have pushed Done, got state %q", iss.StateID)
	}
}

func TestOnDonePushesImmediately(t *testing.T) {
	stub := newStub(t)
	iss := stub.addReady("issue-uuid-9", "MAR-42", "Ship matter plugin", "")
	iss.StateID = stub.states["In Progress"]

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	src := newSource(t, srv.URL)
	err := src.OnDone(context.Background(), "ship-matter-plugin", map[string]string{
		matter.MatterKey:   "linear",
		matter.MatterIDKey: "MAR-42",
	})
	if err != nil {
		t.Fatalf("OnDone: %v", err)
	}
	if iss.StateID != stub.states["Done"] {
		t.Errorf("OnDone should have moved issue to Done, got %q", iss.StateID)
	}
}

func TestOnDoneIgnoresUnrelatedMatter(t *testing.T) {
	stub := newStub(t)
	stub.addReady("issue-uuid-10", "MAR-99", "Wrong adapter", "")

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	src := newSource(t, srv.URL)
	// matter=jira: not us, even though matter_id is set.
	err := src.OnDone(context.Background(), "wrong-adapter", map[string]string{
		matter.MatterKey:   "jira",
		matter.MatterIDKey: "MAR-99",
	})
	if err != nil {
		t.Fatalf("OnDone: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("OnDone should make 0 GraphQL calls when matter != linear, got %d", stub.calls)
	}
}

func TestOnDoneNoOpWithoutID(t *testing.T) {
	stub := newStub(t)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	src := newSource(t, srv.URL)
	if err := src.OnDone(context.Background(), "no-id", map[string]string{}); err != nil {
		t.Fatalf("OnDone: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("want 0 calls, got %d", stub.calls)
	}
}

func TestSyncErrorsOnUnknownState(t *testing.T) {
	stub := newStub(t)
	delete(stub.states, "Ready")

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	src := newSource(t, srv.URL)
	if _, _, err := src.Sync(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected error for missing Ready state")
	}
}

func TestGraphQLSurfacesGraphErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "rate limited"}},
		})
	}))
	defer srv.Close()

	src := newSource(t, srv.URL)
	_, _, err := src.Sync(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected rate-limited error, got %v", err)
	}
}

func TestParseConfigDefaultsAndValidation(t *testing.T) {
	// missing team -> error
	if _, err := parseConfig(matter.Config{
		Name: "linear",
		Options: map[string]string{
			"api_key_env": "LINEAR_API_KEY",
		},
	}); err == nil {
		t.Error("missing team should error")
	}

	// missing api_key source -> error
	if _, err := parseConfig(matter.Config{
		Name:    "linear",
		Options: map[string]string{"team": "MAR"},
	}); err == nil {
		t.Error("missing api_key_* should error")
	}

	// credential_api_key counts as api_key_file
	cfg, err := parseConfig(matter.Config{
		Name: "linear",
		Options: map[string]string{
			"team":               "MAR",
			"credential_api_key": "/run/credentials/x",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKeyFile != "/run/credentials/x" {
		t.Errorf("credential_api_key not adopted: %#v", cfg)
	}
	if cfg.ReadyState != "Ready" || cfg.DoneState != "Done" {
		t.Errorf("defaults not applied: %#v", cfg)
	}
}

func TestRegisteredViaInit(t *testing.T) {
	found := false
	for _, n := range matter.Registered() {
		if n == "linear" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("linear should self-register via init(); registered = %v", matter.Registered())
	}
}

// withFixedClock pins commentsClock for the duration of a test so cursor
// seeding is deterministic. The original clock is restored on cleanup.
func withFixedClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := commentsClock
	commentsClock = func() time.Time { return at }
	t.Cleanup(func() { commentsClock = prev })
}

// inboxEnvelopes returns the parsed (ts, source, body) triples sitting
// in the spore inbox for slug under projectRoot, sorted by ts ascending.
// Excludes the .tmp and read subdirs.
type inboxEnvelope struct{ ts, source, body string }

func readInboxEnvelopes(t *testing.T, projectRoot, slug string) []inboxEnvelope {
	t.Helper()
	dir, err := task.InboxDirForProject(projectRoot, slug)
	if err != nil {
		t.Fatalf("InboxDirForProject: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []inboxEnvelope
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", n, err)
		}
		var ev struct {
			Ts     string `json:"ts"`
			Source string `json:"source"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &ev); err != nil {
			t.Fatalf("unmarshal %s: %v", n, err)
		}
		out = append(out, inboxEnvelope{ts: ev.Ts, source: ev.Source, body: ev.Body})
	}
	return out
}

// writeActiveTask drops a tasks/<slug>.md with status=active wired to
// the linear matter under tasksDir.
func writeActiveTask(t *testing.T, tasksDir, slug, identifier, cursor string) {
	t.Helper()
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	extra := map[string]string{
		matter.MatterKey:   "linear",
		matter.MatterIDKey: identifier,
	}
	if cursor != "" {
		extra[linearCommentsCursorKey] = cursor
	}
	m := frontmatter.Meta{
		Status: "active", Slug: slug, Title: identifier,
		Extra: extra,
	}
	if err := os.WriteFile(filepath.Join(tasksDir, slug+".md"),
		frontmatter.Write(m, []byte("\nbody\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncProjectsCommentsToInbox(t *testing.T) {
	stub := newStub(t)
	iss := stub.addReady("u-1", "MAR-100", "Project comments", "")
	iss.StateID = stub.states["In Progress"]

	// Cursor anchored 10 minutes ago: two newer comments expected to
	// land in the inbox, the older one (5 min before cursor) suppressed.
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	cursor := now.Add(-10 * time.Minute)
	stub.addComment("MAR-100", &stubComment{
		ID: "c-old", Body: "pre-cursor", Author: "alice",
		CreatedAt: cursor.Add(-5 * time.Minute),
		URL:       "https://linear.app/team/comment/c-old",
	})
	stub.addComment("MAR-100", &stubComment{
		ID: "c-new-1", Body: "first new comment", Author: "bob",
		CreatedAt: cursor.Add(2 * time.Minute),
		URL:       "https://linear.app/team/comment/c-new-1",
	})
	stub.addComment("MAR-100", &stubComment{
		ID: "c-new-2", Body: "second new comment", Author: "carol",
		CreatedAt: cursor.Add(5 * time.Minute),
	})

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tasksDir := filepath.Join(root, "tasks")
	writeActiveTask(t, tasksDir, "project-comments", "MAR-100",
		cursor.Format(time.RFC3339Nano))

	src := newSource(t, srv.URL)
	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	got := readInboxEnvelopes(t, root, "project-comments")
	if len(got) != 2 {
		t.Fatalf("inbox envelopes = %d, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.source != "linear" {
			t.Errorf("source = %q, want linear", e.source)
		}
		if !strings.HasPrefix(e.body, "MAR-100 comment from") {
			t.Errorf("body = %q, want MAR-100 comment from <author>:", e.body)
		}
	}
	if !strings.Contains(got[0].body, "first new comment") || !strings.Contains(got[0].body, "bob") {
		t.Errorf("first envelope body wrong: %q", got[0].body)
	}
	if !strings.Contains(got[1].body, "second new comment") || !strings.Contains(got[1].body, "carol") {
		t.Errorf("second envelope body wrong: %q", got[1].body)
	}
	if !strings.Contains(got[0].body, "https://linear.app/team/comment/c-new-1") {
		t.Errorf("expected URL in first envelope body: %q", got[0].body)
	}

	// Cursor should advance to the latest projected comment so the
	// next pass is a no-op.
	raw, err := os.ReadFile(filepath.Join(tasksDir, "project-comments.md"))
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := frontmatter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantCursor := cursor.Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
	if m.Extra[linearCommentsCursorKey] != wantCursor {
		t.Errorf("cursor = %q, want %q", m.Extra[linearCommentsCursorKey], wantCursor)
	}

	// Idempotent second pass: no new comments, no new envelopes.
	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync second pass: %v", err)
	}
	got2 := readInboxEnvelopes(t, root, "project-comments")
	if len(got2) != 2 {
		t.Errorf("second pass inbox count = %d, want still 2", len(got2))
	}
}

func TestSyncSeedsCommentCursorOnAdopt(t *testing.T) {
	stub := newStub(t)
	stub.addReady("u-2", "MAR-200", "Adopt seeds cursor", "Body")
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	withFixedClock(t, now)

	// Comment older than the adoption clock: must NOT flood the
	// freshly-spawned rover. Adoption seeds cursor=now and the same
	// pass's comment fetch only emits createdAt > cursor.
	stub.addComment("MAR-200", &stubComment{
		ID: "old", Body: "predates rover", Author: "alice",
		CreatedAt: now.Add(-1 * time.Hour),
	})

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	src := newSource(t, srv.URL)

	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	tasksDir := filepath.Join(root, "tasks")
	files, err := os.ReadDir(tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 task, got %d", len(files))
	}
	raw, err := os.ReadFile(filepath.Join(tasksDir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := frontmatter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Extra[linearCommentsCursorKey]; got != now.Format(time.RFC3339Nano) {
		t.Errorf("cursor = %q, want %q", got, now.Format(time.RFC3339Nano))
	}

	envs := readInboxEnvelopes(t, root, m.Slug)
	if len(envs) != 0 {
		t.Errorf("freshly adopted task should not flood inbox, got %+v", envs)
	}
}

func TestSyncSeedsCursorForLegacyTaskWithoutEmission(t *testing.T) {
	stub := newStub(t)
	iss := stub.addReady("u-3", "MAR-300", "Legacy active task", "")
	iss.StateID = stub.states["In Progress"]
	stub.addComment("MAR-300", &stubComment{
		ID: "stale", Body: "would have flooded", Author: "alice",
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	withFixedClock(t, now)

	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tasksDir := filepath.Join(root, "tasks")
	// Pre-feature task: status=active, linear matter, NO cursor.
	writeActiveTask(t, tasksDir, "legacy-active", "MAR-300", "")

	src := newSource(t, srv.URL)
	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	envs := readInboxEnvelopes(t, root, "legacy-active")
	if len(envs) != 0 {
		t.Errorf("legacy task with empty cursor should not flood, got %+v", envs)
	}
	raw, err := os.ReadFile(filepath.Join(tasksDir, "legacy-active.md"))
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := frontmatter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Extra[linearCommentsCursorKey]; got != now.Format(time.RFC3339Nano) {
		t.Errorf("cursor = %q, want seeded to %q", got, now.Format(time.RFC3339Nano))
	}
}

func TestSyncSkipsCommentsForDoneTasks(t *testing.T) {
	stub := newStub(t)
	iss := stub.addReady("u-4", "MAR-400", "Done task", "")
	iss.StateID = stub.states["In Progress"]
	stub.addComment("MAR-400", &stubComment{
		ID: "post-done", Body: "rover already gone", Author: "alice",
		CreatedAt: time.Date(2026, 5, 7, 11, 0, 0, 0, time.UTC),
	})

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cursor := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	m := frontmatter.Meta{
		Status: "done", Slug: "done-task", Title: "Done task",
		Extra: map[string]string{
			matter.MatterKey:        "linear",
			matter.MatterIDKey:      "MAR-400",
			linearCommentsCursorKey: cursor,
		},
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "done-task.md"),
		frontmatter.Write(m, []byte("\nbody\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	src := newSource(t, srv.URL)
	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	envs := readInboxEnvelopes(t, root, "done-task")
	if len(envs) != 0 {
		t.Errorf("done task should not project comments, got %+v", envs)
	}
}

func TestSyncProjectsAuthorlessComment(t *testing.T) {
	// Bot-authored or anonymous comments arrive with a null user. The
	// envelope still lands; the body just omits the "from <author>"
	// suffix instead of crashing on a nil deref.
	stub := newStub(t)
	iss := stub.addReady("u-5", "MAR-500", "Authorless", "")
	iss.StateID = stub.states["In Progress"]

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	cursor := now.Add(-1 * time.Hour)
	stub.addComment("MAR-500", &stubComment{
		ID: "c-anon", Body: "no author", CreatedAt: cursor.Add(5 * time.Minute),
		// Author empty -> stub returns user: null.
	})

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tasksDir := filepath.Join(root, "tasks")
	writeActiveTask(t, tasksDir, "anon", "MAR-500", cursor.Format(time.RFC3339Nano))

	src := newSource(t, srv.URL)
	if _, _, err := src.Sync(context.Background(), root); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	envs := readInboxEnvelopes(t, root, "anon")
	if len(envs) != 1 {
		t.Fatalf("want 1 envelope, got %d", len(envs))
	}
	if strings.Contains(envs[0].body, "from") {
		t.Errorf("authorless body should not say 'from': %q", envs[0].body)
	}
	if !strings.Contains(envs[0].body, "no author") {
		t.Errorf("body missing comment text: %q", envs[0].body)
	}
}
