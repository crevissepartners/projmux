package app

import (
	"context"
	"encoding/json"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"os"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Run the same ownership fences for both providers. State projection remains
// the observer's responsibility: unknown and reaped exit retain ownership.
func TestClaudeProcessObservationOwnershipPredicateParity(t *testing.T) {
	data, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*coremetadata.Registry, *processhost.Binding, *processHostObservation)
		want   bool
	}{
		{name: "current", want: true},
		{name: "unknown", want: true, mutate: func(_ *coremetadata.Registry, _ *processhost.Binding, v *processHostObservation) { v.State = "unknown" }},
		{name: "reaped", want: true, mutate: func(_ *coremetadata.Registry, _ *processhost.Binding, v *processHostObservation) {
			v.State = "exited"
			v.Exit = &processhost.Exit{Code: 1}
		}},
		{name: "missing pane", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes = r.Panes[:1]
		}},
		{name: "missing agent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { r.Agents = nil }},
		{name: "missing window", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { r.Windows = nil }},
		{name: "missing project", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { r.Projects = nil }},
		{name: "pane owner absent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Metadata.OwnerRef = nil
		}},
		{name: "pane owner kind", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Metadata.OwnerRef.Kind = coremetadata.KindWindow
		}},
		{name: "pane owner UID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Metadata.OwnerRef.UID = "other"
		}},
		{name: "agent owner absent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Agents[0].Metadata.OwnerRef = nil
		}},
		{name: "agent owner kind", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Agents[0].Metadata.OwnerRef.Kind = coremetadata.KindProject
		}},
		{name: "agent owner UID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Agents[0].Metadata.OwnerRef.UID = "other"
		}},
		{name: "agent pane", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Agents[0].Status.PaneRef = "other"
		}},
		{name: "window owner absent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Windows[0].Metadata.OwnerRef = nil
		}},
		{name: "window owner kind", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Windows[0].Metadata.OwnerRef.Kind = coremetadata.KindAgent
		}},
		{name: "window owner UID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Windows[0].Metadata.OwnerRef.UID = "other"
		}},
		{name: "pane runtime", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Spec.Runtime.Kind = coremetadata.RuntimeTmux
		}},
		{name: "pane role", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Spec.Role = coremetadata.PaneRoleShell
		}},
		{name: "provider drift", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Provider = "other"
		}},
		{name: "unsupported provider", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Provider = "other"
			r.Agents[0].Spec.Provider = "other"
		}},
		{name: "activation kind", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Kind = coremetadata.RuntimeTmux
		}},
		{name: "activation absent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process = nil
		}},
		{name: "tmux handle", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.RuntimeID = "%1"
		}},
		{name: "activation generation", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Generation = "other"
		}},
		{name: "activation operation", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.OperationID = "other"
		}},
		{name: "activation agent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.AgentUID = "other"
		}},
		{name: "host PID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { v.Host.PID++ }},
		{name: "host birth", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Host.Start = "other"
		}},
		{name: "host UID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { v.Host.OwnerUID++ }},
		{name: "child PID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { v.Child.PID++ }},
		{name: "child birth", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Child.Start = "other"
		}},
		{name: "child UID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { v.Child.OwnerUID++ }},
		{name: "host invalid", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Host.PID = 0
			r.Panes[1].Status.Activation.Process.HostProcess = v.Host
		}},
		{name: "child invalid", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Child.Start = ""
			r.Panes[1].Status.Activation.Process.Child = v.Child
		}},
		{name: "foreign host UID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Host.OwnerUID++
			r.Panes[1].Status.Activation.Process.HostProcess = v.Host
		}},
		{name: "foreign child UID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Child.OwnerUID++
			r.Panes[1].Status.Activation.Process.Child = v.Child
		}},
		{name: "requested Host", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { b.Host = "other" }},
		{name: "observed Host", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Binding.Host = "other"
		}},
		{name: "requested Project", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { b.Project = "other" }},
		{name: "observed Project", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Binding.Project = "other"
		}},
		{name: "requested Window", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { b.Window = "other" }},
		{name: "observed Window", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Binding.Window = "other"
		}},
		{name: "requested Agent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { b.Agent = "other" }},
		{name: "observed Agent", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Binding.Agent = "other"
		}},
		{name: "requested Pane", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) { b.Pane = "other" }},
		{name: "observed Pane", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Binding.Pane = "other"
		}},
		{name: "requested Generation", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			b.Generation = "other"
		}},
		{name: "observed Generation", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Binding.Generation = "other"
		}},
		{name: "requested Operation", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			b.Operation = "other"
		}},
		{name: "observed Operation", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			v.Binding.Operation = "other"
		}},
		{name: "activation binding HostInstanceID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process.Binding.HostInstanceID = "other"
		}},
		{name: "activation binding ProjectUID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process.Binding.ProjectUID = "other"
		}},
		{name: "activation binding WindowUID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process.Binding.WindowUID = "other"
		}},
		{name: "activation binding AgentUID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process.Binding.AgentUID = "other"
		}},
		{name: "activation binding PaneUID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process.Binding.PaneUID = "other"
		}},
		{name: "activation binding Generation", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process.Binding.Generation = "other"
		}},
		{name: "activation binding OperationID", mutate: func(r *coremetadata.Registry, b *processhost.Binding, v *processHostObservation) {
			r.Panes[1].Status.Activation.Process.Binding.OperationID = "other"
		}},
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					var reg coremetadata.Registry
					if err := json.Unmarshal(data, &reg); err != nil {
						t.Fatal(err)
					}
					reg.Agents[0].Spec.Provider = provider
					activation := reg.Panes[1].Status.Activation.Process
					activation.HostProcess.OwnerUID = uint32(os.Getuid())
					activation.Child.OwnerUID = uint32(os.Getuid())
					binding := processSchemaBinding(activation.Binding)
					view := processHostObservation{Binding: binding, Host: activation.HostProcess, Child: activation.Child, Provider: provider, State: "ready"}
					if tc.mutate != nil {
						tc.mutate(&reg, &binding, &view)
					}
					if got := processObservationOwnership(reg, binding, view); got != tc.want {
						t.Fatalf("ownership = %v, want %v", got, tc.want)
					}
				})
			}
		})
	}
}

func TestProcessObservationRetiredWaitRemainsOfflineWithoutAuthority(t *testing.T) {
	data, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var reg coremetadata.Registry
			if err := json.Unmarshal(data, &reg); err != nil {
				t.Fatal(err)
			}
			pane := &reg.Panes[1]
			activation := *pane.Status.Activation.Process
			reg.Agents[0].Spec.Provider = provider
			// No invented conversation is needed for termination evidence.
			pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: provider, Binding: activation.Binding, ResumeState: coremetadata.ProcessResumeUnknown}
			code := 0
			receipt := coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal, ObservedAt: time.Unix(100, 0).UTC(), PaneUID: activation.Binding.PaneUID, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID, ExitCode: &code}
			if err := (coremetadata.Mutator{}).RecordProcessWait(&reg, activation, receipt); err != nil {
				t.Fatal(err)
			}
			binding := processSchemaBinding(activation.Binding)
			cases := []struct {
				name   string
				change func(*coremetadata.Registry)
				want   resourcegraph.Status
			}{
				{name: "exact receipt", want: resourcegraph.StatusOffline},
				{name: "missing pane receipt", change: func(r *coremetadata.Registry) { r.Panes[1].Status.LastTermination = nil }},
				{name: "missing agent receipt", change: func(r *coremetadata.Registry) { r.Agents[0].Status.LastTermination = nil }},
				{name: "different agent receipt", change: func(r *coremetadata.Registry) {
					r.Agents[0].Status.LastTermination.ObservedAt = time.Unix(101, 0).UTC()
				}},
				{name: "generation", change: func(r *coremetadata.Registry) { r.Panes[1].Status.LastTermination.Generation = "old" }},
				{name: "operation", change: func(r *coremetadata.Registry) { r.Panes[1].Status.LastTermination.OperationID = "old" }},
				{name: "wrong source", change: func(r *coremetadata.Registry) {
					r.Panes[1].Status.LastTermination.Source = coremetadata.TerminationSourceControlAction
				}},
				{name: "absent Wait", change: func(r *coremetadata.Registry) { r.Panes[1].Status.LastTermination.ExitCode = nil }},
				{name: "running agent", change: func(r *coremetadata.Registry) { r.Agents[0].Status.Phase = coremetadata.PhaseRunning }},
				{name: "session binding", change: func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession.Binding.OperationID = "old" }},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					probe := reg.Clone()
					if tc.change != nil {
						tc.change(&probe)
					}
					if _, _, current := probe.CurrentProcessActivation(activation.Binding); current {
						t.Fatal("retired session granted activation authority")
					}
					view := processHostObservation{Binding: binding, Provider: provider, Host: activation.HostProcess, Child: activation.Child, State: "exited", Exit: &processhost.Exit{Code: 0}}
					if processObservationOwnership(probe, binding, view) {
						t.Fatal("retired session granted host ownership")
					}
					inventory := observeRegistryProcesses(context.Background(), probe)
					want := tc.want
					if want == "" {
						want = resourcegraph.StatusUnknown
					}
					if len(inventory.Declared) != 1 || len(inventory.Observed) != 1 || inventory.Observed[0].Status != want {
						t.Fatalf("inventory=%+v want %s", inventory, want)
					}
				})
			}
		})
	}
}

func TestRecordedOwnerNormalProcessExitMatchesWait(t *testing.T) {
	data, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []string{"", "TERM"} {
		t.Run("signal="+signal, func(t *testing.T) {
			var reg coremetadata.Registry
			if err := json.Unmarshal(data, &reg); err != nil {
				t.Fatal(err)
			}
			pane := &reg.Panes[1]
			activation := *pane.Status.Activation.Process
			activation.HostProcess.OwnerUID = uint32(os.Getuid())
			activation.Child.OwnerUID = uint32(os.Getuid())
			pane.Status.Activation.Process = &activation
			binding := processSchemaBinding(activation.Binding)
			code := 143
			receipt := coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal, ObservedAt: time.Now().UTC(), PaneUID: binding.Pane, AgentUID: binding.Agent, Generation: binding.Generation, OperationID: binding.Operation, ExitCode: &code, Signal: signal}
			if signal != "" {
				receipt.ExitCode = nil
			}
			pane.Status.LastTermination = receipt.Clone()
			s, ok := recordedProcessExit(reg, binding, *pane, activation, "codex")
			if !ok || s.Exit == nil || s.Exit.Signal != signal || (signal == "" && s.Exit.Code != code) {
				t.Fatal("recorded owner Wait lost", s)
			}
			pane.Status.LastTermination.Generation = "old"
			if _, ok := recordedProcessExit(reg, binding, *pane, activation, "codex"); ok {
				t.Fatal("foreign normal receipt accepted")
			}
		})
	}
}
