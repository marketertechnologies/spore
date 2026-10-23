#!/usr/bin/env bash
# Propagate the rules/lang/rails-migrations.md rule to existing
# bootstrapped projects on this host that vendor the spore rule pool.
#
# Discovery: walk $HOME (depth-limited) for any directory shaped like
# a vendored spore consumer pool (a "rules/consumers/" subdirectory
# with at least one *.txt file). The parent of that "rules/" dir is
# treated as the project root.
#
# For each discovered project root that ALSO has a Rails marker pair
# (Gemfile + config/application.rb), this migration:
#   1. Writes "rules/lang/rails-migrations.md" if absent or stale
#      (compared by content equality with the canonical body below).
#   2. Appends "?rails lang/rails-migrations" to every consumer txt
#      under "rules/consumers/" that doesn't already reference it.
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

# `find` collects candidate consumer dirs. -maxdepth 6 covers
# $HOME/<repo>/rules/consumers and $HOME/<group>/<repo>/rules/consumers
# without descending into deep node_modules/vendor trees. -prune skips
# .git and .worktrees explicitly.
candidates=$(find "$home_root" \
    -maxdepth 6 \
    \( -name .git -o -name .worktrees -o -name node_modules -o -name vendor \) -prune \
    -o -type d -name consumers -path '*/rules/consumers' -print \
    2>/dev/null || true)

if [ -z "$candidates" ]; then
    echo "spore migrate 002: no vendored-rules projects found under $home_root; skipping"
    exit 0
fi

touched=0
while IFS= read -r consumers_dir; do
    [ -z "$consumers_dir" ] && continue
    rules_dir=$(dirname "$consumers_dir")
    project_root=$(dirname "$rules_dir")

    # Rails marker check.
    if [ ! -f "$project_root/Gemfile" ] || [ ! -f "$project_root/config/application.rb" ]; then
        continue
    fi

    # At least one consumer txt to patch.
    shopt -s nullglob
    consumer_files=("$consumers_dir"/*.txt)
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
done <<<"$candidates"

if [ "$touched" -eq 0 ]; then
    echo "spore migrate 002: all candidate projects already carry the rails-migrations rule"
fi
