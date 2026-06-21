package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func mkBashReq(t *testing.T, cmd string) Request {
	t.Helper()
	in, err := json.Marshal(BashInput{Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     in,
	}
}

func TestPreToolUse_DefaultForbidden(t *testing.T) {
	cases := []struct {
		name      string
		cmd       string
		wantDeny  bool
		reasonHas string
	}{
		{"plain-ls", "ls -la", false, ""},
		{"sudo-anything", "sudo apt update", true, "sudo"},
		{"sudo-mid-pipe", "ls | sudo tee /etc/x", false, ""},
		{"rm-rf-root", "rm -rf /", true, "rm -rf"},
		{"rm-rf-subdir", "rm -rf /tmp/foo", false, ""},
		{"git-push-force", "git push --force origin main", true, "force"},
		{"git-push-f-flag", "git push -f origin main", true, "force"},
		{"git-push-plain", "git push origin main", false, ""},
	}
	def := DefaultForbidden()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := PreToolUse(mkBashReq(t, tc.cmd), def)
			if !tc.wantDeny {
				if resp.HookSpecificOutput != nil && resp.HookSpecificOutput.PermissionDecision == Deny {
					t.Fatalf("expected allow, got deny: %s", resp.HookSpecificOutput.PermissionDecisionReason)
				}
				return
			}
			if resp.HookSpecificOutput == nil || resp.HookSpecificOutput.PermissionDecision != Deny {
				t.Fatalf("expected deny response, got %+v", resp)
			}
			if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, tc.reasonHas) {
				t.Fatalf("reason %q does not contain %q", resp.HookSpecificOutput.PermissionDecisionReason, tc.reasonHas)
			}
		})
	}
}

func TestPreToolUse_NonBashAllowed(t *testing.T) {
	req := Request{
		HookEventName: "PreToolUse",
		ToolName:      "Read",
		ToolInput:     json.RawMessage(`{"file_path":"/etc/passwd"}`),
	}
	resp := PreToolUse(req, DefaultForbidden())
	if resp.HookSpecificOutput != nil {
		t.Fatalf("expected no decision for non-Bash tool, got %+v", resp)
	}
}

func TestPreToolUse_AskUserQuestionDeniedInWorktree(t *testing.T) {
	cases := []struct {
		name     string
		cwd      string
		wantDeny bool
	}{
		{"worker-worktree", "/home/user/projects/spore/.worktrees/worker-stop-hook-wedge-2", true},
		{"worker-worktree-nested", "/home/user/projects/spore/.worktrees/foo/sub/dir", true},
		{"non-worktree", "/home/user/projects/spore", false},
		{"empty-cwd", "", false},
		{"unrelated-worktrees-suffix", "/tmp/worktrees/foo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{
				HookEventName: "PreToolUse",
				ToolName:      "AskUserQuestion",
				CWD:           tc.cwd,
				ToolInput:     json.RawMessage(`{"questions":[]}`),
			}
			resp := PreToolUse(req, DefaultForbidden())
			isDeny := resp.HookSpecificOutput != nil && resp.HookSpecificOutput.PermissionDecision == Deny
			if isDeny != tc.wantDeny {
				t.Fatalf("cwd=%q: wantDeny=%v got=%+v", tc.cwd, tc.wantDeny, resp)
			}
			if tc.wantDeny && !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "autonomously") {
				t.Fatalf("deny reason missing autonomy hint: %q", resp.HookSpecificOutput.PermissionDecisionReason)
			}
		})
	}
}

func TestPreToolUse_MalformedInput(t *testing.T) {
	req := Request{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`not-json`),
	}
	resp := PreToolUse(req, DefaultForbidden())
	if resp.HookSpecificOutput != nil {
		t.Fatalf("expected no decision on malformed input, got %+v", resp)
	}
}

func denied(resp Response) bool {
	return resp.HookSpecificOutput != nil && resp.HookSpecificOutput.PermissionDecision == Deny
}

func mkPathReq(t *testing.T, tool, key, path, cwd string) Request {
	t.Helper()
	in, err := json.Marshal(map[string]string{key: path})
	if err != nil {
		t.Fatal(err)
	}
	return Request{HookEventName: "PreToolUse", ToolName: tool, CWD: cwd, ToolInput: in}
}

func TestDecide_EnvLeak(t *testing.T) {
	cfg := DefaultPreToolUseConfig()
	cases := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		{"bare-env", "env", true},
		{"env-pipe-grep", "env | grep KEY", true},
		{"printenv-redirect", "printenv > /tmp/dump", true},
		{"env-with-assignment-ok", "env FOO=bar ./run.sh", false},
		{"printenv-one-var-ok", "printenv PATH", false},
		{"bare-set-dump", "set", true},
		{"set-euo-pipefail-ok", "set -euo pipefail", false},
		{"set-o-table", "set -o", true},
		{"set-o-pipefail-ok", "set -o pipefail", false},
		{"export-p", "export -p", true},
		{"bare-export-ok-with-assign", "export FOO=bar", false},
		{"compgen-e", "compgen -e", true},
		{"proc-environ", "tr '\\0' '\\n' < /proc/self/environ", true},
		{"set-x-trace", "set -x; ./run.sh", true},
		{"bash-x-trace", "bash -x ./run.sh", true},
		{"plain-grep-ok", "grep -r foo .", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := Decide(mkBashReq(t, tc.cmd), cfg)
			if denied(resp) != tc.wantDeny {
				t.Fatalf("cmd=%q wantDeny=%v got=%+v", tc.cmd, tc.wantDeny, resp)
			}
		})
	}
}

func TestDecide_SecretPath(t *testing.T) {
	cfg := DefaultPreToolUseConfig()
	cases := []struct {
		name     string
		tool     string
		input    string
		wantDeny bool
	}{
		{"read-secrets-env", "Read", `{"file_path":"/home/x/.config/spore/secrets.env"}`, true},
		{"read-secrets-env-tilde", "Read", `{"file_path":"~/.config/spore/secrets.env"}`, true},
		{"grep-scratch", "Grep", `{"path":"/run/user/1000/spore-secret-abc.txt","pattern":"x"}`, true},
		{"read-age-key", "Read", `{"file_path":"/home/x/.config/spore/age-key.txt"}`, true},
		{"bash-cat-secret", "Bash", `{"command":"cat ~/.config/spore/secrets.env"}`, true},
		{"read-normal-file-ok", "Read", `{"file_path":"/home/x/projects/spore/main.go"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{HookEventName: "PreToolUse", ToolName: tc.tool, ToolInput: json.RawMessage(tc.input)}
			resp := Decide(req, cfg)
			if denied(resp) != tc.wantDeny {
				t.Fatalf("tool=%s input=%s wantDeny=%v got=%+v", tc.tool, tc.input, tc.wantDeny, resp)
			}
		})
	}
}

func TestDecide_MemoryWrites(t *testing.T) {
	cfg := DefaultPreToolUseConfig()
	mem := "/home/x/.claude/projects/-home-x-projects-spore/memory/note.md"
	if resp := Decide(mkPathReq(t, "Write", "file_path", mem, ""), cfg); !denied(resp) {
		t.Fatalf("expected deny on auto-memory write, got %+v", resp)
	}
	// A read of the same path is allowed (only writes are blocked).
	if resp := Decide(mkPathReq(t, "Read", "file_path", mem, ""), cfg); denied(resp) {
		t.Fatalf("expected allow on auto-memory read, got deny")
	}
	// A normal doc write is allowed.
	if resp := Decide(mkPathReq(t, "Write", "file_path", "/home/x/projects/spore/docs/x.md", ""), cfg); denied(resp) {
		t.Fatalf("expected allow on docs write, got deny")
	}
}

func TestDecide_ManualPRCreate(t *testing.T) {
	cfg := DefaultPreToolUseConfig()
	wt := "/home/x/projects/spore/.worktrees/roc-15"
	// gh pr create from a worktree is denied; from the main checkout it is allowed.
	if resp := Decide(Request{ToolName: "Bash", CWD: wt, ToolInput: json.RawMessage(`{"command":"gh pr create --fill"}`)}, cfg); !denied(resp) {
		t.Fatalf("expected deny on worktree gh pr create, got %+v", resp)
	}
	if resp := Decide(Request{ToolName: "Bash", CWD: "/home/x/projects/spore", ToolInput: json.RawMessage(`{"command":"gh pr create --fill"}`)}, cfg); denied(resp) {
		t.Fatalf("expected allow on non-worktree gh pr create, got deny")
	}
	// spore task ship (the gated path) is never blocked.
	if resp := Decide(Request{ToolName: "Bash", CWD: wt, ToolInput: json.RawMessage(`{"command":"spore task ship"}`)}, cfg); denied(resp) {
		t.Fatalf("expected allow on spore task ship, got deny")
	}
}

func TestDecide_NewTaskFileMint(t *testing.T) {
	cfg := DefaultPreToolUseConfig()
	cfg.Exists = func(p string) bool { return strings.HasSuffix(p, "existing.md") }
	// Minting a brand-new task file is denied.
	if resp := Decide(mkPathReq(t, "Write", "file_path", "tasks/brand-new.md", "/repo"), cfg); !denied(resp) {
		t.Fatalf("expected deny on new task file, got %+v", resp)
	}
	// Editing the worker's existing task file (plan section) is allowed.
	if resp := Decide(mkPathReq(t, "Edit", "file_path", "tasks/existing.md", "/repo"), cfg); denied(resp) {
		t.Fatalf("expected allow on existing task file edit, got deny")
	}
	// A non-task .md is never gated.
	if resp := Decide(mkPathReq(t, "Write", "file_path", "docs/new.md", "/repo"), cfg); denied(resp) {
		t.Fatalf("expected allow on docs write, got deny")
	}
}
