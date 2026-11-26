package task

import (
	"strings"
	"testing"

	"github.com/versality/spore/internal/sporetoml"
)

func TestAgentSectionsKnownToSporeTOMLSchema(t *testing.T) {
	var b strings.Builder
	for _, s := range AgentSections {
		b.WriteString("[" + s + "]\n")
		for _, k := range AgentSettingKeys {
			b.WriteString(k + ` = "x"` + "\n")
		}
	}
	if probs := sporetoml.Check(b.String()); len(probs) > 0 {
		t.Errorf("spore.toml schema is missing [agents.*] entries: %v", probs)
	}
}
