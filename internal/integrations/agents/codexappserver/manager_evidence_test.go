package codexappserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// All daemon responses below are SYNTHETIC official-command fixtures, never
// observations of a host manager or proof that a live daemon is managed.
func TestManagerEvidenceContradictionAndOwnershipMatrix(t *testing.T) {
	running := `"status":"running","cliVersion":"0.154.0","appServerVersion":"0.154.0"`
	for _, tt := range []struct {
		name, payload, attach, agreement string
		ready                            EndpointReadiness
		owner                            ManagerOwnership
		action                           NativeActionReadiness
		refusal                          NativeActionRefusal
	}{
		{"running pid current", `{` + running + `,"backend":"pid"}`, "0.154.0", "consistent", EndpointReady, ManagerManaged, NativeActionReady, NativeActionRefusalNone},
		{"running pid skew", `{"status":"running","backend":"pid","cliVersion":"0.154.0","appServerVersion":"0.151.0"}`, "0.151.0", "consistent", EndpointReady, ManagerManaged, NativeActionRefused, NativeActionRefusalVersionSkew},
		{"backend absent", `{` + running + `}`, "0.154.0", "consistent", EndpointReady, ManagerUnmanaged, NativeActionRefused, NativeActionRefusalUnmanaged},
		{"backend null", `{` + running + `,"backend":null}`, "0.154.0", "consistent", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
		{"backend unknown", `{` + running + `,"backend":"secret"}`, "0.154.0", "consistent", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
		{"duplicate ownership status", `{` + running + `,"backend":"pid","status":"running"}`, "0.154.0", "insufficient", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
		{"malformed", `{`, "0.154.0", "insufficient", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
		{"invalid UTF8", "{\"status\":\"running\",\"unknown\":\"\xff\"}", "0.154.0", "insufficient", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
		{"truncated", strings.Repeat("x", maxDaemonVersionBytes+1), "0.154.0", "insufficient", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
		{"version contradiction", `{` + running + `,"backend":"pid"}`, "0.151.0", "contradictory", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalEvidenceContradictory},
		{"ready vs stopped", `{"status":"stopped"}`, "0.154.0", "contradictory", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalEvidenceContradictory},
		{"dead vs running", `{` + running + `,"backend":"pid"}`, "", "contradictory", EndpointDead, ManagerUnknown, NativeActionRefused, NativeActionRefusalEvidenceContradictory},
		{"manager running attach unobserved", `{` + running + `,"backend":"pid"}`, "", "insufficient", EndpointTimedOut, ManagerManaged, NativeActionUnknown, NativeActionRefusalNone},
		{"path only", `{"managedCodexPath":"/secret/managed/0.154.0","pid":999}`, "0.154.0", "insufficient", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
		{"unsafe version", `{"status":"running","backend":"pid","cliVersion":"0.154.0","appServerVersion":"/secret/0.154.0"}`, "0.154.0", "insufficient", EndpointReady, ManagerUnknown, NativeActionRefused, NativeActionRefusalOwnershipUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture, err := startDaemonVersionFixture(t.TempDir(), tt.payload, false, daemonVersionFixtureReadinessTimeout)
			if err != nil {
				t.Fatal(err)
			}
			observation := observeManager(t.Context(), time.Second, func(string) (string, error) { return fixture.executable, nil }, exec.CommandContext)
			if err := fixture.terminate(daemonVersionFixtureTerminationTimeout); err != nil {
				t.Fatal(err)
			}
			health := Decide(AvailabilityAvailable, ReasonNone, tt.attach, EndpointStdioProxy, ConnectionReady, true)
			health.EndpointReadiness = tt.ready
			health = withManagerObservation(health, observation)
			if health.ManagerOwnership != tt.owner || health.NativeAction != tt.action || health.NativeRefusal != tt.refusal || health.ManagerEvidence.Agreement != tt.agreement {
				t.Fatalf("wrong synthetic evidence decision: %+v, evidence %+v", health, health.ManagerEvidence)
			}
			authority := AuthorityFor(health)
			wantAttach, wantLifecycle := EndpointAttachRefused, DaemonLifecycleAuthorityNone
			switch tt.name {
			case "running pid current":
				wantAttach, wantLifecycle = EndpointAttachAllowed, DaemonLifecycleAuthorityManaged
			case "running pid skew":
				wantLifecycle = DaemonLifecycleAuthorityManaged
			case "backend absent":
				wantAttach = EndpointAttachAllowed
			}
			if authority.Attach != wantAttach || authority.Lifecycle != wantLifecycle {
				t.Fatalf("attach/lifecycle axes merged: %+v", authority)
			}
			if tt.ready == EndpointDead && health.RunningVersion != "" {
				t.Fatal("manager claim replaced absent attach observation")
			}
			evidence, _ := json.Marshal(health.ManagerEvidence)
			all, _ := json.Marshal(health)
			if len(evidence) > MaxManagerEvidenceBytes || len(all) > 4096 || strings.Contains(string(all), "secret") {
				t.Fatal("diagnostic bounds/redaction failed")
			}
			var starts int
			policy := testLifecyclePolicy(func(context.Context) Health { return health }, func(context.Context) startResult { starts++; return startSucceeded })
			_, err = policy.ensureReadyForHook(t.Context(), TriggerNativeUserAction, true)
			if err != nil || starts != 0 {
				t.Fatal("synthetic diagnostic authorized manager mutation")
			}
		})
	}
}

func TestManagerEvidenceCommandFailureAndTimeout(t *testing.T) {
	for _, mode := range []string{"command-failed", "timeout", "cancelled"} {
		ctx, cancel := context.WithCancel(t.Context())
		if mode == "cancelled" {
			cancel()
		}
		defer cancel()
		command := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagerDiagnosticDeadlineProcess$")
			cmd.Env = append(os.Environ(), "CODEX_MANAGER_DEADLINE_HELPER="+mode)
			return cmd
		}
		timeout := 5 * time.Second
		if mode == "timeout" {
			timeout = 30 * time.Millisecond
		}
		observation := observeManager(ctx, timeout, func(string) (string, error) { return "synthetic-codex", nil }, command)
		if observation.Evidence.Result != mode || observation.Ownership != ManagerUnknown {
			t.Fatalf("wrong command evidence: %+v", observation.Evidence)
		}
	}
}

func TestManagerDiagnosticDeadlineProcess(t *testing.T) {
	switch os.Getenv("CODEX_MANAGER_DEADLINE_HELPER") {
	case "command-failed":
		os.Exit(91)
	case "timeout":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}
