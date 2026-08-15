package selector

import (
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestResolveProjectsIsAListReadOverTheWholeRegistry pins the Project list
// pipeline: with no --project occurrence the whole registry enters stage one, and
// with one occurrence the scope narrows to that exact name or uid.
func TestResolveProjectsIsAListReadOverTheWholeRegistry(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))
	for _, test := range []struct {
		name  string
		query Query
		want  []string
	}{
		{name: "no occurrence lists every project", want: fixtureProjectUIDs(t)},
		{
			name:  "an exact name narrows to one",
			query: Query{Project: refFor(t, metadata.KindProject, "alpha")},
			want:  []string{"prj-alpha"},
		},
		{
			name:  "the uid form narrows to one",
			query: Query{Project: refFor(t, metadata.KindProject, "uid:prj-beta")},
			want:  []string{"prj-beta"},
		},
		{
			name:  "a duplicate displayName never matches",
			query: Query{Project: refFor(t, metadata.KindProject, "projmux")},
			want:  nil,
		},
		{
			name:  "a spec.root path never matches",
			query: Query{Project: refFor(t, metadata.KindProject, "/srv/alpha")},
			want:  nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolution, err := resolver.ResolveProjects(test.query)
			if err != nil {
				t.Fatalf("ResolveProjects error = %v", err)
			}
			if got := resolution.UIDs(); !reflect.DeepEqual(got, test.want) && !(len(got) == 0 && len(test.want) == 0) {
				t.Fatalf("uids = %v, want %v", got, test.want)
			}
			if !reflect.DeepEqual(traceStages(resolution), StageOrder()) {
				t.Fatalf("trace stages = %v, want %v", traceStages(resolution), StageOrder())
			}
		})
	}
}

// TestResolveAgentsIsWindowScoped pins the Agent pipeline: Agent names are unique
// inside a Window, so the same name legitimately appears under several Windows
// and only the scope disambiguates it.
func TestResolveAgentsIsWindowScoped(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))
	agentRef := func(raw string) []Ref { return []Ref{mustRef(t, metadata.KindAgent, raw)} }

	for _, test := range []struct {
		name  string
		query Query
		want  int
	}{
		{name: "no scope reaches every agent of that name", query: Query{Agents: agentRef("codex")}, want: 1},
		{
			name:  "a project scope narrows the agent set",
			query: Query{Project: refFor(t, metadata.KindProject, "alpha"), Agents: agentRef("codex")},
			want:  1,
		},
		{
			name:  "a window scope that owns no agent resolves nothing",
			query: Query{Windows: windowRefs(t, "review"), Agents: agentRef("codex")},
			want:  0,
		},
		{
			name:  "the owning window scope keeps it",
			query: Query{Project: refFor(t, metadata.KindProject, "alpha"), Windows: windowRefs(t, "main"), Agents: agentRef("codex")},
			want:  1,
		},
		{
			name:  "a displayName-shaped value never matches",
			query: Query{Agents: agentRef("codex-pane")},
			want:  0,
		},
		{name: "no occurrence lists the whole scope", query: Query{Project: refFor(t, metadata.KindProject, "alpha")}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolution, err := resolver.ResolveAgents(test.query)
			if err != nil {
				t.Fatalf("ResolveAgents error = %v", err)
			}
			if got := len(resolution.Matches); got != test.want {
				t.Fatalf("matches = %d, want %d (%v)", got, test.want, resolution.UIDs())
			}
			for _, match := range resolution.Matches {
				if match.Kind != metadata.KindAgent {
					t.Fatalf("match kind = %q, want Agent", match.Kind)
				}
				if match.Owner.Window == "" || match.Owner.Project == "" {
					t.Fatalf("agent match %q has no owner chain: %+v", match.UID, match.Owner)
				}
			}
		})
	}
}

// TestAgentRefsRenderWithoutAScopeFlagSpelling keeps the error text honest: an
// Agent reference arrives positionally, so it must not be reported as a
// `--agent` flag the grammar does not define.
func TestAgentRefsRenderWithoutAScopeFlagSpelling(t *testing.T) {
	t.Parallel()

	ref := mustRef(t, metadata.KindAgent, "codex")
	got := DescribeSelector(Query{Agents: []Ref{ref}})
	if got != "agent codex" {
		t.Fatalf("DescribeSelector = %q, want %q", got, "agent codex")
	}
	if got := DescribeSelector(Query{Project: refFor(t, metadata.KindProject, "alpha"), Agents: []Ref{ref}}); got != "--project alpha agent codex" {
		t.Fatalf("DescribeSelector = %q", got)
	}
}

func traceStages(resolution Resolution) []Stage {
	out := make([]Stage, 0, len(resolution.Trace))
	for _, step := range resolution.Trace {
		out = append(out, step.Stage)
	}
	return out
}

func refFor(t *testing.T, kind metadata.Kind, raw string) *Ref {
	t.Helper()
	ref := mustRef(t, kind, raw)
	return &ref
}

func windowRefs(t *testing.T, raws ...string) []Ref {
	t.Helper()
	out := make([]Ref, 0, len(raws))
	for _, raw := range raws {
		out = append(out, mustRef(t, metadata.KindWindow, raw))
	}
	return out
}

func fixtureProjectUIDs(t *testing.T) []string {
	t.Helper()
	registry := standardRegistry(t)
	out := make([]string, 0, len(registry.Projects))
	for _, project := range registry.Projects {
		out = append(out, project.Metadata.UID)
	}
	return out
}
