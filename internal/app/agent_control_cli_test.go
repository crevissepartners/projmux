package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

type staticAgentControlBinding struct {
	live     agentControlLive
	observed bool
	err      error
}

type exactControlRouteRunner struct {
	target tmuxTransport
	calls  [][]string
	frame  []byte
}

func (r *exactControlRouteRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if name != "tmux" || len(args) < 3 || args[0] != r.target.Flag() || args[1] != r.target.Value {
		return nil, errors.New("unexpected control route")
	}
	switch args[2] {
	case "list-panes":
		return []byte("pan-alpha-codex" + tmuxRowSepFormat + "%7\n"), nil
	case "display-message":
		if r.frame != nil {
			return append([]byte(nil), r.frame...), nil
		}
		return []byte(strings.Join([]string{"%7", "pan-alpha-codex", "thread-1", codexAuthorityControlPlane, "epoch-1", "ready"}, tmuxRowSepFormat) + "\n"), nil
	default:
		return nil, errors.New("unexpected control operation")
	}
}

func TestAgentControlBindingFrameAcceptsOnlySupportedExactSixFieldSpellings(t *testing.T) {
	fields := []string{" %7 ", "pan-alpha-codex", `thread\\literal`, codexAuthorityControlPlane, "epoch-1", ` ready\\n `}
	want := agentControlLive{RuntimeID: fields[0], PaneUID: fields[1], ThreadID: fields[2], Authority: fields[3], Epoch: fields[4], Reason: fields[5]}
	for _, test := range []struct {
		name      string
		separator string
		ending    string
	}{
		{name: "literal octal", separator: tmuxRowSepFormat, ending: "\n"},
		{name: "raw unit separator", separator: tmuxRowSep, ending: "\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseAgentControlBindingFrame([]byte(strings.Join(fields, test.separator) + test.ending))
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("binding = %#v, want byte-faithful %#v", got, want)
			}
		})
	}
}

func TestAgentControlBindingFrameRejectsMalformedMissingExtraAndMixedFrames(t *testing.T) {
	for _, test := range []struct {
		name  string
		frame string
	}{
		{name: "missing literal field", frame: strings.Join([]string{"%7", "pane", "thread", "authority", "epoch"}, tmuxRowSepFormat) + "\n"},
		{name: "extra literal field", frame: strings.Join([]string{"%7", "pane", "thread", "authority", "epoch", "reason", "extra"}, tmuxRowSepFormat) + "\n"},
		{name: "missing raw field", frame: strings.Join([]string{"%7", "pane", "thread", "authority", "epoch"}, tmuxRowSep) + "\n"},
		{name: "extra raw field", frame: strings.Join([]string{"%7", "pane", "thread", "authority", "epoch", "reason", "extra"}, tmuxRowSep) + "\n"},
		{name: "mixed separators", frame: "%7" + tmuxRowSep + "pane" + strings.Repeat(tmuxRowSepFormat+"field", 4) + "\n"},
		{name: "separator missing", frame: "one-field\n"},
		{name: "multiple lines", frame: strings.Join([]string{"%7", "pane", "thread", "authority", "epoch", "reason\nother"}, tmuxRowSepFormat) + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseAgentControlBindingFrame([]byte(test.frame))
			var frameErr *agentControlBindingFrameError
			if err == nil || !errors.As(err, &frameErr) {
				t.Fatalf("error = %v, want typed frame refusal", err)
			}
		})
	}
}

func (b *staticAgentControlBinding) Live(context.Context, string) (agentControlLive, bool, error) {
	return b.live, b.observed, b.err
}

type scriptedAgentControlPicker struct {
	answers  []intpicker.Result
	rendered []intpicker.Options
}

func phase6CLIIdentity() codexLifecycleIdentity {
	return codexLifecycleIdentity{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
}

func phase6CLIEndpoint() coremetadata.CodexEndpointRef {
	return coremetadata.CodexEndpointRef{StateDomainID: "control-test-domain", EndpointGenerationID: "control-test-generation"}
}

func (p *scriptedAgentControlPicker) Run(options intpicker.Options) (intpicker.Result, error) {
	p.rendered = append(p.rendered, options)
	if len(p.answers) == 0 {
		return intpicker.Result{Closed: true}, nil
	}
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer, nil
}

func exactControlCLICommand(t *testing.T) (*agentCommand, *fakeResourceStore, *staticAgentControlBinding) {
	t.Helper()
	store := newFakeResourceStore(t)
	pane, _ := store.registry.Pane("pan-alpha-codex")
	agent, _ := store.registry.Agent("agt-alpha-codex")
	pane.Status.Activation = coremetadata.PaneActivation{Generation: "generation-1", RuntimeID: "%7", AgentUID: agent.Metadata.UID}
	agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
	endpoint := phase6CLIEndpoint()
	if err := store.mutator().StageCodexEndpoint(&store.registry, agent.Metadata.UID, endpoint); err != nil {
		t.Fatalf("stage endpoint fixture: %v", err)
	}
	if changed, err := store.mutator().BindCodexActivation(&store.registry, coremetadata.CodexActivationObservation{
		AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: "generation-1", ThreadID: "thread-1", TurnID: "turn-1",
		Endpoint: endpoint,
	}); err != nil || !changed {
		t.Fatalf("bind fixture: changed=%t err=%v", changed, err)
	}
	pane.Status.Activation.Codex.Authority = &coremetadata.CodexAuthorityRef{
		StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
		BrokerRuntimeID: "control-test-broker", ConnectionEpoch: 1, BindingEpoch: 1,
	}
	cmd, _, _ := newTestAgentCommand(t, store)
	binding := &staticAgentControlBinding{observed: true, live: agentControlLive{RuntimeID: "%7", PaneUID: pane.Metadata.UID, ThreadID: "thread-1", Authority: codexAuthorityControlPlane, Epoch: "epoch-1"}}
	cmd.controlBinding = binding
	cmd.controlPaths = func() (config.Paths, error) { return config.Paths{StateDir: "/tmp/projmux-phase6-cli"}, nil }
	return cmd, store, binding
}

func TestAgentControlCLIExactTurnDispatchAndTextFidelity(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		op   string
		text string
	}{
		{name: "start", args: []string{"turn", "start", "uid:agt-alpha-codex", "--", "  exact start  "}, op: agentControlOpStart, text: "  exact start  "},
		{name: "steer", args: []string{"turn", "steer", "uid:agt-alpha-codex", "--", "\nexact steer\t"}, op: agentControlOpSteer, text: "\nexact steer\t"},
		{name: "interrupt", args: []string{"turn", "interrupt", "uid:agt-alpha-codex"}, op: agentControlOpInterrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _, _ := exactControlCLICommand(t)
			var calls []agentControlRequest
			cmd.controlCall = func(_ context.Context, stateDir string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
				if stateDir != "/tmp/projmux-phase6-cli" || !endpoint.Same(phase6CLIEndpoint()) || identity != phase6CLIIdentity() {
					t.Fatalf("transport binding state=%q endpoint=%+v identity=%+v", stateDir, endpoint, identity)
				}
				calls = append(calls, request)
				return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
			}
			if _, _, err := runRoute(t, cmd, test.args...); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0].Operation != test.op || calls[0].Text != test.text || calls[0].Identity != phase6CLIIdentity() || calls[0].Epoch != "epoch-1" {
				t.Fatalf("control calls = %+v", calls)
			}
		})
	}
}

func TestAgentControlCLIPublicTurnsDispatchFromBothSupportedTmuxFrames(t *testing.T) {
	for _, frame := range []struct {
		name      string
		separator string
	}{
		{name: "tmux literal octal", separator: tmuxRowSepFormat},
		{name: "raw compatibility", separator: tmuxRowSep},
	} {
		for _, turn := range []struct {
			name string
			args []string
			op   string
		}{
			{name: "start", args: []string{"turn", "start", "uid:agt-alpha-codex", "--", "start exact"}, op: agentControlOpStart},
			{name: "steer", args: []string{"turn", "steer", "uid:agt-alpha-codex", "--", "steer exact"}, op: agentControlOpSteer},
			{name: "interrupt", args: []string{"turn", "interrupt", "uid:agt-alpha-codex"}, op: agentControlOpInterrupt},
		} {
			t.Run(frame.name+"/"+turn.name, func(t *testing.T) {
				cmd, _, _ := exactControlCLICommand(t)
				target := tmuxTransport{Kind: tmuxSocketName, Value: "exact-control", Source: tmuxSocketNameSource}
				tmux := &exactControlRouteRunner{
					target: target,
					frame:  []byte(strings.Join([]string{"%7", "pan-alpha-codex", "thread-1", codexAuthorityControlPlane, "epoch-1", "ready"}, frame.separator) + "\n"),
				}
				cmd.controlBinding = nil
				cmd.controlRunner = tmux
				cmd.controlRoute = func(context.Context) (runtimeMutationRoute, error) { return runtimeMutationRoute{target: target}, nil }
				var calls []agentControlRequest
				cmd.controlCall = func(_ context.Context, stateDir string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
					if stateDir != "/tmp/projmux-phase6-cli" || !endpoint.Same(phase6CLIEndpoint()) || identity != phase6CLIIdentity() || request.Identity != identity || request.Epoch != "epoch-1" {
						t.Fatalf("dispatch state=%q endpoint=%+v identity=%+v request=%+v", stateDir, endpoint, identity, request)
					}
					calls = append(calls, request)
					return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
				}
				if _, _, err := runRoute(t, cmd, turn.args...); err != nil {
					t.Fatal(err)
				}
				if len(calls) != 1 || calls[0].Operation != turn.op {
					t.Fatalf("control calls = %+v", calls)
				}
			})
		}
	}
}

func TestAgentControlCLIMalformedTmuxFramesCallNoAppServer(t *testing.T) {
	for _, test := range []struct {
		name  string
		frame string
	}{
		{name: "missing", frame: strings.Join([]string{"%7", "pane", "thread", "authority", "epoch"}, tmuxRowSepFormat) + "\n"},
		{name: "extra", frame: strings.Join([]string{"%7", "pane", "thread", "authority", "epoch", "reason", "extra"}, tmuxRowSepFormat) + "\n"},
		{name: "mixed", frame: "%7" + tmuxRowSep + "pane" + strings.Repeat(tmuxRowSepFormat+"field", 4) + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _, _ := exactControlCLICommand(t)
			target := tmuxTransport{Kind: tmuxSocketName, Value: "exact-control", Source: tmuxSocketNameSource}
			cmd.controlBinding = nil
			cmd.controlRunner = &exactControlRouteRunner{target: target, frame: []byte(test.frame)}
			cmd.controlRoute = func(context.Context) (runtimeMutationRoute, error) { return runtimeMutationRoute{target: target}, nil }
			calls := 0
			cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
				calls++
				return agentControlResponse{}, errors.New("must not be called")
			}
			_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex")
			var frameErr *agentControlBindingFrameError
			if err == nil || !errors.As(err, &frameErr) || calls != 0 {
				t.Fatalf("error=%v typed=%#v app-server calls=%d", err, frameErr, calls)
			}
		})
	}
}

func TestAgentApprovalCLIReviewNoopAndFocusOnlyFlows(t *testing.T) {
	pending := agentPendingApproval{RequestID: "7", Kind: codexappserver.ApprovalCommand, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Command: "make test", CWD: "/work", Decisions: []codexappserver.ApprovalDecision{codexappserver.DecisionAccept}}
	for _, test := range []struct {
		name      string
		answer    string
		wantOps   []string
		wantFocus bool
		wantErr   bool
	}{
		{name: "review once", answer: "decision:accept", wantOps: []string{agentControlOpApprovals, agentControlOpReview}},
		{name: "detail noop", answer: settingsNoopValue, wantOps: []string{agentControlOpApprovals}, wantErr: true},
		{name: "focus only", answer: "open", wantOps: []string{agentControlOpApprovals}, wantFocus: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _, _ := exactControlCLICommand(t)
			picker := &scriptedAgentControlPicker{answers: []intpicker.Result{{Value: test.answer}}}
			cmd.controlPicker = picker
			focus := &recordingArgv{}
			cmd.focus = focus
			var calls []agentControlRequest
			cmd.controlCall = func(_ context.Context, _ string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
				if !endpoint.Same(phase6CLIEndpoint()) || identity != phase6CLIIdentity() || request.Identity != identity || request.Epoch != "epoch-1" {
					t.Fatalf("approval binding endpoint=%+v identity=%+v request=%+v", endpoint, identity, request)
				}
				calls = append(calls, request)
				if request.Operation == agentControlOpApprovals {
					return agentControlResponse{OK: true, Approvals: []agentPendingApproval{pending}}, nil
				}
				return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
			}
			_, _, err := runRoute(t, cmd, "approval", "review", "uid:agt-alpha-codex", "--request", "7")
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%t", err, test.wantErr)
			}
			var ops []string
			for _, call := range calls {
				ops = append(ops, call.Operation)
			}
			if !reflect.DeepEqual(ops, test.wantOps) {
				t.Fatalf("operations=%v want=%v", ops, test.wantOps)
			}
			if test.name == "review once" && (calls[1].RequestKey != "7" || calls[1].Decision != "accept") {
				t.Fatalf("review request=%+v", calls[1])
			}
			if test.wantFocus {
				want := []string{"pane", "uid:pan-alpha-codex", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main"}
				if len(focus.calls) != 1 || !slices.Equal(focus.calls[0], want) {
					t.Fatalf("focus calls=%v want=%v", focus.calls, want)
				}
			} else if len(focus.calls) != 0 {
				t.Fatalf("unexpected focus=%v", focus.calls)
			}
			if len(picker.rendered) != 1 || len(picker.rendered[0].Items) < 2 {
				t.Fatalf("picker surface=%+v", picker.rendered)
			}
		})
	}
}

func TestAgentControlCLIStaleBindingCallsNoControl(t *testing.T) {
	cmd, _, binding := exactControlCLICommand(t)
	binding.live.Epoch = ""
	calls := 0
	cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
		calls++
		return agentControlResponse{}, errors.New("must not be called")
	}
	cmd.focus = &recordingArgv{}
	_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex")
	wantFocus := "`projmux focus pane uid:pan-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main`"
	if err == nil || calls != 0 || !strings.Contains(err.Error(), wantFocus) || strings.Contains(err.Error(), "send-keys") {
		t.Fatalf("stale binding err=%v calls=%d", err, calls)
	}
}

func TestAgentControlCLIReconnectGapAndIdentityDriftRefuseEveryPublicWrite(t *testing.T) {
	actions := []struct {
		name string
		args []string
	}{
		{name: "start", args: []string{"turn", "start", "uid:agt-alpha-codex", "--", "new turn"}},
		{name: "steer", args: []string{"turn", "steer", "uid:agt-alpha-codex", "--", "steer turn"}},
		{name: "interrupt", args: []string{"turn", "interrupt", "uid:agt-alpha-codex"}},
		{name: "approval", args: []string{"approval", "review", "uid:agt-alpha-codex", "--request", "7"}},
	}
	states := []struct {
		name          string
		mutate        func(*fakeResourceStore, *staticAgentControlBinding)
		wantTransport int
	}{
		{name: "disconnect invalidating", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) {
			binding.live.Authority, binding.live.Epoch, binding.live.Reason = codexAuthorityInvalidating, "epoch-1", "disconnected"
		}},
		{name: "reconnect hook gap", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) {
			binding.live.Authority, binding.live.Epoch, binding.live.Reason = codexAuthorityHook, "", "disconnected"
		}},
		{name: "broker unavailable", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) {
			binding.live.Authority, binding.live.Epoch, binding.live.Reason = codexAuthorityHook, "", "unsupported-platform"
		}},
		{name: "generation replaced", wantTransport: 1, mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			pane, _ := store.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.Generation = "generation-replaced"
		}},
		{name: "thread replaced", mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			pane, _ := store.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.Codex.ThreadID = "thread-replaced"
		}},
	}
	for _, state := range states {
		for _, action := range actions {
			t.Run(state.name+"/"+action.name, func(t *testing.T) {
				cmd, store, binding := exactControlCLICommand(t)
				state.mutate(store, binding)
				wire := &fakeExactControlWire{}
				epoch := newCodexControlEpoch(wire, phase6CLIIdentity(), "epoch-1", codexappserver.LifecycleSnapshot{
					ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
					TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
				}, func(codexLifecycleIdentity) bool { return true })
				calls := 0
				cmd.controlCall = func(_ context.Context, _ string, _ coremetadata.CodexEndpointRef, _ codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
					calls++
					return epoch.Handle(context.Background(), request), nil
				}
				cmd.controlPicker = &scriptedAgentControlPicker{answers: []intpicker.Result{{Value: "decision:accept"}}}
				_, _, err := runRoute(t, cmd, action.args...)
				if err == nil || calls != state.wantTransport || wire.writes() != 0 {
					t.Fatalf("error=%v control transports=%d want=%d app-server writes=%d", err, calls, state.wantTransport, wire.writes())
				}
			})
		}
	}
}

func TestAgentControlCLIRegistryAndLiveIdentityMismatchIsTypedAndCallsNoControl(t *testing.T) {
	actions := []struct {
		name string
		args []string
	}{
		{name: "message", args: []string{"turn", "start", "uid:agt-alpha-codex", "--", "new message"}},
		{name: "reply", args: []string{"turn", "steer", "uid:agt-alpha-codex", "--", "same-thread reply"}},
		{name: "interrupt", args: []string{"turn", "interrupt", "uid:agt-alpha-codex"}},
		{name: "approval", args: []string{"approval", "review", "uid:agt-alpha-codex", "--request", "7"}},
	}
	for _, test := range []struct {
		name   string
		mutate func(*fakeResourceStore, *staticAgentControlBinding)
	}{
		{name: "runtime", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) { binding.live.RuntimeID = "%8" }},
		{name: "Pane uid", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) { binding.live.PaneUID = "pan-other" }},
		{name: "thread", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) { binding.live.ThreadID = "thread-other" }},
		{name: "unobserved", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) { binding.observed = false }},
		{name: "activation generation missing", mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			pane, _ := store.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.Generation = ""
		}},
		{name: "activation Agent mismatch", mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			pane, _ := store.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.AgentUID = "agt-other"
		}},
		{name: "durable endpoint missing", mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			agent, _ := store.registry.Agent("agt-alpha-codex")
			agent.Status.SessionRef.Codex.Endpoint = nil
		}},
		{name: "durable thread differs from activation", mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			agent, _ := store.registry.Agent("agt-alpha-codex")
			agent.Status.SessionRef.Codex.ThreadID = "thread-durable-other"
		}},
		{name: "activation authority is a foreign generation", mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			pane, _ := store.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.Codex.Authority.EndpointGenerationID = "control-test-foreign-generation"
		}},
		{name: "epoch empty", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) { binding.live.Epoch = "" }},
	} {
		for _, action := range actions {
			t.Run(test.name+"/"+action.name, func(t *testing.T) {
				cmd, store, binding := exactControlCLICommand(t)
				test.mutate(store, binding)
				calls := 0
				cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
					calls++
					return agentControlResponse{}, errors.New("must not be called")
				}
				_, _, err := runRoute(t, cmd, action.args...)
				var bindingErr *exactAgentControlBindingError
				if err == nil || !errors.As(err, &bindingErr) || calls != 0 {
					t.Fatalf("error=%v typed=%#v control calls=%d", err, bindingErr, calls)
				}
			})
		}
	}
}

func TestAgentControlCLIReplacedGenerationOrEpochWritesNoAppServerControl(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fakeResourceStore, *staticAgentControlBinding)
	}{
		{name: "generation", mutate: func(store *fakeResourceStore, _ *staticAgentControlBinding) {
			pane, _ := store.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.Generation = "generation-replaced"
		}},
		{name: "epoch", mutate: func(_ *fakeResourceStore, binding *staticAgentControlBinding) { binding.live.Epoch = "epoch-replaced" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, store, binding := exactControlCLICommand(t)
			test.mutate(store, binding)
			wire := &fakeExactControlWire{}
			epoch := newCodexControlEpoch(wire, phase6CLIIdentity(), "epoch-1", codexappserver.LifecycleSnapshot{
				ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
				TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
			}, func(codexLifecycleIdentity) bool { return true })
			var calls []agentControlRequest
			cmd.controlCall = func(_ context.Context, _ string, _ coremetadata.CodexEndpointRef, _ codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
				calls = append(calls, request)
				return epoch.Handle(context.Background(), request), nil
			}
			_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex")
			if err == nil || len(calls) != 1 || wire.writes() != 0 {
				t.Fatalf("error=%v calls=%+v app-server writes=%d", err, calls, wire.writes())
			}
		})
	}
}

func TestAgentControlBindingLookupUsesResolvedLogicalRouteOnly(t *testing.T) {
	for _, target := range []tmuxTransport{
		{Kind: tmuxSocketName, Value: "exact-control", Source: tmuxSocketNameSource},
		{Kind: tmuxSocketPath, Value: "/tmp/exact-control.sock", Source: tmuxSocketPathSource},
	} {
		t.Run(strings.TrimPrefix(target.Flag(), "-"), func(t *testing.T) {
			cmd, _, _ := exactControlCLICommand(t)
			tmux := &exactControlRouteRunner{target: target}
			cmd.controlBinding = nil
			cmd.controlRunner = tmux
			cmd.controlRoute = func(context.Context) (runtimeMutationRoute, error) {
				return runtimeMutationRoute{target: target}, nil
			}
			binding, err := cmd.resolveControlBinding("agent turn interrupt", "uid:agt-alpha-codex")
			if err != nil {
				t.Fatal(err)
			}
			if binding.Identity != phase6CLIIdentity() || binding.Epoch != "epoch-1" {
				t.Fatalf("binding = %+v", binding)
			}
			if len(tmux.calls) == 0 {
				t.Fatal("control binding performed no tmux reads")
			}
			for _, call := range tmux.calls {
				if len(call) < 2 || call[0] != target.Flag() || call[1] != target.Value {
					t.Fatalf("ambient/default control read = %q", call)
				}
				if slices.Contains(call, "display-message") {
					format := call[len(call)-1]
					if strings.Count(format, tmuxRowSepFormat) != 5 || strings.Contains(format, tmuxRowSep) {
						t.Fatalf("unsafe control frame format = %q", format)
					}
				}
			}
		})
	}
}
