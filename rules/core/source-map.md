## Source map

```
cmd/spore/             CLI entry point (Go).
cmd/spore-sandbox/     bwrap+proxy sandbox launcher for worker agents.
internal/              Kernel: composer, fleet, task, coordinator, worker,
                       matter (Linear/GitHub backends), hooks, gh, evidence,
                       lints, secret, scout, sandboxcfg, sessionkind, signal,
                       transcript, tmuxsess, wtcheck, ...
rules/core/            Always-on rule fragments composed into CLAUDE.md.
rules/consumers/       Per-consumer rule lists (one fragment id per line).
bootstrap/             Shipped assets: coordinator/role.md, handover hooks,
                       skills, stages, flake.
configs/               Per-agent hook render sources (claude, codex).
docs/                  Design notes; docs/todo/ for multi-session specs.
```

`grep -rn` and the package READMEs are the source of truth; this map is a
top-level pointer, not an index.
