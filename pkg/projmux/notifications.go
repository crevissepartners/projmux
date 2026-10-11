package projmux

import (
	"context"
	"errors"

	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/services/notifications"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeNotificationNotFound        Code = "notification-not-found"
	CodeNotificationAckInvalid      Code = "notification-ack-invalid"
	CodeNotificationLimitInvalid    Code = "notification-limit-invalid"
	CodeNotificationSeverityInvalid Code = "notification-severity-invalid"
	CodeNotificationSourceInvalid   Code = "notification-source-invalid"
)

// ListNotifications reads the saved queue in store order, with CLI filters.
// It does not observe tmux attention, reconcile or project a sidebar.
func (c *Client) ListNotifications(ctx context.Context, f core.NotificationFilter) ([]core.Notification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := notifications.Validate(notifications.FilterOptions(f)); err != nil {
		return nil, notificationRefusal(err)
	}
	if c.pathsErr != nil {
		return nil, c.pathsErr
	}
	entries, err := notifications.List(ctx, notify.NewDefaultStore(c.paths), notifications.FilterOptions(f))
	if err != nil {
		return nil, notificationRefusal(err)
	}
	var out []core.Notification
	if err := copyUsage(entries, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AckNotifications removes one queued id or all entries, returning the count.
// The store lock and missing-id failure match notification ack. No live tmux
// state is changed. Once admitted, the store operation finishes atomically.
func (c *Client) AckNotifications(ctx context.Context, a core.NotificationAck) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if c.pathsErr != nil {
		return 0, c.pathsErr
	}
	n, err := notifications.Ack(ctx, notify.NewDefaultStore(c.paths), a.ID, a.All)
	return n, notificationRefusal(err)
}
func notificationRefusal(err error) error {
	var input *notifications.InputError
	if errors.As(err, &input) {
		message := err.Error()
		switch Code(input.Code) {
		case CodeNotificationLimitInvalid:
			message = "NotificationFilter.Limit must be >= 0"
		case CodeNotificationAckInvalid:
			message = "NotificationAck requires a non-empty ID or All=true with an empty ID"
		}
		return &Refusal{Code: Code(input.Code), message: message}
	}
	if errors.Is(err, notify.ErrNotFound) {
		return &Refusal{Code: CodeNotificationNotFound, message: err.Error()}
	}
	return err
}
