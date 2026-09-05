package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// causeCodexLifecycleConnection is a lifecycle connection that reports why its
// notification stream closed, the way the broker epoch does.
type causeCodexLifecycleConnection struct {
	*fakeCodexLifecycleConnection
	cause codexObserverReason
}

func (c *causeCodexLifecycleConnection) NotificationsClosedCause() codexObserverReason {
	return c.cause
}

type recordingCodexObserverJournal struct {
	mu      sync.Mutex
	entries []aiIngestLogEntry
}

func (j *recordingCodexObserverJournal) append(entry aiIngestLogEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, entry)
}

func (j *recordingCodexObserverJournal) snapshot() []aiIngestLogEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]aiIngestLogEntry(nil), j.entries...)
}

func newTestCodexObserverSnapshot(threadID string) codexappserver.LifecycleSnapshot {
	return codexappserver.LifecycleSnapshot{ThreadID: threadID, ThreadState: codexappserver.ThreadStateActive}
}

// TestCodexObserverEventLoopExitsCarryTheirOwnReasonToken is the acceptance
// surface for "no exit path falls into a not-recorded default".
//
// Before this mapping existed the loop entered with the literal `disconnected`
// and only five of its exits ever overwrote it, so a cancelled observer, a
// closed notification stream, and a real endpoint disconnect all published the
// same word. The `want` column is therefore one token per path, and the
// `authorities` column pins that refining the token did not move the recovery
// strategy: a closed stream still holds, and everything else still falls back.
func TestCodexObserverEventLoopExitsCarryTheirOwnReasonToken(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	ready := codexAuthorityControlPlane + ":" + string(codexObserverReasonReady)
	for _, test := range []struct {
		name string
		// cause is the stream close cause the connection reports; the empty
		// reason means the connection reports none.
		cause codexObserverReason
		// notification, when set, is delivered instead of closing the stream.
		notification *codexappserver.Notification
		cancel       bool
		want         codexObserverReason
		authorities  []string
	}{
		{
			name: "cancelled observer",
			// ctx cancellation is the observer going away, never an endpoint
			// that disconnected, and it no longer borrows that word.
			cancel: true, want: codexObserverReasonCancelled,
			authorities: []string{ready, codexAuthorityInvalidating + ":observer-cancelled", codexAuthorityHook + ":observer-cancelled"},
		},
		{
			name: "stream closed with no recorded cause",
			// Still distinct from both a cancelled observer and an endpoint
			// disconnect: it says the stream ended and nothing named why.
			want:        codexObserverReasonStreamClosed,
			authorities: []string{ready, codexAuthorityInvalidating + ":stream-closed"},
		},
		{
			name: "upstream connection suspended", cause: codexObserverReasonEndpointSuspended,
			want:        codexObserverReasonEndpointSuspended,
			authorities: []string{ready, codexAuthorityInvalidating + ":endpoint-suspended"},
		},
		{
			name: "broker rotated in a replacement epoch", cause: codexObserverReasonEpochRotated,
			want:        codexObserverReasonEpochRotated,
			authorities: []string{ready, codexAuthorityInvalidating + ":epoch-rotated"},
		},
		{
			name: "broker revoked the binding", cause: codexObserverReasonBindingRevoked,
			want:        codexObserverReasonBindingRevoked,
			authorities: []string{ready, codexAuthorityInvalidating + ":binding-revoked"},
		},
		{
			name: "consumer fell behind the backlog", cause: codexObserverReasonBacklogOverflow,
			want:        codexObserverReasonBacklogOverflow,
			authorities: []string{ready, codexAuthorityInvalidating + ":backlog-overflow"},
		},
		{
			name: "observer closed the connection", cause: codexObserverReasonEpochClosed,
			want:        codexObserverReasonEpochClosed,
			authorities: []string{ready, codexAuthorityInvalidating + ":epoch-closed"},
		},
		{
			name:         "undecodable lifecycle frame",
			notification: &codexappserver.Notification{Method: "thread/status/changed", Params: []byte("{")},
			want:         codexObserverReasonProtocolError,
			authorities:  []string{ready, codexAuthorityInvalidating + ":protocol-error", codexAuthorityHook + ":protocol-error"},
		},
		{
			name: "bound thread unloaded",
			notification: &codexappserver.Notification{
				Method: "thread/status/changed",
				Params: []byte(`{"threadId":"thread-1","status":{"type":"notLoaded"}}`),
			},
			want:        codexObserverReasonThreadUnloaded,
			authorities: []string{ready, codexAuthorityInvalidating + ":thread-unloaded", codexAuthorityHook + ":thread-unloaded"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := &fakeCodexLifecycleConnection{
				snapshot: newTestCodexObserverSnapshot(identity.ThreadID),
				events:   make(chan codexappserver.Notification, 1),
			}
			connection := codexLifecycleConnection(base)
			if test.cause != "" {
				connection = &causeCodexLifecycleConnection{fakeCodexLifecycleConnection: base, cause: test.cause}
			}
			sink := newRecordingCodexLifecycleSink()
			journal := &recordingCodexObserverJournal{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			readyEpoch := make(chan codexObserverStartupResult, 4)
			observer := codexNativeObserver{
				identity: identity, sink: sink,
				open:          func(context.Context) (codexLifecycleConnection, error) { return connection, nil },
				transitions:   newCodexObserverLogJournal(journal.append, func() time.Time { return time.Unix(0, 0).UTC() }),
				reportStartup: func(result codexObserverStartupResult) { readyEpoch <- result },
				// One recovery decision, refused, so each case exercises exactly
				// one epoch and one exit.
				waitRecovery: func(context.Context, time.Duration) bool { return false },
			}
			done := make(chan error, 1)
			go func() { done <- observer.Run(ctx) }()
			select {
			case result := <-readyEpoch:
				if result.Status != codexObserverStartupReady {
					t.Fatalf("first startup result = %+v, want ready", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("observer never reached a ready epoch")
			}
			switch {
			case test.cancel:
				cancel()
			case test.notification != nil:
				base.events <- *test.notification
			default:
				close(base.events)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("observer did not finish its transition")
			}
			if got := sink.authoritySnapshot(); !reflect.DeepEqual(got, test.authorities) {
				t.Fatalf("authority writes = %v, want %v", got, test.authorities)
			}
			reasons := journalReasons(journal.snapshot(), codexObserverTransitionDisconnected, codexObserverTransitionFallback)
			if len(reasons) == 0 {
				t.Fatal("no observer transition record named the exit")
			}
			for _, reason := range reasons {
				if reason != string(test.want) {
					t.Fatalf("record reason = %q, want %q", reason, test.want)
				}
			}
		})
	}
}

func journalReasons(entries []aiIngestLogEntry, kinds ...codexObserverTransition) []string {
	reasons := []string{}
	for _, entry := range entries {
		for _, kind := range kinds {
			if entry.Event == string(kind) {
				reasons = append(reasons, entry.Reason)
			}
		}
	}
	return reasons
}

// TestCodexObserverExitPathsAreOneToOneWithTokens keeps the mapping total.
//
// The table above proves each path publishes its token. This proves the
// converse property the contract actually needs: the tokens are distinct, the
// two buckets that mean "not an observation" are never produced by an exit,
// and every exit stores its recovery strategy rather than deriving it from the
// reason it publishes.
func TestCodexObserverExitPathsAreOneToOneWithTokens(t *testing.T) {
	exits := map[string]codexObserverExit{
		"ctx cancelled":    codexObserverExitCancelled,
		"protocol error":   codexObserverExitProtocolError,
		"thread unloaded":  codexObserverExitThreadUnloaded,
		"stream no cause":  codexObserverStreamExit(""),
		"endpoint suspend": codexObserverStreamExit(codexObserverReasonEndpointSuspended),
		"epoch rotated":    codexObserverStreamExit(codexObserverReasonEpochRotated),
		"binding revoked":  codexObserverStreamExit(codexObserverReasonBindingRevoked),
		"backlog overflow": codexObserverStreamExit(codexObserverReasonBacklogOverflow),
		"epoch closed":     codexObserverStreamExit(codexObserverReasonEpochClosed),
	}
	seen := map[codexObserverReason]string{}
	for name, exit := range exits {
		if codexObserverReasonFor(string(exit.reason)) == "" {
			t.Fatalf("exit %q publishes %q, which is outside the vocabulary", name, exit.reason)
		}
		if exit.reason == codexObserverReasonUnrecorded || exit.reason == codexObserverReasonRetired {
			t.Fatalf("exit %q fell into the %q bucket instead of naming its path", name, exit.reason)
		}
		if other, clash := seen[exit.reason]; clash {
			t.Fatalf("exits %q and %q share the token %q", name, other, exit.reason)
		}
		seen[exit.reason] = name
	}
	// Holding is the closed-stream strategy and only the closed-stream
	// strategy. This is the pre-change behavior, restated as a decision the
	// exit carries instead of a comparison against the reason string.
	for name, exit := range exits {
		wantHold := strings.HasPrefix(name, "stream") || strings.HasPrefix(name, "endpoint") ||
			strings.HasPrefix(name, "epoch") || strings.HasPrefix(name, "binding") || strings.HasPrefix(name, "backlog")
		if exit.hold != wantHold {
			t.Fatalf("exit %q hold = %t, want %t", name, exit.hold, wantHold)
		}
	}
	if !codexObserverExitCancelled.stopping {
		t.Fatal("the cancellation exit must stop the observer")
	}
}

// TestCodexObserverReasonVocabularyIsClosed keeps one vocabulary, one owner.
func TestCodexObserverReasonVocabularyIsClosed(t *testing.T) {
	seen := map[codexObserverReason]bool{}
	for _, reason := range codexObserverReasons {
		if seen[reason] {
			t.Fatalf("token %q appears twice in the vocabulary", reason)
		}
		seen[reason] = true
		if got := safeCodexAuthorityReason(string(reason)); got != string(reason) {
			t.Fatalf("safeCodexAuthorityReason(%q) = %q, want the token itself", reason, got)
		}
		if got := codexObserverReasonFor(" " + string(reason) + " "); got != reason {
			t.Fatalf("codexObserverReasonFor trimmed %q to %q", reason, got)
		}
	}
	for _, foreign := range []string{"", "disconnected-ish", "PRIVATE-PROMPT", "/home/user/secret"} {
		if got := codexObserverReasonFor(foreign); got != "" {
			t.Fatalf("codexObserverReasonFor(%q) admitted %q", foreign, got)
		}
		if got := safeCodexAuthorityReason(foreign); got != "bounded reason unavailable" {
			t.Fatalf("safeCodexAuthorityReason(%q) = %q", foreign, got)
		}
	}
	// The four open-failure tokens are members of the one vocabulary, not a
	// second set beside it.
	for _, reason := range []codexObserverReason{
		codexObserverReasonUnsupported, codexObserverReasonProtocolError,
		codexObserverReasonTimeout, codexObserverReasonUnavailable,
	} {
		if !seen[reason] {
			t.Fatalf("codexNativeReason token %q is outside the vocabulary", reason)
		}
	}
}

// TestCodexBrokerEpochRecordsWhyItsStreamClosed is where the observer's answer
// to "who closed it" comes from.
//
// A closed channel looks identical from above no matter who closed it, so the
// cause has to be recorded at the call that closes it. These are all four of
// those calls.
func TestCodexBrokerEpochRecordsWhyItsStreamClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		// close ends the epoch the way one production call site does.
		close func(*codexBrokerObserverSession, *codexBrokerLifecycleEpoch)
		want  codexObserverReason
	}{
		{
			name: "upstream connection suspended",
			close: func(s *codexBrokerObserverSession, _ *codexBrokerLifecycleEpoch) {
				s.retire(make(chan codexBrokerEpochRecord, 1))
			},
			want: codexObserverReasonEndpointSuspended,
		},
		{
			name: "replacement barrier closed",
			close: func(s *codexBrokerObserverSession, _ *codexBrokerLifecycleEpoch) {
				s.rotate(codexBrokerEpochRecord{}, make(chan codexBrokerEpochRecord, 1))
			},
			want: codexObserverReasonEpochRotated,
		},
		{
			name: "binding revoked by the runtime",
			close: func(s *codexBrokerObserverSession, _ *codexBrokerLifecycleEpoch) {
				s.endAfterStreamRevoked(nil)
			},
			want: codexObserverReasonBindingRevoked,
		},
		{
			name: "observer closed the connection",
			close: func(_ *codexBrokerObserverSession, e *codexBrokerLifecycleEpoch) {
				_ = e.Close()
			},
			want: codexObserverReasonEpochClosed,
		},
		{
			name: "consumer fell a full backlog behind",
			close: func(_ *codexBrokerObserverSession, e *codexBrokerLifecycleEpoch) {
				e.deliver(codexbroker.Event{Method: "thread/status/changed"})
			},
			want: codexObserverReasonBacklogOverflow,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &codexBrokerObserverSession{}
			epoch := &codexBrokerLifecycleEpoch{
				session: session,
				// A zero-capacity stream makes the overflow case deterministic
				// and leaves every other case unaffected.
				notifications: make(chan codexappserver.Notification),
				leases:        map[string]codexbroker.ApprovalLease{},
			}
			session.current = epoch
			if got := epoch.NotificationsClosedCause(); got != "" {
				t.Fatalf("a live epoch reported the close cause %q", got)
			}
			test.close(session, epoch)
			if got := epoch.NotificationsClosedCause(); got != test.want {
				t.Fatalf("close cause = %q, want %q", got, test.want)
			}
			if _, open := <-epoch.notifications; open {
				t.Fatal("the notification stream stayed open after the epoch ended")
			}
			// The first close owns the cause; a later one cannot rewrite it.
			epoch.end(codexObserverReasonUnrecorded)
			if got := epoch.NotificationsClosedCause(); got != test.want {
				t.Fatalf("a second close rewrote the cause to %q", got)
			}
		})
	}
}

// TestCodexObserverJournalRecordsConnectDisconnectReconnectInOrder is the
// history the pane option cannot hold.
func TestCodexObserverJournalRecordsConnectDisconnectReconnectInOrder(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	base := &fakeCodexLifecycleConnection{
		snapshot: newTestCodexObserverSnapshot(identity.ThreadID),
		events:   make(chan codexappserver.Notification, 1),
	}
	connection := &causeCodexLifecycleConnection{
		fakeCodexLifecycleConnection: base, cause: codexObserverReasonEndpointSuspended,
	}
	sink := newRecordingCodexLifecycleSink()
	journal := &recordingCodexObserverJournal{}
	reported := make(chan codexObserverStartupResult, 4)
	ctx := t.Context()
	observer := codexNativeObserver{
		identity: identity, sink: sink,
		open:          func(context.Context) (codexLifecycleConnection, error) { return connection, nil },
		transitions:   newCodexObserverLogJournal(journal.append, func() time.Time { return time.Unix(0, 0).UTC() }),
		reportStartup: func(result codexObserverStartupResult) { reported <- result },
		waitRecovery:  func(context.Context, time.Duration) bool { return false },
	}
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("observer never reached a ready epoch")
	}
	close(base.events)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not finish its transition")
	}
	entries := journal.snapshot()
	wantOrder := []struct {
		event  codexObserverTransition
		result string
		reason codexObserverReason
	}{
		{codexObserverTransitionConnected, codexAuthorityControlPlane, codexObserverReasonReady},
		{codexObserverTransitionDisconnected, codexAuthorityInvalidating, codexObserverReasonEndpointSuspended},
		{codexObserverTransitionReconnecting, "retry", codexObserverReasonEndpointSuspended},
	}
	if len(entries) != len(wantOrder) {
		t.Fatalf("observer records = %+v, want %d transitions", entries, len(wantOrder))
	}
	for index, want := range wantOrder {
		entry := entries[index]
		if entry.Event != string(want.event) || entry.Result != want.result || entry.Reason != string(want.reason) {
			t.Fatalf("record %d = %+v, want %s/%s/%s", index, entry, want.event, want.result, want.reason)
		}
		if entry.Source != aiIngestCodexObserverSource || entry.Pane != identity.RuntimeID ||
			entry.ThreadID != identity.ThreadID || entry.Epoch == "" {
			t.Fatalf("record %d lost its routing identity: %+v", index, entry)
		}
	}
	// No fallback record: a closed stream holds, so provider-hook was never
	// published, and the absence of that record is how an operator reads it.
	if reasons := journalReasons(entries, codexObserverTransitionFallback); len(reasons) != 0 {
		t.Fatalf("a held disconnect published a fallback record: %v", reasons)
	}
}

// TestCodexObserverJournalRecordFieldsAreWhitelisted is the negative audit.
//
// The record is written into a log an operator reads and shares. Every column
// it may carry is named here, so adding a field that could hold provider
// content or a path fails this test before it reaches a log.
func TestCodexObserverJournalRecordFieldsAreWhitelisted(t *testing.T) {
	journal := &recordingCodexObserverJournal{}
	writer := newCodexObserverLogJournal(journal.append, func() time.Time { return time.Unix(0, 0).UTC() })
	identity := codexLifecycleIdentity{
		AgentUID: "agent-1", PaneUID: "pane-1", RuntimeID: "%9",
		Generation: "generation-1", ThreadID: "thread-1",
	}
	writer.RecordObserverTransition(identity, codexObserverTransitionDisconnected, "4242-7", codexObserverReasonEndpointSuspended)
	entries := journal.snapshot()
	if len(entries) != 1 {
		t.Fatalf("records = %+v, want exactly one", entries)
	}
	data, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"at": true, "source": true, "event": true, "result": true,
		"reason": true, "pane": true, "thread_id": true, "epoch": true, "repeat": true,
	}
	for field := range fields {
		if !allowed[field] {
			t.Fatalf("observer record carried the unlisted field %q: %s", field, data)
		}
	}
	for _, forbidden := range []string{"cwd", "prompt", "text", "message", "token", "credential", "rollout", "command", "path"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("observer record leaked %q: %s", forbidden, data)
		}
	}
	// A reason outside the vocabulary is named as unrecorded rather than
	// carried through into the log.
	writer.RecordObserverTransition(identity, codexObserverTransitionConnected, "4242-7", codexObserverReason("PRIVATE-CAUSE"))
	entries = journal.snapshot()
	if last := entries[len(entries)-1]; last.Reason != string(codexObserverReasonUnrecorded) {
		t.Fatalf("out-of-vocabulary reason reached the log as %q", last.Reason)
	}
	// An unnamed transition is refused outright.
	writer.RecordObserverTransition(identity, codexObserverTransition("observer.invented"), "", codexObserverReasonReady)
	if got := len(journal.snapshot()); got != 2 {
		t.Fatalf("records = %d, want the invented transition refused", got)
	}
}

// TestCodexObserverJournalCoalescesRepeatedTransitions bounds the write rate.
//
// A flapping observer reconnects about once a second and this sink is shared
// with every provider hook, so an unbounded writer would evict hook history.
// Coalescing keeps the evidence: the suppressed transitions are counted and
// the count rides the next record of the same pair.
func TestCodexObserverJournalCoalescesRepeatedTransitions(t *testing.T) {
	journal := &recordingCodexObserverJournal{}
	now := time.Unix(1700000000, 0).UTC()
	writer := newCodexObserverLogJournal(journal.append, func() time.Time { return now })
	writer.window = 5 * time.Second
	identity := testCodexLifecycleIdentity()
	flap := func() {
		writer.RecordObserverTransition(identity, codexObserverTransitionConnected, "1-1", codexObserverReasonReady)
		writer.RecordObserverTransition(identity, codexObserverTransitionDisconnected, "1-1", codexObserverReasonEndpointSuspended)
	}
	flap()
	for range 4 {
		now = now.Add(time.Second)
		flap()
	}
	if got := len(journal.snapshot()); got != 2 {
		t.Fatalf("records inside one window = %d, want the first pair only", got)
	}
	now = now.Add(5 * time.Second)
	flap()
	entries := journal.snapshot()
	if len(entries) != 4 {
		t.Fatalf("records = %d, want one more pair after the window", len(entries))
	}
	for _, entry := range entries[2:] {
		if entry.Repeat != 4 {
			t.Fatalf("coalesced record %+v lost its suppressed count", entry)
		}
	}
	// A different pair is new information and is never delayed behind another
	// pair's window.
	writer.RecordObserverTransition(identity, codexObserverTransitionFallback, "1-1", codexObserverReasonProtocolError)
	entries = journal.snapshot()
	if last := entries[len(entries)-1]; last.Event != string(codexObserverTransitionFallback) || last.Repeat != 0 {
		t.Fatalf("a distinct transition was delayed or counted: %+v", last)
	}
}

// TestManagedCodexAuthorityDoctorSeparatesFlappingFrozenAndStopped is the
// operator-facing half of the instrumentation.
//
// The source counts alone cannot tell those three apart: every one of them
// sits in the invalidating bucket. What differs is the token that put it
// there, so the census carries the distribution and doctor renders it.
func TestManagedCodexAuthorityDoctorSeparatesFlappingFrozenAndStopped(t *testing.T) {
	diagnostics := map[string]codexLifecycleAuthorityDiagnostic{
		// Reconnecting on the broker's backoff: the upstream connection keeps
		// going away underneath a live binding.
		"pane-flapping": {Source: codexAuthorityInvalidating, Reason: string(codexObserverReasonEndpointSuspended), EpochStatus: "active"},
		// Never advanced past its first epoch: still on the control plane and
		// still reporting the epoch it opened with.
		"pane-frozen": {Source: codexAuthorityControlPlane, Reason: string(codexObserverReasonReady), EpochStatus: "active"},
		// Stopped: the observer process itself went away.
		"pane-stopped": {Source: codexAuthorityHook, Reason: string(codexObserverReasonObserverExited), EpochStatus: "inactive"},
	}
	registry := coremetadata.Registry{}
	for pane := range diagnostics {
		registry.Agents = append(registry.Agents, coremetadata.Agent{
			Metadata: coremetadata.ObjectMeta{UID: "agent-" + pane},
			Spec:     coremetadata.AgentSpec{Provider: aiModeCodex},
			Status:   coremetadata.AgentStatus{PaneRef: pane},
		})
	}
	census := censusCodexLifecycleAuthority(registry, func(paneUID string) codexLifecycleAuthorityDiagnostic {
		return diagnostics[paneUID]
	})
	want := []codexAuthorityReasonCount{
		{Reason: string(codexObserverReasonEndpointSuspended), Count: 1},
		{Reason: string(codexObserverReasonObserverExited), Count: 1},
		{Reason: string(codexObserverReasonReady), Count: 1},
	}
	if !reflect.DeepEqual(census.Reasons, want) {
		t.Fatalf("reason distribution = %+v, want %+v", census.Reasons, want)
	}
	var doctor bytes.Buffer
	writeDoctorCodexAuthorityText(&doctor, &census)
	rendered := doctor.String()
	for _, expected := range []string{
		"Reasons: ",
		string(codexObserverReasonEndpointSuspended) + "=1",
		string(codexObserverReasonObserverExited) + "=1",
		string(codexObserverReasonReady) + "=1",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("doctor output missing %q:\n%s", expected, rendered)
		}
	}
	// The census names no Agent and no Pane. It stays a count-only projection.
	for pane := range diagnostics {
		if strings.Contains(rendered, pane) {
			t.Fatalf("doctor output named the Pane %q:\n%s", pane, rendered)
		}
	}
	// A census with no reasons renders no Reasons line at all, so an empty
	// distribution is not confused with an unrendered one.
	var empty bytes.Buffer
	writeDoctorCodexAuthorityText(&empty, &codexAuthorityCensus{Agents: 1})
	if strings.Contains(empty.String(), "Reasons:") {
		t.Fatalf("an empty distribution rendered a Reasons line:\n%s", empty.String())
	}
}
