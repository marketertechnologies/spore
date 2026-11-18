# worker network isolation

**Status**: shipped. Kernel support for per-worker private network
namespaces, gated by a `spore.toml` knob.

## TL;DR

Set this in a project's `spore.toml`:

```toml
[worker]
isolate_network = true
```

Every worker the kernel spawns then runs inside its own private network
namespace with outbound internet + DNS. N parallel workers reuse the
SAME TCP ports (Rails :3000, Vite :3002, Mockoon :3007, ...) blind to
each other; host ports stay free; no per-worker port bookkeeping.
Default is off (unset knob == no behavior change).

## Mechanism

The kernel wraps the resolved worker command in:

```
pasta --config-net -- <workercmd>
```

`pasta` (from the `passt` package) is a rootless userspace-NAT tool -
the same primitive rootless Podman uses. `--config-net` runs the
command in a fresh user+net namespace with a configured uplink, DNS,
and a private loopback already up. This gives, together and with no
sudo:

- private loopback: identical ports across workers do not collide;
- host ports stay free (inbound is not exposed by default);
- outbound internet + DNS for the agent's own git/web/package work;
- clean teardown (anonymous netns, no `/var/run/netns` leak).

The wrap is composed in Go at the worker-spawn seam
(`internal/task/workerLaunchCommand`, called by `ensureSession`) and
at the loop role-pane spawn (`internal/fleet/rolespawn.go`), both
through `task.LoadIsolation`. It is NOT a launcher-script-via-env
convention: kernel-composed keeps it testable and keeps the on/off
decision in one place.

With a `[fleet] pre_start` hook the whole launch script, hook then
agent, runs inside one namespace, so services the hook starts on
loopback are the ones the agent reaches. Only the agent gets
`IS_SANDBOX=1`. The namespace is per pane, so each role pane of a slug
runs the hook in its own; see "Pre-start hook" in `docs/role-loop.md`
for what that asks of the hook.

No nested `unshare` is needed. `pasta --config-net` alone already maps
the caller to namespace-root and configures the netns; an extra
`unshare --user` layer breaks `CAP_NET_ADMIN` over pasta's netns and is
omitted deliberately.

## Coordinator exemption

Coordinators are never wrapped. They spawn through a separate path
(`internal/fleet/coordinator.go`), and the knob is read only in the
worker-spawn path (`internal/task`). The exemption is structural, not a
flag check - there is no code path that could isolate a coordinator.

## Host delivery

`passt` is bundled in the spore flake
(`bootstrap/flake/configuration.nix` -> `environment.systemPackages`),
so `pasta` is on the system PATH deterministically on any infected
host. `unshare` comes from the base util-linux install.

If `isolate_network` is on but `pasta` is not on PATH (a non-flake
host), the spawn fails loud with an actionable error naming the missing
binary and how to install it. It does not silently fall back to an
unisolated worker.

## Limits and notes

- Inside the netns the worker runs as mapped-root (euid 0). Tools that
  refuse to run as root need a workaround on the isolated path: real
  `initdb` refuses as root, and Chromium needs `--no-sandbox` (its own
  userns sandbox cannot nest). Those flags live in the consumer repo,
  not in spore.
- The gid maps to `nogroup`/65534 under the uid-only map. Fine for
  user-owned files.
- Pathname (filesystem) unix sockets cross the netns boundary, so
  host-side Postgres/Redis reached via socket paths keep working
  unchanged. Abstract-namespace unix sockets do NOT cross.
- Any service that must be reachable at a shared port has to run INSIDE
  each worker's netns (e.g. a per-worker Mockoon), not as one shared
  host process.
- Requires unprivileged user namespaces enabled on the host
  (`user.max_user_namespaces > 0`).

## Evidence

`internal/task/netns_live_test.go` (`TestNetnsIsolationLive`) proves the
real wrap output: two workers bind the same loopback port in parallel
without `EADDRINUSE`. It self-skips where `pasta` is absent so
`just check` stays green on a bare devshell, and runs for real on the
flake-configured host.
