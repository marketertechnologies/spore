# Fix vulns at the root

**When a vuln gate (govulncheck or similar) is red and a real fix exists upstream, fix at the root.** Bump the toolchain, nixpkgs, or the dependency. Do not skip, allowlist, or suppress the gate to get green.

- Fix released upstream but not yet packaged (a Go stdlib advisory whose fix is not in nixpkgs-unstable yet): merging red is allowed. Record the advisory IDs in the PR, then land the bump in a follow-up as soon as it is packaged. Precedent: PRs #34-#38, #65.
- No real fix upstream: report the advisory IDs and let the operator decide. Do not quietly mute the gate.
