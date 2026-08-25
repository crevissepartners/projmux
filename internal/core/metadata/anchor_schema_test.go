package metadata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"testing"
)

type anchorSchemaFixture struct {
	registry       Registry
	windowUID      string
	otherWindowUID string
	shellUID       string
	otherShellUID  string
	agentUID       string
	agentPaneUID   string
}

func newAnchorSchemaFixture(t *testing.T) anchorSchemaFixture {
	t.Helper()
	m := testMutator(dirSet{"/src/projmux": true})
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatal(err)
	}
	first := registered.Windows[0]
	shellUID := first.Spec.DefaultShellPaneRef
	other, otherPanes, err := m.AddWindow(&reg, registered.Project.Metadata.UID,
		BootstrapWindow{Name: "other"}, "/bin/zsh", "op-other")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := m.CreateAgent(&reg, first.Metadata.UID,
		CreateAgentOptions{Provider: "codex", OperationID: "op-agent"})
	if err != nil {
		t.Fatal(err)
	}
	agentPane, err := m.AttachAgentPane(&reg, agent.Metadata.UID,
		BootstrapPane{Command: "codex", CWD: "/src/projmux"}, "op-agent-pane")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return anchorSchemaFixture{
		registry: reg, windowUID: first.Metadata.UID, otherWindowUID: other.Metadata.UID,
		shellUID: shellUID, otherShellUID: otherPanes[0].Metadata.UID,
		agentUID: agent.Metadata.UID, agentPaneUID: agentPane.Metadata.UID,
	}
}

func TestFinalV2AnchorAndDefaultShellValidationMatrix(t *testing.T) {
	t.Parallel()
	fixture := newAnchorSchemaFixture(t)
	tests := []struct {
		name    string
		mutate  func(*Registry)
		wantErr string
	}{
		{name: "shell anchor with default shell"},
		{name: "shell anchor with optional default empty", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.DefaultShellPaneRef = ""
		}},
		{name: "Agent anchor with default shell", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.AnchorPaneRef = fixture.agentPaneUID
		}},
		{name: "Agent anchor with optional default empty", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.AnchorPaneRef = fixture.agentPaneUID
			window.Spec.DefaultShellPaneRef = ""
		}},
		{name: "missing anchor", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.AnchorPaneRef = ""
		}, wantErr: `validate registry: window "zsh" has no anchorPaneRef`},
		{name: "dangling anchor", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.AnchorPaneRef = "pane-missing"
		}, wantErr: `validate registry: window "zsh" anchorPaneRef "pane-missing" does not exist`},
		{name: "cross Window anchor", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.AnchorPaneRef = fixture.otherShellUID
		}, wantErr: fmt.Sprintf(`validate registry: window "zsh" anchorPaneRef %q is not a same-Window shell or Agent Pane`, fixture.otherShellUID)},
		{name: "Agent anchor is not Agent paneRef", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.AnchorPaneRef = fixture.agentPaneUID
			agent, _ := r.Agent(fixture.agentUID)
			agent.Status.Phase = PhaseOffline
			agent.Status.PaneRef = ""
		}, wantErr: fmt.Sprintf(`validate registry: window "zsh" anchorPaneRef %q is not its Agent owner's managed Pane`, fixture.agentPaneUID)},
		{name: "dangling default shell", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.DefaultShellPaneRef = "pane-missing"
		}, wantErr: `validate registry: window "zsh" defaultShellPaneRef "pane-missing" does not exist`},
		{name: "Agent cannot be default shell", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.DefaultShellPaneRef = fixture.agentPaneUID
		}, wantErr: fmt.Sprintf(`validate registry: window "zsh" defaultShellPaneRef %q is not a direct Window-owned shell Pane`, fixture.agentPaneUID)},
		{name: "cross Window default shell", mutate: func(r *Registry) {
			window, _ := r.Window(fixture.windowUID)
			window.Spec.DefaultShellPaneRef = fixture.otherShellUID
		}, wantErr: fmt.Sprintf(`validate registry: window "zsh" defaultShellPaneRef %q is not a direct Window-owned shell Pane`, fixture.otherShellUID)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := fixture.registry.Clone()
			if tt.mutate != nil {
				tt.mutate(&reg)
			}
			err := reg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Validate error = %q, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestEnsureWindowDefaultShellAdoptsOrAllocatesWithoutReplacingAgentAnchor(t *testing.T) {
	for _, test := range []struct {
		name        string
		removeShell bool
		wantCreated bool
	}{
		{name: "adopt existing direct shell"},
		{name: "allocate for Agent-only Window", removeShell: true, wantCreated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAnchorSchemaFixture(t)
			reg := fixture.registry.Clone()
			window, _ := reg.Window(fixture.windowUID)
			window.Spec.AnchorPaneRef = fixture.agentPaneUID
			window.Spec.DefaultShellPaneRef = ""
			if test.removeShell {
				reg.Panes = slices.DeleteFunc(reg.Panes, func(pane Pane) bool { return pane.Metadata.UID == fixture.shellUID })
				reg.NameReservations = slices.DeleteFunc(reg.NameReservations, func(reservation NameReservation) bool {
					return reservation.UID == fixture.shellUID
				})
			}

			pane, created, err := testMutator(dirSet{"/src/projmux": true}).EnsureWindowDefaultShell(&reg, fixture.windowUID, "/bin/zsh", "op-default")
			if err != nil {
				t.Fatal(err)
			}
			if created != test.wantCreated || pane.Spec.Role != PaneRoleShell || pane.Metadata.OwnerUID() != fixture.windowUID {
				t.Fatalf("default shell = %+v, created=%t", pane, created)
			}
			window, _ = reg.Window(fixture.windowUID)
			if window.Spec.AnchorPaneRef != fixture.agentPaneUID || window.Spec.DefaultShellPaneRef != pane.Metadata.UID {
				t.Fatalf("Window refs = anchor %q default %q", window.Spec.AnchorPaneRef, window.Spec.DefaultShellPaneRef)
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("post default-shell graph: %v", err)
			}
		})
	}
}

func TestRebindAgentPanePreservesWindowAnchorAndPaneIdentity(t *testing.T) {
	fixture := newAnchorSchemaFixture(t)
	reg := fixture.registry.Clone()
	window, _ := reg.Window(fixture.windowUID)
	window.Spec.AnchorPaneRef = fixture.agentPaneUID
	window.Spec.DefaultShellPaneRef = ""
	agent, _ := reg.Agent(fixture.agentUID)
	agent.Status.Phase = PhaseOffline
	agent.Status.PaneRef = ""

	pane, err := testMutator(dirSet{"/src/projmux": true}).RebindAgentPane(&reg, fixture.agentUID, fixture.agentPaneUID)
	if err != nil {
		t.Fatal(err)
	}
	agent, _ = reg.Agent(fixture.agentUID)
	window, _ = reg.Window(fixture.windowUID)
	if pane.Metadata.UID != fixture.agentPaneUID || agent.Status.PaneRef != fixture.agentPaneUID ||
		agent.Status.Phase != PhaseRunning || window.Spec.AnchorPaneRef != fixture.agentPaneUID {
		t.Fatalf("rebind changed identity: pane=%s agent=%+v window=%+v", pane.Metadata.UID, agent.Status, window.Spec)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("post rebind graph: %v", err)
	}
}

func TestSchemaVersion2WindowShapeNormalizationTable(t *testing.T) {
	t.Parallel()
	fixture := newAnchorSchemaFixture(t)
	finalBytes := mustMarshalRegistry(t, fixture.registry)
	tests := []struct {
		name      string
		shape     string
		wantRan   bool
		wantError string
	}{
		{name: "final v2", shape: "final"},
		{name: "intermediate v2", shape: "legacy", wantRan: true},
		{name: "mixed within Window", shape: "mixed-window", wantError: "mixes legacy primaryPaneRef"},
		{name: "mixed across Windows", shape: "mixed-registry", wantError: "registry mixes legacy primaryPaneRef"},
		{name: "missing authority", shape: "missing", wantError: "has neither legacy primaryPaneRef nor required final anchorPaneRef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sourceBytes := rewriteWindowShape(t, finalBytes, tt.shape)
			var source Registry
			if err := json.Unmarshal(sourceBytes, &source); err != nil {
				t.Fatal(err)
			}
			before := source.Clone()
			got, ran, report, err := MigrateRegistryWithEnvironment(nil, source, MigrationEnvironment{})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				if !bytes.Equal(mustMarshalRegistry(t, source), mustMarshalRegistry(t, before)) {
					t.Fatal("failed shape classification mutated its input")
				}
				return
			}
			if err != nil || ran != tt.wantRan {
				t.Fatalf("normalize = ran:%t report:%s err:%v", ran, report.String(), err)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("normalized Registry: %v", err)
			}
			encoded := mustMarshalRegistry(t, got)
			if bytes.Contains(encoded, []byte(`"primaryPaneRef"`)) {
				t.Fatalf("final writer emitted legacy authority:\n%s", encoded)
			}
			if tt.shape == "legacy" {
				for _, window := range got.Windows {
					if window.Spec.AnchorPaneRef != window.Spec.DefaultShellPaneRef {
						t.Fatalf("normalized refs differ: %+v", window.Spec)
					}
				}
			}
			again, ranAgain, secondReport, err := MigrateRegistryWithEnvironment(nil, got, MigrationEnvironment{})
			if err != nil || ranAgain || len(secondReport.Repairs) != 0 || !bytes.Equal(mustMarshalRegistry(t, again), encoded) {
				t.Fatalf("second pass changed final v2: ran=%t report=%s err=%v", ranAgain, secondReport.String(), err)
			}
		})
	}
}

func TestIntermediateV2NormalizationPreservesIdentityOwnerAndSessionRefs(t *testing.T) {
	t.Parallel()
	want, err := os.ReadFile("testdata/registry-v011-v2-bytes.golden")
	if err != nil {
		t.Fatal(err)
	}
	intermediate := rewriteWindowShape(t, want, "legacy")
	var source Registry
	if err := json.Unmarshal(intermediate, &source); err != nil {
		t.Fatal(err)
	}
	before := source.Clone()
	normalized, ran, report, err := MigrateRegistryWithEnvironment(nil, source, MigrationEnvironment{})
	if err != nil || !ran || len(report.Repairs) != 1 {
		t.Fatalf("normalize = ran:%t report:%s err:%v", ran, report.String(), err)
	}
	assertExistingIdentityAndAgentPointersPreserved(t, before, normalized)
	got := []byte(mustJSON(t, normalized) + "\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("identity/sessionRef-bearing normalization changed non-shape bytes:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestIntermediateV2ReaderFailsClosedOnFinalV2WithoutProducingBytes(t *testing.T) {
	t.Parallel()
	finalBytes, err := os.ReadFile("testdata/registry-v010-v2-bytes.golden")
	if err != nil {
		t.Fatal(err)
	}
	type intermediateWindow struct {
		Spec struct {
			PrimaryPaneRef string `json:"primaryPaneRef"`
		} `json:"spec"`
	}
	type intermediateRegistry struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Windows       []intermediateWindow `json:"windows"`
	}
	var old intermediateRegistry
	if err := json.Unmarshal(finalBytes, &old); err != nil {
		t.Fatal(err)
	}
	if old.SchemaVersion != SchemaVersion || len(old.Windows) != 1 {
		t.Fatalf("old reader envelope = %+v", old)
	}
	for _, window := range old.Windows {
		if strings.TrimSpace(window.Spec.PrimaryPaneRef) != "" {
			t.Fatalf("old reader unexpectedly found legacy authority %q", window.Spec.PrimaryPaneRef)
		}
	}
	// The matching pre-release validator requires primaryPaneRef, so it fails
	// before any writer is reachable. Keeping the exact source slice untouched
	// models the binary's zero-write refusal; the installed-binary smoke repeats
	// this contract against the actual pre-release executable.
	before := bytes.Clone(finalBytes)
	if !bytes.Equal(before, finalBytes) {
		t.Fatal("old-reader refusal changed final-v2 source bytes")
	}
}

func TestRandomValidFinalV2GraphOrderingIsDeterministicAndIdempotent(t *testing.T) {
	t.Parallel()
	seed := int64(20260824)
	random := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic property input.
	for iteration := range 128 {
		fixture := newAnchorSchemaFixture(t)
		reg := fixture.registry.Clone()
		window, _ := reg.Window(fixture.windowUID)
		if random.Intn(2) == 1 {
			window.Spec.AnchorPaneRef = fixture.agentPaneUID
		}
		if random.Intn(2) == 1 {
			window.Spec.DefaultShellPaneRef = ""
		}
		random.Shuffle(len(reg.Windows), func(i, j int) { reg.Windows[i], reg.Windows[j] = reg.Windows[j], reg.Windows[i] })
		random.Shuffle(len(reg.Panes), func(i, j int) { reg.Panes[i], reg.Panes[j] = reg.Panes[j], reg.Panes[i] })
		if err := reg.Validate(); err != nil {
			t.Fatalf("seed=%d iteration=%d fixture invalid: %v", seed, iteration, err)
		}
		raw := mustMarshalRegistry(t, reg)
		var decoded Registry
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		first, ran, _, err := MigrateRegistryWithEnvironment(nil, decoded, MigrationEnvironment{})
		if err != nil || ran {
			t.Fatalf("seed=%d iteration=%d first pass ran=%t err=%v", seed, iteration, ran, err)
		}
		second, ran, _, err := MigrateRegistryWithEnvironment(nil, first, MigrationEnvironment{})
		if err != nil || ran || !bytes.Equal(mustMarshalRegistry(t, first), mustMarshalRegistry(t, second)) {
			t.Fatalf("seed=%d iteration=%d second pass ran=%t err=%v", seed, iteration, ran, err)
		}
	}
}

func mustMarshalRegistry(t *testing.T, registry Registry) []byte {
	t.Helper()
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func rewriteWindowShape(t *testing.T, source []byte, shape string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(source, &document); err != nil {
		t.Fatal(err)
	}
	windows := document["windows"].([]any)
	for index, item := range windows {
		spec := item.(map[string]any)["spec"].(map[string]any)
		anchor := spec["anchorPaneRef"]
		switch shape {
		case "final":
		case "legacy":
			delete(spec, "anchorPaneRef")
			delete(spec, "defaultShellPaneRef")
			spec["primaryPaneRef"] = anchor
		case "mixed-window":
			if index == 0 {
				spec["primaryPaneRef"] = anchor
			}
		case "mixed-registry":
			if index == 0 {
				delete(spec, "anchorPaneRef")
				delete(spec, "defaultShellPaneRef")
				spec["primaryPaneRef"] = anchor
			}
		case "missing":
			if index == 0 {
				delete(spec, "anchorPaneRef")
				delete(spec, "defaultShellPaneRef")
			}
		default:
			t.Fatalf("unknown shape %q", shape)
		}
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
