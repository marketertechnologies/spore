---
name: select-agent
description: Swap the coordinator to another agent (claude or codex) from inside the coordinator session. Invoke when the operator runs `/select-agent` (Claude Code) or mentions the select-agent skill (Codex), optionally with an agent and model (`/select-agent codex gpt-5`). With no argument, prompts with the available agents. Flushes state.md, then replaces this pane with the chosen agent.
---

# select-agent

Swap the running coordinator to another agent without leaving the
session. `spore coordinator swap` does the work; this skill picks the
agent, flushes `state.md`, and runs the swap with `--no-wrap` (the
pane cannot answer its own inbox wrap request).

## 1. Guard

Run `printenv SPORE_TASK_SLUG`. Unless it prints exactly
`coordinator`, stop and tell the operator: only the coordinator
swaps; workers do not. Do nothing else.

## 2. Pick the agent

Run `spore coordinator agents`. Each line is tab-separated:

```
claude	current	<model, when set>
codex	available
```

Status is `current` (the running driver), `available` (binary on
PATH), or `missing`. Never offer a `missing` agent.

- **Argument given** (`codex`, or `codex gpt-5`): the first word is the
  agent, the rest is the model. Skip the prompt. If the agent is not
  `claude` or `codex`, or is `missing`, say so and stop.
- **No argument**: ask the operator. Offer every non-missing agent,
  current one marked "(current)". Also ask for an optional model
  (free text; empty means the agent's default).
  - If your harness has a structured question tool (Claude Code:
    `AskUserQuestion`), use it: one question, the agents as options,
    and let the operator type a model through the free-text "Other"
    reply or a follow-up.
  - Otherwise print a numbered list in chat, for example
    `1. claude (current)` / `2. codex`, say "reply with a number,
    optionally followed by a model", and end the turn. Resume on the
    reply.

Picking the current agent is a no-op when no model is given or the
model equals the current one (the third column of the `current`
line): say so and stop. The current agent with a different model is
a real swap (same driver, new model).

## 3. Flush state.md

The coordinator's state file is `$SPORE_PROJECT_ROOT/state.md` (the
main repo's `state.md`, the one loaded at session start). Rewrite it
per the wrap-up protocol, section by section (overwrite each section;
do not append a transcript):

- Header timestamp: now, ISO 8601 UTC.
- Active objective: what the incoming agent should do first. Open it
  with one line noting the swap, e.g. `Swapped from claude to codex
  at <ISO> via select-agent; resume below.`
- Active tasks, open operator questions, recent events: current as of
  this turn. Name in-flight work, pending decisions, and next steps
  explicitly; the incoming agent has no transcript.

Re-read the file once to confirm the write landed. Remember the
swap line you wrote: step 4 reverts it if the swap fails.

## 4. Swap

Run, with `--model` only when the operator gave one. Single-quote
the model (`opus[1m]` is a shell glob). `--session` names this pane's
own tmux session; without it swap derives the session from the cwd,
which misses once you have cd'd into a worktree:

```
spore coordinator swap --to <agent> [--model '<model>'] --no-wrap \
  --session "$(tmux display-message -p -t "$TMUX_PANE" '#S')"
```

On success the pane is respawned and this session ends mid-command;
nothing after it runs, so say what you are about to do before running
it. If the command returns, it failed:

1. Revert the swap note in `state.md`: remove the `Swapped from ...`
   line so the file no longer claims a swap happened. Leave the rest
   of the flush in place.
2. Relay the command's stderr to the operator verbatim (the error
   names what was rolled back).
3. Stop. Do not retry with `--force`.
