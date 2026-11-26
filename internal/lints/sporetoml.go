package lints

import "github.com/versality/spore/internal/sporetoml"

// SporeTOML reports unknown sections and keys in spore.toml as
// warnings: the loaders ignore them, so a typo silently drops a
// setting. `spore config check` fails on the same findings.
type SporeTOML struct{}

func (SporeTOML) Name() string { return "spore-toml" }

func (SporeTOML) Run(root string) ([]Issue, error) {
	problems, err := sporetoml.CheckFile(root)
	if err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(problems))
	for _, p := range problems {
		issues = append(issues, Issue{Path: "spore.toml", Message: p.String(), Warn: true})
	}
	return issues, nil
}
