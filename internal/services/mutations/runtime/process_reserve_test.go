package runtime

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const (
	reserveTestOperation = "op-00000000000000aa"
	reserveOtherOp       = "op-00000000000000bb"
)

func reservePane(operation string) coremetadata.Pane {
	return coremetadata.Pane{
		Metadata: coremetadata.ObjectMeta{UID: "pane-r", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agent-r"}},
		Status:   coremetadata.PaneStatus{ProcessSession: &coremetadata.ProcessSessionRecord{Binding: coremetadata.ProcessBinding{OperationID: operation, WindowUID: "win-r"}}},
	}
}

func reserveRegistry(panes ...coremetadata.Pane) func() (coremetadata.Registry, error) {
	return func() (coremetadata.Registry, error) { return coremetadata.Registry{Panes: panes}, nil }
}

func retiredWorker(t *testing.T) coremetadata.ProcessIdentity {
	t.Helper()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	identity, _, err := localipc.Process(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	return identity
}

func TestReserveCleanupReportsRemainOfItsOperation(t *testing.T) {
	err := AwaitDetachedProcessReserveCleanup(reserveRegistry(reservePane(reserveTestOperation)), retiredWorker(t), reserveTestOperation)
	var remains *ReserveRemains
	if !errors.As(err, &remains) || remains.Operation != reserveTestOperation || remains.Binding.Agent != "agent-r" || remains.Binding.Pane != "pane-r" || remains.Binding.Window != "win-r" || remains.Binding.Operation != reserveTestOperation {
		t.Fatalf("remaining reservation: %v, want a ReserveRemains with its exact Binding", err)
	}
	if !strings.Contains(err.Error(), "agent uid:agent-r") || !strings.Contains(err.Error(), "projmux delete agent uid:agent-r") {
		t.Fatalf("remaining reservation text %q lacks its refs", err)
	}
}

func TestReserveFailureCarriesTheBindingCleanupFound(t *testing.T) {
	remainsErr := AwaitDetachedProcessReserveCleanup(reserveRegistry(reservePane(reserveTestOperation)), retiredWorker(t), reserveTestOperation)
	reply, err := reserveFailure(ProcessDetachedReply{}, remainsErr)
	var failure *ProcessCreateFailure
	if !errors.As(err, &failure) || failure.Binding.Agent != "agent-r" || reply.Binding.Agent != "agent-r" || failure.Binding.Pane != "pane-r" {
		t.Fatalf("failure %+v reply binding %+v, want the Binding cleanup found", failure, reply.Binding)
	}
	known := ProcessDetachedReply{Binding: processhost.Binding{Agent: "agent-reported"}}
	reply, err = reserveFailure(known, remainsErr)
	if !errors.As(err, &failure) || failure.Binding.Agent != "agent-reported" || reply.Binding.Agent != "agent-reported" {
		t.Fatalf("a reported Binding was replaced: %+v", failure)
	}
	_, err = reserveFailure(ProcessDetachedReply{}, errors.New("plain"))
	if !errors.As(err, &failure) || failure.Binding.Agent != "" {
		t.Fatalf("a failure without remains invented a Binding: %+v", failure)
	}
}

func TestReserveCleanupIgnoresAnotherOperation(t *testing.T) {
	if err := AwaitDetachedProcessReserveCleanup(reserveRegistry(reservePane(reserveOtherOp)), retiredWorker(t), reserveTestOperation); err != nil {
		t.Fatalf("a reservation of another operation was reported: %v", err)
	}
	if err := AwaitDetachedProcessReserveCleanup(reserveRegistry(), retiredWorker(t), reserveTestOperation); err != nil {
		t.Fatalf("an empty Registry was reported: %v", err)
	}
}

func TestReserveCleanupNeverReturnsNilWithoutAnOperation(t *testing.T) {
	if err := AwaitDetachedProcessReserveCleanup(reserveRegistry(), retiredWorker(t), ""); err == nil {
		t.Fatal("a cleanup without an operation id returned nil")
	}
}

func TestReserveCleanupKillsAWorkerThatOutlivesTheBound(t *testing.T) {
	bound, settle := reserveCleanupBound, reserveKillSettle
	reserveCleanupBound, reserveKillSettle = 100*time.Millisecond, 5*time.Second
	t.Cleanup(func() { reserveCleanupBound, reserveKillSettle = bound, settle })
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _ = child.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = child.Process.Kill() })
	identity, _, err := localipc.Process(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	err = AwaitDetachedProcessReserveCleanup(reserveRegistry(reservePane(reserveTestOperation)), identity, reserveTestOperation)
	if err == nil || !strings.Contains(err.Error(), "kill attempted") || !strings.Contains(err.Error(), "it has exited") || !strings.Contains(err.Error(), "process-reserve-remaining") {
		t.Fatalf("outlived worker: %v, want the kill attempt, its outcome and the remaining refs", err)
	}
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker survived the cleanup")
	}
}

func TestReserveOperationShape(t *testing.T) {
	for id, want := range map[string]bool{reserveTestOperation: true, "": false, "op-xyz": false, "OP-00000000000000aa": false, "op-00000000000000aaa": false} {
		if ValidReserveOperation(id) != want {
			t.Fatalf("ValidReserveOperation(%q) = %t, want %t", id, !want, want)
		}
	}
}
