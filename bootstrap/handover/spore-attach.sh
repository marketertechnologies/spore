#!/usr/bin/env bash
# Login shell for the spore operator user.
#
# Three landing modes:
#   spore-attach              Attach to the singleton coordinator if
#                             alive. If the coordinator is missing,
#                             fall back to a default pilot session
#                             (so the SSH does not bounce) and print
#                             how to start it. This is the primary
#                             operator path; the local handover
#                             "coord" window relies on it.
#   spore-attach coord        Same as the no-arg form (explicit).
#   spore-attach pilot [name] Create / attach to a private pilot
#                             session at spore/pilot/<name> (defaults
#                             to "default"). Pilots that share a key
#                             share a session by name; pilots that
#                             want their own pane pass distinct names.
#
# Wire each pilot up via a per-key forced-command in
# /home/spore/.ssh/authorized_keys, e.g.
#   command="/usr/local/bin/spore-attach pilot andrei",no-port-forwarding,no-X11-forwarding ssh-ed25519 AAAA... andrei@laptop
#
# The coordinator session is owned by `spore fleet reconcile`; this
# script never spawns it. From any pilot pane peek at the coordinator:
#   tmux attach -d -t spore/<project>/coordinator   # take over
#   tmux attach -r -t spore/<project>/coordinator   # read-only
#
# Detach (Ctrl-b d) ends the SSH session. To do anything privileged,
# SSH in as root instead.
#
# Non-attach commands fall through to bash: when the `-c` string does
# not start with spore-attach, it runs as `bash -lc "<string>"`. This
# serves editor keys with no `command=` (VS Code Remote-SSH installs
# and runs its server this way). Forced-command keys never reach the
# fall-through: sshd always hands the forced string, not the client's.
set -e

# sshd invokes the login shell as `<shell> -c "<command-string>"`,
# both for forced commands and for client-supplied ones.
if [ "${1:-}" = "-c" ]; then
    cmd="${2:-}"
    first="${cmd#"${cmd%%[![:space:]]*}"}"
    first="${first%%[[:space:]]*}"
    case "${first##*/}" in
        spore-attach|spore-attach.sh)
            # word-split the attach string on purpose
            # shellcheck disable=SC2086
            set -- $cmd
            shift   # drop the leading script path
            ;;
        *)
            # $BASH is the interpreter running this script; it survives
            # a minimal sshd PATH on NixOS.
            exec "${BASH:-bash}" -lc "$cmd"
            ;;
    esac
fi

mode="${1:-coord}"

attach_pilot() {
    name="${1:-default}"
    # Pass `bash -l` as the session command; the spore user's login
    # shell is this script, and tmux would otherwise recurse into it.
    exec tmux new-session -A -s "spore/pilot/${name}" bash -l
}

case "$mode" in
    coord)
        sessions=$(tmux ls -F '#{session_name}' 2>/dev/null | grep '/coordinator$' || true)
        n=$(printf '%s' "$sessions" | grep -c . || true)
        case "$n" in
            1)
                exec tmux attach -t "$sessions"
                ;;
            0)
                cat >&2 <<'EOF'
spore-attach: no coordinator session running on this host.
spore-attach: dropping into a default pilot session so you can recover.
spore-attach: to start the coordinator, from inside the pane run:
spore-attach:   spore fleet enable && spore fleet reconcile
EOF
                attach_pilot default
                ;;
            *)
                echo "spore-attach: multiple coordinator sessions present:" >&2
                printf '%s\n' "$sessions" | sed 's/^/  /' >&2
                echo "spore-attach: ssh in as root to reconcile." >&2
                exit 1
                ;;
        esac
        ;;
    pilot)
        attach_pilot "${2:-default}"
        ;;
    *)
        echo "spore-attach: unknown mode: $mode" >&2
        echo "usage: spore-attach [coord | pilot [<name>]]" >&2
        exit 2
        ;;
esac
