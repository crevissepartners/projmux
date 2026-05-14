package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

func TestAckFocusedNotificationConsumesSelectedCriticalAndOlderSamePaneAIOnly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	selected := notify.Notification{
		ID:        "selected-critical",
		Severity:  notify.SeverityCritical,
		Source:    notify.SourceAI,
		Session:   "main",
		Window:    "1",
		Pane:      "%7",
		CreatedAt: now,
	}
	entries := []notify.Notification{
		selected,
		{
			ID:        "older-info",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now.Add(-time.Minute),
		},
		{
			ID:        "same-age-info",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now,
		},
		{
			ID:        "older-critical",
			Severity:  notify.SeverityCritical,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now.Add(-2 * time.Minute),
		},
		{
			ID:        "older-permission",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"event": "PermissionRequest"},
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now.Add(-3 * time.Minute),
		},
		{
			ID:        "older-stop-failure",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"event": "StopFailure"},
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now.Add(-4 * time.Minute),
		},
		{
			ID:        "older-external",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceExternal,
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now.Add(-5 * time.Minute),
		},
		{
			ID:        "older-git",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceGit,
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now.Add(-6 * time.Minute),
		},
		{
			ID:        "older-k8s",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceK8s,
			Session:   "main",
			Window:    "1",
			Pane:      "%7",
			CreatedAt: now.Add(-7 * time.Minute),
		},
		{
			ID:        "other-pane",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%8",
			CreatedAt: now.Add(-8 * time.Minute),
		},
	}

	store := &stubNotifyStore{}
	if err := ackFocusedNotification(store, selected, entries); err != nil {
		t.Fatalf("ackFocusedNotification error = %v", err)
	}
	want := []string{"selected-critical", "older-info"}
	if !reflect.DeepEqual(store.ackedIDs, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", store.ackedIDs, want)
	}
}

func TestStoreAttentionNotifyProducerNonCriticalPushCompactsOlderSamePaneAI(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{
				ID:        "latest",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Session:   "main",
				Window:    "1",
				Pane:      "%3",
				CreatedAt: now,
			},
			{
				ID:        "older",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Session:   "main",
				Window:    "1",
				Pane:      "%3",
				CreatedAt: now.Add(-time.Minute),
			},
		},
		pushEntry: notify.Notification{
			ID:        "latest",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%3",
			CreatedAt: now,
		},
	}
	producer := &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	producer.PushReplyReady(attentionNotifyInput{
		PaneID: "%3",
		Lookup: attentionNotifyLookupFunc{
			option: func(paneID, option string) string {
				if option == aiPaneAgentOption {
					return "codex"
				}
				return ""
			},
			format: func(paneID, format string) string {
				switch format {
				case "#S":
					return "main"
				case "#{window_id}":
					return "1"
				case "#{pane_id}":
					return "%3"
				default:
					return ""
				}
			},
		},
	})

	if !reflect.DeepEqual(store.ackedIDs, []string{"older"}) {
		t.Fatalf("ackedIDs = %#v, want older compaction", store.ackedIDs)
	}
}

type attentionNotifyLookupFunc struct {
	option func(paneID, option string) string
	format func(paneID, format string) string
}

func (f attentionNotifyLookupFunc) PaneOption(paneID, option string) string {
	if f.option == nil {
		return ""
	}
	return f.option(paneID, option)
}

func (f attentionNotifyLookupFunc) PaneFormat(paneID, format string) string {
	if f.format == nil {
		return ""
	}
	return f.format(paneID, format)
}
