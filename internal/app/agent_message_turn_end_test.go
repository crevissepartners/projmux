package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const holdTranscriptPath = "/claude/projects/app/session-hold.jsonl"

func sumHoldWaits(waits []time.Duration) time.Duration {
	var sum time.Duration
	for _, wait := range waits {
		sum += wait
	}
	return sum
}

// claudeStamp formats a time the way Claude Code stamps transcript lines.
func claudeStamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// The fixture lines below copy the key shape of a Claude Code 2.1.277
// transcript; their content is placeholder text.

func claudeTurnDurationLine(stamp string) string {
	return `{"parentUuid":"p-1","isSidechain":false,"type":"system","subtype":"turn_duration","durationMs":3111,` +
		`"messageCount":17,"timestamp":"` + stamp + `","uuid":"u-1","isMeta":false,"userType":"external",` +
		`"entrypoint":"cli","cwd":"/src/app","sessionId":"session-hold","version":"2.1.277","gitBranch":"HEAD"}`
}

func claudeToolUseLine(at time.Time) string {
	return `{"type":"assistant","timestamp":"` + claudeStamp(at) + `","message":{"role":"assistant","content":` +
		`[{"type":"text","text":"placeholder"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"true"}}]},` +
		`"sessionId":"session-hold"}`
}

func claudeAssistantTextLine(at time.Time) string {
	return `{"type":"assistant","timestamp":"` + claudeStamp(at) + `","message":{"role":"assistant","content":` +
		`[{"type":"text","text":"placeholder"}]},"sessionId":"session-hold"}`
}

// claudeDenialLines are the two lines Claude writes after its operator denies
// a permission dialog; no hook follows them.
func claudeDenialLines(at time.Time) []string {
	return []string{
		`{"type":"user","timestamp":"` + claudeStamp(at) + `","toolDenialKind":"user-rejected",` +
			`"toolUseResult":"User rejected tool use","message":{"role":"user","content":` +
			`[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"placeholder"}]},"sessionId":"session-hold"}`,
		`{"type":"user","timestamp":"` + claudeStamp(at) + `","message":{"role":"user","content":` +
			`[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
	}
}

// claudeUntimestampedLines are lines Claude writes anywhere, also after a
// turn_duration, without a timestamp, plus two timestamped non-turn lines.
func claudeUntimestampedLines(at time.Time) []string {
	return []string{
		`{"type":"last-prompt","lastPrompt":"placeholder","sessionId":"session-hold"}`,
		`{"type":"ai-title","aiTitle":"placeholder","sessionId":"session-hold"}`,
		`{"type":"mode","mode":"default","sessionId":"session-hold"}`,
		`{"type":"permission-mode","permissionMode":"default","sessionId":"session-hold"}`,
		`{"type":"atis-latch","sessionId":"session-hold"}`,
		`{"type":"file-history-snapshot","messageId":"m-1","snapshot":{"trackedFileBackups":{}},"isSnapshotUpdate":false}`,
		`{"type":"cost-state","sessionId":"session-hold"}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"` + claudeStamp(at) + `","sessionId":"session-hold"}`,
		`{"type":"attachment","timestamp":"` + claudeStamp(at) + `","attachment":{"type":"placeholder"},"sessionId":"session-hold"}`,
	}
}

func claudeTranscript(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

// claudeDeniedTurn is a transcript whose last turn raised a dialog at
// toolAt, was denied at deniedAt, and ended with a turn_duration at endedAt.
func claudeDeniedTurn(before, toolAt, deniedAt, endedAt time.Time) []string {
	lines := []string{claudeTurnDurationLine(claudeStamp(before)), claudeToolUseLine(toolAt)}
	lines = append(lines, claudeDenialLines(deniedAt)...)
	return append(lines, claudeTurnDurationLine(claudeStamp(endedAt)))
}

// bindTranscript records a Claude conversation with a transcript path on the
// fixture Agent, the way the hook ingest binds it.
func (f *holdFixture) bindTranscript(t *testing.T, path string) {
	t.Helper()
	agent, ok := f.registry.Agent(f.claudeUID)
	if !ok {
		t.Fatal("claude agent fixture missing")
	}
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{Provider: "claude", ObservedAt: f.now,
		Claude: &coremetadata.ClaudeSessionRef{SessionID: "session-hold", TranscriptPath: path}}
}

func (f *holdFixture) interaction(t *testing.T) coremetadata.AgentInteraction {
	t.Helper()
	agent, ok := f.registry.Agent(f.claudeUID)
	if !ok {
		t.Fatal("claude agent fixture missing")
	}
	return agent.Status.Interaction
}

// runLaunchedReleases runs, one after another, the releases the sends
// launched, the way the detached processes would.
func (f *holdFixture) runLaunchedReleases(t *testing.T) {
	t.Helper()
	for _, uid := range f.launches {
		if err := f.cmd.releaseHeldMessages(uid); err != nil {
			t.Fatal(err)
		}
	}
}

func wantWatchWaits(t *testing.T, waits []time.Duration) {
	t.Helper()
	for _, wait := range waits {
		if wait <= 0 || wait > agentMessageReleaseWatchInterval {
			t.Fatalf("waits = %v, want every wait in (0, %v]", waits, agentMessageReleaseWatchInterval)
		}
	}
}

// Acceptance 1: a permission denied while messages are held sends no hook. The
// release the hold launched watches the target, sees the turn end in the
// transcript, records response_complete, and delivers every held record once,
// in acceptance order.
func TestAgentMessageReleaseDeliversOnceADeniedPermissionEndsTheTurn(t *testing.T) {
	f := newHoldFixture(t)
	f.installFakeSleep()
	blockedAt := f.now
	f.bindTranscript(t, holdTranscriptPath)
	f.setInteraction(t, coremetadata.InteractionApprovalRequired)
	f.transcript = claudeTranscript(claudeTurnDurationLine(claudeStamp(blockedAt.Add(-time.Minute))),
		claudeToolUseLine(blockedAt.Add(-time.Second)))

	stdout, err := f.send(t, "message-denied-first", false)
	wantHeldReceipt(t, stdout, err, "message-denied-first")
	f.now = f.now.Add(time.Second)
	stdout, err = f.send(t, "message-denied-second", false)
	wantHeldReceipt(t, stdout, err, "message-denied-second")
	if len(f.adapter.submits) != 0 {
		t.Fatalf("submits = %v while the dialog is open", f.adapter.submits)
	}

	// The operator denies the dialog during the third wait.
	waits := 0
	f.onWait = func() {
		waits++
		if waits == 3 {
			lines := claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt.Add(-time.Second), f.now, f.now)
			f.transcript = claudeTranscript(lines...)
		}
	}
	if len(f.launches) == 0 {
		t.Fatal("no release was launched for a blocked hold")
	}
	if err := f.cmd.releaseHeldMessages(f.launches[0]); err != nil {
		t.Fatal(err)
	}
	want := []string{"message-denied-first", "message-denied-second"}
	if !slices.Equal(f.adapter.submits, want) {
		t.Fatalf("the first release pushed %v, want %v", f.adapter.submits, want)
	}
	for _, ref := range want {
		if got := f.delivery(t, ref); got.State != coremessage.StateDelivered {
			t.Fatalf("%s = %+v, want delivered", ref, got)
		}
	}
	got := f.interaction(t)
	if got.Kind != coremetadata.InteractionResponseComplete || got.Source != string(coremetadata.InteractionSourceProviderHook) ||
		!got.ObservedAt.Equal(f.now) || f.registryWrites != 1 {
		t.Fatalf("interaction = %+v writes=%d, want one provider-hook response_complete at %v", got, f.registryWrites, f.now)
	}
	if len(f.waits) != 3 {
		t.Fatalf("waits = %v, want delivery right after the turn end appeared", f.waits)
	}
	wantWatchWaits(t, f.waits)
	// Every other launched release finds nothing left to push.
	f.runLaunchedReleases(t)
	if !slices.Equal(f.adapter.submits, want) || f.registryWrites != 1 {
		t.Fatalf("after every release submits=%v writes=%d, want %v once and one write", f.adapter.submits, f.registryWrites, want)
	}
}

// Acceptance 2: while the dialog stays open the release watches at the fixed
// interval and ends with the record held when its window ends.
func TestAgentMessageReleaseWatchesAnOpenDialogUntilTheWindowEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		later  time.Duration // deadline of a later held record; 0 means none
		window time.Duration
	}{
		{name: "ten minutes", window: agentMessageReleaseRetryWindow},
		{name: "earliest held deadline", later: 3 * time.Minute, window: 3 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHoldFixture(t)
			f.installFakeSleep()
			blockedAt := f.now
			f.bindTranscript(t, holdTranscriptPath)
			f.setInteraction(t, coremetadata.InteractionApprovalRequired)
			f.transcript = claudeTranscript(claudeTurnDurationLine(claudeStamp(blockedAt.Add(-time.Minute))),
				claudeToolUseLine(blockedAt.Add(-time.Second)))
			const ref = "message-dialog-open"
			f.putHeld(t, ref, f.now.Add(-2*time.Second), f.now.Add(time.Hour))
			if tc.later > 0 {
				f.putHeld(t, "message-dialog-open-later", f.now.Add(-time.Second), f.now.Add(tc.later))
			}
			start := f.now
			if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
				t.Fatal(err)
			}
			if got := f.delivery(t, ref); got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 {
				t.Fatalf("record = %+v submits=%v, want still held and no push", got, f.adapter.submits)
			}
			wantWatchWaits(t, f.waits)
			if got := sumHoldWaits(f.waits); got != tc.window || f.now.Sub(start) != tc.window {
				t.Fatalf("watched %v (clock +%v), want exactly %v", got, f.now.Sub(start), tc.window)
			}
			// One look before each wait; the look after the last wait is
			// past the window.
			if f.transcriptReads != len(f.waits) || f.registryWrites != 0 {
				t.Fatalf("transcript reads=%d writes=%d over %d waits, want one read per look and no write",
					f.transcriptReads, f.registryWrites, len(f.waits))
			}
		})
	}
}

// Acceptance 3: every doubt keeps the record held with no write; untimestamped
// lines and a dropped partial first line do not.
func TestAgentMessageReleaseTranscriptJudgment(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	largeDir := t.TempDir()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *holdFixture, blockedAt time.Time)
		deliver bool
	}{
		{name: "no session ref", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			agent, _ := f.registry.Agent(f.claudeUID)
			agent.Status.SessionRef = nil
			f.transcript = claudeTranscript(claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))...)
		}},
		{name: "empty transcript path", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, "")
			f.transcript = claudeTranscript(claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))...)
		}},
		{name: "read error", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			f.transcriptErr = errors.New("permission denied")
		}},
		{name: "missing file", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, missing)
			f.cmd.messageTranscriptTail = nil
		}},
		{name: "broken line", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			lines := claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))
			f.transcript = claudeTranscript(append(lines, `{"type":"system","subtype":"turn_dur`)...)
		}},
		{name: "turn end at the blocked observation", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			// Millisecond stamp of the same instant: not after the ns observation.
			f.transcript = claudeTranscript(claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt)...)
		}},
		{name: "turn end before the blocked observation", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			f.transcript = claudeTranscript(claudeTurnDurationLine(claudeStamp(blockedAt.Add(-time.Second))))
		}},
		{name: "tool_use after the turn end", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			lines := claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))
			f.transcript = claudeTranscript(append(lines, claudeToolUseLine(blockedAt.Add(2*time.Second)))...)
		}},
		{name: "unparseable turn end timestamp", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			lines := []string{claudeToolUseLine(blockedAt), claudeTurnDurationLine("yesterday")}
			f.transcript = claudeTranscript(lines...)
		}},
		{name: "empty transcript", prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
		}},
		{name: "untimestamped lines after the turn end", deliver: true, prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			lines := claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))
			lines = append(lines, claudeUntimestampedLines(blockedAt.Add(2*time.Second))...)
			f.transcript = claudeTranscript(append(lines, claudeAssistantTextLine(blockedAt.Add(3*time.Second)))...)
		}},
		{name: "partial first line of a truncated tail", deliver: true, prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			f.bindTranscript(t, holdTranscriptPath)
			lines := claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))
			f.transcript = claudeTranscript(append([]string{`ntent":"cut mid-line"}]}}`}, lines...)...)
			f.transcriptTruncated = true
		}},
		{name: "file larger than the tail limit", deliver: true, prepare: func(t *testing.T, f *holdFixture, blockedAt time.Time) {
			path := filepath.Join(largeDir, "large.jsonl")
			padding := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", claudeTranscriptTailLimit) + `"}}`
			lines := claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))
			if err := os.WriteFile(path, claudeTranscript(append([]string{padding}, lines...)...), 0o600); err != nil {
				t.Fatal(err)
			}
			f.bindTranscript(t, path)
			f.cmd.messageTranscriptTail = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHoldFixture(t)
			f.installFakeSleep()
			blockedAt := f.now
			f.setInteraction(t, coremetadata.InteractionApprovalRequired)
			tc.prepare(t, f, blockedAt)
			const ref = "message-transcript-judgment"
			f.putHeld(t, ref, f.now.Add(-time.Second), f.now.Add(time.Hour))
			if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
				t.Fatal(err)
			}
			got := f.delivery(t, ref)
			if tc.deliver {
				if got.State != coremessage.StateDelivered || !slices.Equal(f.adapter.submits, []string{ref}) ||
					f.registryWrites != 1 || len(f.waits) != 0 {
					t.Fatalf("record = %+v submits=%v writes=%d waits=%v, want delivered at once after one write",
						got, f.adapter.submits, f.registryWrites, f.waits)
				}
				return
			}
			if got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 || f.registryWrites != 0 {
				t.Fatalf("record = %+v submits=%v writes=%d, want still held, no push, no write",
					got, f.adapter.submits, f.registryWrites)
			}
			if kind := f.interaction(t).Kind; kind != coremetadata.InteractionApprovalRequired {
				t.Fatalf("interaction = %s, want approval_required kept", kind)
			}
		})
	}
}

// The turn-end commit is a compare-and-set: an observation that changed after
// the transcript was judged is not overwritten, and the record is judged again
// on the next look.
func TestAgentMessageReleaseTurnEndCommitRefusesAChangedObservation(t *testing.T) {
	f := newHoldFixture(t)
	f.installFakeSleep()
	blockedAt := f.now
	f.bindTranscript(t, holdTranscriptPath)
	f.setInteraction(t, coremetadata.InteractionApprovalRequired)
	f.transcript = claudeTranscript(claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt, blockedAt.Add(time.Second))...)
	const ref = "message-turn-end-race"
	f.putHeld(t, ref, f.now.Add(-time.Second), f.now.Add(time.Hour))
	// A newer dialog, raised after that turn end, is observed between the
	// transcript read and the commit.
	newer := blockedAt.Add(2 * time.Second)
	f.beforeUpdate = func(working *coremetadata.Registry) {
		f.beforeUpdate = nil
		for _, registry := range []*coremetadata.Registry{working, f.registry} {
			agent, _ := registry.Agent(f.claudeUID)
			agent.Status.Interaction.ObservedAt = newer
		}
	}
	if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
		t.Fatal(err)
	}
	if got := f.delivery(t, ref); got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 || f.registryWrites != 0 {
		t.Fatalf("record = %+v submits=%v writes=%d, want held with no write over the newer dialog",
			got, f.adapter.submits, f.registryWrites)
	}
	if got := f.interaction(t); got.Kind != coremetadata.InteractionApprovalRequired || !got.ObservedAt.Equal(newer) {
		t.Fatalf("interaction = %+v, want the newer approval_required kept", got)
	}
}

// Acceptance 4: an allowed dialog commits in_progress through its hook while
// the release watches; the release delivers within one interval, and the
// release the hook launches afterwards pushes nothing twice.
func TestAgentMessageReleaseDeliversWhenTheDialogIsAllowedDuringTheWatch(t *testing.T) {
	f := newHoldFixture(t)
	f.installFakeSleep()
	blockedAt := f.now
	f.bindTranscript(t, holdTranscriptPath)
	f.setInteraction(t, coremetadata.InteractionApprovalRequired)
	f.transcript = claudeTranscript(claudeTurnDurationLine(claudeStamp(blockedAt.Add(-time.Minute))),
		claudeToolUseLine(blockedAt.Add(-time.Second)))
	const ref = "message-dialog-allowed"
	f.putHeld(t, ref, f.now.Add(-time.Second), f.now.Add(time.Hour))
	waits := 0
	f.onWait = func() {
		waits++
		if waits == 2 {
			f.setInteraction(t, coremetadata.InteractionInProgress)
		}
	}
	if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
		t.Fatal(err)
	}
	if got := f.delivery(t, ref); got.State != coremessage.StateDelivered || !slices.Equal(f.adapter.submits, []string{ref}) {
		t.Fatalf("record = %+v submits=%v, want delivered by one push", got, f.adapter.submits)
	}
	if len(f.waits) != 2 || f.registryWrites != 0 {
		t.Fatalf("waits=%v writes=%d, want delivery on the look right after the hook and no release write", f.waits, f.registryWrites)
	}
	wantWatchWaits(t, f.waits)
	if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.adapter.submits, []string{ref}) {
		t.Fatalf("the hook's release pushed again: submits=%v", f.adapter.submits)
	}
}

// Acceptance 5: after a denial the Registry still says approval_required, so
// a new send is held even with no earlier held message; the release that hold
// launches reads the ended turn and delivers it.
func TestAgentMessageSendToAStaleDeniedDialogIsDeliveredByItsRelease(t *testing.T) {
	f := newHoldFixture(t)
	f.installFakeSleep()
	blockedAt := f.now
	f.bindTranscript(t, holdTranscriptPath)
	f.setInteraction(t, coremetadata.InteractionApprovalRequired)
	f.transcript = claudeTranscript(claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt.Add(-time.Second),
		blockedAt.Add(4*time.Second), blockedAt.Add(5*time.Second))...)
	f.now = f.now.Add(2 * time.Minute)
	f.cmd.messageRelease = func(agentUID string) error {
		f.launches = append(f.launches, agentUID)
		return f.cmd.releaseHeldMessages(agentUID)
	}
	const ref = "message-after-denial"
	stdout, err := f.send(t, ref, false)
	wantHeldReceipt(t, stdout, err, ref)
	if !slices.Equal(f.launches, []string{f.claudeUID}) {
		t.Fatalf("launches = %v, want one release for the blocked hold", f.launches)
	}
	if got := f.delivery(t, ref); got.State != coremessage.StateDelivered || !slices.Equal(f.adapter.submits, []string{ref}) {
		t.Fatalf("record = %+v submits=%v, want delivered by the launched release", got, f.adapter.submits)
	}
	if got := f.interaction(t); got.Kind != coremetadata.InteractionResponseComplete || f.registryWrites != 1 || len(f.waits) != 0 {
		t.Fatalf("interaction = %+v writes=%d waits=%v, want response_complete written once with no wait",
			got, f.registryWrites, f.waits)
	}
}

func TestClaudeTurnEndedAfterReadsOnlyTheTurnShape(t *testing.T) {
	blockedAt := time.Date(2026, 9, 19, 2, 5, 40, 123456789, time.UTC)
	ended := claudeDeniedTurn(blockedAt.Add(-time.Minute), blockedAt, blockedAt.Add(time.Second), blockedAt.Add(10*time.Second))
	for _, tc := range []struct {
		name      string
		tail      []byte
		truncated bool
		want      bool
	}{
		{name: "denied turn ended", tail: claudeTranscript(ended...), want: true},
		{name: "no trailing newline", tail: []byte(strings.Join(ended, "\n")), want: true},
		{name: "assistant string content after the end", want: true, tail: claudeTranscript(append(ended,
			`{"type":"assistant","timestamp":"`+claudeStamp(blockedAt.Add(11*time.Second))+`","message":{"role":"assistant","content":"placeholder"}}`)...)},
		{name: "truncated tail without a newline", tail: []byte(ended[0]), truncated: true},
		{name: "nothing", tail: nil},
		{name: "top-level array line", tail: claudeTranscript(append(ended, `[1,2]`)...)},
		{name: "unreadable assistant message", tail: claudeTranscript(append(ended,
			`{"type":"assistant","timestamp":"`+claudeStamp(blockedAt.Add(11*time.Second))+`","message":"placeholder"}`)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeTurnEndedAfter(tc.tail, tc.truncated, blockedAt); got != tc.want {
				t.Fatalf("claudeTurnEndedAfter = %v, want %v", got, tc.want)
			}
		})
	}
}
