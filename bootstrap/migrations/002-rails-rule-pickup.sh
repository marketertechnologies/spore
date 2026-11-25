#!/usr/bin/env bash
# Propagate the rules/lang/rails-migrations.md rule to existing
# bootstrapped projects on this host that vendor the spore rule pool.
#
# Discovery: find spore consumer repos under $HOME (depth-limited).
# A repo counts only when its root holds spore.toml and a
# "rules/consumers/" directory. Inside it, only consumer lists spore
# renders are edited: a *.txt carrying a "# target:" directive (the
# same opt-in `spore compose --write` and the claude-drift lint key
# on). Anything else under $HOME is never written.
#
# For each consumer repo that ALSO has a Rails marker pair
# (Gemfile + config/application.rb), this migration:
#   1. Writes "rules/lang/rails-migrations.md" if absent or stale
#      (compared by content equality with the canonical body below).
#   2. Appends "?rails lang/rails-migrations" to every spore consumer
#      list under "rules/consumers/" that doesn't already reference it.
#
# Re-rendering of the consumer's target files (CLAUDE.md / AGENTS.md)
# is left to the project's own drift lint on next commit. This
# migration touches the rule pool only; it does not invoke `spore
# compose` against arbitrary trees.
#
# All steps are idempotent: a second run on already-migrated state
# finds the file present and the predicate line present, and exits
# without further writes.
#
# Skipped silently when no candidate projects are found.

set -eu

home_root="${HOME:?HOME unset}"
fragment_id="lang/rails-migrations"
predicate_line="?rails ${fragment_id}"

# Canonical body of the rule fragment. Keep in sync with
# rules/lang/rails-migrations.md in the spore source tree. The
# leading "# Rails ..." markdown heading is emitted via printf
# instead of inlined in the heredoc; otherwise the comment-noise
# lint reads it as a shell section-label comment.
fragment_body=$(printf '# Rails database migrations\n\n'; cat <<'EOF'
When adding a database migration, use the Rails CLI generator:

    bundle exec rails g migration <DescriptiveName> [field:type ...]

Do not hand-author files under `db/migrate/`. The generator stamps a unique timestamp prefix on the filename; that prefix is the migration version key Rails uses to order and dedupe migrations. Hand-authored timestamps race with concurrent work on other branches and collide silently, leaving the schema in an order that depends on filesystem listing rather than commit order.

Edit the generated file in place after generation if the scaffold needs adjusting; do not rename it (renaming changes the version key).
EOF
)

# -maxdepth 5 reaches $HOME/<repo>/spore.toml through
# $HOME/<a>/<b>/<c>/<repo>/spore.toml without descending into deep
# dependency trees.
roots=$(find "$home_root" \
    -maxdepth 5 \
    \( -name .git -o -name .worktrees -o -name node_modules -o -name vendor \) -prune \
    -o -type f -name spore.toml -print \
    2>/dev/null || true)

if [ -z "$roots" ]; then
    echo "spore migrate 002: no spore consumer repos found under $home_root; skipping"
    exit 0
fi

touched=0
while IFS= read -r toml; do
    [ -z "$toml" ] && continue
    project_root=$(dirname "$toml")
    rules_dir="$project_root/rules"
    consumers_dir="$rules_dir/consumers"
    if [ -L "$rules_dir" ] || [ -L "$consumers_dir" ] || [ ! -d "$consumers_dir" ] ||
        [ -L "$rules_dir/lang" ] || [ -L "$rules_dir/lang/rails-migrations.md" ]; then
        continue
    fi

    if [ ! -f "$project_root/Gemfile" ] || [ ! -f "$project_root/config/application.rb" ]; then
        continue
    fi

    consumer_files=()
    shopt -s nullglob
    for txt in "$consumers_dir"/*.txt; do
        if [ -f "$txt" ] && [ ! -L "$txt" ] &&
            grep -qE '^[[:space:]]*#[[:space:]]*target:[[:space:]]*[^[:space:]]' "$txt"; then
            consumer_files+=("$txt")
        fi
    done
    shopt -u nullglob
    if [ ${#consumer_files[@]} -eq 0 ]; then
        continue
    fi

    lang_dir="$rules_dir/lang"
    fragment_path="$lang_dir/rails-migrations.md"

    mkdir -p "$lang_dir"
    if [ ! -f "$fragment_path" ] || [ "$(cat "$fragment_path")" != "$fragment_body" ]; then
        printf '%s\n' "$fragment_body" > "$fragment_path"
        echo "spore migrate 002: wrote $fragment_path"
        touched=1
    fi

    for txt in "${consumer_files[@]}"; do
        if grep -qxF "$predicate_line" "$txt"; then
            continue
        fi
        # Ensure trailing newline so the appended line lands on its own.
        if [ -s "$txt" ] && [ "$(tail -c 1 "$txt")" != "" ]; then
            printf '\n' >> "$txt"
        fi
        printf '%s\n' "$predicate_line" >> "$txt"
        echo "spore migrate 002: appended '$predicate_line' to $txt"
        touched=1
    done
done <<<"$roots"

if [ "$touched" -eq 0 ]; then
    echo "spore migrate 002: all spore consumer repos already carry the rails-migrations rule"
fi
