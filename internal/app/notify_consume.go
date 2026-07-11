package app

import (
	"errors"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

func ackFocusedNotification(store notifyStore, selected notify.Notification, entries []notify.Notification) error {
	if store == nil {
		return nil
	}
	if strings.TrimSpace(selected.ID) == "" {
		return nil
	}
	if err := store.Ack(selected.ID); err != nil && !errors.Is(err, notify.ErrNotFound) {
		return err
	}
	return ackOlderSameTargetAINotifications(store, selected, entries)
}

func ackOlderSameTargetAINotifications(store notifyStore, selected notify.Notification, entries []notify.Notification) error {
	if store == nil {
		return nil
	}
	for _, candidate := range entries {
		if !shouldBulkAckAIAfterFocus(selected, candidate) {
			continue
		}
		if err := store.Ack(candidate.ID); err != nil && !errors.Is(err, notify.ErrNotFound) {
			return err
		}
	}
	return nil
}

func shouldBulkAckAIAfterFocus(selected, candidate notify.Notification) bool {
	if candidate.ID == "" || candidate.ID == selected.ID {
		return false
	}
	if !sameNotifyTargetPane(selected, candidate) {
		return false
	}
	if !candidate.CreatedAt.Before(selected.CreatedAt) {
		return false
	}
	if candidate.Source != notify.SourceAI {
		return false
	}
	if candidate.Severity == notify.SeverityCritical {
		return false
	}
	if protectedAINotifyEvent(candidate) {
		return false
	}
	return true
}

func sameNotifyTargetPane(a, b notify.Notification) bool {
	if strings.TrimSpace(a.Session) == "" || strings.TrimSpace(b.Session) == "" {
		return false
	}
	if strings.TrimSpace(a.Pane) == "" || strings.TrimSpace(b.Pane) == "" {
		return false
	}
	if strings.TrimSpace(a.Session) != strings.TrimSpace(b.Session) {
		return false
	}
	if strings.TrimSpace(a.Pane) != strings.TrimSpace(b.Pane) {
		return false
	}
	aSocket := strings.TrimSpace(a.Socket)
	bSocket := strings.TrimSpace(b.Socket)
	return aSocket == "" || bSocket == "" || aSocket == bSocket
}

func protectedAINotifyEvent(n notify.Notification) bool {
	event := strings.ToLower(strings.TrimSpace(n.Metadata[notify.MetaEvent]))
	id := strings.ToLower(strings.TrimSpace(n.ID))
	return strings.Contains(event, "permission") ||
		strings.Contains(event, "stopfailure") ||
		strings.Contains(event, "stop_failure") ||
		strings.Contains(event, "stop-failure") ||
		strings.Contains(id, ":permission:") ||
		strings.Contains(id, ":stop-failure:")
}
