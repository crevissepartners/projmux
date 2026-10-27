package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// displayProjectUID is Project UID shaped, so a Project's prompt parts are
// planned for it; the fixture's project-01 is not.
const displayProjectUID = "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"

func displaySnapshot(t *testing.T, c *Client) core.RegistrySnapshot {
	t.Helper()
	snapshot, err := c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// shapedProjectSnapshot is snapshot with project-01 renamed to
// displayProjectUID, owner references included.
func shapedProjectSnapshot(snapshot core.RegistrySnapshot) core.RegistrySnapshot {
	snapshot = snapshot.Clone()
	for i := range snapshot.Projects {
		if snapshot.Projects[i].Metadata.UID == "project-01" {
			snapshot.Projects[i].Metadata.UID = displayProjectUID
		}
	}
	for i := range snapshot.Windows {
		if owner := snapshot.Windows[i].Metadata.OwnerRef; owner != nil && owner.UID == "project-01" {
			owner.UID = displayProjectUID
		}
	}
	return snapshot
}

func agentRow(t *testing.T, snapshot *core.RegistrySnapshot, uid string) *core.Agent {
	t.Helper()
	for i := range snapshot.Agents {
		if snapshot.Agents[i].Metadata.UID == uid {
			return &snapshot.Agents[i]
		}
	}
	t.Fatalf("no Agent row %s", uid)
	return nil
}

func countDisplayPlans(t *testing.T) *countingPromptPlanner {
	t.Helper()
	previous := newAgentSettingsPlanner
	counting := &countingPromptPlanner{calls: map[string]int{}}
	newAgentSettingsPlanner = func(paths config.Paths) runtime.RelaunchPromptPlanner {
		counting.next = previous(paths)
		return counting
	}
	t.Cleanup(func() { newAgentSettingsPlanner = previous })
	return counting
}

func readDisplay(t *testing.T, c *Client, snapshot core.RegistrySnapshot, uids ...string) []core.AgentSettingsResolution {
	t.Helper()
	got, err := c.ReadAgentDisplaySettingsFromSnapshot(context.Background(), snapshot, core.ReadAgentSettingsRequest{AgentUIDs: uids})
	if err != nil {
		t.Fatal(err)
	}
	return got.Agents
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// A held snapshot of a clean Registry resolves what ReadAgentSettings
// resolves, without reading the Registry, and only the new entry sets
// Cacheable.
func TestReadAgentDisplaySettingsFromSnapshotMatchesReadAgentSettings(t *testing.T) {
	c, paths, _ := settingsFixtureClient(t)
	snapshot := displaySnapshot(t, c)
	old, err := c.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	loads := 0
	previousSettings, previousSnapshot := loadAgentSettingsRegistry, loadSnapshotRegistry
	loadAgentSettingsRegistry = func(config.Paths) (metadata.Registry, error) {
		loads++
		return metadata.Registry{}, errors.New("registry read")
	}
	loadSnapshotRegistry = loadAgentSettingsRegistry
	t.Cleanup(func() { loadAgentSettingsRegistry, loadSnapshotRegistry = previousSettings, previousSnapshot })
	if err := os.WriteFile(store.PathFor(paths.StateDir), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := c.ReadAgentDisplaySettingsFromSnapshot(context.Background(), snapshot, core.ReadAgentSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if loads != 0 {
		t.Fatalf("Registry loads %d, want 0", loads)
	}
	if len(got.Agents) != len(snapshot.Agents) {
		t.Fatalf("got %d results for %d rows", len(got.Agents), len(snapshot.Agents))
	}
	byUID := map[string]core.AgentSettingsResolution{}
	for _, a := range old.Agents {
		byUID[a.AgentUID] = a
	}
	for i, a := range got.Agents {
		if a.AgentUID != snapshot.Agents[i].Metadata.UID {
			t.Fatalf("result %d is %s, want snapshot row order", i, a.AgentUID)
		}
		want := byUID[a.AgentUID]
		if want.Cacheable || !a.Cacheable {
			t.Fatalf("%s Cacheable old %v new %v", a.AgentUID, want.Cacheable, a.Cacheable)
		}
		want.Cacheable = true
		if !reflect.DeepEqual(a, want) {
			t.Fatalf("%s display\n%+v\nwant ReadAgentSettings\n%+v", a.AgentUID, a, want)
		}
	}
	oldJSON, _ := json.Marshal(old)
	newJSON, _ := json.Marshal(got)
	if bytes.Contains(oldJSON, []byte("cacheable")) || !bytes.Contains(newJSON, []byte(`"cacheable":true`)) {
		t.Fatalf("cacheable JSON: old %s new %s", oldJSON, newJSON)
	}
}

// Results follow snapshot rows, duplicates included, each from its own
// annotations; unknown UIDs follow in UID order. Rows outside the filter and
// unknown UIDs plan and resolve nothing.
func TestReadAgentDisplaySettingsFromSnapshotRowsAndFilter(t *testing.T) {
	c, _, uids := settingsFixtureClient(t)
	snapshot := displaySnapshot(t, c)
	duplicate := *agentRow(t, &snapshot, uids["claude-b"])
	duplicate.Metadata.Annotations = nil
	bad := *agentRow(t, &snapshot, uids["claude-a"])
	bad.Metadata.UID = "agent-bad-profile"
	bad.Metadata.Annotations = map[string]string{metadata.AnnotationAgentProfile: "no-such-profile"}
	snapshot.Agents = append(snapshot.Agents, duplicate, bad)
	counting := countDisplayPlans(t)

	got := readDisplay(t, c, snapshot, "agent-zz", uids["claude-b"], "agent-aa", uids["claude-b"])
	var order []string
	for _, a := range got {
		order = append(order, a.AgentUID)
	}
	if want := []string{uids["claude-b"], uids["claude-b"], "agent-aa", "agent-zz"}; !slices.Equal(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}
	if got[0].New.Instructions.Value != "reviewer" || got[1].New.Instructions.Value != "" || !got[0].Cacheable || !got[1].Cacheable {
		t.Fatalf("duplicate rows not resolved from their own annotations: %+v", got[:2])
	}
	for _, a := range got[2:] {
		if a.Unavailable != string(CodeResourceNotFound) || a.Layered || a.Cacheable {
			t.Fatalf("unknown UID %+v", a)
		}
	}
	if want := map[string]int{"guidance claude tmux": 1, "links claude project-01": 1}; !reflect.DeepEqual(counting.calls, want) {
		t.Fatalf("planned %v, want only the selected rows' parts once: %v", counting.calls, want)
	}

	clear(counting.calls)
	got = readDisplay(t, c, snapshot, "agent-none")
	if len(got) != 1 || got[0].Unavailable != string(CodeResourceNotFound) || len(counting.calls) != 0 {
		t.Fatalf("unknown UID only: %+v planned %v", got, counting.calls)
	}

	clear(counting.calls)
	all := readDisplay(t, c, snapshot)
	if len(all) != len(snapshot.Agents) {
		t.Fatalf("empty filter: %d results for %d rows", len(all), len(snapshot.Agents))
	}
	if last := all[len(all)-1]; last.AgentUID != "agent-bad-profile" || last.Unavailable == "" || last.Cacheable || last.Layered {
		t.Fatalf("unresolvable profile row %+v", last)
	}
	if want := map[string]int{"guidance claude tmux": 1, "links claude project-01": 1, "guidance codex tmux": 1, "links codex project-01": 1, "guidance process": 1}; !reflect.DeepEqual(counting.calls, want) {
		t.Fatalf("planned %v, want each part once per provider and host or Project: %v", counting.calls, want)
	}
}

// Only the selected row's own fields, its Pane's runtime, its Window's owner
// and that Project's UID, name and labels are inputs.
func TestReadAgentDisplaySettingsFromSnapshotIgnoresUnrelatedInputs(t *testing.T) {
	c, _, uids := settingsFixtureClient(t)
	snapshot := shapedProjectSnapshot(displaySnapshot(t, c))
	selected := []string{uids["claude-a"], uids["codex-process"]}
	want := readDisplay(t, c, snapshot, selected...)

	changed := snapshot.Clone()
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Hour)
	changed.APIVersion = "other"
	for _, name := range []string{"claude-b", "codex-tmux"} {
		row := agentRow(t, &changed, uids[name])
		row.Spec.Provider = "not-a-provider"
		row.Metadata.Annotations = map[string]string{metadata.AnnotationAgentProfile: "no-such-profile"}
		row.Metadata.Labels = map[string]string{"x": "y"}
	}
	for _, name := range []string{"claude-a", "codex-process"} {
		row := agentRow(t, &changed, uids[name])
		row.Metadata.Name = "renamed"
		row.Metadata.Labels = map[string]string{"x": "y"}
		row.Status.Phase = "Offline"
		row.Status.Reason = "changed"
		row.Status.Interaction = core.AgentInteraction{}
		row.Status.LastTransitionAt = time.Unix(1, 0)
		row.Spec.Workspace.CWD = "/elsewhere"
	}
	for i := range changed.Windows {
		changed.Windows[i].Metadata.Name = "renamed"
		changed.Windows[i].Metadata.Labels = map[string]string{"x": "y"}
		changed.Windows[i].Spec.AnchorPaneRef = "pane-gone"
		changed.Windows[i].Status.RuntimeID = "@99"
	}
	for i := range changed.Projects {
		changed.Projects[i].Spec.Root = "/elsewhere"
		changed.Projects[i].Spec.PrimaryWindowRef = "window-gone"
	}
	for i := range changed.Panes {
		changed.Panes[i].Metadata.Name = "renamed"
		changed.Panes[i].Spec.CWD = "/elsewhere"
	}
	changed.Projects = append(changed.Projects, core.Project{Metadata: core.ObjectMeta{UID: "proj-bbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "other"}})
	changed.Agents = append(changed.Agents, core.Agent{Metadata: core.ObjectMeta{UID: "agent-extra", Annotations: map[string]string{metadata.AnnotationAgentProfile: "no-such-profile"}}, Spec: core.AgentSpec{Provider: "claude"}})
	changed.NameReservations = append(changed.NameReservations, core.NameReservation{Name: "n", UID: "u"})
	if got := readDisplay(t, c, changed, selected...); !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelated changes changed the result:\n%+v\nwant\n%+v", got, want)
	}

	fallback := snapshot.Clone()
	row := agentRow(t, &fallback, uids["claude-a"])
	row.Spec.Provider = ""
	row.Status.SessionRef = &core.AgentSessionRef{Provider: "codex"}
	if got := readDisplay(t, c, fallback, uids["claude-a"]); got[0].Provider != "codex" || !got[0].Layered {
		t.Fatalf("conversation provider fallback: %+v", got[0])
	}
}

// A missing Window or Project omits only the Project's prompt parts, unlike
// ReadAgentSettings, and is not a failure.
func TestReadAgentDisplaySettingsFromSnapshotMissingOwners(t *testing.T) {
	c, paths, uids := settingsFixtureClient(t)
	snapshot := shapedProjectSnapshot(displaySnapshot(t, c))
	writeFixtureFile(t, filepath.Join(paths.ConfigDir, agentguidance.ProjectDirName, displayProjectUID+".md"), "PROJECT")
	writeFixtureFile(t, filepath.Join(paths.ConfigDir, projectlinks.DirName, displayProjectUID+projectlinks.FileExt), `{"repo":["https://example.com/r"],"links":[{"labelKey":"pr","template":"{repo}/pull/{value}"}]}`)
	counting := countDisplayPlans(t)
	present := readDisplay(t, c, snapshot, uids["claude-a"])[0]
	if !present.Cacheable || !slices.Contains(present.Reasons, agentsettings.ReasonLinkRulesChanged) || !slices.Contains(present.Reasons, agentsettings.ReasonProjectGuidanceChanged) {
		t.Fatalf("Project parts not compared: %+v", present)
	}

	for name, mutate := range map[string]func(*core.RegistrySnapshot){
		"Project": func(s *core.RegistrySnapshot) { s.Projects = nil },
		"Window": func(s *core.RegistrySnapshot) {
			agentRow(t, s, uids["claude-a"]).Metadata.OwnerRef = &core.OwnerRef{Kind: core.KindWindow, UID: "window-gone"}
		},
	} {
		missing := snapshot.Clone()
		mutate(&missing)
		clear(counting.calls)
		got := readDisplay(t, c, missing, uids["claude-a"])[0]
		if got.Unavailable != "" || !got.Layered || !got.Cacheable || got.Reasons == nil {
			t.Fatalf("missing %s: %+v", name, got)
		}
		if slices.Contains(got.Reasons, agentsettings.ReasonLinkRulesChanged) || slices.Contains(got.Reasons, agentsettings.ReasonProjectGuidanceChanged) {
			t.Fatalf("missing %s still compared Project parts: %+v", name, got)
		}
		if counting.calls["guidance claude tmux"] != 1 {
			t.Fatalf("missing %s skipped the agent guidance: %v", name, counting.calls)
		}
	}
}

// Cacheable is false while a prompt part or the instructions cannot be read,
// snapshotted or migrated, and true again once they can; an off part is not a
// failure. The guidance policy, fragments and migration receipt are inputs.
func TestReadAgentDisplaySettingsFromSnapshotCacheableFailures(t *testing.T) {
	c, paths, uids := settingsFixtureClient(t)
	snapshot := shapedProjectSnapshot(displaySnapshot(t, c))
	claude := uids["claude-a"]
	if got := readDisplay(t, c, snapshot, claude)[0]; !got.Cacheable {
		t.Fatalf("clean %+v", got)
	}
	inputs, err := SettingsInputPaths(paths.ConfigDir, displayProjectUID)
	if err != nil {
		t.Fatal(err)
	}
	expect := func(step string, cacheable bool) core.AgentSettingsResolution {
		t.Helper()
		got := readDisplay(t, c, snapshot, claude)[0]
		if got.Cacheable != cacheable || got.Unavailable != "" || !got.Layered || got.Reasons == nil {
			t.Fatalf("%s: %+v, want Cacheable %v with settings kept", step, got, cacheable)
		}
		return got
	}

	writeFixtureFile(t, inputs.ProjectLinkRulesFile, "{")
	expect("malformed link rules", false)
	writeFixtureFile(t, inputs.ProjectLinkRulesFile, `{"links":[]}`)
	expect("link rules recovered", true)

	policy, err := os.ReadFile(inputs.GuidancePolicyFile)
	if err != nil {
		t.Fatalf("first read did not materialize the policy: %v", err)
	}
	writeFixtureFile(t, inputs.GuidancePolicyFile, "{")
	if got := expect("malformed policy", false); slices.Contains(got.Reasons, agentsettings.ReasonGuidanceChanged) {
		t.Fatalf("unreadable guidance compared: %+v", got)
	}
	writeFixtureFile(t, inputs.GuidancePolicyFile, `{"disabled":true}`)
	if got := expect("guidance off", true); slices.Contains(got.Reasons, agentsettings.ReasonGuidanceChanged) {
		t.Fatalf("off guidance compared: %+v", got)
	}
	writeFixtureFile(t, inputs.GuidancePolicyFile, string(policy))

	current := (&runtime.GuidanceService{Paths: func() (config.Paths, error) { return paths, nil }}).LoadAgentGuidance()
	if current.Unavailable != nil || current.Digest == "" {
		t.Fatalf("guidance %+v", current)
	}
	original := snapshot
	snapshot = original.Clone()
	agentRow(t, &snapshot, claude).Metadata.Annotations = map[string]string{metadata.AnnotationAgentGuidanceDigest: current.Digest}
	if got := expect("recorded guidance", true); slices.Contains(got.Reasons, agentsettings.ReasonGuidanceChanged) {
		t.Fatalf("unchanged guidance reported: %+v", got)
	}
	writeFixtureFile(t, filepath.Join(inputs.GuidanceFragmentsDir, "extra.md"), "EXTRA")
	if got := expect("fragment added", true); !slices.Contains(got.Reasons, agentsettings.ReasonGuidanceChanged) {
		t.Fatalf("fragment did not change the guidance: %+v", got)
	}
	writeFixtureFile(t, inputs.GuidancePolicyFile, `{"disabled":false,"disabledFragments":["extra.md"]}`)
	if got := expect("fragment disabled", true); slices.Contains(got.Reasons, agentsettings.ReasonGuidanceChanged) {
		t.Fatalf("disabled fragment changed the guidance: %+v", got)
	}
	snapshot = original

	receipt, err := os.ReadFile(inputs.GuidanceMigrationReceiptFile)
	if err != nil {
		t.Fatalf("first read did not write the migration receipt: %v", err)
	}
	writeFixtureFile(t, inputs.GuidanceMigrationReceiptFile, "{")
	expect("malformed migration receipt", false)
	writeFixtureFile(t, inputs.GuidanceMigrationReceiptFile, string(receipt))
	expect("migration receipt recovered", true)

	snapshots := filepath.Join(paths.StateDir, agentguidance.DirName)
	if err := os.RemoveAll(snapshots); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, snapshots, "not a directory")
	expect("guidance snapshot write failure", false)
	if err := os.Remove(snapshots); err != nil {
		t.Fatal(err)
	}
	expect("guidance snapshot recovered", true)

	if err := os.Remove(filepath.Join(persona.NewDefaultStore(paths).Dir(), "reviewer"+persona.FileExt)); err != nil {
		t.Fatal(err)
	}
	if got := readDisplay(t, c, snapshot, uids["claude-b"])[0]; got.Cacheable || got.Unavailable != "" || !got.Layered {
		t.Fatalf("unreadable instructions: %+v", got)
	}
}

// A process Claude row whose instructions cannot be read keeps its settings
// for display, where ReadAgentSettings refuses it.
func TestReadAgentDisplaySettingsFromSnapshotProcessInstructionsError(t *testing.T) {
	c, paths, uids := settingsFixtureClient(t)
	process := uids["codex-process"]
	_, err := store.NewStore(store.PathFor(paths.StateDir)).Update(func(reg *metadata.Registry) error {
		agent, _ := reg.Agent(process)
		agent.Spec.Provider = "claude"
		agent.Metadata.Annotations = map[string]string{metadata.AnnotationAgentPersona: "gone"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	old, err := c.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{AgentUIDs: []string{process}})
	if err != nil || old.Agents[0].Unavailable != string(CodeRelaunchNoConversation) {
		t.Fatalf("ReadAgentSettings %+v %v", old, err)
	}
	got := readDisplay(t, c, displaySnapshot(t, c), process)[0]
	if got.Host != "process" || got.Unavailable != "" || !got.Layered || got.Cacheable {
		t.Fatalf("display %+v", got)
	}
}

func TestReadAgentDisplaySettingsFromSnapshotRefusals(t *testing.T) {
	c, _, _ := settingsFixtureClient(t)
	snapshot := displaySnapshot(t, c)
	var refusal *Refusal
	for _, uids := range [][]string{{""}, {"uid:agent-01"}} {
		if _, err := c.ReadAgentDisplaySettingsFromSnapshot(context.Background(), snapshot, core.ReadAgentSettingsRequest{AgentUIDs: uids}); !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid {
			t.Fatalf("%q: %v", uids, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ReadAgentDisplaySettingsFromSnapshot(ctx, snapshot, core.ReadAgentSettingsRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	empty, err := c.ReadAgentDisplaySettingsFromSnapshot(context.Background(), core.RegistrySnapshot{}, core.ReadAgentSettingsRequest{})
	if err != nil || empty.Agents == nil || len(empty.Agents) != 0 {
		t.Fatalf("empty snapshot %+v %v", empty, err)
	}
	before := snapshot.Clone()
	_ = readDisplay(t, c, snapshot)
	if !reflect.DeepEqual(before, snapshot) {
		t.Fatal("the read changed the caller's snapshot")
	}
}
