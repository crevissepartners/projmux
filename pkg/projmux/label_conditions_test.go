package projmux

import (
	"errors"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The public parser returns the CLI parser's key and value, and a failure
// carries the CLI parser's error text byte for byte.
func TestParseLabelConditionEqualsInternal(t *testing.T) {
	inputs := []string{"", "=", "a", " a = b ", "a=", "a=b=c", "=b", "k=v ", " =x", "\tk\t=\tv\t", "a==", "é=ü", "a b=c d"}
	var okCount, failCount int
	for _, raw := range inputs {
		want, wantErr := selector.ParseLabel(raw)
		got, gotErr := ParseLabelCondition(raw)
		if (gotErr != nil) != (wantErr != nil) {
			t.Fatalf("ParseLabelCondition(%q) error %v, internal %v", raw, gotErr, wantErr)
		}
		if wantErr != nil {
			failCount++
			var refusal *Refusal
			if !errors.As(gotErr, &refusal) || refusal.Code != CodeSelectorInvalid {
				t.Fatalf("ParseLabelCondition(%q) error %v is not a selector-invalid Refusal", raw, gotErr)
			}
			if gotErr.Error() != wantErr.Error() {
				t.Fatalf("ParseLabelCondition(%q) text %q, internal %q", raw, gotErr.Error(), wantErr.Error())
			}
			var input *selector.SelectorError
			if !errors.As(gotErr, &input) {
				t.Fatalf("ParseLabelCondition(%q) lost the internal cause", raw)
			}
			if !reflect.DeepEqual(got, core.LabelCondition{}) {
				t.Fatalf("ParseLabelCondition(%q) failed with value %+v", raw, got)
			}
			continue
		}
		okCount++
		if got.Key != want.Key || got.Value != want.Value || got.String() != want.String() {
			t.Fatalf("ParseLabelCondition(%q) = %+v %q, internal %+v %q", raw, got, got.String(), want, want.String())
		}
	}
	if okCount == 0 || failCount == 0 {
		t.Fatalf("inputs must cover both outcomes, got %d success and %d failure", okCount, failCount)
	}
}

// labelFixture prepares two Projects. project-01 has two Windows and three
// Agents (labelled, one with an empty value, one without labels); project-02
// has one Agent that carries the same label as project-01's.
func labelFixture(t *testing.T) (*PreparedRegistrySnapshot, metadata.Registry) {
	t.Helper()
	c, paths := graphFixtureClient(t, mutatedGraphFixture(t, func(r *metadata.Registry) {
		project := r.Projects[0]
		project.Metadata.UID, project.Metadata.Name = "project-02", "beta"
		project.Spec.PrimaryWindowRef = "window-03"
		r.Projects = append(r.Projects, project)
		window := func(uid, owner string) metadata.Window {
			w := r.Windows[0]
			w.Metadata.UID, w.Metadata.Name = uid, uid
			w.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindProject, UID: owner}
			return w
		}
		r.Windows = append(r.Windows, window("window-02", "project-01"), window("window-03", "project-02"))
		agent := func(uid, owner string, labels map[string]string) metadata.Agent {
			a := r.Agents[0]
			a.Metadata.UID, a.Metadata.Name = uid, uid
			a.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindWindow, UID: owner}
			a.Metadata.Labels = labels
			a.Status.PaneRef = ""
			return a
		}
		r.Agents[0].Metadata.Labels = map[string]string{"role": "worker", "tier": "gold"}
		r.Agents = append(r.Agents,
			agent("agent-02", "window-01", map[string]string{"role": "worker", "tier": ""}),
			agent("agent-03", "window-02", nil),
			agent("agent-04", "window-02", map[string]string{"role": "lead", "tier": "gold"}),
			agent("agent-05", "window-03", map[string]string{"role": "worker", "tier": "gold"}),
		)
	}))
	prepared, _ := mustPrepare(t, c, nil)
	loaded, err := resources.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	return prepared, loaded
}

func conditionsOf(t *testing.T, raws ...string) []core.LabelCondition {
	t.Helper()
	var out []core.LabelCondition
	for _, raw := range raws {
		condition, err := ParseLabelCondition(raw)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, condition)
	}
	return out
}

// The public answer is the resolver's own: same uids in the same order for no
// condition, one, several and none matching.
func TestAgentsMatchingLabelsEqualsInternal(t *testing.T) {
	prepared, loaded := labelFixture(t)
	cases := []struct {
		name       string
		project    string
		conditions []string
	}{
		{"no-conditions", "project-01", nil},
		{"one", "project-01", []string{"role=worker"}},
		{"several", "project-01", []string{"role=worker", "tier=gold"}},
		{"empty-value", "project-01", []string{"tier="}},
		{"trimmed", "project-01", []string{" role = lead "}},
		{"none-match", "project-01", []string{"role=ghost"}},
		{"conflicting", "project-01", []string{"role=worker", "role=lead"}},
		{"other-project", "project-02", []string{"role=worker"}},
		{"other-project-all", "project-02", nil},
	}
	var sawEmpty, sawMany bool
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conditions := conditionsOf(t, tc.conditions...)
			var query selector.Query
			query.Project = &selector.Ref{Kind: metadata.KindProject, UID: tc.project, Raw: "uid:" + tc.project}
			for _, raw := range tc.conditions {
				label, err := selector.ParseLabel(raw)
				if err != nil {
					t.Fatal(err)
				}
				query.Labels = append(query.Labels, label)
			}
			resolution, err := selector.New(loaded).ResolveAgents(query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := prepared.AgentsMatchingLabels(tc.project, conditions)
			if err != nil {
				t.Fatal(err)
			}
			if want := resolution.UIDs(); !reflect.DeepEqual(got, want) {
				t.Fatalf("AgentsMatchingLabels = %v, internal %v", got, want)
			}
			sawEmpty = sawEmpty || len(got) == 0
			sawMany = sawMany || len(got) > 1
		})
	}
	if !sawEmpty || !sawMany {
		t.Fatalf("cases must cover an empty and a multi-Agent result, got empty=%v many=%v", sawEmpty, sawMany)
	}
}

// The public answer has the same uids as the CLI-backed list read, so the two
// routes cannot disagree.
func TestAgentsMatchingLabelsMatchesListedAgents(t *testing.T) {
	prepared, loaded := labelFixture(t)
	query, err := resourceQuery(ResourceSelector{Project: "uid:project-01", Labels: []string{"tier=gold"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resources.Resolve(loaded, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		t.Fatal(err)
	}
	got, err := prepared.AgentsMatchingLabels("project-01", conditionsOf(t, "tier=gold"))
	if err != nil {
		t.Fatal(err)
	}
	if want := result.UIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("AgentsMatchingLabels = %v, resources.Resolve %v", got, want)
	}
}

func TestAgentsMatchingLabelsRefusals(t *testing.T) {
	prepared, _ := labelFixture(t)
	cases := []struct {
		name     string
		prepared *PreparedRegistrySnapshot
		project  string
		code     Code
	}{
		{"missing-project", prepared, "project-absent", CodeResourceNotFound},
		{"name-is-not-uid", prepared, "alpha", CodeResourceNotFound},
		{"empty-project", prepared, "", CodeSelectorInvalid},
		{"nil-handle", nil, "project-01", CodePreparedSnapshotInvalid},
		{"zero-handle", &PreparedRegistrySnapshot{}, "project-01", CodePreparedSnapshotInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.prepared.AgentsMatchingLabels(tc.project, nil)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != tc.code || got != nil {
				t.Fatalf("got %v, %v; want a %s Refusal and no uids", got, err, tc.code)
			}
		})
	}
}

// The returned slice is the caller's: changing it changes no later answer.
func TestAgentsMatchingLabelsResultIsCopied(t *testing.T) {
	prepared, _ := labelFixture(t)
	first, err := prepared.AgentsMatchingLabels("project-01", nil)
	if err != nil || len(first) == 0 {
		t.Fatalf("first = %v, %v", first, err)
	}
	want := append([]string(nil), first...)
	first[0] = "changed"
	second, err := prepared.AgentsMatchingLabels("project-01", nil)
	if err != nil || !reflect.DeepEqual(second, want) {
		t.Fatalf("second = %v, %v; want %v", second, err, want)
	}
}
