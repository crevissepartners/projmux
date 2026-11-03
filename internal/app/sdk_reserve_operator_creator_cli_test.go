package app

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestPublicReserveAgentOperatorCreatorMatchesCreate reserves with Operator and a
// declared Creator and requires what a create records for the same input: the
// operator creator on the Agent and its Pane, the declaration reported in Notices
// with the CLI's line, and, after StartReservedAgent, the same record.
func TestPublicReserveAgentOperatorCreatorMatchesCreate(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	r := newStartFixture(t)
	ctx := reserveContext(t)
	parentReq := r.request()
	parentReq.Name = "parent"
	parent, err := r.client.ReserveAgent(ctx, parentReq)
	if err != nil {
		t.Fatalf("reserve parent: %v", err)
	}
	declared := parent.Agent.Metadata.UID
	req := r.request()
	req.Name = "operated"
	req.Creator = "uid:" + declared
	req.Operator = core.NewOperatorCreator(sdkOperatorCreatorClient)
	result, err := r.client.ReserveAgent(ctx, req)
	if err != nil {
		t.Fatalf("reserve with Operator: %v", err)
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	// The create path's decision for the same Registry and declaration.
	cli := &createCommand{}
	cli.withoutCreatorProvenance()
	if err = cli.recordOperatorCreator(sdkOperatorCreatorClient); err != nil {
		t.Fatal(err)
	}
	record, err := cli.decideCreator(ctx, canonicalCreateAgent, &reg, declared)
	if err != nil {
		t.Fatal(err)
	}
	want := record.annotations()
	check := func(stage string) {
		t.Helper()
		after, err := r.f.store.LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		agent, ok := after.Agent(result.Agent.Metadata.UID)
		if !ok {
			t.Fatalf("%s: reserved Agent missing", stage)
		}
		pane, ok := after.Pane(result.Binding.PaneUID)
		if !ok {
			t.Fatalf("%s: reserved Pane missing", stage)
		}
		assertCreatorRecord(t, *agent, *pane, want)
		if agent.Metadata.Annotations[coremetadata.AnnotationCreatorBasis] != coremetadata.CreatorBasisOperator {
			t.Fatalf("%s: creator basis %q, want operator", stage, agent.Metadata.Annotations[coremetadata.AnnotationCreatorBasis])
		}
	}
	check("reserved")
	line := "creator declaration not recorded: operator-client (--creator uid:" + declared + ")"
	var declarations []string
	for _, notice := range result.Notices {
		if strings.HasPrefix(notice, "creator declaration not recorded:") {
			declarations = append(declarations, notice)
		}
	}
	if len(declarations) != 1 || declarations[0] != line {
		t.Fatalf("declaration notices %q, want only %q", declarations, line)
	}
	started, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + result.Agent.Metadata.UID})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.stop(t, ctx, "uid:"+result.Agent.Metadata.UID, started.Binding)
	check("started")
	for _, notice := range started.Notices {
		if strings.HasPrefix(notice, "creator declaration not recorded:") {
			t.Fatalf("a start decided the creator again: %q", notice)
		}
	}
}

// TestPublicReserveAgentOperatorAloneRecordsNoDeclaration records the operator
// without a Creator: no declaration notice, the operator record on both.
func TestPublicReserveAgentOperatorAloneRecordsNoDeclaration(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	req := r.request()
	req.Operator = core.NewOperatorCreator(sdkOperatorCreatorClient)
	result, err := r.client.ReserveAgent(ctx, req)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	for _, notice := range result.Notices {
		if strings.HasPrefix(notice, "creator declaration not recorded:") {
			t.Fatalf("a reservation without Creator reported a declaration: %q", notice)
		}
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(result.Agent.Metadata.UID)
	pane, _ := reg.Pane(result.Binding.PaneUID)
	if agent == nil || pane == nil {
		t.Fatal("reserved Agent or Pane missing")
	}
	assertCreatorRecord(t, *agent, *pane, coremetadata.OperatorCreatorAnnotations(sdkOperatorCreatorClient))
}

// TestPublicReserveAgentRefusesAnInvalidOperatorWithoutEffect refuses a client
// name outside the operator client rule with CreateAgent's Code before anything
// changes.
func TestPublicReserveAgentRefusesAnInvalidOperatorWithoutEffect(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	before, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	req := r.request()
	req.Operator = core.NewOperatorCreator("Not Valid")
	_, err = r.client.ReserveAgent(ctx, req)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateCreatorInvalid {
		t.Fatalf("reserve with a bad Operator: %v, want %s", err, projmux.CodeCreateCreatorInvalid)
	}
	after, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if registryCounts(before) != registryCounts(after) {
		t.Fatalf("a refused reservation changed the Registry: %v -> %v", registryCounts(before), registryCounts(after))
	}
}

// TestReserveOwnerRefusesAnOperatorOnANonSDKRequest keeps the refusal for a
// reservation request that is not the SDK's frozen-context request: the owner
// route records an operator creator only for the SDK.
func TestReserveOwnerRefusesAnOperatorOnANonSDKRequest(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	var reply processDetachedReply
	var decodeErr error
	var wg sync.WaitGroup
	wg.Go(func() { decodeErr = json.NewDecoder(client).Decode(&reply) })
	req := processDetachedRequest{Reserve: true, Options: processAgentCreateOptions{
		Project:               selector.Ref{Kind: coremetadata.KindProject, UID: "proj-test"},
		Window:                selector.Ref{Kind: coremetadata.KindWindow, UID: "win-test"},
		OperatorCreatorClient: sdkOperatorCreatorClient,
	}}
	err := runProcessDetachedReserve(server, json.NewEncoder(server), json.NewDecoder(server), req, coremetadata.ProcessIdentity{}, nil)
	wg.Wait()
	if err == nil || err.Error() != "process-detached-operator-creator-sdk-only" {
		t.Fatalf("owner error %v, want process-detached-operator-creator-sdk-only", err)
	}
	if decodeErr != nil || reply.Error != err.Error() {
		t.Fatalf("reply %+v (decode %v), want the same refusal", reply, decodeErr)
	}
}
