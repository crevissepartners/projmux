package app

import (
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestInstalledRecoveryAdmissionRequiresStableExactControlBeforeFirstInput(t *testing.T) {
	baseline := installedRecoveryObservation{
		Stage: "ready",
		Identity: installedRecoveryAgent{Agent: "agent-1", Pane: "pane-1", Runtime: "%1", Activation: "activation-1", Thread: "thread-1", ControlEpoch: "123-2",
			Authority: coremetadata.CodexAuthorityRef{StateDomainID: "state-1", EndpointGenerationID: "codex-0.154.0", BrokerRuntimeID: "broker-1", ConnectionEpoch: 1, BindingEpoch: 1}},
		Control: &installedRecoveryControl{OK: true, Availability: agentControlAvailability{Start: true}},
	}
	for _, test := range []struct {
		name   string
		change func(*installedRecoveryObservation)
	}{
		{"agent", func(out *installedRecoveryObservation) { out.Identity.Agent = "agent-2" }},
		{"project", func(out *installedRecoveryObservation) { out.Identity.Project = "project-2" }},
		{"window", func(out *installedRecoveryObservation) { out.Identity.Window = "window-2" }},
		{"pane", func(out *installedRecoveryObservation) { out.Identity.Pane = "pane-2" }},
		{"runtime", func(out *installedRecoveryObservation) { out.Identity.Runtime = "%2" }},
		{"activation", func(out *installedRecoveryObservation) { out.Identity.Activation = "activation-2" }},
		{"thread", func(out *installedRecoveryObservation) { out.Identity.Thread = "thread-2" }},
		{"session", func(out *installedRecoveryObservation) { out.Identity.Session = "session-2" }},
		{"endpoint", func(out *installedRecoveryObservation) { out.Identity.Authority.EndpointGenerationID = "codex-0.155.0" }},
		{"state-domain", func(out *installedRecoveryObservation) { out.Identity.Authority.StateDomainID = "state-2" }},
		{"broker", func(out *installedRecoveryObservation) { out.Identity.Authority.BrokerRuntimeID = "broker-2" }},
		{"connection", func(out *installedRecoveryObservation) { out.Identity.Authority.ConnectionEpoch++ }},
		{"binding", func(out *installedRecoveryObservation) { out.Identity.Authority.BindingEpoch++ }},
		{"control", func(out *installedRecoveryObservation) { out.Identity.ControlEpoch = "123-3" }},
		{"unknown", func(out *installedRecoveryObservation) { out.Stage = "control-status-refused" }},
		{"missing-control", func(out *installedRecoveryObservation) { out.Control = nil }},
		{"not-startable", func(out *installedRecoveryObservation) { out.Control = &installedRecoveryControl{OK: true} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var admission installedRecoveryAdmission
			start := time.Unix(1700000000, 0)
			if out := admission.observe(start, baseline); out.Stage != "waiting-read-admission" {
				t.Fatal("first observation admitted immediately")
			}
			changed := baseline
			test.change(&changed)
			if out := admission.observe(start.Add(time.Second), changed); out.Stage == "ready" {
				t.Fatal("changed or unknown consumer inherited admission")
			}
			if out := admission.observe(start.Add(2*time.Second), baseline); out.Stage != "waiting-read-admission" {
				t.Fatal("original consumer inherited a different observation's age")
			}
			if out := admission.observe(start.Add(3*time.Second-time.Nanosecond), baseline); out.Stage != "waiting-read-admission" {
				t.Fatal("admission interval shortened")
			}
			if out := admission.observe(start.Add(3*time.Second), baseline); out.Stage != "ready" || out.Identity != baseline.Identity {
				t.Fatal("exact stable control failed admission")
			}
			if out := admission.observe(start, baseline); out.Stage != "waiting-read-admission" {
				t.Fatal("clock reversal retained elapsed admission")
			}
		})
	}
}
