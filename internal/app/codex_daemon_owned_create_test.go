package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// codexDaemonGuidanceCase is one default-endpoint readiness decision a native
// Codex create cannot attach to. Each one used to be the entry to a private
// app-server generation; each one now ends in a daemon step.
type codexDaemonGuidanceCase struct {
	name     string
	health   codexappserver.Health
	guidance string
}

func codexDaemonGuidanceCases() []codexDaemonGuidanceCase {
	skew := func(ownership codexappserver.ManagerOwnership, refusal codexappserver.NativeActionRefusal, recovery codexappserver.OperatorRecovery) codexappserver.Health {
		// The exact topology the retired managed activator acted on: a ready
		// default endpoint running an older Codex than the managed install.
		health := codexappserver.Decide(codexappserver.AvailabilityAvailable, codexappserver.ReasonNone, "0.153.0",
			codexappserver.EndpointStdioProxy, codexappserver.ConnectionReady, true)
		health.InstallCapability = codexappserver.InstallCapabilityManagedReady
		health.VersionRelation = codexappserver.VersionSkew
		health.CLIVersion, health.ManagedVersion, health.RunningVersion = "0.154.0", "0.154.0", "0.153.0"
		health.ManagerOwnership = ownership
		health.NativeAction, health.NativeRefusal = codexappserver.NativeActionRefused, refusal
		health.InterruptionRisk, health.OperatorRecovery = codexappserver.InterruptionRiskSharedClients, recovery
		return health
	}
	dead := codexappserver.Decide(codexappserver.AvailabilityUnavailable, codexappserver.ReasonDaemonNotRunning, "",
		codexappserver.EndpointStdioProxy, codexappserver.ConnectionDisconnected, true)
	dead.InstallCapability = codexappserver.InstallCapabilityManagedReady
	return []codexDaemonGuidanceCase{
		{
			name: "managed-skew",
			health: skew(codexappserver.ManagerManaged, codexappserver.NativeActionRefusalVersionSkew,
				codexappserver.OperatorRecoveryRestartManagedDaemon),
			guidance: "`codex app-server daemon restart`",
		},
		{
			name: "unmanaged-skew",
			health: skew(codexappserver.ManagerUnmanaged, codexappserver.NativeActionRefusalUnmanagedVersionSkew,
				codexappserver.OperatorRecoveryStopOwnerThenStart),
			guidance: "`codex app-server daemon bootstrap`",
		},
		{name: "daemon-not-running", health: dead, guidance: "`codex app-server daemon start`"},
		{
			// The shape every installed probe currently reports: the daemon's
			// version evidence cannot be matched to the attached endpoint.
			name: "ownership-unknown-skew",
			health: func() codexappserver.Health {
				health := skew(codexappserver.ManagerUnknown, codexappserver.NativeActionRefusalOwnershipUnknown,
					codexappserver.OperatorRecoveryInspectProcessOwnership)
				health.ManagerEvidence = &codexappserver.ManagerEvidence{Status: "running", Backend: "pid", Result: "observed", Agreement: "insufficient", Version: "0.153.4"}
				health.RunningVersion = ""
				return health
			}(),
			guidance: "`codex app-server daemon restart`",
		},
	}
}

// TestCodexNativeCreateEndpointSkewRefusesWithoutPrivateGeneration is the C-1
// enforcement: a prompted native Codex create whose default endpoint cannot be
// attached -- a version skew included -- launches no private app-server, writes
// no generation journal, bundle, or runtime root, mutates no Registry or tmux
// state, and refuses with the typed reason plus the `codex app-server daemon`
// step that makes the endpoint attachable.
func TestCodexNativeCreateEndpointSkewRefusesWithoutPrivateGeneration(t *testing.T) {
	for _, tt := range codexDaemonGuidanceCases() {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state")
			probes := 0
			controller := newCodexNativeThreadController(stateDir)
			controller.probe = func(context.Context) codexappserver.Health {
				probes++
				return tt.health
			}
			controller.open = func(context.Context, codexNativeEndpointRoute, bool) (codexNativeThreadClient, error) {
				t.Error("refused create opened a thread client")
				return nil, errFakeNativeUnavailable
			}

			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestAgentCreateCommand(t, store, tmux)
			panes := &fakeNativePaneLauncher{}
			create.codexNative = controller
			create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
			before := store.snapshot()
			for _, argv := range [][]string{
				{"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "must not send"},
				{"codex", "--project", "alpha", "--window", "main", "--", "must not send"},
			} {
				stdout, stderr, err := runRoute(t, create, argv...)
				if err == nil || stdout != "" || stderr != "" {
					t.Fatalf("%v: stdout=%q stderr=%q err=%v", argv, stdout, stderr, err)
				}
				for _, want := range []string{"(" + codexNativeReasonGenerationUnavailable + ")", "no provider conversation was mutated", tt.guidance} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("%v refusal lacks %q: %v", argv, want, err)
					}
				}
				if strings.Contains(err.Error(), "managed-generation") || strings.Contains(err.Error(), "projmux agent app-server") {
					t.Fatalf("%v refusal still routes the operator to a private generation: %v", argv, err)
				}
			}
			if probes != 2 {
				t.Fatalf("default endpoint probes = %d, want one per create", probes)
			}
			if store.snapshot() != before || store.writes != 0 || tmux.argvContains("split-window") || tmux.argvContains("new-window") {
				t.Fatalf("refused create mutated registry/tmux: writes=%d calls=%v", store.writes, tmux.calls)
			}
			if len(panes.plans) != 0 || len(panes.bound) != 0 || len(panes.lifecycle) != 0 {
				t.Fatalf("refused create reached the native pane launcher: %+v", panes)
			}
			// The generation journal, bundle store, qualification store, and
			// private runtime roots all live under this directory.
			if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused create wrote private generation state: %v", err)
			}
		})
	}
}

// TestCodexDaemonGuidanceRefusalAndDoctorGolden pins the operator-facing text
// of the create refusal and the Doctor app-server block for every endpoint that
// a native create cannot attach to. Both name a `codex app-server daemon` step
// and neither names a private generation action.
func TestCodexDaemonGuidanceRefusalAndDoctorGolden(t *testing.T) {
	var out bytes.Buffer
	for _, tt := range codexDaemonGuidanceCases() {
		controller := defaultCodexNativeThreadController{probe: func(context.Context) codexappserver.Health { return tt.health }}
		_, routeErr := controller.Current(context.Background())
		var route *codexNativeRouteError
		if !errors.As(routeErr, &route) || route.Reason != codexNativeReasonGenerationUnavailable ||
			!strings.Contains(route.OperatorAction, tt.guidance) {
			t.Fatalf("%s: default route refusal = %v", tt.name, routeErr)
		}
		if recovery := codexappserver.RecoveryDiagnosticOf(routeErr); recovery == nil || recovery.Operator != tt.health.OperatorRecovery {
			t.Fatalf("%s: refusal lost the read-only recovery diagnostic: %+v", tt.name, recovery)
		}
		refusal := nativeCreatePreparationRefusalForCapability(canonicalCreateAgent, routeErr, tt.health.InstallCapability)
		var doctor bytes.Buffer
		health := tt.health
		writeDoctorAppServerText(&doctor, &health)
		if !strings.Contains(doctor.String(), "Guidance: ") || !strings.Contains(doctor.String(), tt.guidance) {
			t.Fatalf("%s: Doctor lacks daemon guidance %q:\n%s", tt.name, tt.guidance, doctor.String())
		}
		out.WriteString("== " + tt.name + "\n")
		out.WriteString("refusal: " + refusal.Error() + "\n")
		out.WriteString("doctor:" + doctor.String() + "\n")
	}
	for _, retired := range []string{"managed-generation", "projmux agent app-server", "qualification"} {
		if strings.Contains(out.String(), retired) {
			t.Fatalf("daemon guidance names retired private generation surface %q:\n%s", retired, out.String())
		}
	}
	golden := filepath.Join("testdata", "codex_daemon_guidance.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("daemon guidance golden mismatch (UPDATE_GOLDEN=1 rewrites it)\n--- got ---\n%s\n--- want ---\n%s", out.Bytes(), want)
	}
}

// TestDoctorAttachableCodexEndpointHasNoDaemonGuidance keeps the new guidance
// line off a ready, exact-current endpoint: Doctor prescribes a daemon step only
// when there is something for the operator to fix.
func TestDoctorAttachableCodexEndpointHasNoDaemonGuidance(t *testing.T) {
	health := codexappserver.Decide(codexappserver.AvailabilityAvailable, codexappserver.ReasonNone, "0.154.0",
		codexappserver.EndpointStdioProxy, codexappserver.ConnectionReady, true)
	health.VersionRelation, health.ManagerOwnership = codexappserver.VersionCurrent, codexappserver.ManagerManaged
	health.NativeAction = codexappserver.NativeActionReady
	if codexappserver.AuthorityFor(health).Attach != codexappserver.EndpointAttachAllowed {
		t.Fatalf("fixture is not attachable: %+v", codexappserver.AuthorityFor(health))
	}
	var doctor bytes.Buffer
	writeDoctorAppServerText(&doctor, &health)
	if strings.Contains(doctor.String(), "Guidance:") || strings.Contains(doctor.String(), "codex app-server daemon") {
		t.Fatalf("attachable endpoint received daemon guidance:\n%s", doctor.String())
	}
}

// TestIncompleteManagedActivationIsNeverResumedByCreate covers the second
// retired entry: a journal left mid-way through a managed activation (admission
// committed, drain unpublished) is ignored: Current routes to the default
// endpoint and never runs the activation forward, so the journal bytes and
// mtime stay exactly as found.
func TestIncompleteManagedActivationIsNeverResumedByCreate(t *testing.T) {
	root := t.TempDir()
	old := coremetadata.CodexEndpointRef{StateDomainID: "test-domain", EndpointGenerationID: "codex-0.152.1"}
	target := coremetadata.CodexEndpointRef{StateDomainID: "test-domain", EndpointGenerationID: "codex-0.153.0"}
	// A managed activation that committed admission to its private candidate
	// and never published the drain of the old unmanaged endpoint.
	operation := map[string]any{
		"journalVersion": 1, "operationRef": "managed-activation-test", "stateDomainID": target.StateDomainID,
		"oldGenerationID": old.EndpointGenerationID, "targetGenerationID": target.EndpointGenerationID,
		"phase": "admission-current", "candidateLaunchIntended": true, "candidateStarted": true,
		"candidateReady": true, "admissionCommitted": true, "drainPublished": false,
		"handoverRequested": false, "abortIntended": false, "aborted": false,
		"mutations": map[string]any{"candidateLaunchIntent": 1, "candidateStart": 1, "admissionCommit": 1},
	}
	journal := writeCodexRollingJournal(t, filepath.Join(root, "state"), target.StateDomainID, target.EndpointGenerationID, []codexJournalRoute{
		{endpoint: old, state: coremetadata.CodexGenerationDraining, version: "0.152.1", bundleID: "external-0.152.1"},
		{endpoint: target, state: coremetadata.CodexGenerationCurrent, version: "0.153.0", bundleID: "sha256-managed", private: true, root: root},
	}, operation)
	bytesBefore, modBefore := snapshotCodexJournal(t, journal)
	controller := newCodexNativeThreadController(filepath.Join(root, "state"))
	daemon := nativeTestDefaultRoute("codex-0.152.1")
	controller.current = func(context.Context) (codexNativeEndpointRoute, error) { return daemon, nil }

	// The journal names a ready private current route; the default endpoint is
	// the only route create uses, and the activation is never run forward.
	if route, err := controller.Current(context.Background()); err != nil || !route.Default || !route.Endpoint.Same(daemon.Endpoint) {
		t.Fatalf("current on an incomplete activation journal: %+v, %v", route, err)
	}
	bytesAfter, modAfter := snapshotCodexJournal(t, journal)
	if !reflect.DeepEqual(bytesBefore, bytesAfter) || !modBefore.Equal(modAfter) {
		t.Fatalf("create advanced the managed activation: bytes-equal=%t mtime %s -> %s",
			reflect.DeepEqual(bytesBefore, bytesAfter), modBefore, modAfter)
	}
}
