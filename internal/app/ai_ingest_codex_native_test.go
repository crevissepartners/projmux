package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentprogress"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type phase3StaticTmuxRunner struct{ output string }

func (r phase3StaticTmuxRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(r.output), nil
}

func bindNativeCodexTestFixture(t *testing.T, store *fakeResourceStore, mutator coremetadata.Mutator, obs coremetadata.CodexActivationObservation) coremetadata.CodexEndpointRef {
	t.Helper()
	agent, ok := store.registry.Agent(obs.AgentUID)
	if !ok {
		t.Fatalf("fixture Agent %q missing", obs.AgentUID)
	}
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "native-test-domain", EndpointGenerationID: "native-test-endpoint"}
	if agent.Status.SessionRef != nil && agent.Status.SessionRef.Codex != nil && agent.Status.SessionRef.Codex.Endpoint != nil {
		endpoint = *agent.Status.SessionRef.Codex.Endpoint
	} else if err := mutator.StageCodexEndpoint(&store.registry, obs.AgentUID, endpoint); err != nil {
		t.Fatal(err)
	}
	obs.Endpoint = endpoint
	if _, err := mutator.BindCodexActivation(&store.registry, obs); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func phase0RNativeCodexFixture(t *testing.T) (*fakeResourceStore, codexLifecycleIdentity) {
	t.Helper()
	store := newFakeResourceStore(t)
	identity := codexLifecycleIdentity{
		AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%7",
		Generation: "generation-phase0r", ThreadID: "thread-phase0r",
	}
	mutator := store.mutator()
	if _, err := mutator.RecordPaneActivation(&store.registry, identity.PaneUID, coremetadata.PaneActivationOptions{
		Generation: identity.Generation, RuntimeID: identity.RuntimeID, AgentUID: identity.AgentUID, OperationID: "phase0r-authority",
	}); err != nil {
		t.Fatal(err)
	}
	bindNativeCodexTestFixture(t, store, mutator, coremetadata.CodexActivationObservation{
		AgentUID: identity.AgentUID, PaneUID: identity.PaneUID, Generation: identity.Generation, ThreadID: identity.ThreadID,
	})
	return store, identity
}

func phase1GenerationAuthorityFixture(t *testing.T) (*fakeResourceStore, codexLifecycleIdentity, *coremetadata.CodexEndpointRef, *coremetadata.CodexAuthorityRef) {
	t.Helper()
	store, identity := phase0RNativeCodexFixture(t)
	endpoint := &coremetadata.CodexEndpointRef{StateDomainID: "phase1-domain", EndpointGenerationID: "phase1-generation"}
	authority := &coremetadata.CodexAuthorityRef{
		StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
		BrokerRuntimeID: "phase1-broker", ConnectionEpoch: 11, BindingEpoch: 17,
	}
	agent, _ := store.registry.Agent(identity.AgentUID)
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: aiModeCodex, ObservedAt: resourceFixtureClock,
		Codex: &coremetadata.CodexSessionRef{
			ThreadID: identity.ThreadID, Endpoint: endpoint,
			Lifecycle: &coremetadata.CodexGenerationLifecycleRef{
				State: coremetadata.CodexGenerationDraining,
				Operation: &coremetadata.CodexGenerationOperationRef{
					ID: "drain-operation", Endpoint: *endpoint,
				},
			},
		},
	}
	pane, _ := store.registry.Pane(identity.PaneUID)
	pane.Status.Activation.Codex.Authority = authority
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return store, identity, endpoint, authority
}

func phase0RSemanticPaneWrites(commands []recordedAICommand) map[string][]string {
	writes := map[string][]string{}
	for _, command := range commands {
		args := stripRecordedTmuxRoute(command.args)
		if command.name != "tmux" || len(args) < 5 || args[0] != "set-option" {
			continue
		}
		option := args[len(args)-2]
		value := args[len(args)-1]
		if slices.Contains(args, "-u") {
			option, value = args[len(args)-1], ""
		}
		if slices.Contains([]string{aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption}, option) {
			writes[option] = append(writes[option], value)
		}
	}
	return writes
}

func TestNativeCodexHookAuthoritySemanticWriteSet(t *testing.T) {
	for _, test := range []struct {
		authority  string
		readErr    bool
		wantCommit bool
	}{
		{authority: codexAuthorityPending},
		{authority: codexAuthorityControlPlane},
		{authority: codexAuthorityInvalidating},
		{authority: ""},
		{authority: "unknown-authority"},
		{authority: "unavailable", readErr: true},
		{authority: codexAuthorityHook, wantCommit: true},
	} {
		name := test.authority
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			store, identity := phase0RNativeCodexFixture(t)
			beforeAgent, _ := store.registry.Agent(identity.AgentUID)
			home := t.TempDir()
			queue := &stubNotifyStore{}
			cmd := testAICommand(home)
			cmd.loadRegistry = store.store().load
			cmd.updateRegistry = store.store().update
			cmd.notifyStore = queue
			cmd.producer = &storeAttentionNotifyProducer{store: queue, ttl: time.Minute}
			cmd.stdin = bytes.NewBufferString(`{"hook_event_name":"Stop","thread_id":"thread-phase0r","turn_id":"turn-phase0r","cwd":"/repo/projmux"}`)
			baseRead := codexHookIngestReadCommand(identity.RuntimeID)
			cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				switch {
				case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", identity.RuntimeID, "#{" + aiPaneCodexAuthorityOption + "}"}):
					if test.readErr {
						return nil, os.ErrNotExist
					}
					return []byte(test.authority + "\n"), nil
				case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", identity.RuntimeID, "#{" + tmuxopts.PaneUID + "}"}):
					return []byte(identity.PaneUID + "\n"), nil
				default:
					return baseRead(ctx, name, args...)
				}
			}
			desktopWrites := 0
			baseLookup := cmd.lookupEnv
			cmd.lookupEnv = func(name string) string {
				switch name {
				case internalActivationPaneUIDEnv:
					return identity.PaneUID
				case internalActivationGenerationEnv:
					return identity.Generation
				case "PROJMUX_NOTIFY_HOOK":
					return "/tmp/projmux-phase0r-notify-recorder"
				default:
					return baseLookup(name)
				}
			}
			cmd.runCommand = func(_ context.Context, name string, args ...string) error {
				if name == "/tmp/projmux-phase0r-notify-recorder" {
					desktopWrites++
				}
				cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
				return nil
			}

			if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			paneWrites := phase0RSemanticPaneWrites(cmdRecorder(cmd).commands)
			agent, _ := store.registry.Agent(identity.AgentUID)
			if !test.wantCommit {
				if len(paneWrites) != 0 || store.writes != 0 || len(queue.pushed) != 0 || len(queue.ackedIDs) != 0 || desktopWrites != 0 || !reflect.DeepEqual(agent.Status.Interaction, beforeAgent.Status.Interaction) {
					t.Fatalf("suppressed hook write ledger authority=%q pane=%#v Registry=%d interaction=%#v queue=%#v ack=%#v desktop=%d", test.authority, paneWrites, store.writes, agent.Status.Interaction, queue.pushed, queue.ackedIDs, desktopWrites)
				}
				return
			}
			wantPane := map[string][]string{
				aiPaneStateOption:     {"waiting"},
				aiPaneBadgeKindOption: {aiBadgeKindResponseComplete},
				attentionStateOption:  {attentionStateReply},
			}
			if !reflect.DeepEqual(paneWrites, wantPane) || store.writes != 1 ||
				agent.Status.Interaction.Kind != coremetadata.InteractionResponseComplete || agent.Status.Interaction.Source != string(coremetadata.InteractionSourceProviderHook) ||
				len(queue.pushed) != 1 || desktopWrites != 1 {
				t.Fatalf("fallback hook write ledger pane=%#v Registry=%d interaction=%#v queue=%#v desktop=%d", paneWrites, store.writes, agent.Status.Interaction, queue.pushed, desktopWrites)
			}
		})
	}
}

type phase0RTmuxRunner func(context.Context, string, ...string) ([]byte, error)

func (f phase0RTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func TestNativeCodexHookAuthorityChangeAfterGuardCommitsZero(t *testing.T) {
	store, identity := phase0RNativeCodexFixture(t)
	beforeAgent, _ := store.registry.Agent(identity.AgentUID)
	options := map[string]string{
		tmuxopts.PaneUID:           identity.PaneUID,
		aiPaneCodexAuthorityOption: codexAuthorityHook,
		aiPaneCodexEpochOption:     "epoch-before",
		aiPaneCodexReasonOption:    "native-fallback",
	}
	var optionsMu sync.Mutex
	sinkAtAuthorityRead := make(chan struct{})
	allowInvalidation := make(chan struct{})
	var sinkReadOnce sync.Once
	sinkRunner := phase0RTmuxRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || len(args) < 1 {
			return nil, fmt.Errorf("unexpected sink command %s %q", name, args)
		}
		switch args[0] {
		case "show-options":
			option := args[len(args)-1]
			if option == aiPaneCodexAuthorityOption {
				sinkReadOnce.Do(func() {
					close(sinkAtAuthorityRead)
					<-allowInvalidation
				})
			}
			optionsMu.Lock()
			defer optionsMu.Unlock()
			return []byte(options[option] + "\n"), nil
		case "set-option":
			optionsMu.Lock()
			defer optionsMu.Unlock()
			option := args[len(args)-2]
			if slices.Contains(args, "-u") {
				delete(options, args[len(args)-1])
			} else {
				options[option] = args[len(args)-1]
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected sink tmux command %q", args)
		}
	})

	// The injected token has the same ownership semantics as the production
	// flock while exposing a deterministic handoff point to the test.
	token := make(chan struct{}, 1)
	token <- struct{}{}
	acquire := func(paneUID string) (func(), error) {
		if paneUID != identity.PaneUID {
			return nil, fmt.Errorf("authority fence Pane uid = %q, want %q", paneUID, identity.PaneUID)
		}
		<-token
		var once sync.Once
		return func() { once.Do(func() { token <- struct{}{} }) }, nil
	}

	home := t.TempDir()
	sinkCommand := testAICommand(home)
	sinkCommand.loadRegistry = store.store().load
	sinkCommand.acquireCodexAuthority = acquire
	sink := aiCodexLifecycleSink{command: sinkCommand, runner: sinkRunner}
	sinkResult := make(chan error, 1)
	go func() {
		sinkResult <- sink.SetAuthority(identity, codexAuthorityInvalidating, "epoch-after", "endpoint-suspended")
	}()
	select {
	case <-sinkAtAuthorityRead:
	case <-time.After(time.Second):
		t.Fatal("native invalidation did not acquire the authority fence")
	}

	queue := &stubNotifyStore{}
	hookCommand := testAICommand(home)
	hookCommand.loadRegistry = store.store().load
	hookCommand.updateRegistry = store.store().update
	hookCommand.acquireCodexAuthority = acquire
	hookCommand.notifyStore = queue
	hookCommand.producer = &storeAttentionNotifyProducer{store: queue, ttl: time.Minute}
	hookCommand.stdin = bytes.NewBufferString(`{"hook_event_name":"Stop","thread_id":"thread-phase0r","turn_id":"turn-gap","cwd":"/repo/projmux"}`)
	hookInitialGuard := make(chan struct{})
	var hookReadOnce sync.Once
	baseRead := codexHookIngestReadCommand(identity.RuntimeID)
	hookCommand.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", identity.RuntimeID, "#{" + aiPaneCodexAuthorityOption + "}"}):
			optionsMu.Lock()
			value := options[aiPaneCodexAuthorityOption]
			optionsMu.Unlock()
			hookReadOnce.Do(func() { close(hookInitialGuard) })
			return []byte(value + "\n"), nil
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", identity.RuntimeID, "#{" + tmuxopts.PaneUID + "}"}):
			return []byte(identity.PaneUID + "\n"), nil
		default:
			return baseRead(ctx, name, args...)
		}
	}
	desktopWrites := 0
	baseLookup := hookCommand.lookupEnv
	hookCommand.lookupEnv = func(name string) string {
		switch name {
		case internalActivationPaneUIDEnv:
			return identity.PaneUID
		case internalActivationGenerationEnv:
			return identity.Generation
		case "PROJMUX_NOTIFY_HOOK":
			return "/tmp/projmux-phase0r-notify-recorder"
		default:
			return baseLookup(name)
		}
	}
	hookCommand.runCommand = func(_ context.Context, name string, args ...string) error {
		if name == "/tmp/projmux-phase0r-notify-recorder" {
			desktopWrites++
		}
		cmdRecorder(hookCommand).commands = append(cmdRecorder(hookCommand).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		return nil
	}
	hookResult := make(chan error, 1)
	go func() {
		hookResult <- hookCommand.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	select {
	case <-hookInitialGuard:
	case <-time.After(time.Second):
		t.Fatal("Stop hook did not observe the initial provider-hook authority")
	}
	close(allowInvalidation)
	select {
	case err := <-sinkResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("native invalidation did not release the authority fence")
	}
	select {
	case err := <-hookResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("suppressed Stop hook did not finish after invalidation")
	}

	optionsMu.Lock()
	authorityTuple := strings.Join([]string{options[aiPaneCodexAuthorityOption], options[aiPaneCodexEpochOption], options[aiPaneCodexReasonOption]}, "|")
	state, badge, attention := options[aiPaneStateOption], options[aiPaneBadgeKindOption], options[attentionStateOption]
	optionsMu.Unlock()
	agent, _ := store.registry.Agent(identity.AgentUID)
	if authorityTuple != "invalidating|epoch-after|endpoint-suspended" || state != "" || badge != "" || attention != "" ||
		store.writes != 0 || !reflect.DeepEqual(agent.Status.Interaction, beforeAgent.Status.Interaction) ||
		len(queue.pushed) != 0 || len(queue.ackedIDs) != 0 || desktopWrites != 0 {
		t.Fatalf("forced invalidation write ledger authority=%s state=%q badge=%q attention=%q Registry=%d interaction=%#v queue=%#v ack=%#v desktop=%d hook-commands=%#v",
			authorityTuple, state, badge, attention, store.writes, agent.Status.Interaction, queue.pushed, queue.ackedIDs, desktopWrites, cmdRecorder(hookCommand).commands)
	}
}

func TestNativeSemanticApplyAndInvalidationShareExactPaneFence(t *testing.T) {
	store, identity := phase0RNativeCodexFixture(t)
	options := map[string]string{
		tmuxopts.PaneUID:           identity.PaneUID,
		aiPaneCodexAuthorityOption: codexAuthorityControlPlane,
		aiPaneCodexEpochOption:     "epoch-before",
		aiPaneCodexReasonOption:    "ready",
	}
	var optionsMu sync.Mutex
	oldApplyAtPaneWrite := make(chan struct{})
	allowOldPaneWrite := make(chan struct{})
	var oldWriteOnce sync.Once
	runner := phase0RTmuxRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || len(args) == 0 {
			return nil, fmt.Errorf("unexpected semantic fence command %s %q", name, args)
		}
		switch args[0] {
		case "show-options":
			option := args[len(args)-1]
			optionsMu.Lock()
			value := options[option]
			optionsMu.Unlock()
			return []byte(value + "\n"), nil
		case "set-option":
			option, value := args[len(args)-2], args[len(args)-1]
			unset := slices.Contains(args, "-u")
			if unset {
				option, value = args[len(args)-1], ""
			}
			if option == aiPaneStateOption && value == codexgeneration.LifecycleStateWaiting {
				oldWriteOnce.Do(func() {
					close(oldApplyAtPaneWrite)
					<-allowOldPaneWrite
				})
			}
			optionsMu.Lock()
			if unset {
				delete(options, option)
			} else {
				options[option] = value
			}
			optionsMu.Unlock()
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected semantic fence tmux command %q", args)
		}
	})

	token := make(chan struct{}, 1)
	token <- struct{}{}
	invalidationWaiting := make(chan struct{})
	var acquireMu sync.Mutex
	acquires := 0
	acquire := func(paneUID string) (func(), error) {
		if paneUID != identity.PaneUID {
			return nil, fmt.Errorf("authority fence Pane uid = %q, want %q", paneUID, identity.PaneUID)
		}
		acquireMu.Lock()
		acquires++
		attempt := acquires
		acquireMu.Unlock()
		if attempt == 2 {
			close(invalidationWaiting)
		}
		<-token
		var once sync.Once
		return func() { once.Do(func() { token <- struct{}{} }) }, nil
	}

	command := testAICommand(t.TempDir())
	command.loadRegistry = store.store().load
	command.updateRegistry = store.store().update
	command.acquireCodexAuthority = acquire
	sink := aiCodexLifecycleSink{command: command, runner: runner}
	oldResult := make(chan error, 1)
	go func() {
		oldResult <- sink.Apply(identity, codexLifecycleProjection{
			Accepted: true, Interaction: coremetadata.InteractionResponseComplete,
		})
	}()
	select {
	case <-oldApplyAtPaneWrite:
	case <-time.After(time.Second):
		t.Fatal("old native Apply did not reach the forced Registry/tmux split")
	}
	agent, _ := store.registry.Agent(identity.AgentUID)
	if agent.Status.Interaction.Kind != coremetadata.InteractionResponseComplete {
		t.Fatalf("forced split did not occur after Registry commit: %#v", agent.Status.Interaction)
	}

	invalidationResult := make(chan error, 1)
	go func() {
		if err := sink.SetAuthority(identity, codexAuthorityInvalidating, "epoch-after", "endpoint-suspended"); err != nil {
			invalidationResult <- err
			return
		}
		invalidationResult <- sink.Apply(identity, codexLifecycleProjection{
			Accepted: true, Invalidated: true, Interaction: coremetadata.InteractionUnknown,
		})
	}()
	select {
	case <-invalidationWaiting:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not attempt the exact Pane fence")
	}
	close(allowOldPaneWrite)
	for label, result := range map[string]<-chan error{"old Apply": oldResult, "invalidation": invalidationResult} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not finish behind the semantic fence", label)
		}
	}

	optionsMu.Lock()
	authorityTuple := strings.Join([]string{options[aiPaneCodexAuthorityOption], options[aiPaneCodexEpochOption], options[aiPaneCodexReasonOption]}, "|")
	state, badge, attention := options[aiPaneStateOption], options[aiPaneBadgeKindOption], options[attentionStateOption]
	optionsMu.Unlock()
	agent, _ = store.registry.Agent(identity.AgentUID)
	if authorityTuple != "invalidating|epoch-after|endpoint-suspended" || state != "" || badge != "" || attention != "" ||
		agent.Status.Interaction.Kind != coremetadata.InteractionUnknown {
		t.Fatalf("invalidation was not final: authority=%s state=%q badge=%q attention=%q interaction=%#v",
			authorityTuple, state, badge, attention, agent.Status.Interaction)
	}

	// Recovery deliberately applies the replacement snapshot while authority is
	// still invalidating, then publishes the exact ready epoch. The shared fence
	// serializes these writes without making this established ordering illegal.
	if err := sink.Apply(identity, codexLifecycleProjection{Accepted: true, Interaction: coremetadata.InteractionIdle}); err != nil {
		t.Fatalf("replacement Apply while invalidating: %v", err)
	}
	if err := sink.SetAuthority(identity, codexAuthorityControlPlane, "epoch-replacement", "ready"); err != nil {
		t.Fatalf("publish replacement ready authority: %v", err)
	}
	optionsMu.Lock()
	authorityTuple = strings.Join([]string{options[aiPaneCodexAuthorityOption], options[aiPaneCodexEpochOption], options[aiPaneCodexReasonOption]}, "|")
	state = options[aiPaneStateOption]
	optionsMu.Unlock()
	if authorityTuple != codexAuthorityControlPlane+"|epoch-replacement|ready" || state != codexgeneration.LifecycleStateIdle {
		t.Fatalf("replacement ordering = authority:%s state:%q", authorityTuple, state)
	}
}

func TestCodexObserverStartupUsesExactRouteAndFallsBackAfterProcessFailure(t *testing.T) {
	store := newFakeResourceStore(t)
	identity := codexLifecycleIdentity{
		AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%9",
		Generation: "generation-startup", ThreadID: "thread-startup",
	}
	mutator := store.mutator()
	if _, err := mutator.RecordPaneActivation(&store.registry, identity.PaneUID, coremetadata.PaneActivationOptions{
		Generation: identity.Generation, RuntimeID: identity.RuntimeID, AgentUID: identity.AgentUID, OperationID: "phase3-startup",
	}); err != nil {
		t.Fatal(err)
	}
	endpoint := bindNativeCodexTestFixture(t, store, mutator, coremetadata.CodexActivationObservation{
		AgentUID: identity.AgentUID, PaneUID: identity.PaneUID, Generation: identity.Generation, ThreadID: identity.ThreadID,
	})
	cmd := testAICommand(t.TempDir())
	cmd.loadRegistry = store.store().load
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"-L", "exact", "show-options", "-pqv", "-t", identity.RuntimeID, tmuxopts.PaneUID}) {
			return []byte(identity.PaneUID + "\n"), nil
		}
		if name == "tmux" && len(args) == 7 && slices.Equal(args[:6], []string{"-L", "exact", "show-options", "-pqv", "-t", identity.RuntimeID}) {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}
	cmd.executable = func() (string, error) { return "/tmp/projmux.test", nil }
	result := cmd.startNativeCodexLifecycleObserver(codexLifecycleObserverTarget{
		Identity: identity, Route: tmuxTransport{Kind: tmuxSocketName, Value: "exact", Source: tmuxSocketNameSource},
		NativeRoute: codexNativeEndpointRoute{Endpoint: endpoint, State: coremetadata.CodexGenerationCurrent,
			SocketPath: "/tmp/native-test.sock", TUIExecutable: "/tmp/native-test-codex"},
	})
	if result.Status != codexObserverStartupFallback || result.Reason != "observer-start-failed" {
		t.Fatalf("observer start failure result = %+v", result)
	}
	commands := cmdRecorder(cmd).commands
	for _, command := range commands {
		if len(command.args) < 2 || command.args[0] != "-L" || command.args[1] != "exact" {
			t.Fatalf("observer startup used ambient/default route: %#v", commands)
		}
	}
	if !hasRecordedAICommand(commands, recordedAICommand{name: "tmux", args: []string{"-L", "exact", "set-option", "-p", "-t", "%9", aiPaneCodexAuthorityOption, codexAuthorityPending}}) ||
		!hasRecordedAICommand(commands, recordedAICommand{name: "tmux", args: []string{"-L", "exact", "set-option", "-p", "-t", "%9", aiPaneCodexReasonOption, "connecting"}}) {
		t.Fatalf("observer startup did not enter bounded connecting state: %#v", commands)
	}
	if !hasRecordedAICommand(commands, recordedAICommand{name: "tmux", args: []string{"-L", "exact", "set-option", "-p", "-t", "%9", aiPaneCodexAuthorityOption, codexAuthorityHook}}) {
		t.Fatalf("failed observer did not enable bounded fallback: %#v", commands)
	}
	if !hasRecordedAICommand(commands, recordedAICommand{name: "tmux", args: []string{"-L", "exact", "set-option", "-p", "-t", "%9", aiPaneCodexReasonOption, "observer-start-failed"}}) {
		t.Fatalf("failed observer did not retain typed start reason: %#v", commands)
	}

	cmdRecorder(cmd).commands = nil
	stale := identity
	stale.Generation = "generation-replaced"
	result = cmd.startNativeCodexLifecycleObserver(codexLifecycleObserverTarget{Identity: stale, Route: tmuxTransport{Kind: tmuxSocketName, Value: "exact", Source: tmuxSocketNameSource}})
	if result.Status != codexObserverStartupStale {
		t.Fatalf("stale startup result = %+v", result)
	}
	if commands := cmdRecorder(cmd).commands; len(commands) != 0 {
		t.Fatalf("stale/reused runtime startup writes = %#v, want zero", commands)
	}
}

func TestCodexLifecycleAuthorityWriteCompensatesFirstMiddleLastOnExactRoutes(t *testing.T) {
	store := newFakeResourceStore(t)
	identity := codexLifecycleIdentity{
		AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%9",
		Generation: "generation-authority", ThreadID: "thread-authority",
	}
	mutator := store.mutator()
	if _, err := mutator.RecordPaneActivation(&store.registry, identity.PaneUID, coremetadata.PaneActivationOptions{
		Generation: identity.Generation, RuntimeID: identity.RuntimeID, AgentUID: identity.AgentUID, OperationID: "phase2-authority",
	}); err != nil {
		t.Fatal(err)
	}
	bindNativeCodexTestFixture(t, store, mutator, coremetadata.CodexActivationObservation{
		AgentUID: identity.AgentUID, PaneUID: identity.PaneUID, Generation: identity.Generation, ThreadID: identity.ThreadID,
	})
	command := testAICommand(t.TempDir())
	command.loadRegistry = store.store().load
	fields := []string{aiPaneCodexAuthorityOption, aiPaneCodexEpochOption, aiPaneCodexReasonOption}
	for _, route := range []tmuxTransport{{Kind: tmuxSocketName, Value: "authority", Source: tmuxSocketNameSource}, {Kind: tmuxSocketPath, Value: "/tmp/authority.sock", Source: tmuxSocketPathSource}} {
		for failWrite := 1; failWrite <= len(fields); failWrite++ {
			t.Run(strings.TrimPrefix(route.Flag(), "-")+fmt.Sprintf("/failure-%d", failWrite), func(t *testing.T) {
				before := map[string]string{
					tmuxopts.PaneUID: identity.PaneUID, aiPaneCodexAuthorityOption: "old-authority",
					aiPaneCodexEpochOption: "old-epoch", aiPaneCodexReasonOption: "old-reason", "@sibling": "keep",
				}
				raw := &bindingFailureRunner{
					targetFlag: route.Flag(), targetValue: route.Value, options: maps.Clone(before),
					failWrite: failWrite, failureCommand: -1,
				}
				sink := aiCodexLifecycleSink{command: command, runner: explicitTmuxRunner{runner: raw, target: route}}
				if err := sink.SetAuthority(identity, codexAuthorityControlPlane, "epoch-current", "ready"); err == nil {
					t.Fatal("injected authority write failure returned nil")
				}
				if !reflect.DeepEqual(raw.options, before) {
					t.Fatalf("partial authority remained: got %#v want %#v", raw.options, before)
				}
				if raw.failureCommand < 0 {
					t.Fatal("authority failure command was not recorded")
				}
				restores := raw.commands[raw.failureCommand+1:]
				if len(restores) != failWrite-1 {
					t.Fatalf("authority compensation = %q, want %d reverse restores", restores, failWrite-1)
				}
				for index, command := range restores {
					argv := command[2:]
					option := argv[len(argv)-2]
					if slices.Contains(argv, "-u") {
						option = argv[len(argv)-1]
					}
					want := fields[failWrite-2-index]
					if option != want {
						t.Fatalf("authority compensation[%d] = %q, want %q; commands=%q", index, option, want, restores)
					}
				}
				for _, command := range raw.commands {
					if len(command) < 2 || command[0] != route.Flag() || command[1] != route.Value {
						t.Fatalf("authority escaped exact route: %q", raw.commands)
					}
				}
			})
		}
	}
}

func TestCodexLifecycleObserverExecutableValidation(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "projmux")
	if err := os.WriteFile(valid, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := validateCodexLifecycleObserverExecutable(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got != valid {
		t.Fatalf("validated executable = %q, want %q", got, valid)
	}

	notExecutable := filepath.Join(root, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	testBinary := filepath.Join(root, "projmux.test")
	if err := os.WriteFile(testBinary, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"projmux", root, notExecutable, testBinary} {
		if _, err := validateCodexLifecycleObserverExecutable(path); err == nil {
			t.Errorf("unsafe observer executable %q was accepted", path)
		}
	}
}

func TestCodexLifecycleObserverTargetRequiresAndPreservesExactRoute(t *testing.T) {
	identityArgs := []string{
		"--agent-uid", "agent-1", "--pane-uid", "pane-1", "--pane", "%7",
		"--generation", "generation-1", "--thread", "thread-1",
		"--state-domain", "test-domain", "--endpoint-generation", "generation-current",
		"--endpoint-state", string(coremetadata.CodexGenerationCurrent), "--endpoint-socket", "/tmp/codex-generation.sock",
		"--tui-executable", "/tmp/codex-generation/codex",
	}
	for _, routeArgs := range [][]string{{"--tmux-socket-name", "projmux-test"}, {"--tmux-socket-path", "/tmp/projmux-test.sock"}} {
		target, err := parseCodexNativeLifecycleTarget(append(append([]string(nil), identityArgs...), routeArgs...))
		if err != nil {
			t.Fatal(err)
		}
		if target.Identity != phase6Identity() || target.Route.Value != routeArgs[1] || !target.NativeRoute.valid() ||
			target.NativeRoute.Endpoint.StateDomainID != "test-domain" || target.NativeRoute.Endpoint.EndpointGenerationID != "generation-current" {
			t.Fatalf("target = %+v, route args = %q", target, routeArgs)
		}
	}
	if _, err := parseCodexNativeLifecycleTarget(identityArgs); err == nil {
		t.Fatal("route-less observer identity was accepted")
	}
	if _, err := parseCodexNativeLifecycleTarget(append(append([]string(nil), identityArgs...), "--tmux-socket-name", "one", "--tmux-socket-path", "/tmp/two")); err == nil {
		t.Fatal("ambiguous observer routes were accepted")
	}
	clean := withoutInheritedTmuxEnvironment([]string{
		"HOME=/home/test", "TMUX=/tmp/default,1,0", "TMUX_PANE=%9", runtimeMutationAnchorPaneEnv + "=%9",
		codexObserverStartupEnvironment + "=stale",
	})
	if !slices.Equal(clean, []string{"HOME=/home/test"}) {
		t.Fatalf("sanitized observer environment = %q", clean)
	}
}

func TestCodexObserverStartupHandshakeParserIsClosed(t *testing.T) {
	tests := []struct {
		line string
		want codexObserverStartupResult
		ok   bool
	}{
		{line: codexObserverStartupPrefix + " ready 123-1\n", want: codexObserverStartupResult{Status: codexObserverStartupReady, Epoch: "123-1", committed: true}, ok: true},
		{line: codexObserverStartupPrefix + " fallback control-unavailable\n", want: codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "control-unavailable", committed: true}, ok: true},
		{line: codexObserverStartupPrefix + " stale\n", want: codexObserverStartupResult{Status: codexObserverStartupStale, committed: true}, ok: true},
		{line: "ready 123-1\n"},
		{line: codexObserverStartupPrefix + " ready\n"},
		{line: codexObserverStartupPrefix + " fallback\n"},
		{line: codexObserverStartupPrefix + " unknown reason\n"},
	}
	for _, test := range tests {
		got, ok := parseCodexObserverStartupLine(test.line)
		if ok != test.ok || got != test.want {
			t.Errorf("parse startup %q = (%+v, %t), want (%+v, %t)", test.line, got, ok, test.want, test.ok)
		}
	}
}

func TestCodexNativeObserverReportsExactStartupTerminalState(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	for _, test := range []struct {
		name           string
		current        bool
		requireControl bool
		open           func(context.Context) (codexLifecycleConnection, error)
		want           codexObserverStartupResult
	}{
		{
			name: "ready", current: true,
			open: func(context.Context) (codexLifecycleConnection, error) {
				return &fakeCodexLifecycleConnection{
					snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle},
					events:   make(chan codexappserver.Notification),
				}, nil
			},
			want: codexObserverStartupResult{Status: codexObserverStartupReady},
		},
		{
			name: "endpoint unavailable", current: true,
			open: func(context.Context) (codexLifecycleConnection, error) { return nil, codexappserver.ErrDisconnected },
			want: codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "unavailable"},
		},
		{
			name: "control endpoint unavailable", current: true, requireControl: true,
			open: func(context.Context) (codexLifecycleConnection, error) {
				return &fakeCodexLifecycleConnection{
					snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle},
					events:   make(chan codexappserver.Notification),
				}, nil
			},
			want: codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "control-unavailable"},
		},
		{
			name: "stale binding",
			open: func(context.Context) (codexLifecycleConnection, error) {
				t.Fatal("stale binding opened a connection")
				return nil, nil
			},
			want: codexObserverStartupResult{Status: codexObserverStartupStale},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := newRecordingCodexLifecycleSink()
			sink.setCurrent(test.current)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reported := make(chan codexObserverStartupResult, 1)
			observer := codexNativeObserver{
				identity: identity, sink: sink, open: test.open, requireControl: test.requireControl,
				bindingTimeout: time.Millisecond, delay: time.Hour,
				reportStartup: func(result codexObserverStartupResult) { reported <- result },
			}
			done := make(chan error, 1)
			go func() { done <- observer.Run(ctx) }()
			select {
			case got := <-reported:
				if got.Status != test.want.Status || got.Reason != test.want.Reason || (got.Status == codexObserverStartupReady && got.Epoch == "") {
					t.Fatalf("startup result = %+v, want %+v with non-empty ready epoch", got, test.want)
				}
			case <-time.After(time.Second):
				t.Fatal("observer did not report bounded startup result")
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("observer did not stop after cancellation")
			}
		})
	}
}

type fakeCodexLifecycleConnection struct {
	snapshot codexappserver.LifecycleSnapshot
	events   chan codexappserver.Notification
	mu       sync.Mutex
	closed   int
}

func testCodexLifecycleSink(cmd *aiCommand) aiCodexLifecycleSink {
	return aiCodexLifecycleSink{command: cmd, runner: aiCommandMuxBackend{runCommand: cmd.runCommand, readCommand: cmd.readCommand}}
}

func (c *fakeCodexLifecycleConnection) Notifications() <-chan codexappserver.Notification {
	return c.events
}
func (c *fakeCodexLifecycleConnection) LifecycleEventsAvailable() bool { return true }
func (c *fakeCodexLifecycleConnection) ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error) {
	return c.snapshot, nil
}
func (c *fakeCodexLifecycleConnection) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}

func (c *fakeCodexLifecycleConnection) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

type fakeControllableCodexLifecycleConnection struct {
	*fakeCodexLifecycleConnection
	*fakeExactControlWire
}

func TestCodexNativeObserverReadyHandshakeSteersAndShutdownRemovesControlSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "observer-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	identity := testCodexLifecycleIdentity()
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "observer-control-domain", EndpointGenerationID: "observer-control-generation"}
	wire := &fakeExactControlWire{}
	connection := &fakeControllableCodexLifecycleConnection{
		fakeCodexLifecycleConnection: &fakeCodexLifecycleConnection{
			snapshot: codexappserver.LifecycleSnapshot{
				ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive,
				TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
			},
			events: make(chan codexappserver.Notification),
		},
		fakeExactControlWire: wire,
	}
	sink := newRecordingCodexLifecycleSink()
	reported := make(chan codexObserverStartupResult, 4)
	observer := codexNativeObserver{
		identity: identity, sink: sink, requireControl: true,
		open: func(context.Context) (codexLifecycleConnection, error) { return connection, nil },
		startControl: func(epoch *codexControlEpoch) (*codexControlServer, error) {
			return startCodexControlServer(root, endpoint, epoch)
		},
		reportStartup: func(result codexObserverStartupResult) { reported <- result },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	var ready codexObserverStartupResult
	select {
	case ready = <-reported:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("observer did not complete ready handshake")
	}
	if ready.Status == codexObserverStartupFallback && ready.Reason == "control-unavailable" {
		cancel()
		<-done
		t.Skip("Unix sockets are unavailable in this sandbox")
	}
	if ready.Status != codexObserverStartupReady || ready.Epoch == "" {
		cancel()
		<-done
		t.Fatalf("ready handshake = %+v", ready)
	}
	request := agentControlRequest{Operation: agentControlOpSteer, Identity: identity, Epoch: ready.Epoch, Text: "exact steer"}
	response, err := callCodexControl(context.Background(), root, endpoint, identity, request)
	if errors.Is(err, syscall.EPERM) {
		cancel()
		<-done
		t.Skip("Unix sockets are unavailable in this sandbox")
	}
	if err != nil || !response.OK || wire.writes() != 1 {
		cancel()
		<-done
		t.Fatalf("exact steer response=%+v err=%v writes=%d", response, err, wire.writes())
	}
	path, err := agentControlSocketPath(root, endpoint, identity)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer shutdown did not finish")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control socket survived observer shutdown: %s err=%v", path, err)
	}
}

type recordingCodexLifecycleSink struct {
	mu              sync.Mutex
	events          []string
	authorities     []string
	authorityEpochs []string
	wake            chan struct{}
	current         bool
	applyCalls      int
	failApplyAt     int
	failApplyFrom   int
}

type recordingCodexProgressSink struct {
	*recordingCodexLifecycleSink
	progress    []coremetadata.AgentProgress
	diagnostics []agentprogress.Diagnostics
	cancel      context.CancelFunc
	sawProgress bool
}

func (s *recordingCodexProgressSink) ApplyProgress(_ codexLifecycleIdentity, progress coremetadata.AgentProgress, diagnostics agentprogress.Diagnostics) error {
	s.mu.Lock()
	s.progress = append(s.progress, progress)
	s.diagnostics = append(s.diagnostics, diagnostics)
	if progress.TurnRef != "" {
		s.sawProgress = true
	} else if s.sawProgress && s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	return nil
}

func (s *recordingCodexProgressSink) progressSnapshot() ([]coremetadata.AgentProgress, []agentprogress.Diagnostics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]coremetadata.AgentProgress(nil), s.progress...), append([]agentprogress.Diagnostics(nil), s.diagnostics...)
}

type timeoutCodexLifecycleSink struct {
	allowAuthority bool
	authorities    []string
}

func (*timeoutCodexLifecycleSink) BindingCurrent(codexLifecycleIdentity) bool { return false }
func (s *timeoutCodexLifecycleSink) SetAuthority(_ codexLifecycleIdentity, source, _, reason string) error {
	if s.allowAuthority {
		s.authorities = append(s.authorities, source+":"+reason)
	}
	return nil
}
func (*timeoutCodexLifecycleSink) Apply(codexLifecycleIdentity, codexLifecycleProjection) error {
	return nil
}

type replacedCodexLifecycleSink struct{ authorityAttempts int }

func (*replacedCodexLifecycleSink) BindingCurrent(codexLifecycleIdentity) bool { return false }
func (s *replacedCodexLifecycleSink) SetAuthority(codexLifecycleIdentity, string, string, string) error {
	s.authorityAttempts++
	return errManagedAgentObservationIgnored
}
func (*replacedCodexLifecycleSink) Apply(codexLifecycleIdentity, codexLifecycleProjection) error {
	return errManagedAgentObservationIgnored
}

func TestCodexObserverParentFallbackAfterReplacementWritesZero(t *testing.T) {
	sink := &replacedCodexLifecycleSink{}
	result := convergeCodexObserverStartupFallback(sink, testCodexLifecycleIdentity(), "observer-exited")
	if result.Status != codexObserverStartupStale || result.Reason != "" || sink.authorityAttempts != 0 {
		t.Fatalf("replaced parent convergence result=%+v attempts=%d", result, sink.authorityAttempts)
	}
}

func newRecordingCodexLifecycleSink() *recordingCodexLifecycleSink {
	return &recordingCodexLifecycleSink{wake: make(chan struct{}, 32), current: true}
}

func (s *recordingCodexLifecycleSink) BindingCurrent(codexLifecycleIdentity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}
func (s *recordingCodexLifecycleSink) SetAuthority(_ codexLifecycleIdentity, source, epoch, reason string) error {
	s.mu.Lock()
	if !s.current {
		s.mu.Unlock()
		return errManagedAgentObservationIgnored
	}
	s.authorities = append(s.authorities, source+":"+reason)
	s.authorityEpochs = append(s.authorityEpochs, source+":"+epoch+":"+reason)
	s.mu.Unlock()
	s.record("authority:" + source)
	return nil
}
func (s *recordingCodexLifecycleSink) Apply(_ codexLifecycleIdentity, projection codexLifecycleProjection) error {
	s.mu.Lock()
	s.applyCalls++
	call := s.applyCalls
	fail := (s.failApplyAt > 0 && call == s.failApplyAt) || (s.failApplyFrom > 0 && call >= s.failApplyFrom)
	s.mu.Unlock()
	s.record(fmt.Sprintf("apply:%s:invalidated=%t:clears=%d", projection.Interaction, projection.Invalidated, len(projection.ClearNoticeIDs)))
	if fail {
		return errors.New("injected lifecycle sink failure")
	}
	return nil
}
func (s *recordingCodexLifecycleSink) record(event string) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *recordingCodexLifecycleSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *recordingCodexLifecycleSink) setCurrent(current bool) {
	s.mu.Lock()
	s.current = current
	s.mu.Unlock()
}

func (s *recordingCodexLifecycleSink) authoritySnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.authorities...)
}

func (s *recordingCodexLifecycleSink) authorityEpochSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.authorityEpochs...)
}

func TestCodexNativeObserverDropsContentBeforeProgressSinkAndClearsTerminal(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{
			ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive,
			TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
			StartedAt: time.Unix(1_700_000_000, 0).UTC(),
		},
		events: make(chan codexappserver.Notification, 4),
	}
	conn.events <- codexappserver.Notification{Method: "turn/plan/updated", Params: []byte(`{"threadId":"thread-1","turnId":"turn-1","explanation":"PRIVATE-EXPLANATION","plan":[{"status":"completed","step":"PRIVATE-STEP-A"},{"status":"inProgress","step":"PRIVATE-STEP-B"}]}`)}
	conn.events <- codexappserver.Notification{Method: "turn/diff/updated", Params: []byte(`{"threadId":"thread-1","turnId":"turn-1","diff":"diff --git a/PRIVATE-PATH b/PRIVATE-PATH\n--- a/PRIVATE-PATH\n+++ b/PRIVATE-PATH\n"}`)}
	conn.events <- codexappserver.Notification{Method: "item/started", Params: []byte(`{"threadId":"thread-1","turnId":"turn-1","startedAtMs":1700000000100,"item":{"id":"opaque-item-1","type":"commandExecution","status":"inProgress","command":"PRIVATE-COMMAND","cwd":"/PRIVATE/PATH","aggregatedOutput":"PRIVATE-OUTPUT"}}`)}
	conn.events <- codexappserver.Notification{Method: "turn/completed", Params: []byte(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","text":"PRIVATE-MESSAGE"}],"error":{"message":"PRIVATE-ERROR"}}}`)}
	close(conn.events)

	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingCodexProgressSink{recordingCodexLifecycleSink: newRecordingCodexLifecycleSink(), cancel: cancel}
	var clockMu sync.Mutex
	now := time.Unix(1_700_000_000, 0).UTC()
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(300 * time.Millisecond)
		return now
	}
	opened := false
	observer := codexNativeObserver{
		identity: identity, delay: time.Millisecond, sink: sink, now: clock,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			if !opened {
				opened = true
				return conn, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("observer did not clear terminal progress")
	}

	progress, diagnostics := sink.progressSnapshot()
	if len(progress) < 5 {
		t.Fatalf("progress writes = %#v, want startup clear, bounded updates, and terminal clear", progress)
	}
	var sawPlan, sawFiles, sawCommand bool
	for _, projection := range progress {
		sawPlan = sawPlan || (projection.PlanCompleted == 1 && projection.PlanInProgress == 1 && projection.PlanTotal == 2)
		sawFiles = sawFiles || projection.ChangedFiles == 1
		sawCommand = sawCommand || projection.Activity == coremetadata.ProgressCommand
		if strings.Contains(fmt.Sprintf("%#v", projection), "PRIVATE-") {
			t.Fatalf("content reached progress sink: %#v", projection)
		}
	}
	if !sawPlan || !sawFiles || !sawCommand {
		t.Fatalf("bounded projections plan=%t files=%t command=%t: %#v", sawPlan, sawFiles, sawCommand, progress)
	}
	if last := progress[len(progress)-1]; last != (coremetadata.AgentProgress{}) {
		t.Fatalf("terminal progress = %#v, want immediate clear", last)
	}
	for _, diagnostic := range diagnostics {
		if strings.Contains(fmt.Sprintf("%#v", diagnostic), "PRIVATE-") {
			t.Fatalf("content reached progress diagnostics: %#v", diagnostic)
		}
	}
}

func TestCodexNativeObserverForeignThreadSameTurnWritesNoRegistryProgressOrDiagnostics(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{
			ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive,
			TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
		},
		events: make(chan codexappserver.Notification, 2),
	}
	ctx := t.Context()
	sink := &recordingCodexProgressSink{recordingCodexLifecycleSink: newRecordingCodexLifecycleSink()}
	observer := codexNativeObserver{
		identity: identity, delay: time.Millisecond, sink: sink,
		// The binding loss at the end of this test exits through a bounded
		// wait, not a single sample, so the window is set short here rather
		// than left at the three-second production default.
		bindingTimeout: 20 * time.Millisecond,
		open:           func(context.Context) (codexLifecycleConnection, error) { return conn, nil },
	}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	waitForCodexObserverEvents(t, sink.recordingCodexLifecycleSink, 2)
	progressBefore, diagnosticsBefore := sink.progressSnapshot()

	conn.events <- codexappserver.Notification{Method: "turn/plan/updated", Params: []byte(`{"threadId":"thread-foreign","turnId":"turn-1","plan":[{"status":"completed","step":"PRIVATE-FOREIGN"}]}`)}
	// The accepted lifecycle marker is queued after the foreign progress event,
	// so its sink write proves the observer consumed the earlier event.
	conn.events <- codexappserver.Notification{Method: "thread/status/changed", Params: []byte(`{"threadId":"thread-1","status":{"type":"active","activeFlags":[]}}`)}
	waitForCodexObserverEvents(t, sink.recordingCodexLifecycleSink, 3)

	progress, diagnostics := sink.progressSnapshot()
	if !reflect.DeepEqual(progress, progressBefore) {
		t.Fatalf("foreign thread added Registry/progress sink writes: before=%#v after=%#v", progressBefore, progress)
	}
	if !reflect.DeepEqual(diagnostics, diagnosticsBefore) {
		t.Fatalf("foreign thread added diagnostics writes: before=%#v after=%#v", diagnosticsBefore, diagnostics)
	}
	if len(progress) != 2 || !progress[0].IsZero() || progress[1].TurnRef != "turn-1" ||
		len(diagnostics) != 2 || diagnostics[0] != (agentprogress.Diagnostics{}) || diagnostics[1] != (agentprogress.Diagnostics{}) {
		t.Fatalf("unexpected baseline progress=%#v diagnostics=%#v", progress, diagnostics)
	}
	sink.setCurrent(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not exit after exact binding loss")
	}
}

func TestCodexNativeObserverInvalidatesBeforeHookFallback(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		events:   make(chan codexappserver.Notification, 3),
	}
	conn.events <- codexappserver.Notification{Method: "thread/status/changed", Params: []byte(`{"threadId":"thread-1","status":{"type":"active","activeFlags":["waitingOnApproval"]}}`)}
	conn.events <- codexappserver.Notification{Method: "item/commandExecution/requestApproval", RequestID: "request-1", Params: []byte(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1"}`)}
	conn.events <- codexappserver.Notification{Method: "thread/status/changed", Params: []byte(`{"threadId":"thread-1","status":{"type":"notLoaded"}}`)}
	close(conn.events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newRecordingCodexLifecycleSink()
	opened := false
	observer := codexNativeObserver{
		identity: identity,
		delay:    time.Millisecond,
		sink:     sink,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			if !opened {
				opened = true
				return conn, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	want := []string{
		"apply:in_progress:invalidated=false:clears=0",
		"authority:provider-control-plane",
		"apply:in_progress:invalidated=false:clears=0",
		"apply:approval_required:invalidated=false:clears=0",
		"authority:invalidating",
		"apply:unknown:invalidated=true:clears=1",
		"authority:provider-hook",
	}
	waitForCodexObserverEvents(t, sink, len(want))
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := sink.snapshot()[:len(want)]; !reflect.DeepEqual(got, want) {
		t.Fatalf("observer order = %#v, want %#v", got, want)
	}
}

func TestCodexNativeObserverBindingTimeoutFallsBackOnlyThroughExactGuard(t *testing.T) {
	for _, test := range []struct {
		name           string
		allowAuthority bool
		want           []string
	}{
		{name: "still current", allowAuthority: true, want: []string{"provider-hook:observer-timeout"}},
		{name: "binding replaced", allowAuthority: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &timeoutCodexLifecycleSink{allowAuthority: test.allowAuthority}
			observer := codexNativeObserver{
				identity: testCodexLifecycleIdentity(), sink: sink, bindingTimeout: time.Millisecond,
				open: func(context.Context) (codexLifecycleConnection, error) {
					t.Fatal("connection opened without an exact binding")
					return nil, nil
				},
			}
			if err := observer.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sink.authorities, test.want) {
				t.Fatalf("timeout authorities = %#v, want %#v", sink.authorities, test.want)
			}
		})
	}
}

func TestCodexNativeObserverConnectDeadlineConvergesToActionableFallback(t *testing.T) {
	identity := codexLifecycleIdentity{AgentUID: "agent-1", PaneUID: "pane-1", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
	sink := &recordingCodexLifecycleSink{current: true, wake: make(chan struct{}, 20)}
	observer := codexNativeObserver{
		identity: identity, sink: sink, openTimeout: 5 * time.Millisecond, delay: time.Hour,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := observer.Run(ctx); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !slices.Contains(sink.authorities, codexAuthorityHook+":timeout") {
		t.Fatalf("bounded connect authorities = %q", sink.authorities)
	}
}

func TestCodexNativeObserverWithoutControlEndpointNeverClaimsReadyEpoch(t *testing.T) {
	identity := codexLifecycleIdentity{AgentUID: "agent-1", PaneUID: "pane-1", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
	sink := &recordingCodexLifecycleSink{current: true, wake: make(chan struct{}, 20)}
	connection := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle},
		events:   make(chan codexappserver.Notification),
	}
	observer := codexNativeObserver{
		identity: identity, sink: sink, requireControl: true, delay: time.Hour,
		open: func(context.Context) (codexLifecycleConnection, error) { return connection, nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := observer.Run(ctx); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !slices.Contains(sink.authorities, codexAuthorityHook+":control-unavailable") {
		t.Fatalf("missing control fallback authorities = %q", sink.authorities)
	}
	for _, authority := range sink.authorities {
		if strings.HasPrefix(authority, codexAuthorityControlPlane+":") {
			t.Fatalf("observer without endpoint claimed ready authority: %q", sink.authorities)
		}
	}
}

func TestCodexNativeObserverNotLoadedSnapshotClearsBeforeFallbackWithoutHealthyEpoch(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{
			ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateNotLoaded,
			TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted,
		},
		events: make(chan codexappserver.Notification),
	}
	sink := newRecordingCodexLifecycleSink()
	ctx, cancel := context.WithCancel(context.Background())
	observer := codexNativeObserver{identity: identity, delay: time.Millisecond, sink: sink, open: func(context.Context) (codexLifecycleConnection, error) {
		return conn, nil
	}}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	want := []string{
		"authority:invalidating",
		"apply:unknown:invalidated=true:clears=0",
		"authority:provider-hook",
	}
	waitForCodexObserverEvents(t, sink, len(want))
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := sink.snapshot()[:len(want)]; !reflect.DeepEqual(got, want) {
		t.Fatalf("not-loaded snapshot order = %#v, want %#v", got, want)
	}
	if containsCodexObserverEvent(sink.snapshot(), "authority:provider-control-plane") {
		t.Fatalf("not-loaded snapshot established healthy authority: %#v", sink.snapshot())
	}
}

func TestCodexNativeObserverReconnectSnapshotReplacesInvalidatedEpoch(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	connections := []*fakeCodexLifecycleConnection{
		{snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}, events: make(chan codexappserver.Notification)},
		{snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle}, events: make(chan codexappserver.Notification)},
	}
	close(connections[0].events)
	close(connections[1].events)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newRecordingCodexLifecycleSink()
	openIndex := 0
	observer := codexNativeObserver{identity: identity, delay: time.Millisecond, sink: sink, open: func(ctx context.Context) (codexLifecycleConnection, error) {
		if openIndex < len(connections) {
			connection := connections[openIndex]
			openIndex++
			return connection, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	waitForCodexObserverEvents(t, sink, 8)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	events := sink.snapshot()
	wantSecondEpoch := []string{
		"apply:idle:invalidated=false:clears=0",
		"authority:provider-control-plane",
		"authority:invalidating",
		"apply:unknown:invalidated=true:clears=0",
	}
	if !reflect.DeepEqual(events[4:8], wantSecondEpoch) {
		t.Fatalf("reconnect convergence = %#v, want %#v", events, wantSecondEpoch)
	}
	if containsCodexObserverEvent(events, "authority:provider-hook") {
		t.Fatalf("transient reconnect exposed hook fallback: %#v", events)
	}
}

func TestCodexNativeObserverEndpointReplacementRevokesE1BeforeGapAndPublishesExactE2(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	firstWire, secondWire := &fakeExactControlWire{}, &fakeExactControlWire{}
	first := &fakeControllableCodexLifecycleConnection{
		fakeCodexLifecycleConnection: &fakeCodexLifecycleConnection{
			snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
			events:   make(chan codexappserver.Notification),
		},
		fakeExactControlWire: firstWire,
	}
	second := &fakeControllableCodexLifecycleConnection{
		fakeCodexLifecycleConnection: &fakeCodexLifecycleConnection{
			snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle},
			events:   make(chan codexappserver.Notification),
		},
		fakeExactControlWire: secondWire,
	}
	sink := newRecordingCodexLifecycleSink()
	startup := make(chan codexObserverStartupResult, 8)
	allowReconnect := make(chan struct{})
	openCalls := 0
	var epochs []*codexControlEpoch
	observer := codexNativeObserver{
		identity: identity, sink: sink, requireControl: true,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			openCalls++
			switch openCalls {
			case 1:
				return first, nil
			case 2:
				return second, nil
			default:
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
		startControl: func(epoch *codexControlEpoch) (*codexControlServer, error) {
			epochs = append(epochs, epoch)
			return &codexControlServer{epoch: epoch}, nil
		},
		reportStartup: func(result codexObserverStartupResult) { startup <- result },
		waitRecovery: func(ctx context.Context, _ time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-allowReconnect:
				return true
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	e1 := waitForCodexObserverStartupResult(t, startup, codexObserverStartupReady)
	if e1.Epoch == "" || len(epochs) != 1 {
		cancel()
		<-done
		t.Fatalf("E1 startup=%+v epochs=%d", e1, len(epochs))
	}
	close(first.events)
	waitForCodexObserverEvents(t, sink, 4)
	gapRequest := agentControlRequest{Operation: agentControlOpSteer, Identity: identity, Epoch: e1.Epoch, Text: "must not write"}
	if old := epochs[0].Handle(context.Background(), gapRequest); old.OK || firstWire.writes() != 0 {
		cancel()
		<-done
		t.Fatalf("revoked E1 accepted late control: response=%+v writes=%d", old, firstWire.writes())
	}
	close(allowReconnect)
	e2 := waitForCodexObserverStartupResult(t, startup, codexObserverStartupReady)
	if e2.Epoch == "" || e2.Epoch == e1.Epoch || len(epochs) != 2 {
		cancel()
		<-done
		t.Fatalf("replacement E1=%+v E2=%+v epochs=%d", e1, e2, len(epochs))
	}
	if stale := epochs[1].Handle(context.Background(), gapRequest); stale.OK || secondWire.writes() != 0 {
		cancel()
		<-done
		t.Fatalf("E1 request against E2 response=%+v E2writes=%d", stale, secondWire.writes())
	}
	exact := agentControlRequest{Operation: agentControlOpStart, Identity: identity, Epoch: e2.Epoch, Text: "new exact turn"}
	if response := epochs[1].Handle(context.Background(), exact); !response.OK || secondWire.writes() != 1 {
		cancel()
		<-done
		t.Fatalf("E2 control response=%+v writes=%d", response, secondWire.writes())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	beforeLate := sink.snapshot()
	late := observer.reducer.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleTurnStarted, ThreadID: identity.ThreadID, TurnID: "turn-late", TurnState: codexappserver.TurnStateInProgress,
	})
	if late.Accepted || !reflect.DeepEqual(sink.snapshot(), beforeLate) {
		t.Fatalf("late E1 event accepted=%t before=%#v after=%#v", late.Accepted, beforeLate, sink.snapshot())
	}
	authorities := sink.authorityEpochSnapshot()
	firstReady := codexAuthorityControlPlane + ":" + e1.Epoch + ":ready"
	secondReady := codexAuthorityControlPlane + ":" + e2.Epoch + ":ready"
	// The gap token is stream-closed, not disconnected: these fake connections
	// close their stream without recording a cause, and that state is now
	// named apart from an endpoint that actually went away.
	wantOrder := []string{firstReady, codexAuthorityInvalidating + ":" + e1.Epoch + ":" + string(codexObserverReasonStreamClosed), secondReady}
	position := 0
	secondReadyPosition := -1
	for index, authority := range authorities {
		if position < len(wantOrder) && authority == wantOrder[position] {
			position++
		}
		if authority == secondReady && secondReadyPosition < 0 {
			secondReadyPosition = index
		}
	}
	if position != len(wantOrder) {
		t.Fatalf("authority replacement order=%q want subsequence=%q", authorities, wantOrder)
	}
	for _, authority := range authorities[:secondReadyPosition] {
		if strings.HasPrefix(authority, codexAuthorityHook+":") {
			t.Fatalf("replacement gap exposed hook fallback authority: %q", authorities)
		}
	}
	if response := epochs[1].Handle(context.Background(), exact); response.OK || secondWire.writes() != 1 {
		t.Fatalf("E2 control remained live after shutdown: response=%+v writes=%d", response, secondWire.writes())
	}
	if first.closeCount() != 1 || second.closeCount() != 1 {
		t.Fatalf("replacement connection cleanup E1=%d E2=%d, want 1/1", first.closeCount(), second.closeCount())
	}
}

// TestCodexNativeTwoAgentDisconnectRecoversSameAgentControlAndStableProjection
// is the combined C-1 guard. One activation loses its exact control epoch while
// a sibling remains healthy; the target publishes one unavailable projection,
// writes no provider-hook state during the gap, and admits control again only
// through the replacement snapshot's new epoch.
func TestCodexNativeTwoAgentDisconnectRecoversSameAgentControlAndStableProjection(t *testing.T) {
	identityA := testCodexLifecycleIdentity()
	identityB := codexLifecycleIdentity{
		AgentUID: "agent-2", PaneUID: "pane-2", RuntimeID: "%8",
		Generation: "generation-2", ThreadID: "thread-2",
	}
	wireA1, wireA2 := &fakeExactControlWire{}, &fakeExactControlWire{}
	wireB := &fakeExactControlWire{threadID: identityB.ThreadID, turnID: "turn-2"}
	connection := func(identity codexLifecycleIdentity, snapshot codexappserver.LifecycleSnapshot, wire *fakeExactControlWire) *fakeControllableCodexLifecycleConnection {
		return &fakeControllableCodexLifecycleConnection{
			fakeCodexLifecycleConnection: &fakeCodexLifecycleConnection{snapshot: snapshot, events: make(chan codexappserver.Notification)},
			fakeExactControlWire:         wire,
		}
	}
	firstA := connection(identityA, codexappserver.LifecycleSnapshot{
		ThreadID: identityA.ThreadID, ThreadState: codexappserver.ThreadStateActive,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	}, wireA1)
	secondA := connection(identityA, codexappserver.LifecycleSnapshot{
		ThreadID: identityA.ThreadID, ThreadState: codexappserver.ThreadStateIdle,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted,
	}, wireA2)
	connectionB := connection(identityB, codexappserver.LifecycleSnapshot{
		ThreadID: identityB.ThreadID, ThreadState: codexappserver.ThreadStateActive,
		TurnID: "turn-2", TurnState: codexappserver.TurnStateInProgress,
	}, wireB)

	sinkA, sinkB := newRecordingCodexLifecycleSink(), newRecordingCodexLifecycleSink()
	startupA, startupB := make(chan codexObserverStartupResult, 4), make(chan codexObserverStartupResult, 2)
	allowReconnect := make(chan struct{})
	var epochsA, epochsB []*codexControlEpoch
	openA := 0
	observerA := codexNativeObserver{
		identity: identityA, sink: sinkA, requireControl: true,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			openA++
			switch openA {
			case 1:
				return firstA, nil
			case 2:
				return secondA, nil
			default:
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
		startControl: func(epoch *codexControlEpoch) (*codexControlServer, error) {
			epochsA = append(epochsA, epoch)
			return &codexControlServer{epoch: epoch}, nil
		},
		reportStartup: func(result codexObserverStartupResult) { startupA <- result },
		waitRecovery: func(ctx context.Context, _ time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-allowReconnect:
				return true
			}
		},
	}
	observerB := codexNativeObserver{
		identity: identityB, sink: sinkB, requireControl: true,
		open: func(context.Context) (codexLifecycleConnection, error) { return connectionB, nil },
		startControl: func(epoch *codexControlEpoch) (*codexControlServer, error) {
			epochsB = append(epochsB, epoch)
			return &codexControlServer{epoch: epoch}, nil
		},
		reportStartup: func(result codexObserverStartupResult) { startupB <- result },
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- observerA.Run(ctxA) }()
	go func() { doneB <- observerB.Run(ctxB) }()
	e1 := waitForCodexObserverStartupResult(t, startupA, codexObserverStartupReady)
	b1 := waitForCodexObserverStartupResult(t, startupB, codexObserverStartupReady)
	if len(epochsA) != 1 || len(epochsB) != 1 {
		cancelA()
		cancelB()
		<-doneA
		<-doneB
		t.Fatalf("initial control epochs A=%d B=%d", len(epochsA), len(epochsB))
	}
	approval := codexappserver.Notification{
		Method: "item/commandExecution/requestApproval", RequestID: "7", RawRequestID: json.RawMessage(`7`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"make test","cwd":"/work","availableDecisions":["accept","decline","cancel"]}`),
	}
	if err := epochsA[0].ApplyNotification(approval); err != nil {
		t.Fatal(err)
	}

	siblingProjection := sinkB.snapshot()
	siblingAuthority := sinkB.authorityEpochSnapshot()
	close(firstA.events)
	waitForCodexObserverEvents(t, sinkA, 4)
	gapProjection := sinkA.snapshot()
	gapAuthority := sinkA.authorityEpochSnapshot()
	for range 16 {
		if got := sinkA.snapshot(); !reflect.DeepEqual(got, gapProjection) {
			t.Fatalf("reconnect polling changed target projection: before=%#v after=%#v", gapProjection, got)
		}
		if got := sinkA.authorityEpochSnapshot(); !reflect.DeepEqual(got, gapAuthority) {
			t.Fatalf("reconnect polling changed target authority: before=%#v after=%#v", gapAuthority, got)
		}
	}
	if got := gapAuthority; !reflect.DeepEqual(got, []string{
		codexAuthorityControlPlane + ":" + e1.Epoch + ":ready",
		codexAuthorityInvalidating + ":" + e1.Epoch + ":" + string(codexObserverReasonStreamClosed),
	}) {
		t.Fatalf("target gap authority = %q", got)
	}
	if containsCodexObserverEvent(gapProjection, "authority:provider-hook") {
		t.Fatalf("target gap exposed provider-hook: %#v", gapProjection)
	}

	stale := []agentControlRequest{
		{Operation: agentControlOpStart, Identity: identityA, Epoch: e1.Epoch, Text: "stale start"},
		{Operation: agentControlOpSteer, Identity: identityA, Epoch: e1.Epoch, Text: "stale steer"},
		{Operation: agentControlOpInterrupt, Identity: identityA, Epoch: e1.Epoch},
		{Operation: agentControlOpReview, Identity: identityA, Epoch: e1.Epoch, RequestKey: "7", Decision: "accept"},
	}
	for _, request := range stale {
		if response := epochsA[0].Handle(context.Background(), request); response.OK {
			t.Fatalf("stale %s control was accepted: %+v", request.Operation, response)
		}
	}
	if wireA1.writes() != 0 || wireA2.writes() != 0 {
		t.Fatalf("reconnect gap writes E1=%d E2=%d, want 0/0", wireA1.writes(), wireA2.writes())
	}

	siblingControl := agentControlRequest{Operation: agentControlOpSteer, Identity: identityB, Epoch: b1.Epoch, Text: "sibling remains live"}
	if response := epochsB[0].Handle(context.Background(), siblingControl); !response.OK || wireB.writes() != 1 {
		t.Fatalf("healthy sibling control response=%+v writes=%d", response, wireB.writes())
	}
	if got := sinkB.snapshot(); !reflect.DeepEqual(got, siblingProjection) {
		t.Fatalf("target disconnect changed sibling projection: before=%#v after=%#v", siblingProjection, got)
	}
	if got := sinkB.authorityEpochSnapshot(); !reflect.DeepEqual(got, siblingAuthority) {
		t.Fatalf("target disconnect changed sibling authority: before=%#v after=%#v", siblingAuthority, got)
	}

	close(allowReconnect)
	e2 := waitForCodexObserverStartupResult(t, startupA, codexObserverStartupReady)
	waitForCodexObserverEvents(t, sinkA, 6)
	if len(epochsA) != 2 || e2.Epoch == "" || e2.Epoch == e1.Epoch {
		t.Fatalf("replacement control epochs=%d E1=%+v E2=%+v", len(epochsA), e1, e2)
	}
	if response := epochsA[1].Handle(context.Background(), stale[0]); response.OK || wireA2.writes() != 0 {
		t.Fatalf("E1 request crossed into E2: response=%+v E2writes=%d", response, wireA2.writes())
	}
	exact := agentControlRequest{Operation: agentControlOpStart, Identity: identityA, Epoch: e2.Epoch, Text: "replacement exact turn"}
	if response := epochsA[1].Handle(context.Background(), exact); !response.OK || wireA2.writes() != 1 {
		t.Fatalf("same-Agent E2 control response=%+v writes=%d", response, wireA2.writes())
	}
	converged := sinkA.snapshot()
	countEvent := func(want string) int {
		count := 0
		for _, event := range converged {
			if event == want {
				count++
			}
		}
		return count
	}
	if count := countEvent("apply:unknown:invalidated=true:clears=0"); count != 1 {
		t.Fatalf("unavailable projection writes=%d events=%#v", count, converged)
	}
	if count := countEvent("apply:response_complete:invalidated=false:clears=0"); count != 1 {
		t.Fatalf("replacement semantic writes=%d events=%#v", count, converged)
	}
	for _, authority := range sinkA.authorityEpochSnapshot() {
		if strings.HasPrefix(authority, codexAuthorityHook+":") {
			t.Fatalf("reconnect sequence exposed hook authority: %q", sinkA.authorityEpochSnapshot())
		}
	}

	cancelA()
	cancelB()
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
}

// TestRetiredObserverRecoveryBackoffIsCappedAndNeverExhausts replaces the
// retired six-attempt exhaustion proof.
//
// The observer used to publish a terminal `reconnect-exhausted` fallback and
// exit once a fixed budget ran out, which abandoned a live activation whose
// endpoint was merely away. Recovery is now bounded only by the capped backoff
// and by the exact binding: the wait sequence still saturates at maxDelay, the
// gap still writes nothing upstream, and the only exits are a replaced binding
// and a cancelled context.
func TestRetiredObserverRecoveryBackoffIsCappedAndNeverExhausts(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	wire := &fakeExactControlWire{}
	connection := &fakeControllableCodexLifecycleConnection{
		fakeCodexLifecycleConnection: &fakeCodexLifecycleConnection{
			snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle},
			events:   make(chan codexappserver.Notification),
		},
		fakeExactControlWire: wire,
	}
	sink := newRecordingCodexLifecycleSink()
	startup := make(chan codexObserverStartupResult, 4)
	const observedWaits = 6
	var mu sync.Mutex
	openCalls := 0
	var waits []time.Duration
	enough := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := codexNativeObserver{
		identity: identity, sink: sink, requireControl: true,
		delay: time.Millisecond, maxDelay: 4 * time.Millisecond,
		open: func(context.Context) (codexLifecycleConnection, error) {
			mu.Lock()
			openCalls++
			first := openCalls == 1
			mu.Unlock()
			if first {
				return connection, nil
			}
			return nil, codexappserver.ErrDisconnected
		},
		startControl: func(epoch *codexControlEpoch) (*codexControlServer, error) {
			return &codexControlServer{epoch: epoch}, nil
		},
		reportStartup: func(result codexObserverStartupResult) { startup <- result },
		waitRecovery: func(_ context.Context, delay time.Duration) bool {
			mu.Lock()
			waits = append(waits, delay)
			count := len(waits)
			mu.Unlock()
			if count == observedWaits {
				close(enough)
			}
			return true
		},
	}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	e1 := waitForCodexObserverStartupResult(t, startup, codexObserverStartupReady)
	close(connection.events)
	select {
	case <-enough:
	case err := <-done:
		t.Fatalf("recovery terminated while the binding was still current: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("recovery stopped retrying before the capped backoff was observed")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled observer did not stop")
	}
	mu.Lock()
	observed := append([]time.Duration(nil), waits...)
	mu.Unlock()
	if len(observed) < observedWaits {
		t.Fatalf("recovery waits=%v, want at least %d", observed, observedWaits)
	}
	want := []time.Duration{
		time.Millisecond, 2 * time.Millisecond,
		4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond,
	}
	if !reflect.DeepEqual(observed[:observedWaits], want) {
		t.Fatalf("recovery waits=%v, want capped exponential backoff %v", observed[:observedWaits], want)
	}
	if wire.writes() != 0 {
		t.Fatalf("reconnect gap wrote app-server wire %d times", wire.writes())
	}
	authorities := sink.authorityEpochSnapshot()
	for _, entry := range authorities {
		if strings.Contains(entry, retiredObserverExhaustionReason) {
			t.Fatalf("the retired exhaustion fallback was published: %q", authorities)
		}
	}
	if !slices.Contains(authorities, codexAuthorityInvalidating+":"+e1.Epoch+":"+string(codexObserverReasonStreamClosed)) {
		t.Fatalf("E1 was not invalidated before recovery: %q", authorities)
	}
	if connection.closeCount() != 1 {
		t.Fatalf("recovering observer connection closes=%d, want 1", connection.closeCount())
	}
}

// TestCodexNativeObserversSharingOneEndpointRecoverIndependently replaces the
// retired independent-exhaustion proof with the containment it was really
// about: one activation losing its epoch reconnects onto its own replacement
// without touching, terminating, or reordering the other activation, and
// neither of them can now be ended by a retry budget.
func TestCodexNativeObserversSharingOneEndpointRecoverIndependently(t *testing.T) {
	type endpointCounts struct {
		mu    sync.Mutex
		calls map[string]int
	}
	shared := &endpointCounts{calls: map[string]int{}}
	openCount := func(agent string) int {
		shared.mu.Lock()
		defer shared.mu.Unlock()
		shared.calls[agent]++
		return shared.calls[agent]
	}
	identityA := testCodexLifecycleIdentity()
	identityB := codexLifecycleIdentity{AgentUID: "agent-2", PaneUID: "pane-2", RuntimeID: "%8", Generation: "generation-2", ThreadID: "thread-2"}
	firstA := &fakeCodexLifecycleConnection{snapshot: codexappserver.LifecycleSnapshot{ThreadID: identityA.ThreadID, ThreadState: codexappserver.ThreadStateIdle}, events: make(chan codexappserver.Notification)}
	secondA := &fakeCodexLifecycleConnection{snapshot: codexappserver.LifecycleSnapshot{ThreadID: identityA.ThreadID, ThreadState: codexappserver.ThreadStateIdle}, events: make(chan codexappserver.Notification)}
	firstB := &fakeCodexLifecycleConnection{snapshot: codexappserver.LifecycleSnapshot{ThreadID: identityB.ThreadID, ThreadState: codexappserver.ThreadStateIdle}, events: make(chan codexappserver.Notification)}
	sinkA, sinkB := newRecordingCodexLifecycleSink(), newRecordingCodexLifecycleSink()
	waitImmediately := func(context.Context, time.Duration) bool { return true }
	observerA := codexNativeObserver{
		identity: identityA, sink: sinkA, waitRecovery: waitImmediately,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			switch openCount(identityA.AgentUID) {
			case 1:
				return firstA, nil
			case 2:
				return nil, codexappserver.ErrDisconnected
			case 3:
				return secondA, nil
			default:
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
	}
	observerB := codexNativeObserver{
		identity: identityB, sink: sinkB, waitRecovery: waitImmediately,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			if openCount(identityB.AgentUID) == 1 {
				return firstB, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- observerA.Run(ctxA) }()
	go func() { doneB <- observerB.Run(ctxB) }()
	waitForCodexObserverEvents(t, sinkA, 2)
	waitForCodexObserverEvents(t, sinkB, 2)
	beforeB := len(sinkB.snapshot())
	close(firstA.events)
	waitForCodexObserverEvents(t, sinkA, 6)
	select {
	case err := <-doneB:
		cancelA()
		<-doneA
		cancelB()
		t.Fatalf("the untouched activation stopped with its sibling: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if after := len(sinkB.snapshot()); after != beforeB {
		cancelA()
		<-doneA
		cancelB()
		<-doneB
		t.Fatalf("sibling recovery projected %d extra events onto the untouched activation", after-beforeB)
	}
	shared.mu.Lock()
	callsA, callsB := shared.calls[identityA.AgentUID], shared.calls[identityB.AgentUID]
	shared.mu.Unlock()
	if callsA != 3 || callsB != 1 {
		cancelA()
		<-doneA
		cancelB()
		<-doneB
		t.Fatalf("shared endpoint opens A=%d B=%d, want 3 for the recovering activation and 1 for the untouched one", callsA, callsB)
	}
	for name, authorities := range map[string][]string{"A": sinkA.authorityEpochSnapshot(), "B": sinkB.authorityEpochSnapshot()} {
		for _, entry := range authorities {
			if strings.Contains(entry, retiredObserverExhaustionReason) {
				cancelA()
				<-doneA
				cancelB()
				<-doneB
				t.Fatalf("observer %s published the retired exhaustion fallback: %q", name, authorities)
			}
		}
	}
	cancelA()
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	cancelB()
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
	if firstA.closeCount() != 1 || secondA.closeCount() != 1 || firstB.closeCount() != 1 {
		t.Fatalf("multi-observer connection cleanup A1=%d A2=%d B1=%d", firstA.closeCount(), secondA.closeCount(), firstB.closeCount())
	}
}

func waitForCodexObserverStartupResult(t *testing.T, results <-chan codexObserverStartupResult, status codexObserverStartupStatus) codexObserverStartupResult {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	var seen []codexObserverStartupResult
	for {
		select {
		case result := <-results:
			seen = append(seen, result)
			if result.Status == status {
				return result
			}
		case <-deadline.C:
			t.Fatalf("observer did not report startup status %s; seen=%+v", status, seen)
		}
	}
}

func TestCodexNativeObserverCancellationInvalidatesSilentConnection(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		events:   make(chan codexappserver.Notification),
	}
	ctx, cancel := context.WithCancel(context.Background())
	sink := newRecordingCodexLifecycleSink()
	observer := codexNativeObserver{identity: identity, delay: time.Millisecond, sink: sink, open: func(context.Context) (codexLifecycleConnection, error) { return conn, nil }}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	waitForCodexObserverEvents(t, sink, 2)
	cancel()
	waitForCodexObserverEvents(t, sink, 5)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply:in_progress:invalidated=false:clears=0",
		"authority:provider-control-plane",
		"authority:invalidating",
		"apply:unknown:invalidated=true:clears=0",
		"authority:provider-hook",
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("silent cancellation order = %#v, want %#v", got, want)
	}
}

func TestCodexNativeObserverBindingLossExitsSilentConnectionWithoutWrites(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle},
		events:   make(chan codexappserver.Notification),
	}
	ctx := t.Context()
	sink := newRecordingCodexLifecycleSink()
	// The exit this test owns waits the binding window out before it terminates,
	// because BindingCurrent returns the same false for a replaced binding and
	// for one that could not be read right now. The window is short here; the
	// contract below is unchanged - a provably lost binding still writes no
	// stale cleanup and no fallback authority.
	observer := codexNativeObserver{
		identity: identity, delay: time.Millisecond, sink: sink, bindingTimeout: 20 * time.Millisecond,
		open: func(context.Context) (codexLifecycleConnection, error) { return conn, nil },
	}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	waitForCodexObserverEvents(t, sink, 2)
	sink.setCurrent(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not exit after exact binding loss")
	}
	if got := sink.snapshot(); len(got) != 2 {
		t.Fatalf("binding loss wrote stale cleanup/fallback: %#v", got)
	}
}

func TestCodexNativeObserverSinkFailureRemainsInvalidatingWhenClearFails(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		events:   make(chan codexappserver.Notification, 1),
	}
	conn.events <- codexappserver.Notification{Method: "thread/status/changed", Params: []byte(`{"threadId":"thread-1","status":{"type":"idle"}}`)}
	sink := newRecordingCodexLifecycleSink()
	sink.failApplyFrom = 2
	observer := codexNativeObserver{identity: identity, delay: time.Millisecond, sink: sink, open: func(context.Context) (codexLifecycleConnection, error) { return conn, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := observer.Run(ctx); err == nil {
		t.Fatal("sink failure was swallowed")
	}
	events := sink.snapshot()
	if !containsCodexObserverEvent(events, "authority:invalidating") || containsCodexObserverEvent(events, "authority:provider-hook") {
		t.Fatalf("failed clear exposed fallback authority: %#v", events)
	}
	if authorities := sink.authoritySnapshot(); len(authorities) == 0 || authorities[len(authorities)-1] != "invalidating:sink-error" {
		t.Fatalf("failed clear diagnostic authority = %#v", authorities)
	}
}

func TestCodexNativeObserverInitialSinkFailureClearsPendingBeforeFallback(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		events:   make(chan codexappserver.Notification),
	}
	sink := newRecordingCodexLifecycleSink()
	sink.failApplyAt = 1
	observer := codexNativeObserver{identity: identity, delay: time.Millisecond, sink: sink, open: func(context.Context) (codexLifecycleConnection, error) { return conn, nil }}
	if err := observer.Run(context.Background()); err == nil {
		t.Fatal("initial sink failure was swallowed")
	}
	want := []string{
		"apply:in_progress:invalidated=false:clears=0",
		"authority:invalidating",
		"apply:unknown:invalidated=true:clears=0",
		"authority:provider-hook",
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("initial sink cleanup = %#v, want %#v", got, want)
	}
}

func TestCodexLifecycleSinkIntegratesExactRegistryTmuxAndQuietPolicy(t *testing.T) {
	store := newFakeResourceStore(t)
	mutator := store.mutator()
	if _, err := mutator.RecordPaneActivation(&store.registry, "pan-alpha-codex", coremetadata.PaneActivationOptions{
		Generation: "generation-1", RuntimeID: "%7", AgentUID: "agt-alpha-codex", OperationID: "phase3-test",
	}); err != nil {
		t.Fatal(err)
	}
	bindNativeCodexTestFixture(t, store, mutator, coremetadata.CodexActivationObservation{
		AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", Generation: "generation-1", ThreadID: "thread-1",
	})
	cmd := testAICommand(t.TempDir())
	notifyStore := &stubNotifyStore{}
	cmd.notifyStore = notifyStore
	cmd.producer = &storeAttentionNotifyProducer{store: notifyStore, ttl: time.Minute}
	cmd.loadRegistry = store.store().load
	cmd.updateRegistry = store.store().update
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) == 5 && reflect.DeepEqual(args[:4], []string{"show-options", "-pqv", "-t", "%7"}) {
			switch args[4] {
			case tmuxopts.PaneUID:
				return []byte("pan-alpha-codex\n"), nil
			case aiPaneAgentOption:
				return []byte("codex\n"), nil
			case aiPaneTopicOption:
				return []byte("topic\n"), nil
			}
		}
		if name == "tmux" && len(args) == 5 && reflect.DeepEqual(args[:4], []string{"display-message", "-p", "-t", "%7"}) {
			switch args[4] {
			case "#{@projmux_pane_uid}":
				return []byte("pan-alpha-codex\n"), nil
			case "#{@projmux_ai_agent}":
				return []byte("codex\n"), nil
			case "#S":
				return []byte("phase3\n"), nil
			case "#{window_id}":
				return []byte("@3\n"), nil
			case "#{pane_id}":
				return []byte("%7\n"), nil
			case "#{socket_path}":
				return []byte("/tmp/phase3.sock\n"), nil
			case "#{pane_current_path}":
				return []byte("/repo/projmux\n"), nil
			}
		}
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		return nil, os.ErrNotExist
	}
	paths, err := configPaths(cmd.homeDir, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	policies := config.DefaultAISemanticPolicies()
	policies.Events[config.AISemanticApprovalRequired] = config.AISemanticQuiet
	if err := config.SaveAISemanticPoliciesFile(paths.AISemanticPoliciesFile(), policies); err != nil {
		t.Fatal(err)
	}
	identity := codexLifecycleIdentity{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
	projection := codexLifecycleProjection{Accepted: true, Interaction: coremetadata.InteractionApprovalRequired, Notices: []codexLifecycleNotice{{
		Category: "approval_required", ID: "notice-1", Severity: "critical", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", RequestID: "request-1",
	}}}
	notifyStore.ackErr = notify.ErrNotFound
	if err := testCodexLifecycleSink(cmd).Apply(identity, projection); err != nil {
		t.Fatal(err)
	}
	agent, _ := store.registry.Agent(identity.AgentUID)
	if agent.Status.Interaction.Kind != coremetadata.InteractionUnknown || agent.Status.Interaction.Source != string(coremetadata.InteractionSourceProviderControl) {
		t.Fatalf("quiet Registry projection = %#v", agent.Status.Interaction)
	}
	if len(notifyStore.pushed) != 0 || notifyStore.ackedID != "notice-1" {
		t.Fatalf("quiet notification writes pushed=%#v acked=%q", notifyStore.pushed, notifyStore.ackedID)
	}
	notifyStore.ackErr = nil
	commands := cmdRecorder(cmd).commands
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneStateOption, "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%7", aiPaneBadgeKindOption}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%7", attentionStateOption}},
	} {
		if !hasRecordedAICommand(commands, want) {
			t.Fatalf("tmux projection = %#v, missing %#v", commands, want)
		}
	}

	policies.Events[config.AISemanticApprovalRequired] = config.AISemanticNotify
	if err := config.SaveAISemanticPoliciesFile(paths.AISemanticPoliciesFile(), policies); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	if err := testCodexLifecycleSink(cmd).Apply(identity, codexLifecycleProjection{
		Accepted: true, Interaction: coremetadata.InteractionApprovalRequired,
		Notices: []codexLifecycleNotice{{Category: "approval_required", ID: "notice-2", Severity: "critical", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", RequestID: "request-2", ResponderAvailable: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(notifyStore.pushed) != 1 || notifyStore.pushed[0].ID != "notice-2" {
		t.Fatalf("Notify queue writes = %#v", notifyStore.pushed)
	}
	if got := notifyStore.pushed[0].Metadata["action_label"]; got != agentActionReviewApproval || notifyStore.pushed[0].Metadata["focus_available"] != "true" {
		t.Fatalf("pending approval availability = %#v", notifyStore.pushed[0].Metadata)
	}
	for key := range notifyStore.pushed[0].Metadata {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{"prompt", "reasoning", "output", "diff", "body", "text"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("Notify metadata leaked forbidden field %q: %#v", key, notifyStore.pushed[0].Metadata)
			}
		}
	}
	desktopWrites := 0
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			desktopWrites++
		}
	}
	if desktopWrites != 1 {
		t.Fatalf("Notify desktop writes = %d; commands=%#v", desktopWrites, cmdRecorder(cmd).commands)
	}
	if err := testCodexLifecycleSink(cmd).Apply(identity, codexLifecycleProjection{
		Accepted: true, Interaction: coremetadata.InteractionInProgress, ClearNoticeIDs: []string{"notice-2"},
	}); err != nil {
		t.Fatal(err)
	}
	if notifyStore.ackedID != "notice-2" {
		t.Fatalf("resolved approval did not clean queue row: acked=%q", notifyStore.ackedID)
	}
	notifyStore.pushed = nil
	if err := testCodexLifecycleSink(cmd).Apply(identity, codexLifecycleProjection{
		Accepted: true, Interaction: coremetadata.InteractionApprovalRequired,
		Notices: []codexLifecycleNotice{{Category: "approval_required", ID: "notice-focus", Severity: "critical", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", RequestID: "request-focus"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(notifyStore.pushed) != 1 || notifyStore.pushed[0].Metadata["action_label"] != agentActionOpenCodex || notifyStore.pushed[0].Metadata["focus_available"] != "true" {
		t.Fatalf("focus-only approval availability = %#v", notifyStore.pushed)
	}

	cmdRecorder(cmd).commands = nil
	notifyStore.pushed = nil
	stale := identity
	stale.Generation = "replaced-generation"
	if err := testCodexLifecycleSink(cmd).Apply(stale, projection); !errors.Is(err, errManagedAgentObservationIgnored) {
		t.Fatalf("stale Apply error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 || len(notifyStore.pushed) != 0 {
		t.Fatalf("stale identity wrote commands=%#v queue=%#v", cmdRecorder(cmd).commands, notifyStore.pushed)
	}
}

func TestCodexSemanticDeliveryMatrix(t *testing.T) {
	events := []struct {
		name        string
		interaction coremetadata.AgentInteractionKind
	}{
		{name: "approval required", interaction: coremetadata.InteractionApprovalRequired},
		{name: "response complete", interaction: coremetadata.InteractionResponseComplete},
	}
	policies := []struct {
		name    string
		policy  config.AISemanticPolicy
		visible bool
		notify  bool
	}{
		{name: "notify", policy: config.AISemanticNotify, visible: true, notify: true},
		{name: "state only", policy: config.AISemanticStateOnly, visible: true},
		{name: "quiet", policy: config.AISemanticQuiet},
	}
	for _, event := range events {
		for _, policy := range policies {
			t.Run(event.name+"/"+policy.name, func(t *testing.T) {
				got := codexSemanticDeliveryFor(policy.policy, event.interaction)
				canonical := codexgeneration.ProjectLifecycle(codexgeneration.LifecycleProjectionInput{Interaction: event.interaction})
				wantRegistry, wantBadge := coremetadata.InteractionUnknown, ""
				if policy.visible {
					wantRegistry, wantBadge = event.interaction, canonical.Badge
				}
				wantAttention := ""
				if policy.notify {
					wantAttention = canonical.Attention
				}
				if got.State != canonical.State || got.RegistryInteraction != wantRegistry || got.Badge != wantBadge || got.Attention != wantAttention || got.Notify != policy.notify {
					t.Fatalf("delivery = %#v", got)
				}
			})
		}
	}
}

func TestGenerationLifecycleSinkCompositeAuthorityHasZeroCrossWrites(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*coremetadata.CodexEndpointRef, *coremetadata.CodexAuthorityRef, *codexgeneration.LifecycleOperationRef)
		eventRuntime  string
		wantCommitted bool
	}{
		{name: "owner current target", wantCommitted: true},
		{name: "sibling runtime target", eventRuntime: "%8"},
		{
			name: "same numeric epochs across endpoint generations",
			mutate: func(endpoint *coremetadata.CodexEndpointRef, authority *coremetadata.CodexAuthorityRef, operation *codexgeneration.LifecycleOperationRef) {
				endpoint.EndpointGenerationID = "foreign-generation"
				authority.EndpointGenerationID = endpoint.EndpointGenerationID
				operation.Endpoint = *endpoint
			},
		},
		{
			name: "broker restart reuses numeric epochs",
			mutate: func(_ *coremetadata.CodexEndpointRef, authority *coremetadata.CodexAuthorityRef, _ *codexgeneration.LifecycleOperationRef) {
				authority.BrokerRuntimeID = "restarted-broker"
			},
		},
		{
			name: "connection epoch stale",
			mutate: func(_ *coremetadata.CodexEndpointRef, authority *coremetadata.CodexAuthorityRef, _ *codexgeneration.LifecycleOperationRef) {
				authority.ConnectionEpoch++
			},
		},
		{
			name: "binding epoch stale",
			mutate: func(_ *coremetadata.CodexEndpointRef, authority *coremetadata.CodexAuthorityRef, _ *codexgeneration.LifecycleOperationRef) {
				authority.BindingEpoch++
			},
		},
		{
			name: "operation belongs to a foreign generation",
			mutate: func(_ *coremetadata.CodexEndpointRef, _ *coremetadata.CodexAuthorityRef, operation *codexgeneration.LifecycleOperationRef) {
				operation.Endpoint.EndpointGenerationID = "foreign-operation-generation"
			},
		},
		{
			name: "provider self asserts another valid operation",
			mutate: func(_ *coremetadata.CodexEndpointRef, _ *coremetadata.CodexAuthorityRef, operation *codexgeneration.LifecycleOperationRef) {
				operation.ID = "another-valid-operation"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, identity, durableEndpoint, storedAuthority := phase1GenerationAuthorityFixture(t)
			if test.eventRuntime != "" {
				identity.RuntimeID = test.eventRuntime
			}
			presentedEndpoint := *durableEndpoint
			presentedAuthority := *storedAuthority
			operation := codexgeneration.LifecycleOperationRef{ID: "drain-operation", Endpoint: presentedEndpoint}
			if test.mutate != nil {
				test.mutate(&presentedEndpoint, &presentedAuthority, &operation)
			}
			cmd := testAICommand(t.TempDir())
			cmd.loadRegistry = store.store().load
			cmd.updateRegistry = store.store().update
			cmd.acquireCodexAuthority = func(string) (func(), error) { return func() {}, nil }
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "tmux" && reflect.DeepEqual(args, []string{"show-options", "-pqv", "-t", identity.RuntimeID, tmuxopts.PaneUID}) {
					return []byte(identity.PaneUID + "\n"), nil
				}
				return nil, os.ErrNotExist
			}
			projection := codexLifecycleProjection{
				Accepted: true, Interaction: coremetadata.InteractionIdle,
				Endpoint: &presentedEndpoint, GenerationState: codexgeneration.StateDraining,
				Operation: &operation, Authority: &presentedAuthority,
			}
			err := testCodexLifecycleSink(cmd).Apply(identity, projection)
			commands := cmdRecorder(cmd).commands
			writes := phase0RSemanticPaneWrites(commands)
			tmuxWrites := 0
			for _, command := range commands {
				if command.name == "tmux" && len(command.args) > 0 && command.args[0] == "set-option" {
					tmuxWrites++
				}
			}
			if test.wantCommitted {
				if err != nil || store.writes != 1 || tmuxWrites != 3 {
					t.Fatalf("exact generation Apply err=%v Registry writes=%d tmux writes=%d", err, store.writes, tmuxWrites)
				}
				if got := writes[aiPaneStateOption]; !reflect.DeepEqual(got, []string{codexgeneration.LifecycleStateDraining}) {
					t.Fatalf("state writes=%v", got)
				}
				if got := writes[aiPaneBadgeKindOption]; !reflect.DeepEqual(got, []string{codexgeneration.LifecycleBadgeDraining}) {
					t.Fatalf("badge writes=%v", got)
				}
				if got := writes[attentionStateOption]; !reflect.DeepEqual(got, []string{""}) {
					t.Fatalf("attention writes=%v", got)
				}
				return
			}
			if !errors.Is(err, errManagedAgentObservationIgnored) {
				t.Fatalf("refused generation Apply error=%v", err)
			}
			if store.writes != 0 || tmuxWrites != 0 || len(writes) != 0 {
				t.Fatalf("refused generation wrote Registry=%d tmux=%d semantic=%v", store.writes, tmuxWrites, writes)
			}
		})
	}
}

func TestCodexSemanticPolicySourceAndFallbackOverrideMatrix(t *testing.T) {
	events := []struct {
		hook        string
		semantic    config.AISemanticEvent
		interaction coremetadata.AgentInteractionKind
	}{
		{hook: "PermissionRequest", semantic: config.AISemanticApprovalRequired, interaction: coremetadata.InteractionApprovalRequired},
		{hook: "Stop", semantic: config.AISemanticResponseComplete, interaction: coremetadata.InteractionResponseComplete},
	}
	policies := []config.AISemanticPolicy{
		config.AISemanticNotify,
		config.AISemanticStateOnly,
		config.AISemanticQuiet,
	}
	sources := []string{codexAuthorityControlPlane, codexAuthorityHook}
	overrides := []struct {
		name   string
		action string
		set    bool
	}{
		{name: "unset"},
		{name: aiHookActionNotify, action: aiHookActionNotify, set: true},
		{name: aiHookActionState, action: aiHookActionState, set: true},
		{name: aiHookActionQuiet, action: aiHookActionQuiet, set: true},
	}
	for _, event := range events {
		for _, semantic := range policies {
			for _, source := range sources {
				for _, override := range overrides {
					name := strings.Join([]string{event.hook, string(semantic), source, override.name}, "/")
					t.Run(name, func(t *testing.T) {
						home := t.TempDir()
						paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
						semanticFile := config.DefaultAISemanticPolicies()
						semanticFile.Events[event.semantic] = semantic
						if err := config.SaveAISemanticPoliciesFile(paths.AISemanticPoliciesFile(), semanticFile); err != nil {
							t.Fatal(err)
						}
						if override.set {
							if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
								Version: 1,
								Providers: map[string]config.AIHookProviderActions{
									aiHookProviderCodex: {Events: map[string]string{event.hook: override.action}},
								},
							}); err != nil {
								t.Fatal(err)
							}
						}
						var rawBefore []byte
						if override.set {
							var err error
							rawBefore, err = os.ReadFile(paths.AIHookActionsFile())
							if err != nil {
								t.Fatal(err)
							}
						}
						cmd := testAICommand(home)
						cmd.readFile = os.ReadFile
						effective := cmd.codexSemanticPolicyForInteraction(event.interaction)
						rawApplied := false
						if source == codexAuthorityHook {
							effective, rawApplied = cmd.codexHookSemanticPolicy(event.hook, event.interaction)
						}
						want := semantic
						if source == codexAuthorityHook && override.set {
							want = map[string]config.AISemanticPolicy{
								aiHookActionNotify: config.AISemanticNotify,
								aiHookActionState:  config.AISemanticStateOnly,
								aiHookActionQuiet:  config.AISemanticQuiet,
							}[override.action]
						}
						if effective != want {
							t.Fatalf("effective policy = %q, want %q", effective, want)
						}
						if wantRawApplied := source == codexAuthorityHook && override.set; rawApplied != wantRawApplied {
							t.Fatalf("raw override applied = %v, want %v", rawApplied, wantRawApplied)
						}
						if got, expected := codexSemanticDeliveryFor(effective, event.interaction), codexSemanticDeliveryFor(want, event.interaction); !reflect.DeepEqual(got, expected) {
							t.Fatalf("delivery = %#v, want %#v", got, expected)
						}
						if override.set {
							rawAfter, err := os.ReadFile(paths.AIHookActionsFile())
							if err != nil {
								t.Fatal(err)
							}
							if !bytes.Equal(rawAfter, rawBefore) {
								t.Fatalf("policy resolution changed raw bytes: before=%q after=%q", rawBefore, rawAfter)
							}
						}
					})
				}
			}
		}
	}
}

func TestCodexHookFallbackSemanticDeliverySurfacesAndExactAck(t *testing.T) {
	events := []struct {
		name        string
		interaction coremetadata.AgentInteractionKind
		semantic    config.AISemanticEvent
		badge       string
		noticeID    string
		payload     string
	}{
		{
			name:        "PermissionRequest",
			interaction: coremetadata.InteractionApprovalRequired,
			semantic:    config.AISemanticApprovalRequired,
			badge:       aiBadgeKindApprovalRequired,
			noticeID:    "ai:codex:permission:codex-session:turn-456:Bash:go test ./internal/app",
			payload:     `{"hook_event_name":"PermissionRequest","session_id":"codex-session","turn_id":"turn-456","cwd":"/repo/projmux","tool_name":"Bash","tool_input":{"command":"go test ./internal/app"}}`,
		},
		{
			name:        "Stop",
			interaction: coremetadata.InteractionResponseComplete,
			semantic:    config.AISemanticResponseComplete,
			badge:       aiBadgeKindResponseComplete,
			noticeID:    "ai:codex:stop:codex-session:turn-456",
			payload:     `{"hook_event_name":"Stop","session_id":"codex-session","turn_id":"turn-456","cwd":"/repo/projmux"}`,
		},
	}
	policies := []struct {
		name          string
		policy        config.AISemanticPolicy
		wantBadge     bool
		wantAttention bool
		wantNotify    bool
	}{
		{name: "notify", policy: config.AISemanticNotify, wantBadge: true, wantAttention: true, wantNotify: true},
		{name: "state only", policy: config.AISemanticStateOnly, wantBadge: true},
		{name: "quiet", policy: config.AISemanticQuiet},
	}
	for _, event := range events {
		for _, policy := range policies {
			t.Run(event.name+"/"+policy.name, func(t *testing.T) {
				home := t.TempDir()
				paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
				raw := []byte(`{"version":1,"providers":{"codex":{"events":{"PreToolUse":"notify"}}}}` + "\n")
				if err := os.MkdirAll(filepath.Dir(paths.AIHookActionsFile()), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.AIHookActionsFile(), raw, 0o644); err != nil {
					t.Fatal(err)
				}
				semanticFile := config.DefaultAISemanticPolicies()
				semanticFile.Events[event.semantic] = policy.policy
				if err := config.SaveAISemanticPoliciesFile(paths.AISemanticPoliciesFile(), semanticFile); err != nil {
					t.Fatal(err)
				}

				store := &stubNotifyStore{listEntries: []notify.Notification{{ID: event.noticeID}, {ID: "foreign-notice"}}}
				cmd := testAICommand(home)
				cmd.notifyStore = store
				cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
				cmd.readFile = os.ReadFile
				cmd.stdin = strings.NewReader(event.payload)
				cmd.readCommand = codexHookIngestReadCommand("%7")
				desktopCalls := 0
				cmd.lookupEnv = func(name string) string {
					switch name {
					case "HOME":
						return home
					case "PROJMUX_NOTIFY_HOOK":
						return "/tmp/projmux-phase3-notify-spy"
					default:
						return ""
					}
				}
				cmd.runCommand = func(_ context.Context, name string, args ...string) error {
					if name == "/tmp/projmux-phase3-notify-spy" {
						desktopCalls++
						return nil
					}
					cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
					return nil
				}
				if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}

				badgeArgs := routedAppSocketArgs("set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, event.badge)
				if !policy.wantBadge {
					badgeArgs = routedAppSocketArgs("set-option", "-p", "-u", "-t", "%7", aiPaneBadgeKindOption)
				}
				if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: badgeArgs}) {
					t.Fatalf("commands = %#v, want badge projection %#v", cmdRecorder(cmd).commands, badgeArgs)
				}
				attentionArgs := routedAppSocketArgs("set-option", "-p", "-u", "-t", "%7", attentionStateOption)
				if policy.wantAttention {
					attentionArgs = routedAppSocketArgs("set-option", "-p", "-t", "%7", attentionStateOption, attentionStateReply)
				}
				if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: attentionArgs}) {
					t.Fatalf("commands = %#v, want attention projection %#v", cmdRecorder(cmd).commands, attentionArgs)
				}
				wantNotifyCount := 0
				if policy.wantNotify {
					wantNotifyCount = 1
				}
				if len(store.pushed) != wantNotifyCount || desktopCalls != wantNotifyCount {
					t.Fatalf("queue pushes=%d desktop=%d, want %d/%d", len(store.pushed), desktopCalls, wantNotifyCount, wantNotifyCount)
				}
				if policy.wantNotify {
					if len(store.ackedIDs) != 0 {
						t.Fatalf("Notify acked ids = %#v, want none", store.ackedIDs)
					}
				} else if !reflect.DeepEqual(store.ackedIDs, []string{event.noticeID}) || len(store.listEntries) != 1 || store.listEntries[0].ID != "foreign-notice" {
					t.Fatalf("non-Notify acked=%#v remaining=%#v", store.ackedIDs, store.listEntries)
				}
				after, err := os.ReadFile(paths.AIHookActionsFile())
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, raw) {
					t.Fatalf("fallback semantic delivery changed raw override bytes: before=%q after=%q", raw, after)
				}
			})
		}
	}
}

func TestCodexHookFallbackQuietHidesManagedRegistryAggregate(t *testing.T) {
	store := newFakeResourceStore(t)
	mutator := store.mutator()
	identity := codexLifecycleIdentity{
		AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%7",
		Generation: "generation-fallback", ThreadID: "thread-fallback",
	}
	if _, err := mutator.RecordPaneActivation(&store.registry, identity.PaneUID, coremetadata.PaneActivationOptions{
		Generation: identity.Generation, RuntimeID: identity.RuntimeID, AgentUID: identity.AgentUID, OperationID: "phase3-fallback-policy",
	}); err != nil {
		t.Fatal(err)
	}
	bindNativeCodexTestFixture(t, store, mutator, coremetadata.CodexActivationObservation{
		AgentUID: identity.AgentUID, PaneUID: identity.PaneUID, Generation: identity.Generation, ThreadID: identity.ThreadID,
	})
	if _, err := mutator.SetAgentInteraction(&store.registry, identity.AgentUID, coremetadata.InteractionApprovalRequired, string(coremetadata.InteractionSourceProviderControl)); err != nil {
		t.Fatal(err)
	}

	notifyStore := &stubNotifyStore{listEntries: []notify.Notification{{ID: "stale-hook-notice"}}}
	cmd := testAICommand(t.TempDir())
	cmd.loadRegistry = store.store().load
	cmd.updateRegistry = store.store().update
	cmd.notifyStore = notifyStore
	baseRead := codexHookIngestReadCommand(identity.RuntimeID)
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", identity.RuntimeID, "#{@projmux_pane_uid}"}) {
			return []byte(identity.PaneUID + "\n"), nil
		}
		return baseRead(ctx, name, args...)
	}
	baseLookup := cmd.lookupEnv
	cmd.lookupEnv = func(name string) string {
		switch name {
		case internalActivationPaneUIDEnv:
			return identity.PaneUID
		case internalActivationGenerationEnv:
			return identity.Generation
		default:
			return baseLookup(name)
		}
	}
	if err := cmd.applyCodexHookSemanticDelivery(identity.RuntimeID, coremetadata.InteractionApprovalRequired, config.AISemanticQuiet, attentionNotifyInput{ID: "stale-hook-notice"}); err != nil {
		t.Fatal(err)
	}
	agent, _ := store.registry.Agent(identity.AgentUID)
	if agent.Status.Interaction.Kind != coremetadata.InteractionUnknown || agent.Status.Interaction.Source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("quiet fallback Registry projection = %#v", agent.Status.Interaction)
	}
	if !reflect.DeepEqual(notifyStore.ackedIDs, []string{"stale-hook-notice"}) {
		t.Fatalf("quiet fallback acked ids = %#v", notifyStore.ackedIDs)
	}
	// The hook inherits no tmux environment here, so every reflection write
	// must carry the app-owned route the delivery proved before writing.
	for _, option := range []string{aiPaneBadgeKindOption, attentionStateOption} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"-L", defaultAppSocket, "set-option", "-p", "-u", "-t", identity.RuntimeID, option}}) {
			t.Fatalf("quiet fallback commands = %#v, want unset %s", cmdRecorder(cmd).commands, option)
		}
	}
}

func TestCodexSemanticSettingsMatrixAndMixedAuthorityDiagnostics(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return root, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return configRoot
			}
			return ""
		},
		tmuxRunner: phase3StaticTmuxRunner{output: strings.Join([]string{
			"codex\x1fprovider-control-plane\x1fepoch-1\x1fready",
			"codex\x1fprovider-hook\x1f\x1fdisconnected",
			"claude\x1f\x1f\x1f",
		}, "\n")},
	}
	if summary := cmd.codexLifecycleAuthoritySummary(); !strings.Contains(summary, "mixed (provider-control-plane 1, provider-hook 1)") || !strings.Contains(summary, "epochs active 1, pending 0, inactive 1") {
		t.Fatalf("authority summary = %q", summary)
	}
	if fallback := cmd.codexHookFallbackSummary(); fallback != "active on 1 live Codex pane(s); inactive on 1" {
		t.Fatalf("fallback summary = %q", fallback)
	}
	for _, test := range []struct {
		name, source, epoch, reason, want string
	}{
		{name: "native authority", source: codexAuthorityControlPlane, epoch: "epoch-native", reason: "ready", want: "inactive on 1 live Codex pane(s)"},
		{name: "hook authority", source: codexAuthorityHook, reason: "endpoint-suspended", want: "active on 1 live Codex pane(s); inactive on 0"},
	} {
		t.Run(test.name+" raw override activity", func(t *testing.T) {
			cmd.tmuxRunner = phase3StaticTmuxRunner{output: strings.Join([]string{"codex", test.source, test.epoch, test.reason}, "\x1f")}
			if got := cmd.codexHookFallbackSummary(); got != test.want {
				t.Fatalf("raw hook fallback summary = %q, want %q", got, test.want)
			}
		})
	}
	cmd.tmuxRunner = phase3StaticTmuxRunner{output: strings.Join([]string{
		"codex\x1fprovider-control-plane\x1fepoch-1\x1fready",
		"codex\x1fprovider-hook\x1f\x1fdisconnected",
		"claude\x1f\x1f\x1f",
	}, "\n")}
	entries := cmd.aiHookEventEntries(aiHookProviderCodex)
	for _, value := range []string{
		settingsActionPrefixAISemanticEvent + string(config.AISemanticApprovalRequired),
		settingsActionPrefixAISemanticEvent + string(config.AISemanticResponseComplete),
	} {
		found := false
		for _, entry := range entries {
			found = found || entry.Value == value
		}
		if !found {
			t.Fatalf("Codex entries missing %q: %#v", value, entries)
		}
	}
	if !entryLabelsContain(entries, "Hook fallback behavior (advanced)") || !entryLabelsContain(entries, "active on 1 live Codex pane(s)") || !entryLabelsContain(entries, "fallback only") {
		t.Fatalf("Codex settings do not expose bounded fallback state: %#v", entries)
	}

	rawPath := filepath.Join(configRoot, "projmux", config.AIHookActionsFileName)
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"providers":{"codex":{"events":{"Stop":"quiet"}}}}` + "\n")
	if err := os.WriteFile(rawPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, event := range []config.AISemanticEvent{config.AISemanticApprovalRequired, config.AISemanticResponseComplete} {
		for _, policy := range []config.AISemanticPolicy{config.AISemanticNotify, config.AISemanticStateOnly, config.AISemanticQuiet} {
			if err := cmd.setAISemanticPolicy(event, policy, nil); err != nil {
				t.Fatal(err)
			}
			if got := cmd.currentAISemanticPolicies().Events[event]; got != policy {
				t.Fatalf("Settings semantic matrix %s = %s, want %s", event, got, policy)
			}
		}
	}
	after, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, raw) {
		t.Fatalf("semantic Settings changed raw hook override: before=%q after=%q", raw, after)
	}
	for _, event := range []config.AISemanticEvent{config.AISemanticApprovalRequired, config.AISemanticResponseComplete} {
		choices := cmd.aiSemanticPolicyChoiceEntries(event)
		for _, policy := range []config.AISemanticPolicy{config.AISemanticNotify, config.AISemanticStateOnly, config.AISemanticQuiet} {
			if !hasEntryValue(choices, settingsActionPrefixAISemanticSet+string(event)+":"+string(policy)) {
				t.Fatalf("semantic choices missing %s=%s: %#v", event, policy, choices)
			}
		}
	}
}

func TestCodexLifecycleSettingsLocalizationParity(t *testing.T) {
	dynamicKeys := []i18n.Key{
		"settings.result.codex_native_semantic_policy",
		"settings.text.codex_no_runtime_observation",
		"settings.text.codex_tmux_observation_failed",
		"settings.text.codex_lifecycle_unavailable",
		"settings.text.codex_lifecycle_no_live_pane",
		"settings.text.codex_lifecycle_mixed_sources",
		"settings.text.codex_lifecycle_bounded_reasons",
		"settings.text.codex_bounded_reason_unavailable",
		"settings.text.codex_lifecycle_epochs",
		"settings.text.codex_hook_status_unavailable",
		"settings.text.codex_hook_active_counts",
		"settings.text.codex_hook_inactive_count",
	}
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, i18n.Locale("ko-KR")} {
		localizer := i18n.NewLocalizer(locale)
		for _, key := range dynamicKeys {
			text, err := localizer.Text(key)
			if err != nil || strings.TrimSpace(text.String()) == "" {
				t.Errorf("locale %s key %s = %q, %v", locale, key, text.String(), err)
			}
		}
	}

	staticFallbacks := []string{
		"Native semantic policy",
		"Codex native semantic policy",
		"Effective source",
		"Hook fallback behavior (advanced)",
		"State only",
		"Quiet",
		"Notify",
		"Approval required",
		"Response complete",
		"applies to native lifecycle and hook fallback",
		"State only - badge only; queue and desktop off",
		"Quiet - badge, queue, and desktop off",
		"Notify - badge, queue, and desktop on",
		"content-free runtime authority",
		"raw overrides below are preserved",
		"fallback only",
	}
	for _, fallback := range staticFallbacks {
		if got := localizeUIText(i18n.FallbackLocale, fallback); got != fallback {
			t.Errorf("en-US %q = %q", fallback, got)
		}
		if got := localizeUIText(i18n.Locale("ko-KR"), fallback); got == fallback {
			t.Errorf("ko-KR left Phase 3 Settings text untranslated: %q", fallback)
		}
	}

	for _, test := range []struct {
		locale      i18n.Locale
		wantTitle   string
		wantPrompt  string
		wantResult  string
		wantSummary []string
		wantHook    string
	}{
		{
			locale:     i18n.FallbackLocale,
			wantTitle:  "Agent event behavior - Codex - Approval required",
			wantPrompt: "Settings > Notifications > Agent event behavior > Codex > Approval required > ",
			wantResult: "Codex native semantic policy: Response complete = State only\n",
			// uncaptured-default: the bounded read renders this for a pane option
			// outside the vocabulary, so it is the correct answer to a malformed
			// value rather than a reason any transition produced.
			wantSummary: []string{"mixed (provider-control-plane 1, provider-hook 1)", "bounded reason unavailable", "epochs active 1, pending 0, inactive 1"},
			wantHook:    "active on 1 live Codex pane(s); inactive on 1",
		},
		{
			locale:      i18n.Locale("ko-KR"),
			wantTitle:   "Agent 이벤트 동작 - Codex - 승인 필요",
			wantPrompt:  "설정 > 알림 > Agent 이벤트 동작 > Codex > 승인 필요 > ",
			wantResult:  "Codex 네이티브 의미 정책: 응답 완료 = 상태만\n",
			wantSummary: []string{"혼합(provider-control-plane 1, provider-hook 1)", "제한된 사유를 확인할 수 없음", "epoch 활성 1, 대기 0, 비활성 1"},
			wantHook:    "활성 Codex Pane 1개에서 활성; 1개에서 비활성",
		},
	} {
		t.Run(string(test.locale), func(t *testing.T) {
			root := t.TempDir()
			cmd := &settingsCommand{
				homeDir: func() (string, error) { return root, nil },
				lookupEnv: func(name string) string {
					switch name {
					case "PROJMUX_LOCALE":
						return string(test.locale)
					case "XDG_CONFIG_HOME":
						return filepath.Join(root, "config")
					default:
						return ""
					}
				},
				tmuxRunner: phase3StaticTmuxRunner{output: strings.Join([]string{
					"codex\x1fprovider-control-plane\x1fepoch-1\x1fprompt=private",
					"codex\x1fprovider-hook\x1f\x1fprompt=private",
				}, "\n")},
			}
			if got := codexSemanticEventTitle(test.locale, config.AISemanticApprovalRequired); got != test.wantTitle {
				t.Errorf("title = %q, want %q", got, test.wantTitle)
			}
			if got := codexSemanticEventPrompt(test.locale, config.AISemanticApprovalRequired); got != test.wantPrompt {
				t.Errorf("prompt = %q, want %q", got, test.wantPrompt)
			}
			for _, want := range test.wantSummary {
				if got := cmd.codexLifecycleAuthoritySummary(); !strings.Contains(got, want) {
					t.Errorf("authority summary = %q, want %q", got, want)
				}
			}
			if got := cmd.codexHookFallbackSummary(); got != test.wantHook {
				t.Errorf("hook summary = %q, want %q", got, test.wantHook)
			}
			var stdout bytes.Buffer
			if err := cmd.setAISemanticPolicy(config.AISemanticResponseComplete, config.AISemanticStateOnly, &stdout); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != test.wantResult {
				t.Errorf("result = %q, want %q", got, test.wantResult)
			}
		})
	}
}

func TestDescribeCodexAgentShowsContentFreeLifecycleAuthority(t *testing.T) {
	store := newFakeResourceStore(t)
	cmd := newTestDescribeCommand(t, store)
	cmd.codexAuthority = func(paneUID string) codexLifecycleAuthorityDiagnostic {
		if paneUID != "pan-alpha-codex" {
			t.Fatalf("pane UID = %q", paneUID)
		}
		return codexLifecycleAuthorityDiagnostic{Source: codexAuthorityControlPlane, Reason: "ready", EpochStatus: "active", Dropped: 2, Unknown: 3, Overflow: 1}
	}
	stdout, stderr, err := runRoute(t, cmd, "agent", "uid:agt-alpha-codex")
	if err != nil {
		t.Fatalf("describe: %v (%s)", err, stderr)
	}
	for _, want := range []string{"LifecycleSource:", codexAuthorityControlPlane, "LifecycleReason:", "ready", "LifecycleEpoch:", "active", "ProgressDropped:", "2", "ProgressUnknown:", "3", "ProgressOverflow:", "1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("describe missing %q:\n%s", want, stdout)
		}
	}
	for _, forbidden := range []string{"prompt", "reasoning", "output", "full diff", "path", "command", "tool name", "model", "effort"} {
		if strings.Contains(strings.ToLower(stdout), forbidden) {
			t.Fatalf("describe leaked forbidden field %q:\n%s", forbidden, stdout)
		}
	}
}

func TestCodexLifecycleAuthorityRejectsUnboundedRuntimeReason(t *testing.T) {
	diagnostic := observeCodexLifecycleAuthority(context.Background(), phase3StaticTmuxRunner{output: "pan-alpha-codex\x1fprovider-control-plane\x1fepoch-1\x1fprompt=private"}, "pan-alpha-codex")
	// uncaptured-default: the fixture publishes an out-of-vocabulary reason on
	// purpose; the bounded fallback is what a truthful read of it must say.
	if diagnostic.Source != codexAuthorityControlPlane || diagnostic.EpochStatus != "active" || diagnostic.Reason != "bounded reason unavailable" {
		t.Fatalf("sanitized diagnostic = %#v", diagnostic)
	}
}

func TestCodexLifecycleAuthorityPreservesTypedStartupReasons(t *testing.T) {
	for _, reason := range []string{"observer-start-failed", "observer-exited", "observer-timeout", "control-unavailable"} {
		diagnostic := observeCodexLifecycleAuthority(context.Background(), phase3StaticTmuxRunner{
			output: "pan-alpha-codex\x1fprovider-hook\x1f\x1f" + reason,
		}, "pan-alpha-codex")
		if diagnostic.Source != codexAuthorityHook || diagnostic.EpochStatus != "inactive" || diagnostic.Reason != reason {
			t.Errorf("typed startup reason %q = %#v", reason, diagnostic)
		}
	}
}

func TestCodexProgressDiagnosticsAcceptOnlyBoundedNumericCounters(t *testing.T) {
	diagnostic := observeCodexLifecycleAuthority(context.Background(), phase3StaticTmuxRunner{output: "pan-alpha-codex\x1fprovider-control-plane\x1fepoch-1\x1fready\x1f2\x1f3\x1f1"}, "pan-alpha-codex")
	if diagnostic.Dropped != 2 || diagnostic.Unknown != 3 || diagnostic.Overflow != 1 {
		t.Fatalf("progress counters = %#v", diagnostic)
	}
	malformed := observeCodexLifecycleAuthority(context.Background(), phase3StaticTmuxRunner{output: "pan-alpha-codex\x1fprovider-control-plane\x1fepoch-1\x1fready\x1fprompt=private\x1f-1\x1f4294967296"}, "pan-alpha-codex")
	if malformed.Dropped != 0 || malformed.Unknown != 0 || malformed.Overflow != 0 {
		t.Fatalf("malformed progress counters crossed diagnostics: %#v", malformed)
	}
}

func TestCodexLifecyclePersistedAndLoggedFieldInventoryIsContentFree(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[codexappserver.LifecycleEvent](),
		reflect.TypeFor[codexappserver.LifecycleSnapshot](),
		reflect.TypeFor[codexPendingApproval](),
		reflect.TypeFor[codexLifecycleNotice](),
		reflect.TypeFor[aiIngestLogEntry](),
	}
	for _, typ := range types {
		for index := 0; index < typ.NumField(); index++ {
			name := strings.ToLower(typ.Field(index).Name)
			for _, forbidden := range []string{"prompt", "reasoning", "output", "diff", "params", "body", "text"} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("%s persists/logs forbidden field %q", typ, typ.Field(index).Name)
				}
			}
		}
	}
	allowedNoticeMetadata := map[string]bool{
		"agent": true, "category": true, "thread_id": true, "turn_id": true,
		"item_id": true, "request_id": true, "approval_kind": true,
	}
	metadata := map[string]string{
		"agent": "codex", "category": "approval_required", "thread_id": "thread-1", "turn_id": "turn-1",
		"item_id": "item-1", "request_id": "request-1", "approval_kind": "command",
	}
	for key := range metadata {
		if !allowedNoticeMetadata[key] {
			t.Fatalf("native notice metadata field %q is outside the content-free inventory", key)
		}
	}
}

func entryLabelsContain(entries []intpickercompat.Entry, needle string) bool {
	for _, entry := range entries {
		if strings.Contains(entry.Label, needle) {
			return true
		}
	}
	return false
}

func containsCodexObserverEvent(events []string, want string) bool {
	return slices.Contains(events, want)
}

func waitForCodexObserverEvents(t *testing.T, sink *recordingCodexLifecycleSink, count int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(sink.snapshot()) < count {
		select {
		case <-sink.wake:
		case <-deadline.C:
			t.Fatalf("timed out waiting for observer events: %#v", sink.snapshot())
		}
	}
}
