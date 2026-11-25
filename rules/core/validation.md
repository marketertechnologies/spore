## Validation

Spore self-validates with the lint set it ships (`lints.Default()`, listed in `bootstrap/stages/validation-green.md`). Run `spore lint` plus `go test ./...` before push; both must be green.
