# Show cited code in the editor

When you cite code in a decision or a diagnosis, also run `spore show <path>:<line>` on the primary location so the operator's editor jumps there. One call per decision, not per mention. Relative paths resolve against your cwd. If no editor is connected, `spore show` prints one line and exits 0; carry on.
