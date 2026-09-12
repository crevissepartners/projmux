package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

func TestRecoveryFailureEvidenceRetainsPartialIdentityWithoutContent(t *testing.T) {
	store, identity, endpoint, _ := phase1GenerationAuthorityFixture(t)
	agent, _ := store.registry.Agent(identity.AgentUID)
	agent.Status.Reason = "PRIVATE-PROMPT-AND-SECRET"
	agent.Spec.Workspace.CWD = "/PRIVATE-PROMPT-AND-SECRET"
	pane, _ := store.registry.Pane(identity.PaneUID)
	pane.Status.Activation.Codex.Authority = nil
	attempt := &installedRecoveryAttempt{InputIndex: 0, Sample: "survivor", Stage: "authority-timeout", AgentUID: identity.AgentUID}
	observed := recoveryRegistryObservation(store.registry, identity.AgentUID, endpoint.EndpointGenerationID, nil)
	observed.Error = recoveryError(errors.New("PRIVATE-PROMPT-AND-SECRET"))
	attempt.observe(observed)
	if observed.Stage != "authority-missing-or-invalid" || observed.Identity.Pane != identity.PaneUID || observed.Identity.Thread != identity.ThreadID || observed.Identity.Runtime != identity.RuntimeID {
		t.Fatalf("partial identity lost: %+v", observed)
	}
	raw, err := json.Marshal(installedRecoveryLedger{Result: "FAIL", Submissions: 1, Rows: []installedRecoveryRow{{Attempts: []*installedRecoveryAttempt{attempt}}}})
	if err != nil || strings.Contains(string(raw), "PRIVATE-PROMPT-AND-SECRET") || !strings.Contains(string(raw), `"submissions":1`) || !strings.Contains(string(raw), identity.AgentUID) {
		t.Fatalf("unsafe or missing attempt evidence: %s err=%v", raw, err)
	}
	for i := range 50 {
		observed.Stage = strings.Repeat("x", i)
		attempt.observe(observed)
	}
	if len(attempt.Observations) != 32 || attempt.Observations[31].Stage != observed.Stage {
		t.Fatal("bounded evidence lost the terminal observation")
	}
}

type recoveryControlReaderFixture struct {
	binding    exactAgentControlBinding
	resolveErr error
	response   agentControlResponse
	callErr    error
	requests   []agentControlRequest
}

func (fixture *recoveryControlReaderFixture) resolveControlBinding(string, string) (exactAgentControlBinding, error) {
	return fixture.binding, fixture.resolveErr
}
func (fixture *recoveryControlReaderFixture) callControl(_ exactAgentControlBinding, request agentControlRequest) (agentControlResponse, error) {
	fixture.requests = append(fixture.requests, request)
	return fixture.response, fixture.callErr
}

func TestRecoveryReadinessDistinguishesRefusalsAndOnlyRequestsStatus(t *testing.T) {
	for _, kind := range []string{"missing-authority", "generation-mismatch", "retired", "binding-refusal", "transport", "status-refused", "not-startable", "ready"} {
		t.Run(kind, func(t *testing.T) {
			store, identity, endpoint, authority := phase1GenerationAuthorityFixture(t)
			var retired *coremetadata.CodexAuthorityRef
			want := ""
			switch kind {
			case "missing-authority":
				pane, _ := store.registry.Pane(identity.PaneUID)
				pane.Status.Activation.Codex.Authority = nil
				want = "authority-missing-or-invalid"
			case "generation-mismatch":
				want = "generation-mismatch"
			case "retired":
				retired = authority
				want = "authority-retired"
			}
			generation := endpoint.EndpointGenerationID
			if kind == "generation-mismatch" {
				generation = "other"
			}
			out := recoveryRegistryObservation(store.registry, identity.AgentUID, generation, retired)
			fixture := &recoveryControlReaderFixture{binding: exactAgentControlBinding{Identity: identity, Endpoint: *endpoint, Epoch: "test-epoch"}, response: agentControlResponse{OK: true, Availability: agentControlAvailability{Start: true}, Message: "PRIVATE-RESPONSE", Approvals: []agentPendingApproval{{Command: "PRIVATE-COMMAND"}}}}
			switch kind {
			case "binding-refusal":
				fixture.resolveErr = &exactAgentControlBindingError{Reason: "PRIVATE-ERROR"}
				want = "control-binding"
			case "transport":
				fixture.callErr = errors.New("PRIVATE-TRANSPORT")
				want = "control-status-transport"
			case "status-refused":
				fixture.response.OK = false
				fixture.response.Code = "PRIVATE-CODE"
				want = "control-status-refused"
			case "not-startable":
				fixture.response.Availability.Start = false
				want = "control-not-startable"
			case "ready":
				want = "ready"
			}
			if out.Stage == "control-pending" {
				out = readInstalledRecoveryControl(fixture, out, true)
			}
			if out.Stage != want {
				t.Fatalf("stage=%s want=%s", out.Stage, want)
			}
			for _, request := range fixture.requests {
				if request.Operation != agentControlOpStatus || request.Text != "" || request.Decision != "" || request.RequestKey != "" {
					t.Fatal("evidence issued a provider mutation")
				}
			}
			raw, err := json.Marshal(out)
			if err != nil || strings.Contains(string(raw), "PRIVATE-") {
				t.Fatalf("content escaped evidence: %s %v", raw, err)
			}
		})
	}
}

func TestRecoveryObserverEvidenceFiltersIdentityVocabularyAndContent(t *testing.T) {
	identity := installedRecoveryAgent{Thread: "thread", Runtime: "%7"}
	entries := []aiIngestLogEntry{
		{At: time.Now().UTC().Format(time.RFC3339Nano), Source: aiIngestCodexObserverSource, Event: string(codexObserverTransitionFallback), Reason: "PRIVATE-CAUSE", Pane: "%7", ThreadID: "thread", CWD: "PRIVATE-PATH", Epoch: "PRIVATE-EPOCH"},
		{At: time.Now().UTC().Format(time.RFC3339Nano), Source: aiIngestCodexObserverSource, Event: string(codexObserverTransitionFallback), Reason: aiIngestReason("protocol-error"), Pane: "%8", ThreadID: "foreign"},
	}
	got := recoveryObserverTransitions(entries, identity)
	raw, err := json.Marshal(got)
	if err != nil || len(got) != 1 || got[0].Reason != string(codexObserverReasonUnrecorded) || strings.Contains(string(raw), "PRIVATE-") || strings.Contains(string(raw), "foreign") {
		t.Fatalf("unsafe journal projection=%s err=%v", raw, err)
	}
}

type recoveryConnectionEndpoint struct{ *brokerTestEndpoint }

func (*recoveryConnectionEndpoint) NegotiatedVersion() string { return "0.151.0" }

type recoveryConnectionOwned struct {
	peer   codexappserver.PeerIdentity
	closed bool
	reads  int
}

func (owned *recoveryConnectionOwned) PeerIdentity() codexappserver.PeerIdentity { return owned.peer }
func (owned *recoveryConnectionOwned) Close() error                              { owned.closed = true; return nil }
func (owned *recoveryConnectionOwned) ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error) {
	owned.reads++
	return codexappserver.LifecycleSnapshot{}, errors.New("forbidden diagnostic thread read")
}

func TestConnectionDiagnosticInitializesAndClosesWithoutProviderOperations(t *testing.T) {
	for _, failOwned := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "owned-refused"}[failOwned], func(t *testing.T) {
			endpoint := &recoveryConnectionEndpoint{newBrokerTestEndpoint()}
			owned := &recoveryConnectionOwned{peer: endpoint.peer}
			current := func(context.Context) (codexNativeEndpointRoute, error) {
				return codexNativeEndpointRoute{Endpoint: coremetadata.CodexEndpointRef{StateDomainID: "fixture", EndpointGenerationID: "codex-0.151.0"}, State: coremetadata.CodexGenerationCurrent, Default: true, TUIExecutable: "/fixture/codex"}, nil
			}
			opens := 0
			stages := []installedConnectionStage{}
			err := probeInstalledRecoveryConnections(context.Background(), "0.151.0", current, func(codexNativeEndpointRoute) (codexbroker.Opener, codexbroker.LifecycleOpener, error) {
				return func(context.Context) (codexbroker.Endpoint, error) { opens++; return endpoint, nil }, func(_ context.Context, peer codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
					if !codexappserver.SamePeerIdentity(peer, endpoint.peer) {
						t.Fatal("owned opener lost exact witness")
					}
					if failOwned {
						return nil, codexappserver.ErrEndpointChanged
					}
					return owned, nil
				}, nil
			}, func(stage installedConnectionStage) { stages = append(stages, stage) })
			if (err != nil) != failOwned || opens != 1 || !endpoint.closed || (!failOwned && !owned.closed) || len(endpoint.requests) != 0 || len(endpoint.answers) != 0 || owned.reads != 0 {
				t.Fatalf("diagnostic mutated/retried/leaked endpoint: opens=%d requests=%v reads=%d err=%v", opens, endpoint.requests, owned.reads, err)
			}
			if len(stages) < 4 || stages[len(stages)-1].Stage != "shared-close" {
				t.Fatal("diagnostic lost its terminal close evidence")
			}
		})
	}
}

func TestConnectionDiagnosticRefusesModelAuthAndProviderInputFields(t *testing.T) {
	input := map[string]any{"root": "/tmp/connection-proof", "binary": "/candidate/projmux", "binarySHA256": strings.Repeat("b", 64), "sourceHead": strings.Repeat("a", 40), "sourceTree": strings.Repeat("c", 40), "releases": []string{"/payload/0.151.0", "/payload/0.154.0"}, "hostNamespaces": map[string]string{"pid": "host-pid", "mnt": "host-mnt", "net": "host-net"}, "evidence": "/artifact/connection.json"}
	valid, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeInstalledConnectionInput(valid); err != nil {
		t.Fatalf("complete zero-input fixture refused: %v", err)
	}
	for _, field := range []string{"authFile", "model", "inputs", "thread", "agent"} {
		input[field] = "PRIVATE-INPUT"
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeInstalledConnectionInput(raw); err == nil || strings.Contains(err.Error(), "PRIVATE-INPUT") {
			t.Fatalf("forbidden diagnostic field %s accepted or echoed", field)
		}
		delete(input, field)
	}
	if _, err := decodeInstalledConnectionInput(append(valid, []byte(` {"model":"PRIVATE-INPUT"}`)...)); err == nil {
		t.Fatal("trailing input object accepted")
	}
}
