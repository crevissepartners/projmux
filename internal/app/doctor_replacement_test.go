package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// replacementCase is one complete set of layer evidence and the verdict the
// contract fixes for it.
type replacementCase struct {
	name        string
	inputs      doctorReplacementInputs
	layer       string
	replacement string
	restoration string
	reason      string
}

func replacementQualified() *codexgeneration.QualificationResult {
	return &codexgeneration.QualificationResult{Verdict: codexgeneration.VerdictYes, Reason: codexgeneration.ReasonQualified}
}

// replacementFreshResidualVintage is a fleet an install has just left behind:
// residual processes whose ages are all inside the drain cutoff, so the drain
// is still in progress rather than over its bound.
func replacementFreshResidualVintage() projmuxProcessVintage {
	return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
		{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{412}},
		{Role: projmuxProcessRoleSupervisor, Processes: 3, Current: 1, Replaced: 2, ReplacedAgeSeconds: []int{237, 2019}},
	}}
}

// replacementResidualVintage is this repository's own fleet as one census read
// it: five residual supervisors, the operator's attached session, and an
// unnamed remainder, with the oldest at 606482 seconds. Every age past 86400
// is over the adopted cutoff, which is why this fixture selects the cutoff
// token rather than the plain residual one.
func replacementResidualVintage() projmuxProcessVintage {
	return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
		{Role: projmuxProcessRoleSupervisor, Processes: 5, Replaced: 5, ReplacedAgeSeconds: []int{6984, 9049, 232277, 583716, 583761}},
		{Role: projmuxProcessRoleSessionClient, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{606482}},
		{Role: projmuxProcessRoleAgentEndpoint, Processes: 3, Current: 1, Replaced: 2, ReplacedAgeSeconds: []int{237, 2019}},
		{Role: projmuxProcessRoleOther, Processes: 4, Replaced: 4, ReplacedAgeSeconds: []int{2588, 6979, 323774, 583799}},
	}}
}

// replacementCases enumerates every reachable combination, one per reason
// token. The set is exhaustive by construction: the vocabulary test below
// requires the union of the tokens produced here to equal the published
// inventory, so a token added to the code without a case here fails.
func replacementCases() []replacementCase {
	live := doctorProviderSessionCensus{Observed: 1, Running: 2, Live: 2}
	return []replacementCase{
		{
			name:        "L1 platform without an executable link",
			inputs:      doctorReplacementInputs{},
			layer:       doctorReplacementLayerImage,
			replacement: doctorReplacementUnknown, restoration: doctorRestorationUnknown,
			reason: doctorReplacementReasonUnsupportedPlatform,
		},
		{
			name:        "L1 image that does not resolve back to its executable path",
			inputs:      doctorReplacementInputs{Image: doctorInstalledImage{Supported: true}},
			layer:       doctorReplacementLayerImage,
			replacement: doctorReplacementUnknown, restoration: doctorRestorationUnknown,
			reason: doctorReplacementReasonImageUnresolved,
		},
		{
			name:        "L1 image unlinked under this reader",
			inputs:      doctorReplacementInputs{Image: doctorInstalledImage{Supported: true, Resolved: true, Unlinked: true}},
			layer:       doctorReplacementLayerImage,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationNotRestorable,
			reason: doctorReplacementReasonImageUnlinked,
		},
		{
			name:        "L1 installed path still publishes this image",
			inputs:      doctorReplacementInputs{Image: doctorInstalledImage{Supported: true, Resolved: true}},
			layer:       doctorReplacementLayerImage,
			replacement: doctorReplacementReplaced, restoration: doctorRestorationNotRestorable,
			reason: doctorReplacementReasonImageCurrent,
		},
		{
			name:        "L2 platform without a process table",
			inputs:      doctorReplacementInputs{},
			layer:       doctorReplacementLayerProcesses,
			replacement: doctorReplacementUnknown, restoration: doctorRestorationUnknown,
			reason: doctorReplacementReasonUnsupportedPlatform,
		},
		{
			name:        "L2 install left live children on the old image",
			inputs:      doctorReplacementInputs{Processes: replacementFreshResidualVintage()},
			layer:       doctorReplacementLayerProcesses,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationRestorable,
			reason: doctorReplacementReasonResidualProcesses,
		},
		{
			// The same fleet after the drain cutoff has passed. The verdict
			// does not soften and the processes are not ended: the row states
			// that this replacement is not going to complete, and restoration
			// stays `restorable` because the route that ends and relaunches
			// them is the one it always was.
			name:        "L2 a residual process outlived the drain cutoff",
			inputs:      doctorReplacementInputs{Processes: replacementResidualVintage()},
			layer:       doctorReplacementLayerProcesses,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationRestorable,
			reason: doctorReplacementReasonCutoffReached,
		},
		{
			name: "L2 every observed child runs the installed image",
			inputs: doctorReplacementInputs{Processes: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
				{Role: projmuxProcessRoleSupervisor, Processes: 2, Current: 2},
			}}},
			layer:       doctorReplacementLayerProcesses,
			replacement: doctorReplacementReplaced, restoration: doctorRestorationRestorable,
			reason: doctorReplacementReasonNoResidual,
		},
		{
			name: "L2 no live child observable and the ledger recorded residue",
			inputs: doctorReplacementInputs{
				Processes:      projmuxProcessVintage{Supported: true},
				Residue:        installResidueRecord{Installer: "make", Supported: true, Observed: 9, Replaced: 9},
				ResidueRecords: 214,
				ResidueOK:      true,
			},
			layer:       doctorReplacementLayerProcesses,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationRestorable,
			reason: doctorReplacementReasonLedgerResidue,
		},
		{
			name: "L2 no live child observable and the ledger recorded none",
			inputs: doctorReplacementInputs{
				Processes:      projmuxProcessVintage{Supported: true},
				Residue:        installResidueRecord{Installer: "npm", Supported: true, Observed: 4},
				ResidueRecords: 3,
				ResidueOK:      true,
			},
			layer:       doctorReplacementLayerProcesses,
			replacement: doctorReplacementReplaced, restoration: doctorRestorationRestorable,
			reason: doctorReplacementReasonLedgerClean,
		},
		{
			name: "L2 neither the live census nor an informative ledger record said anything",
			inputs: doctorReplacementInputs{
				Processes: projmuxProcessVintage{Supported: true},
				// A ledger whose newest records are silent censuses. The
				// reader finds nothing informative in it, and the row keeps
				// the length so the gap stays recoverable.
				ResidueRecords: 194,
			},
			layer:       doctorReplacementLayerProcesses,
			replacement: doctorReplacementUnknown, restoration: doctorRestorationUnknown,
			reason: doctorReplacementReasonNoObservedProcess,
		},
		{
			name:        "L3 pool diagnosis was never read",
			inputs:      doctorReplacementInputs{},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementUnknown, restoration: doctorRestorationUnknown,
			reason: doctorReplacementReasonPoolUnobserved,
		},
		{
			name: "L3 provider evidence contradicts a Registry Running Agent",
			inputs: doctorReplacementInputs{
				Pool:     &doctorCodexGenerationPool{Status: "ready", Reason: "qualified", Qualification: replacementQualified()},
				Sessions: doctorProviderSessionCensus{Observed: 1, Running: 4, Live: 1, Dead: 3},
			},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationNotRestorable,
			reason: doctorReplacementReasonSessionDead,
		},
		{
			name: "L3 pool installed with no qualification result",
			inputs: doctorReplacementInputs{
				Pool: &doctorCodexGenerationPool{
					Status: "blocked", Reason: "qualification-missing",
					Action: "run-isolated-version-pair-qualification",
					Generations: []doctorCodexGeneration{
						{GenerationID: "codex-0.153.2", State: codexgeneration.StateDraining},
						{GenerationID: "codex-0.153.4", State: codexgeneration.StateCurrent},
					},
				},
				Sessions: live,
			},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementReplaced, restoration: doctorRestorationNotRestorable,
			reason: doctorReplacementReasonQualification,
		},
		{
			name: "L3 pool diagnosis blocked for a reason other than qualification",
			inputs: doctorReplacementInputs{
				Pool: &doctorCodexGenerationPool{
					Status: "blocked", Reason: "bundle-drift", Action: "restore-bundle",
					Qualification: replacementQualified(),
				},
				Sessions: live,
			},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationNotRestorable,
			reason: doctorReplacementReasonPoolBlocked,
		},
		{
			name: "L3 pending operation with a handover route",
			inputs: doctorReplacementInputs{
				Pool: &doctorCodexGenerationPool{
					Status: "action-required", Reason: "handover-required",
					Qualification: replacementQualified(),
					Generations: []doctorCodexGeneration{
						{GenerationID: "codex-0.153.2", State: codexgeneration.StateHandoverPending},
					},
				},
				Sessions: live,
			},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementReplaced, restoration: doctorRestorationRestorable,
			reason: doctorReplacementReasonHandoverRequired,
		},
		{
			name: "L3 Running Agents resting on the Registry alone",
			inputs: doctorReplacementInputs{
				Pool:     &doctorCodexGenerationPool{Status: "ready", Reason: "qualified", Qualification: replacementQualified()},
				Sessions: doctorProviderSessionCensus{Observed: 1, Running: 3, Live: 2, Unobservable: 1},
			},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationUnknown,
			reason: doctorReplacementReasonSessionUnobserved,
		},
		{
			name: "L3 no generation journal exists",
			inputs: doctorReplacementInputs{
				Pool:     &doctorCodexGenerationPool{Status: "absent", Reason: "generation-pool-not-installed"},
				Sessions: live,
			},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationUnknown,
			reason: doctorReplacementReasonPoolNotInstalled,
		},
		{
			name: "L3 pool installed qualified and settled",
			inputs: doctorReplacementInputs{
				Pool:     &doctorCodexGenerationPool{Status: "ready", Reason: "qualified", Qualification: replacementQualified()},
				Sessions: live,
			},
			layer:       doctorReplacementLayerProvider,
			replacement: doctorReplacementNotReplaced, restoration: doctorRestorationRestorable,
			reason: doctorReplacementReasonPoolReady,
		},
	}
}

func replacementRow(t *testing.T, report doctorReplacementReport, layer string) doctorReplacementRow {
	t.Helper()
	for _, row := range report.Rows {
		if row.Layer == layer {
			return row
		}
	}
	t.Fatalf("replacement table has no %s row: %+v", layer, report.Rows)
	return doctorReplacementRow{}
}

// TestDoctorReplacementLayerVerdictsAreFixedByInputCombination pins both axes
// and the governing token of every reachable evidence combination.
func TestDoctorReplacementLayerVerdictsAreFixedByInputCombination(t *testing.T) {
	t.Parallel()

	for _, tc := range replacementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			row := replacementRow(t, projectDoctorReplacement(tc.inputs), tc.layer)
			if row.Replacement != tc.replacement || row.Restoration != tc.restoration || row.Reason != tc.reason {
				t.Fatalf("%s = replacement %q restoration %q reason %q, want %q/%q/%q",
					tc.layer, row.Replacement, row.Restoration, row.Reason, tc.replacement, tc.restoration, tc.reason)
			}
			if !slices.Contains(doctorReplacementLayerReasons[tc.layer], row.Reason) {
				t.Fatalf("%s emitted %q, which is not in its published token list", tc.layer, row.Reason)
			}
		})
	}
}

// TestDoctorReplacementUnknownVerdictsNameTheEvidenceGap requires every
// `unknown` on either axis to come with a token that says which evidence was
// missing, rather than collapsing several distinct gaps into one word.
func TestDoctorReplacementUnknownVerdictsNameTheEvidenceGap(t *testing.T) {
	t.Parallel()

	// The closed set of gaps this surface is allowed to answer `unknown` for.
	// Each entry is a reading that was silent, never a reading that said the
	// layer is clean.
	gaps := []string{
		doctorReplacementReasonUnsupportedPlatform,
		doctorReplacementReasonImageUnresolved,
		doctorReplacementReasonNoObservedProcess,
		doctorReplacementReasonPoolUnobserved,
		doctorReplacementReasonSessionUnobserved,
		doctorReplacementReasonPoolNotInstalled,
	}
	seen := map[string]bool{}
	for _, tc := range replacementCases() {
		row := replacementRow(t, projectDoctorReplacement(tc.inputs), tc.layer)
		if row.Replacement != doctorReplacementUnknown && row.Restoration != doctorRestorationUnknown {
			continue
		}
		if !slices.Contains(gaps, row.Reason) {
			t.Fatalf("%s answered unknown under %q, which names no evidence gap", tc.layer, row.Reason)
		}
		seen[row.Reason] = true
	}
	for _, gap := range gaps {
		if !seen[gap] {
			t.Fatalf("published evidence gap %q is unreachable from any case", gap)
		}
	}
}

// TestDoctorReplacementEveryReasonTokenCarriesARecoverableDiscriminant is the
// negative test C-3 requires: zero rows with a token and nothing to recover the
// verdict from.
func TestDoctorReplacementEveryReasonTokenCarriesARecoverableDiscriminant(t *testing.T) {
	t.Parallel()

	tokenless := []string{}
	for _, tc := range replacementCases() {
		for _, row := range projectDoctorReplacement(tc.inputs).Rows {
			if row.Reason == "" {
				t.Fatalf("%s produced a row with no reason token: %+v", tc.name, row)
			}
			if len(row.Signals) == 0 {
				tokenless = append(tokenless, fmt.Sprintf("%s/%s=%s", tc.name, row.Layer, row.Reason))
			}
		}
	}
	if len(tokenless) != 0 {
		t.Fatalf("rows carrying a token with no discriminant = %d, want 0: %v", len(tokenless), tokenless)
	}
}

// TestDoctorReplacementSignalValuesCarryNoPathProcessOrFreeFormText holds the
// serialization rule the contract publishes: the discriminant is in JSON only
// because every value is a counter or a closed token.
func TestDoctorReplacementSignalValuesCarryNoPathProcessOrFreeFormText(t *testing.T) {
	t.Parallel()

	for _, tc := range replacementCases() {
		for _, row := range projectDoctorReplacement(tc.inputs).Rows {
			for _, signal := range row.Signals {
				if !slices.Contains(doctorReplacementSignalInventory, signal.Key) {
					t.Fatalf("%s emitted signal key %q outside the published inventory", tc.name, signal.Key)
				}
				if !doctorReplacementSafeValue.MatchString(signal.Value) {
					t.Fatalf("%s emitted signal %s=%q, which is not a counter or closed token", tc.name, signal.Key, signal.Value)
				}
				if strings.ContainsAny(signal.Value, "/\\ ") {
					t.Fatalf("%s emitted signal %s=%q, which could quote a path or an argv word", tc.name, signal.Key, signal.Value)
				}
			}
		}
	}

	// A hostile value never reaches a serialized surface, and the row still
	// keeps a discriminant because the key names which evidence decided it.
	signals := doctorReplacementSignals(doctorReplacementSignalPoolReason, "/home/someone/.local/state/projmux/registry.json")
	if len(signals) != 1 || signals[0].Value != doctorReplacementUnclassified {
		t.Fatalf("path-shaped signal value = %+v, want a single %q", signals, doctorReplacementUnclassified)
	}
}

// TestDoctorReplacementDarwinReportsUnsupportedPlatformForImageAndProcessLayers
// holds the platform branch without a device: `codex_controlplane_vintage_darwin.go`
// is the path that reports no readable process table, and both layers that read
// it must answer unknown rather than imply currency.
func TestDoctorReplacementDarwinReportsUnsupportedPlatformForImageAndProcessLayers(t *testing.T) {
	t.Parallel()

	images, supported := []codexProcessImage(nil), false
	report := projectDoctorReplacement(doctorReplacementInputs{
		Image:     projectDoctorInstalledImage("/usr/local/bin/projmux", 4242, images, supported),
		Processes: projectProjmuxProcessVintage("/usr/local/bin/projmux", 4242, images, supported),
	})
	for _, layer := range []string{doctorReplacementLayerImage, doctorReplacementLayerProcesses} {
		row := replacementRow(t, report, layer)
		if row.Reason != doctorReplacementReasonUnsupportedPlatform {
			t.Fatalf("%s reason = %q, want %q", layer, row.Reason, doctorReplacementReasonUnsupportedPlatform)
		}
		if row.Replacement != doctorReplacementUnknown || row.Restoration != doctorRestorationUnknown {
			t.Fatalf("%s = %q/%q, want unknown on both axes", layer, row.Replacement, row.Restoration)
		}
		if len(row.Signals) == 0 {
			t.Fatalf("%s reported unsupported-platform with no discriminant", layer)
		}
	}
}

// TestDoctorReplacementQualificationMissingMakesGenerationPoolNotRestorable is
// the acceptance mapping for C-1's L3 Guarantee, fixed against a fixture rather
// than against whatever this machine's pool happens to hold.
func TestDoctorReplacementQualificationMissingMakesGenerationPoolNotRestorable(t *testing.T) {
	t.Parallel()

	pool := &doctorCodexGenerationPool{
		Status: "blocked", Reason: "qualification-missing",
		Action: "run-isolated-version-pair-qualification",
		Generations: []doctorCodexGeneration{
			{GenerationID: "codex-0.153.2", State: codexgeneration.StateDraining},
		},
	}
	row := projectDoctorReplacementProviderRow(pool, doctorProviderSessionCensus{Observed: 1, Running: 1, Live: 1})
	if row.Reason != doctorReplacementReasonQualification {
		t.Fatalf("reason = %q, want %q", row.Reason, doctorReplacementReasonQualification)
	}
	if row.Restoration != doctorRestorationNotRestorable {
		t.Fatalf("restoration = %q, want %q", row.Restoration, doctorRestorationNotRestorable)
	}
	if row.Replacement != doctorReplacementReplaced {
		t.Fatalf("replacement = %q, want %q for a draining generation", row.Replacement, doctorReplacementReplaced)
	}
}

// replacementAgent builds one Running Agent and the Pane its activation lives
// on. It is deliberately a literal rather than a mutator sequence: the shape
// under test is a Registry that says Running while the provider session behind
// it is gone, and the mutators exist to prevent exactly that state.
func replacementAgent(uid, paneUID, provider string, activation coremetadata.PaneActivation) (coremetadata.Agent, coremetadata.Pane) {
	agent := coremetadata.Agent{
		Kind:     coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{UID: uid},
		Spec:     coremetadata.AgentSpec{Provider: provider},
		Status:   coremetadata.AgentStatus{Phase: coremetadata.PhaseRunning, PaneRef: paneUID},
	}
	activation.AgentUID = uid
	pane := coremetadata.Pane{
		Kind:     coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent},
		Status:   coremetadata.PaneStatus{Activation: activation},
	}
	return agent, pane
}

// TestDoctorReplacementCensusSeparatesRegistryRunningFromLiveProviderSession is
// the fixture the 2026-09-08 sample is reproduced from.
//
// It holds the structural point the live sample made: the discriminant differs
// by provider. A Claude Pane carries a real process handle and its liveness is
// a direct question about a pid. A Codex Pane carries no pid at all, so the
// only checkable fact is whether its composite authority still belongs to the
// runtime serving the state domain. A Running Agent with neither is counted as
// unobservable, never as live.
func TestDoctorReplacementCensusSeparatesRegistryRunningFromLiveProviderSession(t *testing.T) {
	t.Parallel()

	const liveRuntime = "81d61d6353e37fc1fab4e7809237f77a"
	authority := func(runtime string) *coremetadata.CodexActivationBinding {
		return &coremetadata.CodexActivationBinding{
			ThreadID: "01a07d26-842b-76b1-9c0d-3f54d1b015d7",
			Authority: &coremetadata.CodexAuthorityRef{
				StateDomainID:        "codex-state-0b0135c6d415d91ed688278589900bb5",
				EndpointGenerationID: "codex-0.153.2",
				BrokerRuntimeID:      runtime,
				ConnectionEpoch:      1,
				BindingEpoch:         2,
			},
		}
	}
	claudeProcess := func(pid int) *coremetadata.ClaudeActivationBinding {
		return &coremetadata.ClaudeActivationBinding{
			Process: coremetadata.ProcessIdentity{PID: pid, OwnerUID: 1000, Start: "boot-1:4242"},
		}
	}

	registry := coremetadata.NewRegistry()
	add := func(agent coremetadata.Agent, pane coremetadata.Pane) {
		registry.Agents = append(registry.Agents, agent)
		registry.Panes = append(registry.Panes, pane)
	}
	add(replacementAgent("agent-claude-live", "pane-claude-live", aiModeClaude,
		coremetadata.PaneActivation{Generation: "g1", Claude: claudeProcess(101)}))
	add(replacementAgent("agent-claude-dead", "pane-claude-dead", aiModeClaude,
		coremetadata.PaneActivation{Generation: "g1", Claude: claudeProcess(102)}))
	add(replacementAgent("agent-codex-live", "pane-codex-live", aiModeCodex,
		coremetadata.PaneActivation{Generation: "g1", Codex: authority(liveRuntime)}))
	add(replacementAgent("agent-codex-foreign", "pane-codex-foreign", aiModeCodex,
		coremetadata.PaneActivation{Generation: "g1", Codex: authority("2f0f6ab6e2c04d2f9a34c7bb8e5d1c77")}))
	add(replacementAgent("agent-codex-bare", "pane-codex-bare", aiModeCodex,
		coremetadata.PaneActivation{Generation: "g1"}))
	// A Running Agent whose Pane record is gone entirely.
	registry.Agents = append(registry.Agents, coremetadata.Agent{
		Kind:     coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{UID: "agent-orphan"},
		Spec:     coremetadata.AgentSpec{Provider: aiModeCodex},
		Status:   coremetadata.AgentStatus{Phase: coremetadata.PhaseRunning, PaneRef: "pane-gone"},
	})
	// An Offline Agent is not part of the question and must not be counted.
	registry.Agents = append(registry.Agents, coremetadata.Agent{
		Kind:     coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{UID: "agent-offline"},
		Spec:     coremetadata.AgentSpec{Provider: aiModeClaude},
		Status:   coremetadata.AgentStatus{Phase: coremetadata.PhaseOffline},
	})

	census := censusDoctorProviderSessions(registry, doctorProviderSessionProbe{
		ProcessAlive:    func(process coremetadata.ProcessIdentity) bool { return process.PID == 101 },
		BrokerRuntimeID: liveRuntime,
	})
	want := doctorProviderSessionCensus{Observed: 1, Running: 6, Live: 2, Dead: 2, Unobservable: 2}
	if census != want {
		t.Fatalf("census = %+v, want %+v", census, want)
	}

	row := projectDoctorReplacementProviderRow(
		&doctorCodexGenerationPool{Status: "absent", Reason: "generation-pool-not-installed"}, census)
	if row.Reason != doctorReplacementReasonSessionDead {
		t.Fatalf("reason = %q, want the dedicated mismatch token %q", row.Reason, doctorReplacementReasonSessionDead)
	}
	if row.Restoration != doctorRestorationNotRestorable {
		t.Fatalf("restoration = %q, want %q", row.Restoration, doctorRestorationNotRestorable)
	}
	// Count-only: no Agent UID, Pane UID, pid, or thread id reaches the row.
	rendered, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"agent-", "pane-", "01a07d26", liveRuntime, "101", "102"} {
		if bytes.Contains(rendered, []byte(secret)) {
			t.Fatalf("row names %q: %s", secret, rendered)
		}
	}

	// With no live runtime observed, a Codex binding is unobservable rather
	// than foreign: a reader that reached no runtime cannot tell a stale
	// binding from a live one.
	unreached := censusDoctorProviderSessions(registry, doctorProviderSessionProbe{
		ProcessAlive: func(process coremetadata.ProcessIdentity) bool { return process.PID == 101 },
	})
	if unreached.Dead != 1 || unreached.Unobservable != 4 {
		t.Fatalf("census with no observed runtime = %+v, want the Codex bindings unobservable", unreached)
	}
}

// TestDoctorReplacementReasonTokensMatchTheContractDocument holds the published
// vocabulary and the code constants equal in both directions.
func TestDoctorReplacementReasonTokensMatchTheContractDocument(t *testing.T) {
	t.Parallel()

	documented := replacementDocumentedTokens(t)
	for layer, want := range doctorReplacementLayerReasons {
		got := documented[layer]
		sorted := append([]string(nil), want...)
		sort.Strings(sorted)
		sort.Strings(got)
		if !slices.Equal(sorted, got) {
			t.Fatalf("%s tokens: docs/replacement-contract.md has %v, code has %v", layer, got, sorted)
		}
	}
	for layer := range documented {
		if _, ok := doctorReplacementLayerReasons[layer]; !ok {
			t.Fatalf("docs/replacement-contract.md publishes layer %q that the code has no vocabulary for", layer)
		}
	}

	// Every published token must also be reachable, so the document cannot
	// promise a verdict no evidence produces.
	reachable := map[string]bool{}
	for _, tc := range replacementCases() {
		for _, row := range projectDoctorReplacement(tc.inputs).Rows {
			reachable[row.Layer+"/"+row.Reason] = true
		}
	}
	for layer, tokens := range doctorReplacementLayerReasons {
		for _, token := range tokens {
			if !reachable[layer+"/"+token] {
				t.Fatalf("published token %s/%s is not reachable from any evidence combination", layer, token)
			}
		}
	}
}

var (
	replacementDocRow       = regexp.MustCompile("^\\| `(L[123])` \\| `([a-z0-9-]+)` \\|")
	replacementDocSignalRow = regexp.MustCompile("^\\| `([a-z0-9.-]+)` \\| `L")
	replacementDocRoleRow   = regexp.MustCompile("^\\| `([a-z-]+)` \\| ")
)

// TestDoctorReplacementSignalKeysMatchTheContractDocument holds the published
// signal-key inventory and the code constants equal. The keys are what a
// support-report allowlist tracks, so a key that exists in only one of the two
// places is a key nothing tracks.
func TestDoctorReplacementSignalKeysMatchTheContractDocument(t *testing.T) {
	t.Parallel()

	body := readRepoText(t, "docs/replacement-contract.md")
	_, after, ok := strings.Cut(body, "## Signal key inventory")
	if !ok {
		t.Fatal("docs/replacement-contract.md has no `## Signal key inventory` section")
	}
	if before, _, cut := strings.Cut(after, "\n## "); cut {
		after = before
	}
	documented := []string{}
	for line := range strings.SplitSeq(after, "\n") {
		if match := replacementDocSignalRow.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			documented = append(documented, match[1])
		}
	}
	code := append([]string(nil), doctorReplacementSignalInventory...)
	sort.Strings(documented)
	sort.Strings(code)
	if !slices.Equal(documented, code) {
		t.Fatalf("signal keys: docs/replacement-contract.md has %v, code has %v", documented, code)
	}
}

func replacementDocumentedTokens(t *testing.T) map[string][]string {
	t.Helper()
	body := readRepoText(t, "docs/replacement-contract.md")
	_, after, ok := strings.Cut(body, "## Reason token vocabulary")
	if !ok {
		t.Fatal("docs/replacement-contract.md has no `## Reason token vocabulary` section")
	}
	if before, _, cut := strings.Cut(after, "\n## "); cut {
		after = before
	}
	out := map[string][]string{}
	for line := range strings.SplitSeq(after, "\n") {
		match := replacementDocRow.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		out[match[1]] = append(out[match[1]], match[2])
	}
	if len(out) == 0 {
		t.Fatal("docs/replacement-contract.md publishes no reason tokens")
	}
	return out
}

// TestDoctorRendersReplacementTableInDefaultRunAndSectionFilter holds the
// output contract: the table is on an unfiltered run, it is selectable on its
// own, and its JSON carries the same tokens the text does.
func TestDoctorRendersReplacementTableInDefaultRunAndSectionFilter(t *testing.T) {
	t.Parallel()

	newCommand := func() *doctorCommand {
		cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
		cmd.installedImage = func() doctorInstalledImage {
			return doctorInstalledImage{Supported: true, Resolved: true}
		}
		cmd.projmuxProcessVintage = func() projmuxProcessVintage { return replacementResidualVintage() }
		cmd.installResidue = func() (installResidueRecord, int, bool) {
			return installResidueRecord{Installer: "make", Supported: true, Observed: 9, Replaced: 9}, 214, true
		}
		cmd.processAlive = func(coremetadata.ProcessIdentity) bool { return true }
		return cmd
	}

	for _, args := range [][]string{nil, {"--section", "replacement"}} {
		var stdout, stderr bytes.Buffer
		if err := newCommand().Run(args, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%v) error = %v", args, err)
		}
		text := stdout.String()
		for _, want := range []string{
			"Replacement and restoration",
			"[L1] installed-executable-image",
			"[L2] long-lived-projmux-processes",
			"[L3] provider-sessions-and-generations",
			"replacement=not-replaced",
			"reason=" + doctorReplacementReasonCutoffReached,
			"underlying: platform.observable=true",
			"residual.oldest-seconds=606482",
			// The named roles reach the rendered row, not only the record.
			"residual.role." + projmuxProcessRoleSessionClient + "=1",
			"residual.role." + projmuxProcessRoleAgentEndpoint + "=2",
			"residual.role." + projmuxProcessRoleOther + "=4",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("Run(%v) output missing %q:\n%s", args, want, text)
			}
		}
	}

	var stdout, stderr bytes.Buffer
	if err := newCommand().Run([]string{"--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run(--json) error = %v", err)
	}
	var decoded struct {
		Replacement *doctorReplacementReport `json:"replacement"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode doctor JSON: %v", err)
	}
	if decoded.Replacement == nil || len(decoded.Replacement.Rows) != len(doctorReplacementLayerOrder) {
		t.Fatalf("JSON replacement table = %+v, want %d rows", decoded.Replacement, len(doctorReplacementLayerOrder))
	}
	for i, row := range decoded.Replacement.Rows {
		if row.Layer != doctorReplacementLayerOrder[i] {
			t.Fatalf("JSON row %d layer = %q, want %q", i, row.Layer, doctorReplacementLayerOrder[i])
		}
		if row.Reason == "" || len(row.Signals) == 0 {
			t.Fatalf("JSON row %d carries a token with no discriminant: %+v", i, row)
		}
	}
}

// TestDoctorReplacementSectionCreatesNothing keeps the section read-only in the
// strongest available sense: a machine that never created a Project does not
// get a state directory out of asking the question.
func TestDoctorReplacementSectionCreatesNothing(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := newDoctorCommand()
	cmd.getenv = func(key string) string {
		switch key {
		case "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME":
			return filepath.Join(home, strings.ToLower(key))
		default:
			return ""
		}
	}
	cmd.readRegistry = func() (coremetadata.Registry, error) { return coremetadata.NewRegistry(), nil }
	cmd.brokerDiagnostic = func() codexBrokerDiagnostic {
		return codexBrokerDiagnostic{State: codexBrokerStateAbsent}
	}
	cmd.codexGeneration = nil

	report := cmd.evaluateReplacement(nil, nil)
	if len(report.Rows) != len(doctorReplacementLayerOrder) {
		t.Fatalf("rows = %d, want %d", len(report.Rows), len(doctorReplacementLayerOrder))
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("evaluating the replacement section created %v", names)
	}
}

// TestDoctorReplacementProcessRowNamesTheResidualRoles closes the gap between
// a count and a target.
//
// `processes.residual=12` tells an operator that an install left work behind
// and nothing about what to do next. The named roles are the difference: a
// residual `session-client` is the operator's own attached session, a residual
// `supervisor` is a pane to recreate, and a residual `other` is mostly
// short-lived calls that need nothing. The row must carry them, must keep the
// census order so two runs can be diffed, and must omit a role with nothing
// residual rather than print a zero.
func TestDoctorReplacementProcessRowNamesTheResidualRoles(t *testing.T) {
	t.Parallel()

	row := replacementRow(t, projectDoctorReplacement(doctorReplacementInputs{Processes: replacementResidualVintage()}),
		doctorReplacementLayerProcesses)

	got := map[string]string{}
	var order []string
	for _, signal := range row.Signals {
		if !strings.HasPrefix(signal.Key, doctorReplacementSignalRoleResidualPrefix) {
			continue
		}
		got[strings.TrimPrefix(signal.Key, doctorReplacementSignalRoleResidualPrefix)] = signal.Value
		order = append(order, strings.TrimPrefix(signal.Key, doctorReplacementSignalRoleResidualPrefix))
	}
	want := map[string]string{
		projmuxProcessRoleSupervisor:    "5",
		projmuxProcessRoleSessionClient: "1",
		projmuxProcessRoleAgentEndpoint: "2",
		projmuxProcessRoleOther:         "4",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("residual role signals = %v, want %v", got, want)
	}

	// Census order, so a row can be read down a column against another run.
	wantOrder := []string{}
	for _, role := range projmuxProcessRoleOrder {
		if _, ok := want[role]; ok {
			wantOrder = append(wantOrder, role)
		}
	}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("residual role order = %v, want the census order %v", order, wantOrder)
	}

	// A fleet with no residue names no role, rather than naming every role
	// with a zero.
	clean := replacementRow(t, projectDoctorReplacement(doctorReplacementInputs{
		Processes: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: projmuxProcessRoleSupervisor, Processes: 2, Current: 2},
		}},
	}), doctorReplacementLayerProcesses)
	for _, signal := range clean.Signals {
		if strings.HasPrefix(signal.Key, doctorReplacementSignalRoleResidualPrefix) {
			t.Fatalf("clean fleet emitted %s=%s, want no residual role on a row with no residue", signal.Key, signal.Value)
		}
	}
}

// TestReplacementProcessRolesMatchTheContractDocument is the role half of the
// vocabulary drift guard.
//
// The reason tokens and signal keys already have one. Roles need the same one
// for a stronger reason: this vocabulary is published on two surfaces at once
// -- the `doctor` L2 row and every `install-residue.jsonl` record -- and the
// contract document is what states which long-lived processes an operator is
// entitled to see named and what is left in the remainder. A role added to the
// code without that sentence is a name with no contract behind it, and a
// sentence with no role is a promise the census does not keep.
func TestReplacementProcessRolesMatchTheContractDocument(t *testing.T) {
	t.Parallel()

	body := readRepoText(t, "docs/replacement-contract.md")
	_, after, ok := strings.Cut(body, "## Process role vocabulary")
	if !ok {
		t.Fatal("docs/replacement-contract.md has no `## Process role vocabulary` section")
	}
	if before, _, cut := strings.Cut(after, "\n## "); cut {
		after = before
	}
	documented := []string{}
	for line := range strings.SplitSeq(after, "\n") {
		if match := replacementDocRoleRow.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			documented = append(documented, match[1])
		}
	}
	code := append([]string(nil), projmuxProcessRoleOrder...)
	sorted := append([]string(nil), documented...)
	sort.Strings(sorted)
	sortedCode := append([]string(nil), code...)
	sort.Strings(sortedCode)
	if !slices.Equal(sorted, sortedCode) {
		t.Fatalf("process roles: docs/replacement-contract.md has %v, code has %v", sorted, sortedCode)
	}
	// The remainder is documented last for the same reason it renders last.
	if documented[len(documented)-1] != projmuxProcessRoleOther {
		t.Fatalf("documented roles end with %q, want the remainder %q", documented[len(documented)-1], projmuxProcessRoleOther)
	}
}

// TestDoctorReplacementProcessRowCarriesTheCutoffAndThePassAccount is the L2
// half of the replacement guarantee, seen from the row an operator reads.
//
// A bounded drain that reported nothing would be indistinguishable from no
// drain at all, and a row that said `replaced` while a residual process sat
// past the cutoff would be the exact falsehood C-1's Assumption names. So the
// row has to carry three things at once: the bound it judged against, how many
// processes are past it, and what the last install pass actually did about
// them.
func TestDoctorReplacementProcessRowCarriesTheCutoffAndThePassAccount(t *testing.T) {
	t.Parallel()

	signalsOf := func(row doctorReplacementRow) map[string]string {
		got := map[string]string{}
		for _, signal := range row.Signals {
			got[signal.Key] = signal.Value
		}
		return got
	}

	// A fleet past the cutoff, with the pass that tried and could not finish.
	row := replacementRow(t, projectDoctorReplacement(doctorReplacementInputs{
		Processes: replacementResidualVintage(),
		Replacement: installReplacementOutcome{
			Outcome: installReplacementOutcomePending, Supported: true,
			Attempted: 1, Reported: 11, Refusal: "drain-required",
		},
		ReplacementOK: true,
	}), doctorReplacementLayerProcesses)

	if row.Replacement != doctorReplacementNotReplaced || row.Reason != doctorReplacementReasonCutoffReached {
		t.Fatalf("row = %s/%s, want not-replaced/%s", row.Replacement, row.Reason, doctorReplacementReasonCutoffReached)
	}
	signals := signalsOf(row)
	for key, want := range map[string]string{
		doctorReplacementSignalCutoffSeconds: "86400",
		// Three of the five supervisors, the attached session, and two of the
		// unnamed remainder are past a day; the rest are inside it.
		doctorReplacementSignalBeyondCutoff:  "6",
		doctorReplacementSignalPassOutcome:   installReplacementOutcomePending,
		doctorReplacementSignalPassRefusal:   "drain-required",
		doctorReplacementSignalPassAttempted: "1",
		doctorReplacementSignalPassDrained:   "0",
		doctorReplacementSignalPassReported:  "11",
	} {
		if got := signals[key]; got != want {
			t.Fatalf("signal %s = %q, want %q (row: %+v)", key, got, want, row.Signals)
		}
	}

	// The cutoff travels with the reader, so a short one turns an ordinary
	// residual fleet into the reported case. This is the branch the isolated
	// smoke reaches.
	short := replacementRow(t, projectDoctorReplacement(doctorReplacementInputs{
		Processes: replacementFreshResidualVintage(),
		Cutoff:    time.Minute,
	}), doctorReplacementLayerProcesses)
	if short.Reason != doctorReplacementReasonCutoffReached {
		t.Fatalf("row under a one-minute cutoff = %s, want %s", short.Reason, doctorReplacementReasonCutoffReached)
	}
	if got := signalsOf(short)[doctorReplacementSignalCutoffSeconds]; got != "60" {
		t.Fatalf("cutoff signal under a one-minute cutoff = %q, want 60", got)
	}

	// With no pass record the row still reaches its verdict from the live
	// census, and it says the pass never ran rather than inventing one.
	absent := replacementRow(t, projectDoctorReplacement(doctorReplacementInputs{
		Processes: replacementFreshResidualVintage(),
	}), doctorReplacementLayerProcesses)
	if absent.Reason != doctorReplacementReasonResidualProcesses {
		t.Fatalf("row with no pass record = %s, want %s", absent.Reason, doctorReplacementReasonResidualProcesses)
	}
	signals = signalsOf(absent)
	if got := signals[doctorReplacementSignalPassOutcome]; got != installReplacementOutcomeNotAttempted {
		t.Fatalf("outcome with no pass record = %q, want %q", got, installReplacementOutcomeNotAttempted)
	}
	for _, key := range []string{
		doctorReplacementSignalPassAttempted,
		doctorReplacementSignalPassDrained,
		doctorReplacementSignalPassReported,
		doctorReplacementSignalPassRefusal,
	} {
		if _, ok := signals[key]; ok {
			t.Fatalf("signal %s present with no pass record; an absent account is not a zero one", key)
		}
	}

	// A clean fleet is `replaced` and carries the cutoff it was judged against,
	// so two runs can be compared without one of them hiding its bound.
	clean := replacementRow(t, projectDoctorReplacement(doctorReplacementInputs{
		Processes: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Current: 1},
		}},
		Replacement:   installReplacementOutcome{Outcome: installReplacementOutcomeComplete, Supported: true, Attempted: 1, Drained: 1},
		ReplacementOK: true,
	}), doctorReplacementLayerProcesses)
	if clean.Replacement != doctorReplacementReplaced || clean.Reason != doctorReplacementReasonNoResidual {
		t.Fatalf("clean row = %s/%s, want replaced/%s", clean.Replacement, clean.Reason, doctorReplacementReasonNoResidual)
	}
	signals = signalsOf(clean)
	if signals[doctorReplacementSignalBeyondCutoff] != "0" ||
		signals[doctorReplacementSignalPassOutcome] != installReplacementOutcomeComplete {
		t.Fatalf("clean row signals = %+v", clean.Signals)
	}
}
