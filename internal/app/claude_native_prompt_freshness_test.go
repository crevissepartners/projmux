package app

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// nativePromptStep is how far the native-prompt scripts move the fake clock
// between steps: two steps make one refresh interval.
const nativePromptStep = claudeNativePromptRefreshInterval / 2

// nativePromptHorizon is how far past its raise every script drives a native
// prompt: past the freshness window plus two minutes, where the raise alone
// reads as unknown.
const nativePromptHorizon = coremetadata.AgentInteractionFreshFor + 2*time.Minute

func nativePromptToolUseLine(at time.Time, id string) string {
	return `{"type":"assistant","timestamp":"` + claudeStamp(at) + `","message":{"role":"assistant","content":` +
		`[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"true"}}]},"sessionId":"session-hold"}`
}

func nativePromptToolResultLine(at time.Time, id string) string {
	return `{"type":"user","timestamp":"` + claudeStamp(at) + `","message":{"role":"user","content":` +
		`[{"type":"tool_result","tool_use_id":"` + id + `","content":"placeholder"}]},"sessionId":"session-hold"}`
}

// nativePromptOpenTurn is a transcript whose turn raised a dialog for toolu_1
// just before raisedAt and wrote nothing since, which is what Claude leaves
// while a permission dialog or an elicitation is up.
func nativePromptOpenTurn(raisedAt time.Time) []string {
	return []string{claudeTurnDurationLine(claudeStamp(raisedAt.Add(-time.Minute))), claudeToolUseLine(raisedAt.Add(-time.Second))}
}

// nativePromptEvents is the operations journal the tests' recorder writes to.
type nativePromptEvents struct{ events []diagnostics.Event }

func (w *nativePromptEvents) Append(event diagnostics.Event) error {
	w.events = append(w.events, event)
	return nil
}

// nativePromptFixture is the hold fixture's Claude Agent raised by a native
// prompt at raisedAt, with the supervisor's refresh wired to the fixture's
// Registry, transcript, and clock, and to a recorder writing to events.
type nativePromptFixture struct {
	*holdFixture
	raisedAt  time.Time
	spec      superviseSpec
	refresh   claudeNativePromptRefresh
	events    *nativePromptEvents
	updateErr error
	// observed is the Agent's ObservedAt after every committed write.
	observed []time.Time
}

func newNativePromptFixture(t *testing.T, kind coremetadata.AgentInteractionKind) *nativePromptFixture {
	t.Helper()
	f := &nativePromptFixture{holdFixture: newHoldFixture(t), events: &nativePromptEvents{}}
	f.raisedAt = f.now
	f.bindTranscript(t, holdTranscriptPath)
	f.setInteraction(t, kind)
	f.transcript = claudeTranscript(nativePromptOpenTurn(f.raisedAt)...)
	agent := f.agent(t)
	pane, ok := f.registry.Pane(agent.Status.PaneRef)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || pane.Status.Activation.AgentUID != f.claudeUID {
		t.Fatalf("fixture Agent %+v on Pane %+v is not a running activation", agent.Status, pane)
	}
	f.spec = superviseSpec{PaneUID: agent.Status.PaneRef, AgentUID: f.claudeUID, Generation: pane.Status.Activation.Generation}
	f.refresh = newClaudeNativePromptRefresh(f.spec, diagnostics.NewLifecycleRecorder(f.events, "run-native-prompt", "test", "tmux").AI())
	f.refresh.now = func() time.Time { return f.now }
	f.refresh.load = func() (coremetadata.Registry, error) { return f.registry.Clone(), nil }
	f.refresh.readTail = f.cmd.messageTranscriptTail
	f.refresh.update = func(fn func(*coremetadata.Registry) error) error {
		if f.updateErr != nil {
			return f.updateErr
		}
		registry, err := f.cmd.store.update(fn)
		if err == nil {
			agent, _ := registry.Agent(f.claudeUID)
			f.observed = append(f.observed, agent.Status.Interaction.ObservedAt)
		}
		return err
	}
	return f
}

func (f *nativePromptFixture) agent(t *testing.T) coremetadata.Agent {
	t.Helper()
	agent, ok := f.registry.Agent(f.claudeUID)
	if !ok {
		t.Fatal("claude agent fixture missing")
	}
	return agent.Clone()
}

// stepUntil advances the fake clock by nativePromptStep and steps the refresh
// until the clock is past horizon after the raise, or, like the watcher, until
// a step reports that the refresh can never apply again.
func (f *nativePromptFixture) stepUntil(horizon time.Duration) {
	for f.now.Sub(f.raisedAt) <= horizon {
		f.now = f.now.Add(nativePromptStep)
		if !f.refresh.step() {
			return
		}
	}
}

// Acceptance 1: a native prompt left open past the freshness window is
// recommitted once per refresh interval, each commit moving ObservedAt to the
// injected clock, so past the window plus two minutes the effective interaction
// is still the raised kind where the raise alone reads as unknown, and the hold
// predicate the send path asks still holds the Agent.
func TestClaudeNativePromptRefreshKeepsAnOpenDialogBlocking(t *testing.T) {
	for _, kind := range []coremetadata.AgentInteractionKind{coremetadata.InteractionApprovalRequired, coremetadata.InteractionInputRequired} {
		t.Run(string(kind), func(t *testing.T) {
			f := newNativePromptFixture(t, kind)
			f.stepUntil(nativePromptHorizon)
			at := f.now
			want := []time.Time{f.raisedAt.Add(claudeNativePromptRefreshInterval), f.raisedAt.Add(2 * claudeNativePromptRefreshInterval),
				f.raisedAt.Add(3 * claudeNativePromptRefreshInterval)}
			if !slices.EqualFunc(f.observed, want, time.Time.Equal) {
				t.Fatalf("committed ObservedAt = %v, want one recommit per interval %v", f.observed, want)
			}
			agent := f.agent(t)
			if got := agent.EffectiveInteraction(at); got.Kind != kind || got.Source != string(coremetadata.InteractionSourceProviderHook) {
				t.Fatalf("effective interaction at %s = %+v, want provider-hook %s", at.Sub(f.raisedAt), got, kind)
			}
			unrefreshed := agent.Clone()
			unrefreshed.Status.Interaction.ObservedAt = f.raisedAt
			if got := unrefreshed.EffectiveInteraction(at); got.Kind != coremetadata.InteractionUnknown {
				t.Fatalf("the raise alone reads as %+v at %s, want unknown", got, at.Sub(f.raisedAt))
			}
			if !claudeAgentAwaitsOperator(agent, at) {
				t.Fatal("the send path's hold predicate no longer holds the Agent")
			}
			if len(f.events.events) != 0 {
				t.Fatalf("diagnostics = %+v, want none for committed recommits", f.events.events)
			}
		})
	}
}

// Acceptance 2: a transcript that does not positively show the dialog open --
// the turn ended after a denial or Esc, the tool_use has its result, the read
// failed, a line is not JSON, the file is empty, or the Agent recorded no
// transcript -- is never recommitted, and the raise decays to unknown.
func TestClaudeNativePromptRefreshLeavesAClosedOrDoubtfulDialogToDecay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *nativePromptFixture)
	}{
		{name: "denied turn ended", setup: func(f *nativePromptFixture) {
			r := f.raisedAt
			f.transcript = claudeTranscript(claudeDeniedTurn(r.Add(-time.Minute), r.Add(-time.Second), r.Add(time.Minute), r.Add(time.Minute+time.Second))...)
		}},
		{name: "tool_use answered", setup: func(f *nativePromptFixture) {
			f.transcript = claudeTranscript(append(nativePromptOpenTurn(f.raisedAt), nativePromptToolResultLine(f.raisedAt.Add(time.Minute), "toolu_1"))...)
		}},
		{name: "read error", setup: func(f *nativePromptFixture) { f.transcriptErr = errors.New("transcript read failed") }},
		{name: "non-JSON line", setup: func(f *nativePromptFixture) {
			f.transcript = claudeTranscript(append(nativePromptOpenTurn(f.raisedAt), "not json")...)
		}},
		{name: "empty file", setup: func(f *nativePromptFixture) { f.transcript = nil }},
		{name: "missing path", setup: func(f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Status.SessionRef = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativePromptFixture(t, coremetadata.InteractionApprovalRequired)
			tc.setup(f)
			f.stepUntil(nativePromptHorizon)
			if f.registryWrites != 0 || len(f.events.events) != 0 {
				t.Fatalf("writes=%d diagnostics=%+v, want no recommit and nothing recorded", f.registryWrites, f.events.events)
			}
			if got := f.agent(t).EffectiveInteraction(f.now); got.Kind != coremetadata.InteractionUnknown {
				t.Fatalf("effective interaction at %s = %+v, want unknown", f.now.Sub(f.raisedAt), got)
			}
		})
	}
}

// Acceptance 2: the judge answers true only on positive evidence of an
// unanswered tool_use after the last turn_duration.
func TestClaudeNativePromptOpenReadsOnlyUnansweredToolUses(t *testing.T) {
	raisedAt := time.Date(2026, 9, 25, 5, 10, 0, 0, time.UTC)
	open := nativePromptOpenTurn(raisedAt)
	parallel := []string{claudeTurnDurationLine(claudeStamp(raisedAt.Add(-time.Minute))),
		nativePromptToolUseLine(raisedAt, "toolu_a"), nativePromptToolUseLine(raisedAt.Add(time.Second), "toolu_b")}
	for _, tc := range []struct {
		name      string
		tail      []byte
		truncated bool
		want      bool
	}{
		{name: "unanswered tool_use", tail: claudeTranscript(open...), want: true},
		{name: "unanswered tool_use without a trailing newline", tail: []byte(strings.Join(open, "\n")), want: true},
		{name: "unanswered tool_use among lines that are not turns",
			tail: claudeTranscript(append(open, claudeUntimestampedLines(raisedAt)...)...), want: true},
		{name: "truncated head dropped", tail: claudeTranscript(append([]string{`partial"}`}, open...)...), truncated: true, want: true},
		{name: "parallel tool_uses both unanswered", tail: claudeTranscript(parallel...), want: true},
		{name: "parallel tool_uses one still unanswered",
			tail: claudeTranscript(append(parallel, nativePromptToolResultLine(raisedAt.Add(2*time.Second), "toolu_a"))...), want: true},
		{name: "parallel tool_uses both answered", tail: claudeTranscript(append(parallel,
			nativePromptToolResultLine(raisedAt.Add(31*time.Minute), "toolu_a"), nativePromptToolResultLine(raisedAt.Add(31*time.Minute), "toolu_b"))...)},
		{name: "tool_result for a tool_use outside the tail is ignored",
			tail: claudeTranscript(append(open, nativePromptToolResultLine(raisedAt, "toolu_gone"))...), want: true},
		{name: "tool_use after a turn_duration", want: true, tail: claudeTranscript(claudeTurnDurationLine(claudeStamp(raisedAt.Add(time.Minute))),
			nativePromptToolUseLine(raisedAt.Add(2*time.Minute), "toolu_2"))},
		{name: "tool_use only before the last turn_duration", tail: claudeTranscript(nativePromptToolUseLine(raisedAt.Add(-2*time.Minute), "toolu_0"),
			claudeTurnDurationLine(claudeStamp(raisedAt.Add(-time.Minute))))},
		{name: "denied turn ended", tail: claudeTranscript(claudeDeniedTurn(raisedAt.Add(-time.Minute), raisedAt.Add(-time.Second),
			raisedAt.Add(time.Minute), raisedAt.Add(time.Minute+time.Second))...)},
		{name: "answered", tail: claudeTranscript(append(open, nativePromptToolResultLine(raisedAt.Add(time.Minute), "toolu_1"))...)},
		{name: "no tool_use", tail: claudeTranscript(claudeTurnDurationLine(claudeStamp(raisedAt.Add(-time.Minute))), claudeAssistantTextLine(raisedAt))},
		{name: "empty", tail: nil},
		{name: "truncated head without a newline", tail: []byte(open[1]), truncated: true},
		{name: "non-JSON line", tail: claudeTranscript(append(open, "not json")...)},
		{name: "tool_use without an id", tail: claudeTranscript(append(open, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`)...)},
		{name: "tool_result without a tool_use_id", tail: claudeTranscript(append(open, `{"type":"user","message":{"content":[{"type":"tool_result"}]}}`)...)},
		{name: "unreadable assistant message", tail: claudeTranscript(append(open, `{"type":"assistant","message":"placeholder"}`)...)},
		{name: "unreadable user content", tail: claudeTranscript(append(open, `{"type":"user","message":{"content":{"type":"tool_result"}}}`)...)},
		{name: "assistant without a message", tail: claudeTranscript(append(open, `{"type":"assistant"}`)...)},
		{name: "content item not an object", tail: claudeTranscript(append(open, `{"type":"assistant","message":{"content":["tool_use"]}}`)...)},
		{name: "user string content", want: true, tail: claudeTranscript(append(open, `{"type":"user","message":{"role":"user","content":"placeholder"}}`)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeNativePromptOpen(tc.tail, tc.truncated, raisedAt); got != tc.want {
				t.Fatalf("claudeNativePromptOpen = %v, want %v", got, tc.want)
			}
		})
	}
}

// Acceptance 3: nothing is written unless the raw interaction is a
// provider-hook native prompt that is still exactly the judged observation, on
// the running Claude Agent bound to this supervisor's Pane and activation.
func TestClaudeNativePromptRefreshWritesNothingOutsideItsFenceAndObservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *nativePromptFixture)
	}{
		{name: "in_progress", setup: func(t *testing.T, f *nativePromptFixture) { f.setInteraction(t, coremetadata.InteractionInProgress) }},
		{name: "approval_required from another source", setup: func(t *testing.T, f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Status.Interaction.Source = string(coremetadata.InteractionSourceCompatibilityAI)
		}},
		{name: "observation changed after the judge", setup: func(_ *testing.T, f *nativePromptFixture) {
			f.beforeUpdate = func(working *coremetadata.Registry) {
				for _, registry := range []*coremetadata.Registry{working, f.registry} {
					agent, _ := registry.Agent(f.claudeUID)
					agent.Status.Interaction.ObservedAt = agent.Status.Interaction.ObservedAt.Add(time.Second)
				}
			}
		}},
		{name: "phase not running", setup: func(_ *testing.T, f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Status.Phase = coremetadata.PhaseOffline
		}},
		{name: "pane ref differs", setup: func(_ *testing.T, f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Status.PaneRef = "pan-other"
		}},
		{name: "pane generation differs", setup: func(_ *testing.T, f *nativePromptFixture) {
			pane, _ := f.registry.Pane(f.spec.PaneUID)
			pane.Status.Activation.Generation = "gen-other"
		}},
		{name: "activation agent differs", setup: func(_ *testing.T, f *nativePromptFixture) {
			pane, _ := f.registry.Pane(f.spec.PaneUID)
			pane.Status.Activation.AgentUID = "agt-other"
		}},
		{name: "provider not claude", setup: func(_ *testing.T, f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Spec.Provider = aiModeCodex
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativePromptFixture(t, coremetadata.InteractionApprovalRequired)
			tc.setup(t, f)
			before := f.agent(t).Status.Interaction
			f.stepUntil(claudeNativePromptRefreshInterval)
			if f.registryWrites != 0 || len(f.events.events) != 0 {
				t.Fatalf("writes=%d diagnostics=%+v, want no write and nothing recorded", f.registryWrites, f.events.events)
			}
			// Only the simulated other writer moves ObservedAt, a second per
			// commit attempt; a refresh would stamp a step's time.
			if got := f.agent(t).Status.Interaction; got.Kind != before.Kind || got.Source != before.Source ||
				got.ObservedAt.Sub(before.ObservedAt) >= time.Minute {
				t.Fatalf("interaction = %+v, want %+v kept", got, before)
			}
		})
	}
}

// Acceptance 4: a dialog denied after a recommit ends its turn with a
// turn_duration stamped after the recommitted observation, so the next step
// does not recommit and the held-message release still reads the turn end and
// commits response_complete. A denial written between the step's tail read and
// its commit is still after the recommit, which is stamped before the read.
func TestClaudeNativePromptRefreshLeavesADeniedDialogToTheTurnEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		deny func(f *nativePromptFixture) time.Time
	}{
		{name: "denied after the recommit", deny: func(f *nativePromptFixture) time.Time {
			f.stepUntil(claudeNativePromptRefreshInterval)
			deniedAt := f.now.Add(time.Minute)
			f.transcript = claudeTranscript(claudeDeniedTurn(f.raisedAt.Add(-time.Minute), f.raisedAt.Add(-time.Second), deniedAt, deniedAt.Add(time.Second))...)
			return deniedAt
		}},
		{name: "denied during the step", deny: func(f *nativePromptFixture) time.Time {
			var deniedAt time.Time
			read := f.refresh.readTail
			f.refresh.readTail = func(path string) ([]byte, bool, error) {
				tail, truncated, err := read(path)
				f.now = f.now.Add(2 * time.Second)
				deniedAt = f.now.Add(-time.Second)
				f.transcript = claudeTranscript(claudeDeniedTurn(f.raisedAt.Add(-time.Minute), f.raisedAt.Add(-time.Second), deniedAt, deniedAt)...)
				return tail, truncated, err
			}
			f.stepUntil(claudeNativePromptRefreshInterval)
			f.refresh.readTail = read
			return deniedAt
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativePromptFixture(t, coremetadata.InteractionApprovalRequired)
			deniedAt := tc.deny(f)
			if len(f.observed) != 1 || !f.observed[0].Before(deniedAt) {
				t.Fatalf("committed ObservedAt = %v, want one recommit before the denial at %v", f.observed, deniedAt)
			}
			writes := f.registryWrites
			f.stepUntil(2 * claudeNativePromptRefreshInterval)
			if f.registryWrites != writes {
				t.Fatalf("writes = %d after the denial, want %d", f.registryWrites, writes)
			}
			if !f.cmd.endBlockedClaudeTurn(f.agent(t)) {
				t.Fatal("the held-message release no longer reads the denied turn as ended")
			}
			if got := f.agent(t).Status.Interaction; got.Kind != coremetadata.InteractionResponseComplete ||
				got.Source != string(coremetadata.InteractionSourceProviderHook) {
				t.Fatalf("interaction = %+v, want provider-hook response_complete", got)
			}
		})
	}
}

// Acceptance 6: a dialog the transcript shows open whose recommit fails on its
// binding fence or on a Registry error records the ai.ingest.outcome failure
// once through the operations journal recorder; an observation another writer
// replaced records nothing.
func TestClaudeNativePromptRefreshRecordsAFailedRecommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   coremetadata.AgentInteractionKind
		setup  func(f *nativePromptFixture)
		want   diagnostics.AIKind
		record bool
	}{
		{name: "fence", kind: coremetadata.InteractionApprovalRequired, want: diagnostics.AIKindPermission, record: true,
			setup: func(f *nativePromptFixture) {
				f.beforeUpdate = func(working *coremetadata.Registry) {
					pane, _ := working.Pane(f.spec.PaneUID)
					pane.Status.Activation.Generation = "gen-next"
				}
			}},
		{name: "registry error", kind: coremetadata.InteractionInputRequired, want: diagnostics.AIKindNotification, record: true,
			setup: func(f *nativePromptFixture) { f.updateErr = errors.New("registry write failed") }},
		{name: "interaction changed", kind: coremetadata.InteractionApprovalRequired,
			setup: func(f *nativePromptFixture) {
				f.beforeUpdate = func(working *coremetadata.Registry) {
					agent, _ := working.Agent(f.claudeUID)
					agent.Status.Interaction.ObservedAt = agent.Status.Interaction.ObservedAt.Add(time.Second)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativePromptFixture(t, tc.kind)
			tc.setup(f)
			f.stepUntil(nativePromptHorizon)
			if f.registryWrites != 0 {
				t.Fatalf("writes = %d, want none", f.registryWrites)
			}
			if !tc.record {
				if len(f.events.events) != 0 {
					t.Fatalf("diagnostics = %+v, want none for another writer's observation", f.events.events)
				}
				return
			}
			if len(f.events.events) != 1 {
				t.Fatalf("diagnostics = %+v, want exactly one coalesced failure", f.events.events)
			}
			got := f.events.events[0]
			if got.Event != "ai.ingest.outcome" || got.Component != "ai" || got.Level != "error" || got.Result != "error" ||
				got.Kind != "runtime" || got.Provider != string(diagnostics.ProviderClaude) || got.AIKind != string(tc.want) ||
				got.AIResult != string(diagnostics.AIResultFailed) || got.Failure != string(diagnostics.AIFailureRoute) {
				t.Fatalf("diagnostic = %+v, want claude %s failed/route-failed", got, tc.want)
			}
		})
	}
}

// Acceptance 6 on disk: the real operations journal accepts the failure the
// refresh records, and it reads back from the store file.
func TestClaudeNativePromptRefreshFailureLandsInTheOperationsJournal(t *testing.T) {
	for _, tc := range []struct {
		kind coremetadata.AgentInteractionKind
		want diagnostics.AIKind
	}{
		{kind: coremetadata.InteractionApprovalRequired, want: diagnostics.AIKindPermission},
		{kind: coremetadata.InteractionInputRequired, want: diagnostics.AIKindNotification},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			f := newNativePromptFixture(t, tc.kind)
			store := diagnostics.NewStore(filepath.Join(t.TempDir(), "diagnostics", "events.jsonl"))
			wired := newClaudeNativePromptRefresh(f.spec, diagnostics.NewLifecycleRecorder(store, "run-native-prompt", "test", "tmux").AI())
			f.refresh.record = wired.record
			f.updateErr = errors.New("registry write failed")
			f.stepUntil(claudeNativePromptRefreshInterval)
			events, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("journal = %+v, want exactly one event", events)
			}
			got := events[0]
			if got.Event != "ai.ingest.outcome" || got.Provider != string(diagnostics.ProviderClaude) || got.AIKind != string(tc.want) ||
				got.AIResult != string(diagnostics.AIResultFailed) || got.Failure != string(diagnostics.AIFailureRoute) || got.Level != "error" {
				t.Fatalf("journal event = %+v, want claude %s failed/route-failed", got, tc.want)
			}
		})
	}
}

// The watcher stops asking once a step reports that the refresh can never
// apply to this activation -- the loaded snapshot is outside the binding fence:
// the Agent is gone, is not a Claude Agent, is not Running on this Pane, or
// the Pane's activation is not this one -- and such a step records nothing and
// reads no transcript although the raise is due. It keeps asking on a failed
// load or any interaction that is not due.
func TestClaudeNativePromptRefreshStopsOnlyWhenItCanNeverApply(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *nativePromptFixture)
		keep  bool
	}{
		{name: "provider not claude", setup: func(_ *testing.T, f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Spec.Provider = aiModeCodex
		}},
		{name: "agent gone", setup: func(_ *testing.T, f *nativePromptFixture) { f.spec.AgentUID = "agt-gone"; f.refresh.spec = f.spec }},
		{name: "phase not running", setup: func(_ *testing.T, f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Status.Phase = coremetadata.PhaseOffline
		}},
		{name: "pane ref differs", setup: func(_ *testing.T, f *nativePromptFixture) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Status.PaneRef = "pan-other"
		}},
		{name: "pane generation differs", setup: func(_ *testing.T, f *nativePromptFixture) {
			pane, _ := f.registry.Pane(f.spec.PaneUID)
			pane.Status.Activation.Generation = "gen-other"
		}},
		{name: "activation agent differs", setup: func(_ *testing.T, f *nativePromptFixture) {
			pane, _ := f.registry.Pane(f.spec.PaneUID)
			pane.Status.Activation.AgentUID = "agt-other"
		}},
		{name: "load error", keep: true, setup: func(_ *testing.T, f *nativePromptFixture) {
			f.refresh.load = func() (coremetadata.Registry, error) {
				return coremetadata.Registry{}, errors.New("registry read failed")
			}
		}},
		{name: "not due", keep: true, setup: func(_ *testing.T, f *nativePromptFixture) { f.now = f.now.Add(-claudeNativePromptRefreshInterval) }},
		{name: "other kind", keep: true, setup: func(t *testing.T, f *nativePromptFixture) {
			f.setInteraction(t, coremetadata.InteractionInProgress)
			f.now = f.now.Add(-claudeNativePromptRefreshInterval)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativePromptFixture(t, coremetadata.InteractionApprovalRequired)
			f.now = f.now.Add(claudeNativePromptRefreshInterval + time.Minute)
			tc.setup(t, f)
			if got := f.refresh.step(); got != tc.keep {
				t.Fatalf("step() = %v, want %v", got, tc.keep)
			}
			if f.registryWrites != 0 || len(f.events.events) != 0 || f.transcriptReads != 0 {
				t.Fatalf("writes=%d diagnostics=%+v tail reads=%d, want none", f.registryWrites, f.events.events, f.transcriptReads)
			}
		})
	}
}

// A binding that changes between the snapshot and the commit of an open
// dialog's recommit is recorded exactly once and stops the refresh, since it
// never comes back within the activation; a transient Registry error is
// recorded and retried on the next check; an observation another writer
// replaced is neither recorded nor a reason to stop.
func TestClaudeNativePromptRefreshStopsOnACommitTimeBindingChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(f *nativePromptFixture)
		keep   bool
		record bool
	}{
		{name: "binding changed at commit", record: true, setup: func(f *nativePromptFixture) {
			f.beforeUpdate = func(working *coremetadata.Registry) {
				pane, _ := working.Pane(f.spec.PaneUID)
				pane.Status.Activation.Generation = "gen-next"
			}
		}},
		{name: "transient registry error", keep: true, record: true, setup: func(f *nativePromptFixture) {
			f.updateErr = errors.New("registry write failed")
		}},
		{name: "interaction changed", keep: true, setup: func(f *nativePromptFixture) {
			f.beforeUpdate = func(working *coremetadata.Registry) {
				agent, _ := working.Agent(f.claudeUID)
				agent.Status.Interaction.ObservedAt = agent.Status.Interaction.ObservedAt.Add(time.Second)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativePromptFixture(t, coremetadata.InteractionApprovalRequired)
			tc.setup(f)
			f.now = f.now.Add(claudeNativePromptRefreshInterval + time.Minute)
			if got := f.refresh.step(); got != tc.keep {
				t.Fatalf("step() = %v, want %v", got, tc.keep)
			}
			if f.registryWrites != 0 || f.transcriptReads != 1 {
				t.Fatalf("writes=%d tail reads=%d, want no write after one judged read", f.registryWrites, f.transcriptReads)
			}
			want := 0
			if tc.record {
				want = 1
			}
			if len(f.events.events) != want {
				t.Fatalf("diagnostics = %+v, want %d", f.events.events, want)
			}
		})
	}
}
