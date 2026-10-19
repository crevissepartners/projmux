package consumer_test

import (
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalResolveProjectLabelLink(t *testing.T) {
	var resolve func(core.ProjectLinkRules, core.Project, string, string) (string, bool) = projmux.ResolveProjectLabelLink
	rules := core.ProjectLinkRules{Repo: []string{"https://github.com/example/"}, Links: []core.ProjectLink{{LabelKey: "pr", Template: "{repo}/{project.name}/{value}"}}}
	project := core.Project{Metadata: core.ObjectMeta{Name: "a/b"}}
	if got, ok := resolve(rules, project, "pr", "725 #"); !ok || got != "https://github.com/example/a%2Fb/725%20%23" {
		t.Fatalf("%q %v", got, ok)
	}
	if got, ok := resolve(rules, project, "unknown", "725"); ok || got != "" {
		t.Fatalf("%q %v", got, ok)
	}
	rules.Links[0].Template = "{repo}/{project.labels.missing}/{value}"
	if got, ok := resolve(rules, project, "pr", "725"); ok || got != "" {
		t.Fatalf("missing Project label: %q %v", got, ok)
	}
}
