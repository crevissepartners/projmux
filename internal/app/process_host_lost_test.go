package app

import (
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func hostLostFixture(t *testing.T) (coremetadata.Registry, coremetadata.ProcessActivation) {
	t.Helper()
	reg := processResumeQueryFixture(t)
	p := &reg.Panes[0]
	for i := range reg.Panes {
		if reg.Panes[i].Status.ProcessSession != nil {
			p = &reg.Panes[i]
		}
	}
	a := coremetadata.ProcessActivation{Binding: p.Status.ProcessSession.Binding, HostProcess: coremetadata.ProcessIdentity{PID: 987651, OwnerUID: uint32(os.Getuid()), Start: "host-birth"}, Child: coremetadata.ProcessIdentity{PID: 987652, OwnerUID: uint32(os.Getuid()), Start: "child-birth"}}
	p.Status.ProcessSession.ResumeState = coremetadata.ProcessResumeUnknown
	p.Status.Activation = coremetadata.PaneActivation{Kind: coremetadata.RuntimeProcess, Process: &a, AgentUID: a.Binding.AgentUID, Generation: a.Binding.Generation, OperationID: a.Binding.OperationID}
	agent, _ := reg.Agent(a.Binding.AgentUID)
	agent.Status.Phase = coremetadata.PhaseRunning
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return reg, a
}
func absentProcess(int) (coremetadata.ProcessIdentity, int, error) {
	return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
}

func TestHostLostResumeRechecksKernelAndCAS(t *testing.T) {
	for _, which := range []string{"absent", "owner-live", "child-live", "unknown", "pid-reused", "changed-activation", "changed-session"} {
		t.Run(which, func(t *testing.T) {
			reg, a := hostLostFixture(t)
			candidate, ok := hostLostResumeCandidate(reg, a.Binding.AgentUID, absentProcess)
			if !ok {
				t.Fatal("candidate absent")
			}
			next := a.Binding
			next.HostInstanceID = "new-host"
			next.Generation = "new-generation"
			next.OperationID = "new-operation"
			read := processIdentityReader(absentProcess)
			switch which {
			case "owner-live", "child-live":
				read = func(pid int) (coremetadata.ProcessIdentity, int, error) {
					if which == "owner-live" && pid == a.HostProcess.PID {
						return a.HostProcess, 0, nil
					}
					if which == "child-live" && pid == a.Child.PID {
						return a.Child, 0, nil
					}
					return absentProcess(pid)
				}
			case "unknown":
				read = func(int) (coremetadata.ProcessIdentity, int, error) {
					return coremetadata.ProcessIdentity{}, 0, errors.New("permission denied")
				}
			case "pid-reused":
				read = func(pid int) (coremetadata.ProcessIdentity, int, error) {
					return coremetadata.ProcessIdentity{PID: pid, OwnerUID: uint32(os.Getuid()), Start: "new-birth"}, 0, nil
				}
			case "changed-activation":
				p, _ := reg.Pane(a.Binding.PaneUID)
				p.Status.Activation.Process.Child.Start = "other"
			case "changed-session":
				p, _ := reg.Pane(a.Binding.PaneUID)
				p.Status.ProcessSession.TurnID = "other-turn"
				p.Status.ProcessSession.Pending = nil
			}
			before := reg.Clone()
			err := reserveHostLostResume(&reg, candidate, next, read, coremetadata.Mutator{})
			if which != "absent" && which != "pid-reused" {
				if err == nil || !reflect.DeepEqual(before, reg) {
					t.Fatal("unproven or changed activation accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, _ := reg.Pane(next.PaneUID)
			agent, _ := reg.Agent(next.AgentUID)
			if p.Status.ProcessSession.ResumeState != coremetadata.ProcessResumeUnknown || p.Status.ProcessSession.SessionID != candidate.Record.SessionID || p.Status.ProcessSession.History.Binding != a.Binding || p.Status.ProcessSession.History.InterruptedTurnID != "interrupted" || len(p.Status.ProcessSession.History.Expired) != 1 {
				t.Fatal("conversation/history changed")
			}
			r := p.Status.LastTermination
			if r == nil || r.Source != coremetadata.TerminationSourceReconcile || r.Classification != coremetadata.TerminationUnknown || r.ExitCode != nil || r.Signal != "" || !reflect.DeepEqual(r, agent.Status.LastTermination) {
				t.Fatal("invented Wait", r)
			}
			if err := (coremetadata.Mutator{}).RestoreProcessHostLostResume(&reg, next, candidate.Record, a); err != nil {
				t.Fatal(err)
			}
			p, _ = reg.Pane(a.Binding.PaneUID)
			if *p.Status.Activation.Process != a || !reflect.DeepEqual(p.Status.ProcessSession, &candidate.Record) {
				t.Fatal("dead activation not restored")
			}
			// A retry re-proves absence and retains the original unknown receipt.
			candidate, ok = hostLostResumeCandidate(reg, a.Binding.AgentUID, absentProcess)
			if !ok {
				t.Fatal("retry unavailable")
			}
			if err := reserveHostLostResume(&reg, candidate, next, absentProcess, coremetadata.Mutator{}); err != nil {
				t.Fatal("retry", err)
			}
		})
	}
}

func TestHostLostResumeRequiresExplicitCLIAndRetainsUnknownRefusal(t *testing.T) {
	reg, a := hostLostFixture(t)
	// These kernel identities have no live PID, so the CLI preflight can inspect
	// them. CLI and private consumers must opt in; ordinary deferred claims
	// and relaunch still refuse host-lost generations.
	c := &agentCommand{loadRegistry: func() (coremetadata.Registry, error) { return reg, nil }}
	request := processAgentResumeRequest{options: processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: a.Binding.AgentUID}}}
	if _, err := c.processResumeCandidate(request); err == nil {
		t.Fatal("implicit host-lost resume")
	}
	request.options.allowHostLost = true
	if _, err := c.processResumeCandidate(request); err != nil {
		t.Fatal("CLI preflight", err)
	}
	p, _ := reg.Pane(a.Binding.PaneUID)
	p.Status.Activation = coremetadata.PaneActivation{}
	agent, _ := reg.Agent(a.Binding.AgentUID)
	agent.Status.Phase = coremetadata.PhaseOffline
	if _, err := c.processResumeCandidate(request); err == nil {
		t.Fatal("retired unknown without identities admitted")
	}
}

func TestHostLostLeaseRemovesOnlyEmptyPrivateDirectory(t *testing.T) {
	for _, kind := range []string{"empty", "nonempty", "public", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			_, a := hostLostFixture(t)
			path := filepath.Join(t.TempDir(), "registry.json")
			dir := claudeActivationLeaseDir(path, a.Binding.PaneUID, a.Binding.Generation)
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			if kind == "symlink" {
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "public" {
					_ = os.Chmod(dir, 0755)
				}
				if kind == "nonempty" {
					_ = os.WriteFile(filepath.Join(dir, "host.sock"), []byte("residue"), 0600)
				}
			}
			removeHostLostLease(path, a)
			_, err := os.Lstat(dir)
			if kind == "empty" {
				if !os.IsNotExist(err) {
					t.Fatal("empty private lease remains", err)
				}
			} else if err != nil {
				t.Fatal("unsafe lease removed", err)
			}
		})
	}
}
