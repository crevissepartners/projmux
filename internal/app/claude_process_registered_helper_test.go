package app

import (
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"os"
	"reflect"
	"testing"
)

func TestRegisteredClaudeHelperReparentSnapshot(t *testing.T) {
	for _, change := range []string{"check", "lookup", "hook-live", "hook-reused", "hook-unreadable", "second-unavailable", "second-uid", "second-birth", "arbitrary-parent", "supervisor-reused", "host-reused", "child-parent", "late-parent", "old-parent", "lease-replaced", "generation-replaced", "binding-replaced", "cleared", "registry-changed", "register", "helper", "input", "foreground", "mixed", "ordinary-error", "second-transition", "observe", "questions", "request-binding", "not-ready"} {
		t.Run(change, func(t *testing.T) {
			reg, activation := hostLostFixture(t)
			store := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
			if _, _, err := store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg.Clone(); return nil }); err != nil {
				t.Fatal(err)
			}
			host, child := activation.HostProcess, activation.Child
			supervisor := coremetadata.ProcessIdentity{PID: 987653, OwnerUID: host.OwnerUID, Start: "supervisor"}
			hook := coremetadata.ProcessIdentity{PID: 987654, OwnerUID: host.OwnerUID, Start: "hook"}
			lease := coremetadata.ProcessIdentity{PID: 987655, OwnerUID: host.OwnerUID, Start: "helper"}
			b := processSchemaBinding(activation.Binding)
			registration := &coremetadata.ClaudeRegistration{Ready: true, Authority: coremetadata.ClaudeAuthorityRef{Process: child, LeaseProcess: lease, SessionID: "session", RegistrationGeneration: "registered"}}
			s := claudeProcessService{registryPath: store.Path(), binding: b, ownedProcess: child, ownedHostProcess: host, registration: registration, registrationGeneration: "registered", registeredHelper: &claudeRegisteredHelper{Hook: hook, Lease: lease, Generation: "registered", Binding: b}}
			request := claudeProcessCheck{Binding: b, Session: "session", Lookup: change == "lookup"}
			failure := error(localipc.ParentTransitionWitness{Process: lease, OldParent: hook.PID, NewParent: supervisor.PID})
			switch change {
			case "old-parent":
				failure = localipc.ParentTransitionWitness{Process: lease, OldParent: 123, NewParent: supervisor.PID}
			case "lease-replaced":
				s.registeredHelper.Lease.Start = "replacement"
			case "generation-replaced":
				s.registrationGeneration = "replacement"
			case "binding-replaced":
				s.registeredHelper.Binding.Generation = "replacement"
			case "cleared":
				s.registeredHelper = nil
			case "registry-changed":
				if _, _, err := store.UpdateConvergent(func(r *coremetadata.Registry) error {
					a, _ := r.Agent(b.Agent)
					a.Status.Phase = coremetadata.PhaseOffline
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "register":
				request.Register = true
			case "helper":
				request.Helper = &claudeProcessRegistration{}
			case "input":
				request.Input = &claudeProcessInput{}
			case "foreground":
				request.Foreground = &processForegroundRequest{}
			case "mixed":
				request.Register, request.Lookup = true, true
			case "observe":
				request.Observe = &b
			case "questions":
				request.Questions = &processQuestionRequest{}
			case "request-binding":
				request.Binding.Generation = "replacement"
			case "not-ready":
				registration.Ready = false
			case "ordinary-error":
				failure = errors.New("peer unavailable")
			}
			before, _ := store.LoadReadOnly()
			history := s.registeredHelper
			var historyValue claudeRegisteredHelper
			if history != nil {
				historyValue = *history
			}
			peerCalls := 0
			peer := func() (coremetadata.ProcessIdentity, int, error) {
				peerCalls++
				actual, parent := lease, supervisor.PID
				switch change {
				case "second-transition":
					return coremetadata.ProcessIdentity{}, 0, localipc.ParentTransitionWitness{Process: lease, OldParent: supervisor.PID, NewParent: 123}
				case "second-unavailable":
					return coremetadata.ProcessIdentity{}, 0, errors.New("unavailable")
				case "second-uid":
					actual.OwnerUID++
				case "second-birth":
					actual.Start = "replacement"
				case "arbitrary-parent":
					parent = 123
				}
				return actual, parent, nil
			}
			counts := map[int]int{}
			read := func(pid int) (coremetadata.ProcessIdentity, int, error) {
				counts[pid]++
				switch pid {
				case hook.PID:
					if change == "hook-live" {
						return hook, child.PID, nil
					}
					if change == "hook-reused" {
						x := hook
						x.Start = "replacement"
						return x, 1, nil
					}
					if change == "hook-unreadable" {
						return coremetadata.ProcessIdentity{}, 0, os.ErrPermission
					}
					return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
				case child.PID:
					parent := supervisor.PID
					if change == "child-parent" || (change == "late-parent" && counts[pid] == 2) {
						parent = 123
					}
					return child, parent, nil
				case supervisor.PID:
					actual := supervisor
					if change == "supervisor-reused" && counts[pid] == 2 {
						actual.Start = "replacement"
					}
					return actual, host.PID, nil
				case host.PID:
					actual := host
					if change == "host-reused" && counts[pid] == 2 {
						actual.Start = "replacement"
					}
					return actual, 1, nil
				}
				return coremetadata.ProcessIdentity{}, 0, errors.New("unexpected PID")
			}
			actual, parent, err := s.recheckRegisteredHelper(request, failure, peer, read)
			positive := change == "check" || change == "lookup"
			if positive {
				if err != nil || actual != lease || parent != supervisor.PID || peerCalls != 1 {
					t.Fatalf("stable registered helper refused: %+v %d %v calls=%d", actual, parent, err, peerCalls)
				}
			} else if err == nil || actual.PID != 0 || parent != 0 {
				t.Fatalf("unsafe recheck: %+v %d %v", actual, parent, err)
			}
			if peerCalls > 1 {
				t.Fatal("unbounded recheck")
			}
			after, _ := store.LoadReadOnly()
			if !reflect.DeepEqual(before, after) || s.registration != registration || s.registeredHelper != history || (history != nil && *history != historyValue) {
				t.Fatal("read-only recheck mutated registration/Registry")
			}
		})
	}
}

func TestRegisteredClaudeHelperHistoryFollowsAdmission(t *testing.T) {
	s := claudeProcessService{}
	hook := coremetadata.ProcessIdentity{PID: 1, OwnerUID: 1000, Start: "hook"}
	lease := coremetadata.ProcessIdentity{PID: 2, OwnerUID: 1000, Start: "lease"}
	next := &coremetadata.ClaudeRegistration{Ready: true, Authority: coremetadata.ClaudeAuthorityRef{LeaseProcess: lease, RegistrationGeneration: "one"}}
	claim := claudeProcessRegistration{Hook: hook}
	if !s.commitRegisteredHelper(claim, next, "one") {
		t.Fatal("initial history refused")
	}
	s.registration, s.registrationGeneration = next, "one"
	original := *s.registeredHelper
	if !s.commitRegisteredHelper(claim, next, "one") || *s.registeredHelper != original {
		t.Fatal("idempotent history changed")
	}
	claim.Hook.Start = "replacement"
	if s.commitRegisteredHelper(claim, next, "one") || *s.registeredHelper != original {
		t.Fatal("same generation accepted replacement Hook")
	}
	next.Authority.LeaseProcess.Start = "new-lease"
	next.Authority.RegistrationGeneration = "two"
	if !s.commitRegisteredHelper(claim, next, "two") || s.registeredHelper.Generation != "two" || s.registeredHelper.Lease != next.Authority.LeaseProcess {
		t.Fatal("new admission did not replace history")
	}
	if !s.commitRegisteredHelper(claudeProcessRegistration{Phase: "clear"}, nil, "two") || s.registeredHelper != nil {
		t.Fatal("clear retained history")
	}
}
