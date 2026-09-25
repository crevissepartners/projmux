package profile

import (
	"slices"
	"testing"
)

func TestClaudeModelsAreAcceptedDistinctAndCopied(t *testing.T) {
	t.Parallel()
	models := ClaudeModels()
	if len(models) == 0 {
		t.Fatal("ClaudeModels is empty")
	}
	for _, alias := range []string{"fable", "opus", "sonnet"} {
		if !slices.Contains(models, alias) {
			t.Errorf("ClaudeModels = %v, missing %q", models, alias)
		}
	}
	seen := map[string]bool{}
	for _, name := range models {
		if !ModelName.MatchString(name) {
			t.Errorf("listed model %q does not match ModelName %s", name, ModelName)
		}
		if seen[name] {
			t.Errorf("listed model %q is duplicated", name)
		}
		seen[name] = true
	}
	models[0] = "mutated"
	if got := ClaudeModels(); got[0] == "mutated" {
		t.Fatalf("ClaudeModels returned the shared list: %v", got)
	}
}

// The list is a suggestion: a name outside it still parses as a profile model.
func TestParseAcceptsAModelOutsideTheSuggestedList(t *testing.T) {
	t.Parallel()
	const unlisted = "claude-opus-5"
	if slices.Contains(ClaudeModels(), unlisted) {
		t.Fatalf("%q is listed; pick a name outside the list", unlisted)
	}
	spec, err := Parse([]byte("model = \"" + unlisted + "\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Model != unlisted {
		t.Fatalf("spec.Model = %q, want %q", spec.Model, unlisted)
	}
}
