set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

default:
    @just --list

check: fmt-check lint test vuln nix-check

fmt:
    gofmt -w $(find . -name '*.go' -not -path './.git/*' -not -path './.worktrees/*' | sort)
    nixpkgs-fmt $(find . -name '*.nix' -not -path './.git/*' -not -path './.worktrees/*' | sort)

fmt-check:
    @unformatted="$(gofmt -l $(find . -name '*.go' -not -path './.git/*' -not -path './.worktrees/*' | sort))"; \
    if [ -n "$unformatted" ]; then \
      printf 'gofmt needed:\n%s\n' "$unformatted"; \
      exit 1; \
    fi
    nixpkgs-fmt --check $(find . -name '*.nix' -not -path './.git/*' -not -path './.worktrees/*' | sort)

lint:
    go vet ./...
    golangci-lint run ./...
    go run ./cmd/spore lint

test:
    go test ./...

coverage:
    mkdir -p coverage
    go test -covermode=atomic -coverprofile=coverage/coverage.out ./...
    go tool cover -func=coverage/coverage.out | tee coverage/coverage.txt

vuln:
    govulncheck ./...

nix-check:
    nix flake check

build: go-build nix-build

go-build:
    mkdir -p build
    go build -trimpath -o build/spore ./cmd/spore

nix-build:
    nix build .

# deploy: rebuild the rocky host from this checkout. Run on the
# rocky box as root. Picks up any committed (or dirty) changes to
# flake.nix, nixosModules/, nix/hosts/rocky/, bootstrap/handover/,
# etc., and atomically activates the new system. The spore binary
# on the coordinator's PATH ends up matching the source tree.
#
# Pinned to /home/spore/project so `just deploy` works from any CWD
# (root's home has no flake.nix; just changes dir to the justfile
# location but we want the flake path baked in so a stray run from
# /root behaves the same as one from /home/spore/project).
deploy:
    #!/usr/bin/env bash
    set -euo pipefail
    repo=/home/spore/project
    # nix/hosts/rocky/local.nix holds the host SSH keys but is gitignored,
    # and `nixos-rebuild switch --flake` only reads tracked/staged files. A
    # plain switch would not see local.nix and would deploy empty authorized
    # keys, locking the box out. Force-stage it for the build (no commit),
    # and unstage on exit so it is never accidentally committed or pushed.
    if [ -f "$repo/nix/hosts/rocky/local.nix" ]; then
      git -C "$repo" add -f nix/hosts/rocky/local.nix
      trap 'git -C "$repo" restore --staged nix/hosts/rocky/local.nix 2>/dev/null || true' EXIT
    fi
    nixos-rebuild switch --flake "$repo#rocky"

# release X.Y.Z: bump VERSION, commit, and tag vX.Y.Z. Aborts on a
# dirty tree, a failing `just check`, or an existing tag. Does NOT
# push -- inspect the commit + tag, then `git push origin main vX.Y.Z`.
release VERSION:
    @if [ -z "$(echo {{VERSION}} | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$')" ]; then \
      echo "release: version must be X.Y.Z, got {{VERSION}}"; exit 2; \
    fi
    @if [ -n "$(git status --porcelain)" ]; then \
      echo "release: tree is dirty; commit or stash first"; \
      git status --short; exit 2; \
    fi
    @if git rev-parse --verify --quiet "v{{VERSION}}" >/dev/null; then \
      echo "release: tag v{{VERSION}} already exists"; exit 2; \
    fi
    just check
    echo "{{VERSION}}" > VERSION
    git add VERSION
    git commit -m "release: v{{VERSION}}"
    git tag -a "v{{VERSION}}" -m "v{{VERSION}}"
    @echo
    @echo "release: committed VERSION={{VERSION}} and tagged v{{VERSION}}"
    @echo "next:    git push origin main v{{VERSION}}"
