package codexappserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const minimumLifecycleEvents = "v0.149.0"

type lifecycleThreadReadParams struct {
	ThreadID     string `json:"threadId"`
	IncludeTurns bool   `json:"includeTurns"`
}

type lifecycleThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

type lifecycleTurn struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	StartedAt *float64 `json:"startedAt"`
}

type lifecycleThread struct {
	ID     string                `json:"id"`
	Status lifecycleThreadStatus `json:"status"`
	Turns  []lifecycleTurn       `json:"turns"`
}

type lifecycleThreadReadResult struct {
	Thread lifecycleThread `json:"thread"`
}

// LifecycleEventKind is the complete app-server event vocabulary consumed by
// the Phase 3 interaction projection. Content-bearing item and delta events
// deliberately have no representation here.
type LifecycleEventKind string

const (
	LifecycleThreadStatus    LifecycleEventKind = "thread-status"
	LifecycleTurnStarted     LifecycleEventKind = "turn-started"
	LifecycleTurnCompleted   LifecycleEventKind = "turn-completed"
	LifecycleApprovalPending LifecycleEventKind = "approval-pending"
	LifecycleRequestResolved LifecycleEventKind = "request-resolved"
)

type ThreadState string

const (
	ThreadStateUnknown            ThreadState = "unknown"
	ThreadStateNotLoaded          ThreadState = "not-loaded"
	ThreadStateIdle               ThreadState = "idle"
	ThreadStateActive             ThreadState = "active"
	ThreadStateWaitingOnApproval  ThreadState = "waiting-on-approval"
	ThreadStateWaitingOnUserInput ThreadState = "waiting-on-user-input"
	ThreadStateSystemError        ThreadState = "system-error"
)

type TurnState string

const (
	TurnStateUnknown     TurnState = "unknown"
	TurnStateInProgress  TurnState = "in-progress"
	TurnStateCompleted   TurnState = "completed"
	TurnStateFailed      TurnState = "failed"
	TurnStateInterrupted TurnState = "interrupted"
)

type ApprovalKind string

const (
	ApprovalCommand     ApprovalKind = "command"
	ApprovalFileChange  ApprovalKind = "file-change"
	ApprovalPermissions ApprovalKind = "permissions"
)

// LifecycleEvent is a bounded, content-free projection. It is safe for the app
// layer and diagnostics: prompts, commands, paths, reasons, output, reasoning,
// and diffs are discarded during decoding.
type LifecycleEvent struct {
	Kind         LifecycleEventKind
	ThreadID     string
	TurnID       string
	ItemID       string
	RequestID    string
	ThreadState  ThreadState
	TurnState    TurnState
	ApprovalKind ApprovalKind
}

type LifecycleSnapshot struct {
	ThreadID    string
	ThreadState ThreadState
	TurnID      string
	TurnState   TurnState
	StartedAt   time.Time
}

// LifecycleEventsAvailable closes the event-capability decision on the
// negotiated initialize version. Unknown and older versions stay on hook
// fallback rather than assuming notification/request shapes they did not
// advertise in the validated schema epoch.
func (c *Client) LifecycleEventsAvailable() bool {
	c.mu.Lock()
	version := c.version
	c.mu.Unlock()
	match := versionPattern.FindStringSubmatch(version)
	return len(match) == 2 && semver.IsValid("v"+match[1]) && semver.Compare("v"+match[1], minimumLifecycleEvents) >= 0
}

// ReadLifecycleSnapshot converges one exact thread after a connection is
// initialized. The wire response can contain rich turn items, but the decoder
// selects only identifiers and closed statuses and never retains item content.
func (c *Client) ReadLifecycleSnapshot(ctx context.Context, threadID string) (LifecycleSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return LifecycleSnapshot{}, fmt.Errorf("%w: lifecycle snapshot thread is empty", ErrProtocol)
	}
	var result lifecycleThreadReadResult
	if err := c.Request(ctx, methodThreadRead, lifecycleThreadReadParams{ThreadID: threadID, IncludeTurns: true}, &result); err != nil {
		return LifecycleSnapshot{}, err
	}
	if strings.TrimSpace(result.Thread.ID) != threadID {
		return LifecycleSnapshot{}, fmt.Errorf("%w: lifecycle snapshot returned a different thread", ErrProtocol)
	}
	snapshot := LifecycleSnapshot{ThreadID: threadID, ThreadState: normalizeThreadState(result.Thread.Status)}
	if snapshot.ThreadState == ThreadStateUnknown {
		return LifecycleSnapshot{}, fmt.Errorf("%w: lifecycle snapshot returned an unknown thread state", ErrProtocol)
	}
	if len(result.Thread.Turns) > 0 {
		turn := result.Thread.Turns[len(result.Thread.Turns)-1]
		snapshot.TurnID = strings.TrimSpace(turn.ID)
		snapshot.TurnState = normalizeTurnState(turn.Status)
		if turn.StartedAt != nil {
			snapshot.StartedAt = unixSeconds(*turn.StartedAt)
		}
		if snapshot.TurnID == "" || snapshot.TurnState == TurnStateUnknown {
			return LifecycleSnapshot{}, fmt.Errorf("%w: lifecycle snapshot returned an incompatible latest turn", ErrProtocol)
		}
	}
	return snapshot, nil
}

// DecodeLifecycleEvent recognizes only the Phase 3 lifecycle and approval
// methods. Unknown messages are ignored so later progress/catalog/control
// phases cannot accidentally widen this consumer.
func DecodeLifecycleEvent(notification Notification) (LifecycleEvent, bool, error) {
	method := strings.TrimSpace(notification.Method)
	switch method {
	case "thread/status/changed":
		var params struct {
			ThreadID string                `json:"threadId"`
			Status   lifecycleThreadStatus `json:"status"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return LifecycleEvent{}, true, err
		}
		state := normalizeThreadState(params.Status)
		if strings.TrimSpace(params.ThreadID) == "" || state == ThreadStateUnknown {
			return LifecycleEvent{}, true, fmt.Errorf("%w: invalid thread status event", ErrProtocol)
		}
		return LifecycleEvent{Kind: LifecycleThreadStatus, ThreadID: strings.TrimSpace(params.ThreadID), ThreadState: state}, true, nil
	case "turn/started", "turn/completed":
		var params struct {
			ThreadID string        `json:"threadId"`
			Turn     lifecycleTurn `json:"turn"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return LifecycleEvent{}, true, err
		}
		kind := LifecycleTurnStarted
		state := TurnStateInProgress
		if method == "turn/completed" {
			kind = LifecycleTurnCompleted
			state = normalizeTurnState(params.Turn.Status)
		}
		if strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.Turn.ID) == "" || state == TurnStateUnknown {
			return LifecycleEvent{}, true, fmt.Errorf("%w: invalid turn lifecycle event", ErrProtocol)
		}
		return LifecycleEvent{Kind: kind, ThreadID: strings.TrimSpace(params.ThreadID), TurnID: strings.TrimSpace(params.Turn.ID), TurnState: state}, true, nil
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			ItemID   string `json:"itemId"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return LifecycleEvent{}, true, err
		}
		approvalKind := ApprovalCommand
		if method == "item/fileChange/requestApproval" {
			approvalKind = ApprovalFileChange
		} else if method == "item/permissions/requestApproval" {
			approvalKind = ApprovalPermissions
		}
		event := LifecycleEvent{
			Kind: LifecycleApprovalPending, ThreadID: strings.TrimSpace(params.ThreadID),
			TurnID: strings.TrimSpace(params.TurnID), ItemID: strings.TrimSpace(params.ItemID),
			RequestID: strings.TrimSpace(notification.RequestID), ApprovalKind: approvalKind,
		}
		if event.ThreadID == "" || event.TurnID == "" || event.ItemID == "" || event.RequestID == "" {
			return LifecycleEvent{}, true, fmt.Errorf("%w: incomplete approval request identity", ErrProtocol)
		}
		return event, true, nil
	case "serverRequest/resolved":
		var params struct {
			ThreadID  string          `json:"threadId"`
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return LifecycleEvent{}, true, err
		}
		requestID, err := normalizeServerRequestID(params.RequestID)
		if err != nil || requestID == "" || strings.TrimSpace(params.ThreadID) == "" {
			return LifecycleEvent{}, true, fmt.Errorf("%w: incomplete resolved request identity", ErrProtocol)
		}
		return LifecycleEvent{Kind: LifecycleRequestResolved, ThreadID: strings.TrimSpace(params.ThreadID), RequestID: requestID}, true, nil
	default:
		return LifecycleEvent{}, false, nil
	}
}

func decodeLifecycleParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, target) != nil {
		return fmt.Errorf("%w: malformed lifecycle event params", ErrProtocol)
	}
	return nil
}

func normalizeThreadState(status lifecycleThreadStatus) ThreadState {
	switch strings.TrimSpace(status.Type) {
	case "notLoaded":
		return ThreadStateNotLoaded
	case "idle":
		return ThreadStateIdle
	case "systemError":
		return ThreadStateSystemError
	case "active":
		if slices.Contains(status.ActiveFlags, "waitingOnApproval") {
			return ThreadStateWaitingOnApproval
		}
		if slices.Contains(status.ActiveFlags, "waitingOnUserInput") {
			return ThreadStateWaitingOnUserInput
		}
		return ThreadStateActive
	default:
		return ThreadStateUnknown
	}
}

func normalizeTurnState(status string) TurnState {
	switch strings.TrimSpace(status) {
	case "inProgress":
		return TurnStateInProgress
	case "completed":
		return TurnStateCompleted
	case "failed":
		return TurnStateFailed
	case "interrupted":
		return TurnStateInterrupted
	default:
		return TurnStateUnknown
	}
}
