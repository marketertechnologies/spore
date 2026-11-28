## Roles

spore has two agent roles: the `coordinator` (one per project; dispatches tasks, talks to the operator) and the `worker` (one per task, in its own worktree). A `loop: role` task runs an `engineer` plus reviewers instead of a single worker. Use these names in code, docs and rules.
