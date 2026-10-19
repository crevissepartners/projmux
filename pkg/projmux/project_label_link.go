package projmux

import (
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ResolveProjectLabelLink resolves an actual label key/value using the Project's
// link rules. Values and Project UID, name and labels are path-escaped; URL
// placeholders have trailing slashes removed. Missing rules or variables, empty
// values and expansions rejected by CORE yield ("", false).
//
// It uses only the supplied inputs, without reading files, the environment or
// Registry, contacting tmux, or changing the inputs. It does not load or validate
// a stored rule set, resolve Project references, or open the resulting URL.
func ResolveProjectLabelLink(rules core.ProjectLinkRules, project core.Project, key, value string) (string, bool) {
	input := projectlinks.Rules{Jira: rules.Jira, Repo: rules.Repo, URLs: rules.URLs}
	for _, link := range rules.Links {
		input.Links = append(input.Links, projectlinks.Link{LabelKey: link.LabelKey, Template: link.Template})
	}
	return projectlinks.Resolve(input, projectlinks.Project{
		UID: project.Metadata.UID, Name: project.Metadata.Name, Labels: project.Metadata.Labels,
	}, key, value)
}
