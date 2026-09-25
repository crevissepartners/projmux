package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
)

// questionRaiseProjection is one call of the hook's Pane projection seam.
type questionRaiseProjection struct {
	paneID string
	kind   coremetadata.AgentInteractionKind
}

// questionRaiseSeams stands in for the Registry transaction and the Pane
// projection of the way-2 raise over the question fixture's Registry. mutate,
// when set, changes the Registry the transaction sees relative to the snapshot
// the hook loaded; fail makes the transaction fail before its body runs.
type questionRaiseSeams struct {
	mu          sync.Mutex
	fixture     *questionFixture
	mutate      func(*coremetadata.Registry)
	fail        error
	updates     int
	writes      int
	fnErrs      []error
	projections []questionRaiseProjection
}

func (s *questionRaiseSeams) install(hook *claudeQuestionHook) {
	hook.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.updates++
		if s.fail != nil {
			return coremetadata.Registry{}, s.fail
		}
		working := s.fixture.resources.registry.Clone()
		if s.mutate != nil {
			s.mutate(&working)
		}
		if err := fn(&working); err != nil {
			s.fnErrs = append(s.fnErrs, err)
			return coremetadata.Registry{}, err
		}
		if err := working.Validate(); err != nil {
			return coremetadata.Registry{}, err
		}
		s.fixture.resources.registry = working
		s.writes++
		return working.Clone(), nil
	}
	hook.projectInteraction = func(paneID string, kind coremetadata.AgentInteractionKind) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.projections = append(s.projections, questionRaiseProjection{paneID: paneID, kind: kind})
		return nil
	}
}

func (s *questionRaiseSeams) interaction(t *testing.T) coremetadata.AgentInteraction {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.fixture.resources.registry.Agent(questionTestAgent)
	if !ok {
		t.Fatal("question Agent missing")
	}
	return agent.Status.Interaction
}

func (s *questionRaiseSeams) counts() (updates, writes, projections int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates, s.writes, len(s.projections)
}

// runRaiseHook runs the hook in the background. Its first poll reports on
// polled, so a test observes what had happened before the wait started, and
// then answers the question when answer is set.
func runRaiseHook(t *testing.T, ctx context.Context, fixture *questionFixture, hook claudeQuestionHook, answer map[string]string) (<-chan struct{}, <-chan string) {
	t.Helper()
	polled := make(chan struct{})
	var once sync.Once
	hook.readRecord = func(store *agentquestion.Store, id string) (agentquestion.Record, bool, error) {
		once.Do(func() {
			close(polled)
			if answer != nil {
				if _, err := store.Answer(id, questionTestAgent, answer); err != nil {
					t.Errorf("answer: %v", err)
				}
			}
		})
		return store.Get(id)
	}
	done := make(chan string, 1)
	go func() {
		var stdout bytes.Buffer
		hook.run(ctx, []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
		done <- stdout.String()
	}()
	return polled, done
}

var questionRaiseAnswer = map[string]string{"Which build tool?": "make", "Which branch?": "main"}

const questionRaiseDecision = `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"questions":` +
	questionTestQuestions + `,"answers":{"Which branch?":"main","Which build tool?":"make"}}}}` + "\n"

func wantInputRequiredProjection(t *testing.T, projections []questionRaiseProjection) {
	t.Helper()
	want := []questionRaiseProjection{{paneID: "%7", kind: coremetadata.InteractionInputRequired}}
	if !slices.Equal(projections, want) {
		t.Fatalf("projections = %+v, want %+v", projections, want)
	}
}

// TestClaudeQuestionHookRaisesInputRequiredBeforeItWaits holds acceptance 1: a
// way-2 question commits the asking Agent's interaction as provider-hook
// input_required in one transaction and projects it onto the Agent Pane's live
// handle, both before the wait's first poll, and the answer still decides.
func TestClaudeQuestionHookRaisesInputRequiredBeforeItWaits(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, true)
	seams := &questionRaiseSeams{fixture: fixture}
	hook := fixture.hook(time.Minute)
	seams.install(&hook)
	var atFirstPoll coremetadata.AgentInteraction
	var projectedAtFirstPoll int
	polled, done := runRaiseHook(t, context.Background(), fixture, hook, questionRaiseAnswer)
	select {
	case <-polled:
		atFirstPoll = seams.interaction(t)
		_, _, projectedAtFirstPoll = seams.counts()
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never polled")
	}
	if atFirstPoll.Kind != coremetadata.InteractionInputRequired ||
		atFirstPoll.Source != string(coremetadata.InteractionSourceProviderHook) || atFirstPoll.ObservedAt.IsZero() {
		t.Fatalf("interaction at the first poll = %+v, want provider-hook input_required", atFirstPoll)
	}
	if projectedAtFirstPoll != 1 {
		t.Fatalf("projections at the first poll = %d, want 1", projectedAtFirstPoll)
	}
	if got := waitHookOutput(t, done); got != questionRaiseDecision {
		t.Fatalf("decision =\n%s\nwant\n%s", got, questionRaiseDecision)
	}
	wantInputRequiredProjection(t, seams.projections)
	// The projection spells the same three options the ingest path does.
	state, badge, attention := agentTmuxProjection(coremetadata.InteractionInputRequired)
	if state == "" || badge != aiBadgeKindInputRequired || attention == "" {
		t.Fatalf("input_required projection = %q %q %q", state, badge, attention)
	}
	if updates, writes, _ := seams.counts(); updates != 1 || writes != 1 {
		t.Fatalf("updates=%d writes=%d, want exactly one committed transaction", updates, writes)
	}
}

// TestClaudeQuestionHookRaiseIsLoweredByTheAskUserQuestionPostToolUse holds
// acceptance 2 end to end over the ingest fixture's Registry: the hook raises
// the Agent to input_required through the real routed Pane writer, a message is
// held during the wait, and after the answer the AskUserQuestion PostToolUse
// moves the Agent to in_progress and launches the held release.
func TestClaudeQuestionHookRaiseIsLoweredByTheAskUserQuestionPostToolUse(t *testing.T) {
	t.Parallel()

	f := newClaudeQuietHookFixture(t, true)
	// Settle the session ref first, so the close is judged on its own write.
	f.ingest(t, "PreToolUse")
	messages := messagestore.NewStore(t.TempDir())
	var launches []string
	f.cmd.heldRelease = heldMessageRelease{
		store:  func() (agentMessageHeldLister, error) { return messages, nil },
		launch: func(agentUID string) error { launches = append(launches, agentUID); return nil },
	}
	questions := agentquestion.NewStore(t.TempDir()).WithClock(func() time.Time { return claudeQuietHookClock })
	commandsBefore := len(cmdRecorder(f.cmd).commands)
	hook := claudeQuestionHook{
		loadRegistry:       func() (coremetadata.Registry, error) { return f.registry.Clone(), nil },
		store:              func() (*agentquestion.Store, error) { return questions, nil },
		answering:          func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux },
		window:             func() time.Duration { return time.Minute },
		poll:               10 * time.Millisecond,
		newID:              agentquestion.NewID,
		now:                func() time.Time { return claudeQuietHookClock },
		updateRegistry:     f.cmd.updateRegistry,
		projectInteraction: f.cmd.projectManagedAgentInteraction,
	}
	var raised coremetadata.AgentInteraction
	var once sync.Once
	hook.readRecord = func(store *agentquestion.Store, id string) (agentquestion.Record, bool, error) {
		once.Do(func() {
			agent, _ := f.registry.Agent(f.agentUID)
			raised = agent.Status.Interaction
			// A peer's message arrives while the question waits and is held.
			route := coremessage.Route{AgentUID: f.agentUID, PaneUID: "pane-held", ActivationGeneration: "generation-held",
				Provider: "claude", Incarnation: "incarnation-held"}
			now := time.Now().UTC()
			envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-held-during-question",
				ConversationRef: "conversation-held-during-question", Source: route, Target: route, Authority: coremessage.PeerAuthority(),
				Payload: "coordinate", AcceptedAt: now, Deadline: now.Add(time.Minute)}
			if _, _, err := messages.PutAccepted(envelope, "claude-coordination"); err != nil {
				t.Error(err)
			}
			if _, _, err := messages.Apply(envelope.MessageRef, coremessage.Event{Kind: coremessage.EventHold, MessageRef: envelope.MessageRef,
				ConversationRef: envelope.ConversationRef, Target: route, Reason: claudeHoldReasonAwaitingOperator, ObservedAt: now}); err != nil {
				t.Error(err)
			}
			if _, err := store.Answer(id, f.agentUID, questionRaiseAnswer); err != nil {
				t.Errorf("answer: %v", err)
			}
		})
		return store.Get(id)
	}
	var stdout bytes.Buffer
	payload := `{"hook_event_name":"PreToolUse","session_id":"` + claudeQuietHookSession + `","tool_name":"AskUserQuestion","tool_use_id":"toolu_1","tool_input":{"questions": ` + questionTestQuestions + `}}`
	hook.run(context.Background(), []string{"--pane=" + claudeQuietHookPane}, strings.NewReader(payload), &stdout, &bytes.Buffer{})
	if stdout.String() != questionRaiseDecision {
		t.Fatalf("decision =\n%s\nwant\n%s", stdout.String(), questionRaiseDecision)
	}
	if raised.Kind != coremetadata.InteractionInputRequired || raised.Source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("interaction while waiting = %+v, want provider-hook input_required", raised)
	}
	paneState := claudeQuietHookPaneState(t, cmdRecorder(f.cmd).commands[commandsBefore:])
	state, badge, attention := agentTmuxProjection(coremetadata.InteractionInputRequired)
	if paneState[aiPaneStateOption] != state || paneState[aiPaneBadgeKindOption] != badge || paneState[attentionStateOption] != attention {
		t.Fatalf("pane options after the raise = %v, want %q %q %q", paneState, state, badge, attention)
	}
	if len(launches) != 0 {
		t.Fatalf("the hook launched a release: %v", launches)
	}

	closed, err := json.Marshal(map[string]string{
		"hook_event_name": "PostToolUse",
		"session_id":      claudeQuietHookSession,
		"cwd":             claudeQuietHookCWD,
		"transcript_path": claudeQuietHookTranscript,
		"tool_name":       "AskUserQuestion",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cmd.ingestClaudeHook(closed, f.explicitPane); err != nil {
		t.Fatalf("ingest PostToolUse: %v", err)
	}
	agent, _ := f.registry.Agent(f.agentUID)
	if agent.Status.Interaction.Kind != coremetadata.InteractionInProgress ||
		agent.Status.Interaction.Source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("interaction after PostToolUse = %+v, want provider-hook in_progress", agent.Status.Interaction)
	}
	if !slices.Equal(launches, []string{f.agentUID}) {
		t.Fatalf("launches = %v, want the held release for %s", launches, f.agentUID)
	}
}

// TestClaudeQuestionHookNeverLowersTheRaise holds acceptance 3: a question
// that expires, whose popup is canceled with Esc, or whose hook is canceled
// gives the question back and writes nothing after the one raise.
func TestClaudeQuestionHookNeverLowersTheRaise(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		window time.Duration
		popup  bool
		cancel bool
		state  agentquestion.State
	}{
		{name: "expired", window: 150 * time.Millisecond, state: agentquestion.StateExpired},
		{name: "popup canceled", window: time.Minute, popup: true, state: agentquestion.StateClosed},
		{name: "hook canceled", window: time.Minute, cancel: true, state: agentquestion.StateClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, true)
			if test.popup {
				popup := newFakeQuestionPopup("client-1")
				fixture.runPickerInPopup(popup, pickRow("make"), pressEsc)
				fixture.popup = popup
			}
			seams := &questionRaiseSeams{fixture: fixture}
			hook := fixture.hook(test.window)
			seams.install(&hook)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			polled, done := runRaiseHook(t, ctx, fixture, hook, nil)
			if test.cancel {
				<-polled
				cancel()
			}
			if got := waitHookOutput(t, done); got != "" {
				t.Fatalf("hook printed %q", got)
			}
			records, _ := fixture.store.List(questionTestAgent)
			if len(records) != 1 || records[0].State != test.state {
				t.Fatalf("records = %#v, want one %s", records, test.state)
			}
			if got := seams.interaction(t); got.Kind != coremetadata.InteractionInputRequired {
				t.Fatalf("interaction after give-back = %+v, want input_required left for the provider", got)
			}
			if updates, writes, projections := seams.counts(); updates != 1 || writes != 1 || projections != 1 {
				t.Fatalf("updates=%d writes=%d projections=%d, want the raise alone", updates, writes, projections)
			}
		})
	}
}

// TestClaudeQuestionHookRaiseFencesOnTheBindingItRead holds acceptance 4: a
// binding that changed between the snapshot and the transaction writes nothing
// and projects nothing, and the question is recorded, waited, and answered as
// if the raise did not exist.
func TestClaudeQuestionHookRaiseFencesOnTheBindingItRead(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*coremetadata.Registry)
	}{
		{name: "Agent not Running", mutate: func(registry *coremetadata.Registry) {
			agent, _ := registry.Agent(questionTestAgent)
			agent.Status.Phase = coremetadata.PhaseOffline
		}},
		{name: "Agent PaneRef changed", mutate: func(registry *coremetadata.Registry) {
			agent, _ := registry.Agent(questionTestAgent)
			agent.Status.PaneRef = "pan-alpha-other"
		}},
		{name: "Pane activation Generation changed", mutate: func(registry *coremetadata.Registry) {
			pane, _ := registry.Pane(questionTestPane)
			pane.Status.Activation.Generation = "gen-2"
		}},
		{name: "Pane activation AgentUID changed", mutate: func(registry *coremetadata.Registry) {
			pane, _ := registry.Pane(questionTestPane)
			pane.Status.Activation.AgentUID = "agt-beta-codex"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, true)
			before := fixture.resources.registry.Clone()
			seams := &questionRaiseSeams{fixture: fixture, mutate: test.mutate}
			hook := fixture.hook(time.Minute)
			seams.install(&hook)
			_, done := runRaiseHook(t, context.Background(), fixture, hook, questionRaiseAnswer)
			if got := waitHookOutput(t, done); got != questionRaiseDecision {
				t.Fatalf("decision =\n%s\nwant\n%s", got, questionRaiseDecision)
			}
			records, _ := fixture.store.List(questionTestAgent)
			if len(records) != 1 || records[0].State != agentquestion.StateAnswered {
				t.Fatalf("records = %#v, want one answered", records)
			}
			updates, writes, projections := seams.counts()
			if updates != 1 || writes != 0 || projections != 0 {
				t.Fatalf("updates=%d writes=%d projections=%d, want a fenced transaction and nothing else", updates, writes, projections)
			}
			if len(seams.fnErrs) != 1 || !errors.Is(seams.fnErrs[0], errClaudeQuestionBindingChanged) {
				t.Fatalf("transaction errors = %v, want the binding fence", seams.fnErrs)
			}
			agent, _ := fixture.resources.registry.Agent(questionTestAgent)
			beforeAgent, _ := before.Agent(questionTestAgent)
			if agent.Status.Interaction != beforeAgent.Status.Interaction {
				t.Fatalf("interaction = %+v, want it untouched", agent.Status.Interaction)
			}
		})
	}
}

// TestClaudeQuestionHookRaiseStaysOutOfWayOneAndFailsSilently holds acceptance
// 5: way 1 never opens the transaction or touches tmux, and a transaction that
// fails leaves the decision byte for byte what it would have been.
func TestClaudeQuestionHookRaiseStaysOutOfWayOneAndFailsSilently(t *testing.T) {
	t.Parallel()

	t.Run("way 1", func(t *testing.T) {
		t.Parallel()
		fixture := newQuestionFixture(t, false)
		seams := &questionRaiseSeams{fixture: fixture}
		hook := fixture.hook(time.Minute)
		seams.install(&hook)
		var stdout bytes.Buffer
		hook.run(context.Background(), []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
		if stdout.Len() != 0 || fixture.opened != 0 {
			t.Fatalf("way 1 stdout=%q store opens=%d, want silence and no store", stdout.String(), fixture.opened)
		}
		if updates, _, projections := seams.counts(); updates != 0 || projections != 0 {
			t.Fatalf("way 1 updates=%d projections=%d, want none", updates, projections)
		}
	})
	t.Run("update fails", func(t *testing.T) {
		t.Parallel()
		fixture := newQuestionFixture(t, true)
		seams := &questionRaiseSeams{fixture: fixture, fail: errors.New("registry locked")}
		hook := fixture.hook(time.Minute)
		seams.install(&hook)
		_, done := runRaiseHook(t, context.Background(), fixture, hook, questionRaiseAnswer)
		if got := waitHookOutput(t, done); got != questionRaiseDecision {
			t.Fatalf("decision =\n%s\nwant\n%s", got, questionRaiseDecision)
		}
		if updates, writes, projections := seams.counts(); updates != 1 || writes != 0 || projections != 0 {
			t.Fatalf("updates=%d writes=%d projections=%d, want one failed transaction and no projection", updates, writes, projections)
		}
	})
}
