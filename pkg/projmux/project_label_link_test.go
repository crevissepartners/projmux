package projmux_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestResolveProjectLabelLinkCOREParity(t *testing.T) {
	project := core.Project{Metadata: core.ObjectMeta{UID: "proj/a", Name: "한 글", Labels: map[string]string{"team": "a/b"}}}
	project.Spec.Root = "/must/not/be/exposed"
	for _, tc := range []struct {
		name, template, key, value, want string
		ok                               bool
	}{
		{"repo", "{repo}/pull/{value}", "pr", "725", "https://repo.example/pull/725", true},
		{"escaping", "{repo[1]}/{value}", "pr", "a/b ?#%한", "https://second.example/a%2Fb%20%3F%23%25%ED%95%9C", true},
		{"jira", "{jira}/{jira[1]}/{value}", "pr", "A-1", "https://jira.example/https://partner.example/A-1", true},
		{"project", "{wiki}/{project.uid}/{project.name}/{project.labels.team}/{value}", "pr", "v", "https://wiki.example/proj%2Fa/%ED%95%9C%20%EA%B8%80/a%2Fb/v", true},
		{"empty", "{repo}/{value}", "pr", "", "", false},
		{"unknown key", "{repo}/{value}", "other", "1", "", false},
		{"case sensitive", "{repo}/{value}", "PR", "1", "", false},
		{"missing index", "{repo[2]}/{value}", "pr", "1", "", false},
		{"missing named URL", "{absent}/{value}", "pr", "1", "", false},
		{"missing label", "{repo}/{project.labels.absent}/{value}", "pr", "1", "", false},
		{"private field", "{repo}/{project.root}/{value}", "pr", "1", "", false},
		{"malformed", "{repo}/{value", "pr", "1", "", false},
		{"scheme", "file:///tmp/{value}", "pr", "1", "", false},
		{"credentials", "https://user:pass@example.com/{value}", "pr", "1", "", false},
		{"relative", "/{value}", "pr", "1", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := core.ProjectLinkRules{Repo: []string{"https://repo.example///", "https://second.example/"}, Jira: []string{"https://jira.example/", "https://partner.example/"}, URLs: map[string]string{"wiki": "https://wiki.example/"}, Links: []core.ProjectLink{{LabelKey: "pr", Template: tc.template}}}
			canonical := projectlinks.Rules{Repo: rules.Repo, Jira: rules.Jira, URLs: rules.URLs, Links: []projectlinks.Link{{LabelKey: "pr", Template: tc.template}}}
			want, ok := projectlinks.Resolve(canonical, projectlinks.Project{UID: project.Metadata.UID, Name: project.Metadata.Name, Labels: project.Metadata.Labels}, tc.key, tc.value)
			if want != tc.want || ok != tc.ok {
				t.Fatalf("CORE result (%q,%v), expected (%q,%v)", want, ok, tc.want, tc.ok)
			}
			beforeRules, _ := json.Marshal(rules)
			beforeProject, _ := json.Marshal(project)
			for range 3 {
				got, gotOK := projmux.ResolveProjectLabelLink(rules, project, tc.key, tc.value)
				if got != want || gotOK != ok {
					t.Fatalf("SDK (%q,%v), CORE (%q,%v)", got, gotOK, want, ok)
				}
			}
			afterRules, _ := json.Marshal(rules)
			afterProject, _ := json.Marshal(project)
			if string(beforeRules) != string(afterRules) || string(beforeProject) != string(afterProject) {
				t.Fatal("input changed")
			}
		})
	}
	if got, ok := projmux.ResolveProjectLabelLink(core.ProjectLinkRules{}, core.Project{}, "pr", "1"); got != "" || ok {
		t.Fatalf("zero inputs: %q %v", got, ok)
	}
}

func TestResolveProjectLabelLinkNoEffects(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PROJMUX_CONFIG_DIR", "PROJMUX_STATE_DIR"} {
		t.Setenv(name, root)
	}
	// Any attempted executable invocation fails; no Client or Registry is needed.
	t.Setenv("PATH", "")
	t.Setenv("TMUX", "invalid-socket")
	beforeEnv := os.Environ()
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	rules := core.ProjectLinkRules{Links: []core.ProjectLink{{LabelKey: "pr", Template: "https://example.com/{value}"}}}
	for range 3 {
		got, ok := projmux.ResolveProjectLabelLink(rules, core.Project{}, "pr", "a/b")
		if got != "https://example.com/a%2Fb" || !ok {
			t.Fatalf("%q %v", got, ok)
		}
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeEnv, os.Environ()) {
		t.Fatal("filesystem or environment changed")
	}
	if _, err := os.Stat(filepath.Join(root, "registry.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected Registry: %v", err)
	}
}
