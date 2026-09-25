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

// questionFakeClock is the injected clock of the freshness tests. The tests
// advance it from the readRecord seam, so the hook's refresh schedule, the
// Mutator stamps, and the question store all read the same time without a
// real half-hour wait.
type questionFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newQuestionFakeClock(start time.Time) *questionFakeClock {
	return &questionFakeClock{now: start.UTC()}
}

func (c *questionFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *questionFakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// questionFreshnessStep is how far each poll of the freshness scripts moves
// the fake clock: five polls make one refresh interval.
const questionFreshnessStep = claudeQuestionRefreshInterval / 5

// newFreshnessFixture is the question fixture on the fake clock: the hook's
// now and the question store's clock are both clock, so a window longer than
// the scripted advance never expires, and the raise seams are installed.
func newFreshnessFixture(t *testing.T, clock *questionFakeClock, window time.Duration) (*questionFixture, *questionRaiseSeams, claudeQuestionHook) {
	t.Helper()
	fixture := newQuestionFixture(t, true)
	fixture.store = agentquestion.NewStore(t.TempDir()).WithClock(clock.Now)
	seams := &questionRaiseSeams{fixture: fixture}
	hook := fixture.hook(window)
	hook.now = clock.Now
	seams.install(&hook)
	return fixture, seams, hook
}

// recordObservedAt wraps the hook's Registry transaction so every committed
// write reports the asking Agent's ObservedAt it left behind. The slice is
// written by the hook goroutine only and read after the hook returned.
func recordObservedAt(hook *claudeQuestionHook) *[]time.Time {
	var observed []time.Time
	inner := hook.updateRegistry
	hook.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		registry, err := inner(fn)
		if err == nil {
			agent, _ := registry.Agent(questionTestAgent)
			observed = append(observed, agent.Status.Interaction.ObservedAt)
		}
		return registry, err
	}
	return &observed
}

// runFreshnessHook runs the hook in the background with script called on
// every poll, before the record is read, with the 1-based poll number.
func runFreshnessHook(t *testing.T, ctx context.Context, hook claudeQuestionHook, script func(poll int, store *agentquestion.Store, id string)) <-chan string {
	t.Helper()
	poll := 0
	hook.readRecord = func(store *agentquestion.Store, id string) (agentquestion.Record, bool, error) {
		poll++
		script(poll, store, id)
		return store.Get(id)
	}
	done := make(chan string, 1)
	go func() {
		var stdout bytes.Buffer
		hook.run(ctx, []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
		done <- stdout.String()
	}()
	return done
}

func (s *questionRaiseSeams) agent(t *testing.T) coremetadata.Agent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.fixture.resources.registry.Agent(questionTestAgent)
	if !ok {
		t.Fatal("question Agent missing")
	}
	return agent.Clone()
}

func (s *questionRaiseSeams) errs() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.fnErrs)
}

// TestClaudeQuestionHookRefreshKeepsAWaitingQuestionInputRequired holds
// acceptance 1: a way-2 question left waiting for longer than the interaction
// freshness window recommits provider-hook input_required once per refresh
// interval, each commit moving ObservedAt to the hook's clock, so past
// AgentInteractionFreshFor plus a minute the effective interaction is still
// input_required where the raise's own observation alone reads as unknown, and
// the hold predicate the send path asks still holds the Agent.
func TestClaudeQuestionHookRefreshKeepsAWaitingQuestionInputRequired(t *testing.T) {
	t.Parallel()

	start := time.Now().UTC()
	clock := newQuestionFakeClock(start)
	_, seams, hook := newFreshnessFixture(t, clock, 2*time.Hour)
	observed := recordObservedAt(&hook)
	// Sixteen polls of two minutes reach 32 minutes, past the window plus a
	// minute and across three refresh intervals; poll 17 judges and answers.
	const polls = 16
	var atEnd coremetadata.Agent
	var judgedAt time.Time
	done := runFreshnessHook(t, context.Background(), hook, func(poll int, store *agentquestion.Store, id string) {
		if poll <= polls {
			clock.Advance(questionFreshnessStep)
			return
		}
		if poll == polls+1 {
			atEnd, judgedAt = seams.agent(t), clock.Now()
			if _, err := store.Answer(id, questionTestAgent, questionRaiseAnswer); err != nil {
				t.Errorf("answer: %v", err)
			}
		}
	})
	if got := waitHookOutput(t, done); got != questionRaiseDecision {
		t.Fatalf("decision =\n%s\nwant\n%s", got, questionRaiseDecision)
	}
	if judgedAt.Sub(start) <= coremetadata.AgentInteractionFreshFor+time.Minute {
		t.Fatalf("the script only reached %s", judgedAt.Sub(start))
	}
	want := []time.Time{start, start.Add(claudeQuestionRefreshInterval), start.Add(2 * claudeQuestionRefreshInterval), start.Add(3 * claudeQuestionRefreshInterval)}
	if !slices.EqualFunc(*observed, want, time.Time.Equal) {
		t.Fatalf("committed ObservedAt = %v, want the raise and one refresh per interval %v", *observed, want)
	}
	if got := atEnd.EffectiveInteraction(judgedAt); got.Kind != coremetadata.InteractionInputRequired ||
		got.Source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("effective interaction at %s = %+v, want provider-hook input_required", judgedAt.Sub(start), got)
	}
	unrefreshed := atEnd.Clone()
	unrefreshed.Status.Interaction.ObservedAt = start
	if got := unrefreshed.EffectiveInteraction(judgedAt); got.Kind != coremetadata.InteractionUnknown {
		t.Fatalf("the raise alone reads as %+v at %s, want unknown", got, judgedAt.Sub(start))
	}
	if !claudeAgentAwaitsOperator(atEnd, judgedAt) {
		t.Fatal("the send path's hold predicate no longer holds the Agent")
	}
	// Refreshes rewrite the observation only; the Pane keeps the raise's one
	// projection.
	if updates, writes, projections := seams.counts(); updates != 4 || writes != 4 || projections != 1 {
		t.Fatalf("updates=%d writes=%d projections=%d, want the raise, three refreshes, one projection", updates, writes, projections)
	}
}

// TestClaudeQuestionHookRefreshKeepsTheHoldAndThePostToolUseLowering holds
// acceptance 2 end to end over the ingest fixture's Registry, with the ingest
// command's clock and the hook's clock the same fake clock: 32 minutes into a
// waiting question the Agent still awaits its operator, a peer's message is
// held, and after the answer the AskUserQuestion PostToolUse, judged at that
// same time, moves the Agent to provider-hook in_progress and launches the held
// release. Without the refresh the PostToolUse gate would read the raise as
// stale and stay quiet.
func TestClaudeQuestionHookRefreshKeepsTheHoldAndThePostToolUseLowering(t *testing.T) {
	t.Parallel()

	f := newClaudeQuietHookFixture(t, true)
	// Settle the session ref first, so the close is judged on its own write.
	f.ingest(t, "PreToolUse")
	clock := newQuestionFakeClock(claudeQuietHookClock)
	// PostToolUse judges staleness through sessionRefClock, which is cmd.now.
	f.cmd.now = clock.Now
	messages := messagestore.NewStore(t.TempDir())
	var launches []string
	f.cmd.heldRelease = heldMessageRelease{
		store:  func() (agentMessageHeldLister, error) { return messages, nil },
		launch: func(agentUID string) error { launches = append(launches, agentUID); return nil },
	}
	questions := agentquestion.NewStore(t.TempDir()).WithClock(clock.Now)
	hook := claudeQuestionHook{
		loadRegistry:       func() (coremetadata.Registry, error) { return f.registry.Clone(), nil },
		store:              func() (*agentquestion.Store, error) { return questions, nil },
		answering:          func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux },
		window:             func() time.Duration { return 2 * time.Hour },
		poll:               10 * time.Millisecond,
		newID:              agentquestion.NewID,
		now:                clock.Now,
		updateRegistry:     f.cmd.updateRegistry,
		projectInteraction: f.cmd.projectManagedAgentInteraction,
	}
	const polls = 16
	var waiting coremetadata.Agent
	var judgedAt time.Time
	poll := 0
	hook.readRecord = func(store *agentquestion.Store, id string) (agentquestion.Record, bool, error) {
		poll++
		if poll <= polls {
			clock.Advance(questionFreshnessStep)
		} else if poll == polls+1 {
			agent, _ := f.registry.Agent(f.agentUID)
			waiting, judgedAt = agent.Clone(), clock.Now()
			// A peer's message arrives while the question waits and is held.
			route := coremessage.Route{AgentUID: f.agentUID, PaneUID: "pane-held", ActivationGeneration: "generation-held",
				Provider: "claude", Incarnation: "incarnation-held"}
			now := time.Now().UTC()
			envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-held-past-freshness",
				ConversationRef: "conversation-held-past-freshness", Source: route, Target: route, Authority: coremessage.PeerAuthority(),
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
		}
		return store.Get(id)
	}
	var stdout bytes.Buffer
	payload := `{"hook_event_name":"PreToolUse","session_id":"` + claudeQuietHookSession + `","tool_name":"AskUserQuestion","tool_use_id":"toolu_1","tool_input":{"questions": ` + questionTestQuestions + `}}`
	hook.run(context.Background(), []string{"--pane=" + claudeQuietHookPane}, strings.NewReader(payload), &stdout, &bytes.Buffer{})
	if stdout.String() != questionRaiseDecision {
		t.Fatalf("decision =\n%s\nwant\n%s", stdout.String(), questionRaiseDecision)
	}
	if judgedAt.Sub(claudeQuietHookClock) <= coremetadata.AgentInteractionFreshFor+time.Minute {
		t.Fatalf("the script only reached %s", judgedAt.Sub(claudeQuietHookClock))
	}
	if !waiting.Status.Interaction.ObservedAt.After(claudeQuietHookClock) {
		t.Fatalf("ObservedAt = %s, want a refresh after the raise at %s", waiting.Status.Interaction.ObservedAt, claudeQuietHookClock)
	}
	if !claudeAgentAwaitsOperator(waiting, judgedAt) {
		t.Fatalf("interaction %+v does not hold at %s", waiting.Status.Interaction, judgedAt.Sub(claudeQuietHookClock))
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
	if !clock.Now().Equal(judgedAt) {
		t.Fatalf("clock moved to %s after the answer", clock.Now())
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

// TestClaudeQuestionHookRefreshNeverOverwritesAnotherWrite holds acceptance 3:
// once anything but the raise owns the interaction (Stop's response_complete,
// UserPromptSubmit's in_progress, a manual input_required) or the binding moved
// (Agent PaneRef, Pane activation Generation), the next refresh aborts on its
// fence or compare-and-set, writes nothing, and no later interval writes again,
// even when provider-hook input_required comes back.
func TestClaudeQuestionHookRefreshNeverOverwritesAnotherWrite(t *testing.T) {
	t.Parallel()

	setInteraction := func(kind coremetadata.AgentInteractionKind, source coremetadata.AgentInteractionSource) func(*coremetadata.Registry, func() time.Time) {
		return func(registry *coremetadata.Registry, now func() time.Time) {
			if _, err := (coremetadata.Mutator{Now: now}).SetAgentInteraction(registry, questionTestAgent, kind, string(source)); err != nil {
				panic(err)
			}
		}
	}
	for _, test := range []struct {
		name   string
		change func(*coremetadata.Registry, func() time.Time)
		// restore writes provider-hook input_required back later, which must
		// not rearm the refresh.
		restore bool
		want    error
	}{
		{name: "Stop response_complete", change: setInteraction(coremetadata.InteractionResponseComplete, coremetadata.InteractionSourceProviderHook), want: errClaudeQuestionInteractionChanged},
		{name: "Stop response_complete then provider-hook input_required again", change: setInteraction(coremetadata.InteractionResponseComplete, coremetadata.InteractionSourceProviderHook), restore: true, want: errClaudeQuestionInteractionChanged},
		{name: "UserPromptSubmit in_progress", change: setInteraction(coremetadata.InteractionInProgress, coremetadata.InteractionSourceProviderHook), want: errClaudeQuestionInteractionChanged},
		{name: "manual input_required", change: setInteraction(coremetadata.InteractionInputRequired, coremetadata.InteractionSourceManual), want: errClaudeQuestionInteractionChanged},
		{name: "Agent PaneRef changed", change: func(registry *coremetadata.Registry, _ func() time.Time) {
			agent, _ := registry.Agent(questionTestAgent)
			agent.Status.PaneRef = "pan-alpha-other"
		}, want: errClaudeQuestionBindingChanged},
		{name: "Pane activation Generation changed", change: func(registry *coremetadata.Registry, _ func() time.Time) {
			pane, _ := registry.Pane(questionTestPane)
			pane.Status.Activation.Generation = "gen-2"
		}, want: errClaudeQuestionBindingChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			clock := newQuestionFakeClock(time.Now())
			fixture, seams, hook := newFreshnessFixture(t, clock, 2*time.Hour)
			var changed coremetadata.AgentInteraction
			// Poll 5 refreshes once (10 minutes), poll 6 changes the Registry,
			// poll 10 (20 minutes) aborts, and polls 15 and 20 (30 and 40
			// minutes) must not try again; poll 21 answers.
			done := runFreshnessHook(t, context.Background(), hook, func(poll int, store *agentquestion.Store, id string) {
				switch {
				case poll <= 20:
					clock.Advance(questionFreshnessStep)
					if poll == 6 || (test.restore && poll == 12) {
						seams.mu.Lock()
						if poll == 6 {
							test.change(&fixture.resources.registry, clock.Now)
						} else {
							setInteraction(coremetadata.InteractionInputRequired, coremetadata.InteractionSourceProviderHook)(&fixture.resources.registry, clock.Now)
						}
						agent, _ := fixture.resources.registry.Agent(questionTestAgent)
						changed = agent.Status.Interaction
						seams.mu.Unlock()
					}
				case poll == 21:
					if _, err := store.Answer(id, questionTestAgent, questionRaiseAnswer); err != nil {
						t.Errorf("answer: %v", err)
					}
				}
			})
			if got := waitHookOutput(t, done); got != questionRaiseDecision {
				t.Fatalf("decision =\n%s\nwant\n%s", got, questionRaiseDecision)
			}
			if updates, writes, projections := seams.counts(); updates != 3 || writes != 2 || projections != 1 {
				t.Fatalf("updates=%d writes=%d projections=%d, want the raise, one refresh, one aborted refresh", updates, writes, projections)
			}
			if errs := seams.errs(); len(errs) != 1 || !errors.Is(errs[0], test.want) {
				t.Fatalf("transaction errors = %v, want %v", errs, test.want)
			}
			if got := seams.interaction(t); got != changed {
				t.Fatalf("interaction = %+v, want the other writer's %+v", got, changed)
			}
		})
	}
}

// TestClaudeQuestionHookRefreshRetriesATransientFailureOncePerMinute holds the
// retry bound: a Registry transaction that fails for a reason other than the
// fence is tried again claudeQuestionRefreshRetry later by the hook's clock,
// not on every poll, and the refresh resumes once it succeeds.
func TestClaudeQuestionHookRefreshRetriesATransientFailureOncePerMinute(t *testing.T) {
	t.Parallel()

	clock := newQuestionFakeClock(time.Now())
	_, seams, hook := newFreshnessFixture(t, clock, 2*time.Hour)
	step := 10 * time.Second
	// Poll 1 jumps to the first due refresh with the Registry failing; then
	// every poll moves ten seconds. The failure clears at 12m10s (poll 14) and
	// the script answers at 13m (poll 20), so the attempts are 10m, 11m, 12m
	// failing and 13m committing, out of twenty polls.
	done := runFreshnessHook(t, context.Background(), hook, func(poll int, store *agentquestion.Store, id string) {
		switch {
		case poll == 1:
			seams.mu.Lock()
			seams.fail = errors.New("registry locked")
			seams.mu.Unlock()
			clock.Advance(claudeQuestionRefreshInterval)
		case poll < 20:
			clock.Advance(step)
			if poll == 14 {
				seams.mu.Lock()
				seams.fail = nil
				seams.mu.Unlock()
			}
		case poll == 20:
			if _, err := store.Answer(id, questionTestAgent, questionRaiseAnswer); err != nil {
				t.Errorf("answer: %v", err)
			}
		}
	})
	if got := waitHookOutput(t, done); got != questionRaiseDecision {
		t.Fatalf("decision =\n%s\nwant\n%s", got, questionRaiseDecision)
	}
	if updates, writes, _ := seams.counts(); updates != 5 || writes != 2 {
		t.Fatalf("updates=%d writes=%d, want the raise, three failed retries a minute apart, and one refresh", updates, writes)
	}
}

// TestClaudeQuestionHookRefreshPanicIsContained holds the silence of the
// refresh: a panic inside its transaction is recovered, stops refreshing for
// the rest of the wait, and leaves the answer's decision byte for byte.
func TestClaudeQuestionHookRefreshPanicIsContained(t *testing.T) {
	t.Parallel()

	clock := newQuestionFakeClock(time.Now())
	_, seams, hook := newFreshnessFixture(t, clock, 2*time.Hour)
	done := runFreshnessHook(t, context.Background(), hook, func(poll int, store *agentquestion.Store, id string) {
		switch {
		case poll == 1:
			seams.mu.Lock()
			seams.mutate = func(*coremetadata.Registry) { panic("refresh transaction") }
			seams.mu.Unlock()
			fallthrough
		case poll <= 15:
			clock.Advance(questionFreshnessStep)
		case poll == 16:
			if _, err := store.Answer(id, questionTestAgent, questionRaiseAnswer); err != nil {
				t.Errorf("answer: %v", err)
			}
		}
	})
	if got := waitHookOutput(t, done); got != questionRaiseDecision {
		t.Fatalf("decision =\n%s\nwant\n%s", got, questionRaiseDecision)
	}
	if updates, writes, _ := seams.counts(); updates != 2 || writes != 1 {
		t.Fatalf("updates=%d writes=%d, want the raise and one panicking refresh, then none", updates, writes)
	}
}

// TestClaudeQuestionHookRefreshStopsWhenTheRecordEnds holds acceptance 4: a
// question that is answered, whose popup picker closes it, whose hook is
// canceled, or that expires writes nothing after it ends, even though the
// clock is already past the next refresh on the very poll that sees the end
// and moves on after the hook returned: the writes stay at the raise and the
// two refreshes before the end.
func TestClaudeQuestionHookRefreshStopsWhenTheRecordEnds(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		window time.Duration
		popup  bool
		cancel bool
		state  agentquestion.State
		output string
	}{
		{name: "answered", window: 2 * time.Hour, state: agentquestion.StateAnswered, output: questionRaiseDecision},
		{name: "popup closed", window: 2 * time.Hour, popup: true, state: agentquestion.StateClosed},
		{name: "hook canceled", window: 2 * time.Hour, cancel: true, state: agentquestion.StateClosed},
		{name: "expired", window: 2*claudeQuestionRefreshInterval + time.Minute, state: agentquestion.StateExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			clock := newQuestionFakeClock(time.Now())
			fixture := newQuestionFixture(t, true)
			fixture.store = agentquestion.NewStore(t.TempDir()).WithClock(clock.Now)
			// The popup stands in for a picker the operator leaves with Esc:
			// it closes the record itself, then ends.
			escape, escaped := make(chan struct{}), make(chan struct{})
			if test.popup {
				popup := newFakeQuestionPopup("client-1")
				popup.open = func(ctx context.Context, target claudeQuestionPopupTarget, _ <-chan struct{}) error {
					select {
					case <-escape:
					case <-ctx.Done():
						return nil
					}
					_, err := fixture.store.Close(target.QuestionID)
					close(escaped)
					return err
				}
				fixture.popup = popup
			}
			seams := &questionRaiseSeams{fixture: fixture}
			hook := fixture.hook(test.window)
			hook.now = clock.Now
			seams.install(&hook)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Polls 5 and 10 refresh (10 and 20 minutes); poll 11 ends the
			// record and jumps the clock 25 minutes past the next due refresh.
			done := runFreshnessHook(t, ctx, hook, func(poll int, store *agentquestion.Store, id string) {
				switch {
				case poll <= 10:
					clock.Advance(questionFreshnessStep)
				case poll == 11:
					switch {
					case test.popup:
						close(escape)
						select {
						case <-escaped:
						case <-time.After(10 * time.Second):
							t.Error("the popup never closed the record")
						}
					case test.cancel:
						cancel()
					case test.state == agentquestion.StateAnswered:
						if _, err := store.Answer(id, questionTestAgent, questionRaiseAnswer); err != nil {
							t.Errorf("answer: %v", err)
						}
					}
					clock.Advance(claudeQuestionRefreshInterval + 25*time.Minute)
				}
			})
			if got := waitHookOutput(t, done); got != test.output {
				t.Fatalf("hook printed %q, want %q", got, test.output)
			}
			records, _ := fixture.store.List(questionTestAgent)
			if len(records) != 1 || records[0].State != test.state {
				t.Fatalf("records = %#v, want one %s", records, test.state)
			}
			clock.Advance(3 * claudeQuestionRefreshInterval)
			if updates, writes, projections := seams.counts(); updates != 3 || writes != 3 || projections != 1 {
				t.Fatalf("updates=%d writes=%d projections=%d, want the raise and the two refreshes before the end", updates, writes, projections)
			}
		})
	}
}
