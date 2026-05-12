package app

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

const notifyHookDepthEnv = "PROJMUX_NOTIFY_HOOK_DEPTH"

type notifyAsyncHookRunner interface {
	RunAsync(ctx context.Context, event hooks.Event, c hooks.Context) <-chan hooks.AsyncResult
}

type notifyHookMeta struct {
	Type    string
	Agent   string
	Topic   string
	Message string
}

type notifyHookPayload struct {
	Event     string            `json:"event"`
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Agent     string            `json:"agent"`
	Topic     string            `json:"topic"`
	Pane      string            `json:"pane"`
	Session   string            `json:"session"`
	Message   string            `json:"message"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type sendNotiHookDispatcher struct {
	runner    notifyAsyncHookRunner
	lookupEnv func(string) string
	getwd     func() (string, error)
}

func newSendNotiHookDispatcher() *sendNotiHookDispatcher {
	return &sendNotiHookDispatcher{
		runner:    defaultLifecycleHookRunner(),
		lookupEnv: os.Getenv,
		getwd:     os.Getwd,
	}
}

func (d *sendNotiHookDispatcher) Dispatch(entry notify.Notification, meta notifyHookMeta) {
	if d == nil || d.runner == nil {
		return
	}
	depth := d.currentDepth()
	if depth >= 1 {
		return
	}

	payload := notifyHookPayload{
		Event:     string(hooks.EventSendNoti),
		ID:        strings.TrimSpace(entry.ID),
		Type:      strings.TrimSpace(meta.Type),
		Agent:     strings.TrimSpace(meta.Agent),
		Topic:     strings.TrimSpace(meta.Topic),
		Pane:      strings.TrimSpace(entry.Pane),
		Session:   strings.TrimSpace(entry.Session),
		Message:   strings.TrimSpace(meta.Message),
		Metadata:  entry.Metadata,
		CreatedAt: entry.CreatedAt,
	}
	if payload.Type == "" {
		payload.Type = strings.TrimSpace(entry.Source)
	}
	if payload.Message == "" {
		payload.Message = strings.TrimSpace(entry.Text)
	}

	stdin, err := json.Marshal(payload)
	if err != nil {
		return
	}

	d.runner.RunAsync(context.Background(), hooks.EventSendNoti, hooks.Context{
		SessionName: payload.Session,
		CWD:         d.resolveCWD(),
		Socket:      strings.TrimSpace(entry.Socket),
		PaneID:      payload.Pane,
		Env: map[string]string{
			notifyHookDepthEnv:       strconv.Itoa(depth + 1),
			"PROJMUX_NOTIFY_ID":      payload.ID,
			"PROJMUX_NOTIFY_TYPE":    payload.Type,
			"PROJMUX_NOTIFY_AGENT":   payload.Agent,
			"PROJMUX_NOTIFY_TOPIC":   payload.Topic,
			"PROJMUX_NOTIFY_PANE":    payload.Pane,
			"PROJMUX_NOTIFY_SESSION": payload.Session,
			"PROJMUX_NOTIFY_MESSAGE": payload.Message,
		},
		Stdin: stdin,
	})
}

func (d *sendNotiHookDispatcher) currentDepth() int {
	if d == nil || d.lookupEnv == nil {
		return 0
	}
	raw := strings.TrimSpace(d.lookupEnv(notifyHookDepthEnv))
	if raw == "" {
		return 0
	}
	depth, err := strconv.Atoi(raw)
	if err != nil || depth < 0 {
		return 0
	}
	return depth
}

func (d *sendNotiHookDispatcher) resolveCWD() string {
	if d != nil && d.lookupEnv != nil {
		if raw := strings.TrimSpace(d.lookupEnv("PROJMUX_CWD")); raw != "" {
			return raw
		}
	}
	if d == nil || d.getwd == nil {
		return ""
	}
	wd, err := d.getwd()
	if err != nil {
		return ""
	}
	wd = strings.TrimSpace(wd)
	if wd == "" {
		return ""
	}
	if root := nearestProjectMarker(wd); root != "" {
		return root
	}
	return wd
}
