# Version bump by change size

**Every landed unit carries a VERSION bump on its branch, sized to the change.** Fixes get a patch bump; real features or upgrades get a bigger one. Pick the number before the branch is handed off, not after.

- Fixes (bug fixes, test fixes, small rule or doc tweaks): patch bump (0.4.5 -> 0.4.6).
- Real features or upgrades (new commands, toolchain or nixpkgs upgrades, behavior changes consumers notice): minor or larger bump.
- Before picking a bump, check existing tags (`git tag -l`). Never reuse a version already tagged on another branch's line. If the natural bump would collide, ask the operator which number to use.
