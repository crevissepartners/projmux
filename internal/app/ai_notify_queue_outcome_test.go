package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// outcomeLookup answers the producer's tmux reads from a map keyed by option or
// format; an unlisted key reads as empty, the way a failed read does.
type outcomeLookup map[string]string

func (l outcomeLookup) PaneOption(_, option string) string { return l[option] }
func (l outcomeLookup) PaneFormat(_, format string) string { return l[format] }

// Each guard of the queue decision reports its own token, and a queued entry
// reports none.
func TestPushReplyReadyReportsTheGuardThatStoppedIt(t *testing.T) {
	appOwned := outcomeLookup{"#{@projmux_app}": "1", "#{@projmux_pane_uid}": "pane-1", "#S": "ws"}
	for _, tc := range []struct {
		name       string
		producer   *storeAttentionNotifyProducer
		in         attentionNotifyInput
		wantQueued bool
		want       notifyQueueReason
	}{
		{"no-lookup", &storeAttentionNotifyProducer{store: &stubNotifyStore{}}, attentionNotifyInput{PaneID: "%7"}, false, notifyQueueReasonNoStore},
		{"no-store", &storeAttentionNotifyProducer{}, attentionNotifyInput{PaneID: "%7", Lookup: appOwned}, false, notifyQueueReasonNoStore},
		{"blank-pane", &storeAttentionNotifyProducer{store: &stubNotifyStore{}}, attentionNotifyInput{PaneID: " ", Lookup: appOwned}, false, notifyQueueReasonNoPane},
		{"manual-toggle", &storeAttentionNotifyProducer{store: &stubNotifyStore{}}, attentionNotifyInput{PaneID: "%7", Lookup: appOwned}, false, notifyQueueReasonNoProvider},
		{"foreign-server", &storeAttentionNotifyProducer{store: &stubNotifyStore{}}, attentionNotifyInput{PaneID: "%7", Provider: "claude", Lookup: outcomeLookup{"#S": "ws"}}, false, notifyQueueReasonNotAppOwned},
		{"no-pane-uid", &storeAttentionNotifyProducer{store: &stubNotifyStore{}}, attentionNotifyInput{PaneID: "%7", Provider: "claude", Lookup: outcomeLookup{"#{@projmux_app}": "1", "#S": "ws"}}, false, notifyQueueReasonNoPaneUID},
		{"no-session", &storeAttentionNotifyProducer{store: &stubNotifyStore{}}, attentionNotifyInput{PaneID: "%7", Lookup: outcomeLookup{aiPaneAgentOption: "claude"}}, false, notifyQueueReasonNoSession},
		{"push-refused", &storeAttentionNotifyProducer{store: &stubNotifyStore{pushErr: errors.New("disk full")}}, attentionNotifyInput{PaneID: "%7", Provider: "claude", Lookup: appOwned}, false, notifyQueueReasonPushFailed},
		{"queued", &storeAttentionNotifyProducer{store: &stubNotifyStore{}}, attentionNotifyInput{PaneID: "%7", Provider: "claude", Lookup: appOwned}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.producer.PushReplyReady(tc.in)
			if got.Queued != tc.wantQueued || got.Reason != tc.want {
				t.Fatalf("outcome = %+v, want queued=%v reason=%q", got, tc.wantQueued, tc.want)
			}
		})
	}
	if got := (noopAttentionNotifyProducer{}).PushReplyReady(attentionNotifyInput{PaneID: "%7"}); got.Queued || got.Reason != notifyQueueReasonNoStore {
		t.Fatalf("noop producer outcome = %+v", got)
	}
}

// The ai status path reports the reasons it decides itself: the event is state
// only, or the Pane is already in front of the user.
func TestApplyAIStatusReportsWhyItDidNotQueue(t *testing.T) {
	const pane = "%7"
	newCmd := func(visible bool) (*aiCommand, *stubNotifyStore) {
		cmd, store := unmarkedShellCommand(t, pane, unmarkedShellOptions{appMarker: "1", paneUID: "pane-shell"})
		// The direct call carries no hook route, so answer the reads without
		// the hook harness's route assertion.
		cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "tmux" {
				return nil, os.ErrNotExist
			}
			if slices.Contains(args, "list-clients") {
				if visible {
					return []byte(pane + "\n"), nil
				}
				return nil, nil
			}
			plain := stripRecordedTmuxRoute(args)
			if len(plain) > 0 && plain[0] == "display-message" {
				values := map[string]string{
					"#{" + aiPaneAgentOption + "}": "",
					"#{@projmux_app}":              "1",
					"#{@projmux_pane_uid}":         "pane-shell",
					"#S":                           "workspace", "#{window_id}": "@1",
					"#{pane_id}": pane, "#{socket_path}": unmarkedShellSocket,
				}
				return []byte(values[plain[len(plain)-1]]), nil
			}
			return nil, nil
		}
		return cmd, store
	}

	cmd, store := newCmd(false)
	if err := cmd.applyAIStatusStateOnly("waiting", pane, attentionNotifyInput{Provider: "claude", Force: true}); err != nil {
		t.Fatal(err)
	}
	if got, ok := cmd.takeNotifyQueueOutcome(); !ok || got.Queued || got.Reason != notifyQueueReasonStateOnly {
		t.Fatalf("state-only outcome = %+v recorded=%v", got, ok)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("state-only event queued %d entries", len(store.pushed))
	}

	cmd, store = newCmd(true)
	if err := cmd.applyAIStatusWithNotify("waiting", pane, attentionNotifyInput{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := cmd.takeNotifyQueueOutcome(); !ok || got.Queued || got.Reason != notifyQueueReasonVisiblePane {
		t.Fatalf("visible-pane outcome = %+v recorded=%v", got, ok)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("visible pane queued %d entries", len(store.pushed))
	}

	cmd, store = newCmd(false)
	if err := cmd.applyAIStatusWithNotify("waiting", pane, attentionNotifyInput{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := cmd.takeNotifyQueueOutcome(); !ok || !got.Queued {
		t.Fatalf("queued outcome = %+v recorded=%v", got, ok)
	}
	if len(store.pushed) != 1 || store.pushed[0].Source != notify.SourceAI {
		t.Fatalf("queued pushes = %+v", store.pushed)
	}
}

// Only a hook's `notify` record takes the outcome; every other result and every
// other source leaves it, and the fields that are already there, alone.
func TestWithNotifyQueueOutcomeOnlyAnnotatesHookNotifyRecords(t *testing.T) {
	cmd := &aiCommand{}
	cmd.noteNotifyQueueOutcome(notNotifyQueued(notifyQueueReasonNotAppOwned))
	if got := cmd.withNotifyQueueOutcome(aiIngestLogEntry{Source: "codex-observer", Result: "notify"}); got.Queue != "" {
		t.Fatalf("observer record annotated: %+v", got)
	}
	if got := cmd.withNotifyQueueOutcome(aiIngestLogEntry{Source: "claude-hook", Result: "state"}); got.Queue != "" {
		t.Fatalf("state record annotated: %+v", got)
	}
	cmd.noteNotifyQueueOutcome(notNotifyQueued(notifyQueueReasonNotAppOwned))
	got := cmd.withNotifyQueueOutcome(aiIngestLogEntry{Source: "claude-hook", Result: "notify", Pane: "%7"})
	if got.Result != "notify" || got.Pane != "%7" || got.Queue != "not_queued" || got.QueueReason != "not_app_owned" {
		t.Fatalf("annotated record = %+v", got)
	}
	if again := cmd.withNotifyQueueOutcome(aiIngestLogEntry{Source: "claude-hook", Result: "notify"}); again.Queue != "" {
		t.Fatalf("one decision annotated two records: %+v", again)
	}
}

// diagnostics agent-hook shows the fields in both forms.
func TestDiagnosticsAgentHookShowsTheQueueFields(t *testing.T) {
	entry := aiIngestLogEntry{At: "2026-11-12T00:00:00Z", Source: "claude-hook", Event: "Stop", Result: "notify", Pane: "%7", Queue: "not_queued", QueueReason: "no_pane_uid"}
	text := formatAIIngestLogEntry(entry)
	if !strings.Contains(text, "queue=not_queued") || !strings.Contains(text, "queue_reason=no_pane_uid") {
		t.Fatalf("text = %q", text)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"queue":"not_queued"`) || !strings.Contains(string(raw), `"queue_reason":"no_pane_uid"`) {
		t.Fatalf("json = %s", raw)
	}
	plain := formatAIIngestLogEntry(aiIngestLogEntry{At: "t", Source: "claude-hook", Result: "state"})
	if strings.Contains(plain, "queue") {
		t.Fatalf("a record without a queue decision grew queue text: %q", plain)
	}
}
