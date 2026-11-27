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
		{"git-push-force-trailing", "git push origin main --force", true, "force"},
		{"git-push-f-trailing", "git push origin main -f", true, "force"},
		{"git-dash-C-push-force", "git -C /w push --force origin x", true, "force"},
		{"wrapped-push-force", "spore gh exec -- git push --force origin x", true, "force"},
		{"gh-exec-git", "spore gh exec -- git push -u origin wt/x", false, ""},
		{"gh-exec-gh", "spore gh exec -- gh pr view 1", false, ""},
		{"gh-exec-git-path", "spore gh exec -- /run/current-system/sw/bin/git fetch", false, ""},
		{"gh-exec-no-dashes", "spore gh exec git status", false, ""},
		{"gh-exec-shell", "spore gh exec -- sh -c 'cat secrets.env'", true, "sandbox"},
		{"gh-exec-env", "cd /w && spore gh exec -- env", true, "sandbox"},
		{"gh-exec-quoted-cat", `spore gh exec -- "cat" x`, true, "sandbox"},
		{"gh-config", "spore gh config", false, ""},
		{"gh-config-set", "spore gh config --set app_id=1", true, "operator"},
		{"gh-token", `curl -H "Authorization: Bearer $(spore gh token)" https://api.github.com`, true, "sandbox"},
		{"gh-help", "spore gh --help", false, ""},
		{"gh-exec-bare", "spore gh exec", true, "sandbox"},
		{"gh-pr-create", "spore gh pr create --draft --title t --body-file - < body.md", false, ""},
		{"gh-pr-ready", "spore gh pr ready 12", false, ""},
		{"gh-api", "spore gh api repos/o/r/pulls/12/comments", false, ""},
		{"gh-push", "spore gh push wt/x", false, ""},
		{"gh-push-lease", "spore gh push wt/x --force-with-lease=0123abc", false, ""},
		{"gh-token-bare", "spore gh token", true, "spore gh pr|api|push"},
		{"gh-push-in-prose", "echo 'use `spore gh push` / `spore gh pr`' >> notes.md", false, ""},
		{"gh-token-backticked", "x=`spore gh token`", true, "sandbox"},
		{"gh-unknown", "spore gh auth status", true, "sandbox"},
		{"compound-push-force", "cd /w && git push -f", true, "force"},
		{"force-with-lease", "git push --force-with-lease origin x", false, ""},
		{"force-with-lease-pinned", "git push --quiet origin HEAD:refs/heads/x --force-with-lease=refs/heads/x:abc", false, ""},
		{"force-if-includes", "git push --force-with-lease --force-if-includes origin x", false, ""},
		{"plus-refspec", "git push origin +branch", true, "force"},
		{"plus-head-refspec", "git push origin +HEAD:main", true, "force"},
		{"plus-full-ref", "git push origin +refs/heads/x", true, "force"},
		{"plus-refspec-dash-C", "git -C /w push origin +x", true, "force"},
		{"plain-full-ref", "git push origin refs/heads/x:refs/heads/y", false, ""},
		{"plus-refspec-quoted", `git push origin "+HEAD:refs/heads/x"`, true, "force"},
		{"plus-refspec-wrapped", "spore gh exec -- git push -u origin +wt/x", true, "force"},
		{"plus-refspec-in-bash-c", `bash -c "git push origin +x"`, true, "force"},
		{"plus-refspec-pipe", "git -C /w push origin +x|tee log", true, "force"},
		{"plus-refspec-second", "git push origin main +x:y", true, "force"},
		{"head-refspec", "git push origin HEAD:branch", false, ""},
		{"plus-refspec-backslash", `git push origin \+main`, true, "force"},
		{"config-push-plus", "git -c remote.origin.push=+refs/heads/*:refs/heads/* push origin", true, "force"},
		{"config-push-plus-quoted", `git -c 'remote.origin.push=+refs/heads/*:refs/heads/*' push origin`, true, "force"},
		{"config-push-plain", "git -c remote.origin.push=refs/heads/*:refs/heads/* push origin", false, ""},
		{"config-other-plus", "git -c user.name=+x push origin x", false, ""},
		{"push-option-plus", "git push -o +ci.skip origin feat/x", true, "force"},
		{"plus-after-separator", "git push origin x && echo +1", false, ""},
		{"plus-inside-branch-name", "git push origin a+b", false, ""},
		{"forced-word-in-branch", "git push origin feat/force-x", false, ""},
		{"follow-tags", "git push --follow-tags origin main", false, ""},
		{"spore-gh-push", "spore gh exec -- git push -u origin feat/x", false, ""},
		{"force-after-separator", "git push origin x && echo -f", false, ""},
		{"git-push-uf-cluster", "git push -uf origin x", true, "force"},
		{"git-push-fu-cluster", "git push -fu origin x", true, "force"},
		{"git-no-pager-push-f", "git --no-pager push -f origin x", true, "force"},
		{"git-push-f-semicolon", "git -C /w push origin x -f;echo ok", true, "force"},
		{"git-push-force-pipe", "git -C /w push origin x --force|tee log", true, "force"},
		{"git-push-f-subshell", "(git push origin x -f)", true, "force"},
		{"git-dash-C-quoted-path", `git -C "/home/u/my repo" push --force origin main`, true, "force"},
		{"git-dash-C-single-quoted", `git -C '/home/u/my repo' push -f`, true, "force"},
		{"git-push-u-only", "git push -u origin feat/x", false, ""},
		{"git-push-follow-tags-cluster", "git push --no-verify -u origin x", false, ""},
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
