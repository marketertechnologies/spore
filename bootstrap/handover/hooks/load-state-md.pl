#!/usr/bin/env perl
# SessionStart hook: load <project_root>/state.md into the model context.
#
# Resolves <project_root> via `git rev-parse --git-common-dir` so worker
# sessions running in <project_root>/.worktrees/<slug>/ still pick up the
# canonical state.md in the main worktree. Falls back to cwd when not in
# a git repo so the hook is harmless outside spore.
use strict;
use warnings;
use JSON::PP;
use Cwd qw(getcwd abs_path);

my $cwd = $ENV{CLAUDE_PROJECT_DIR} || getcwd();

my $root = $cwd;
my $gcd = `git -C "$cwd" rev-parse --git-common-dir 2>/dev/null`;
chomp $gcd;
if ($gcd ne '') {
    # rev-parse returns either ".git" (cwd is the toplevel) or an
    # absolute path under .worktrees/<slug>/.git. Walk to its parent
    # to land on the project root in either case.
    my $abs = ($gcd =~ m{^/}) ? $gcd : "$cwd/$gcd";
    my $resolved = abs_path("$abs/..");
    $root = $resolved if defined $resolved;
}

my $path = "$root/state.md";

# Mint a stub state.md on first coordinator boot so the agent always has
# an anchor to read. Coordinator-only: workers run inside .worktrees/<slug>/
# but resolve to the same $root via rev-parse, so this still creates the
# canonical project-root state.md once, not one per worker.
unless (-e $path) {
    if (open my $w, '>:encoding(UTF-8)', $path) {
        print $w <<"STUB";
# coordinator state

(stub auto-minted on first SessionStart; rewrite after your first
meaningful turn. Re-read on every respawn; the transcript is not
durable.)

## Active tasks

| slug | intent | blocker | last seen |
|---|---|---|---|

## Open operator questions

## Recent events
STUB
        close $w;
    }
}

open my $fh, '<:encoding(UTF-8)', $path or exit 0;
my $body = do { local $/; <$fh> };
close $fh;
exit 0 unless defined $body && length $body;

print encode_json({
  hookSpecificOutput => {
    hookEventName     => "SessionStart",
    additionalContext => $body,
  },
});
exit 0;
