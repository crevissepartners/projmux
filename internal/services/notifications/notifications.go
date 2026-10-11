// Package notifications shares saved queue reads and explicit acknowledgements.
package notifications

import (
	"context"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

type Store interface {
	List() ([]notify.Notification, error)
	Ack(string) error
	AckAll() (int, error)
}
type FilterOptions struct {
	Severities, Sources []string
	Limit               int
}
type InputError struct{ Code, Message string }

func (e *InputError) Error() string            { return e.Message }
func (e *InputError) MetadataUsageError() bool { return true }
func Validate(f FilterOptions) error {
	if f.Limit < 0 {
		return &InputError{Code: "notification-limit-invalid", Message: "get notifications --limit must be >= 0"}
	}
	for _, v := range f.Severities {
		if err := notify.ValidateSeverity(v); err != nil {
			return &InputError{Code: "notification-severity-invalid", Message: err.Error()}
		}
	}
	for _, v := range f.Sources {
		if err := notify.ValidateSource(v); err != nil {
			return &InputError{Code: "notification-source-invalid", Message: err.Error()}
		}
	}
	return nil
}
func List(ctx context.Context, store Store, f FilterOptions) ([]notify.Notification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Validate(f); err != nil {
		return nil, err
	}
	entries, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	entries = Filter(entries, f.Severities, f.Sources)
	if f.Limit > 0 && len(entries) > f.Limit {
		entries = entries[:f.Limit]
	}
	if entries == nil {
		entries = []notify.Notification{}
	}
	return entries, nil
}
func Ack(ctx context.Context, store Store, id string, all bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if all {
		if id != "" {
			return 0, &InputError{Code: "notification-ack-invalid", Message: "notification ack --all does not accept positional arguments"}
		}
		n, err := store.AckAll()
		if err != nil {
			return 0, fmt.Errorf("ack all notifications: %w", err)
		}
		return n, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, &InputError{Code: "notification-ack-invalid", Message: "notification ack requires a non-empty <id> argument"}
	}
	if err := store.Ack(id); err != nil {
		return 0, fmt.Errorf("ack notification: %w", err)
	}
	return 1, nil
}
func Filter(entries []notify.Notification, severities, sources []string) []notify.Notification {
	if len(severities) == 0 && len(sources) == 0 {
		return entries
	}
	sevSet := toSet(severities)
	srcSet := toSet(sources)
	out := make([]notify.Notification, 0, len(entries))
	for _, e := range entries {
		if len(sevSet) > 0 {
			if _, ok := sevSet[e.Severity]; !ok {
				continue
			}
		}
		if len(srcSet) > 0 {
			if _, ok := srcSet[e.Source]; !ok {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}
func toSet(values []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}
