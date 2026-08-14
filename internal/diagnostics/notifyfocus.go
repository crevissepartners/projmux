package diagnostics

import (
	"sync"
	"sync/atomic"
	"time"
)

// Transition is the closed state boundary represented by a notify/focus
// operational event. It never contains queue IDs, targets, or user payloads.
type Transition string

const (
	TransitionNotifyEnqueue  Transition = "enqueue"
	TransitionNotifyDelivery Transition = "delivery"
	TransitionFocusRequest   Transition = "request"
)

// Disposition is the closed terminal state of a transition.
type Disposition string

const (
	DispositionQueued       Disposition = "queued"
	DispositionDeduplicated Disposition = "deduplicated"
	DispositionDelivered    Disposition = "delivered"
	DispositionSuppressed   Disposition = "suppressed"
	DispositionFocused      Disposition = "focused"
	DispositionNotifyOnly   Disposition = "notify-only"
	DispositionSessionOnly  Disposition = "session-only"
	DispositionWindowOnly   Disposition = "window-only"
	DispositionFailed       Disposition = "failed"
)

// Provider is a content-free producer classification. Unknown provider
// payloads must be projected to ProviderOther before reaching the recorder.
type Provider string

const (
	ProviderClaude      Provider = "claude"
	ProviderCodex       Provider = "codex"
	ProviderAntigravity Provider = "antigravity"
	ProviderAI          Provider = "ai"
	ProviderK8s         Provider = "k8s"
	ProviderGit         Provider = "git"
	ProviderExternal    Provider = "external"
	ProviderProjmux     Provider = "projmux"
	ProviderOther       Provider = "other"
)

// Category is the closed semantic notification/focus classification.
type Category string

const (
	CategoryApprovalRequired     Category = "approval_required"
	CategoryInputRequired        Category = "input_required"
	CategoryResponseComplete     Category = "response_complete"
	CategoryError                Category = "error"
	CategorySubagentStopped      Category = "subagent_stopped"
	CategoryTeammateWaiting      Category = "teammate_waiting"
	CategorySelectionRequired    Category = "selection_required"
	CategoryConfirmationRequired Category = "confirmation_required"
	CategorySessionReady         Category = "session_ready"
	CategorySegmentClick         Category = "segment_click"
	CategoryToastClick           Category = "toast_click"
	CategoryRowSelect            Category = "row_select"
	CategoryGroupSelect          Category = "group_select"
	CategoryReplyReady           Category = "reply_ready"
	CategoryBusyCleared          Category = "busy_cleared"
	CategoryCustom               Category = "custom"
	CategoryOther                Category = "other"
)

// Route is the closed storage, sender, or focus entrypoint selected for a
// transition. It does not carry executable paths or tmux routing identity.
type Route string

const (
	RouteQueue         Route = "queue"
	RouteHook          Route = "hook"
	RouteNotifySend    Route = "notify-send"
	RouteWSLToast      Route = "wsl-toast"
	RouteWSLNotifySend Route = "wsl-notify-send"
	RouteDisabled      Route = "disabled"
	RouteDedupe        Route = "dedupe"
	RouteVisiblePane   Route = "visible-pane"
	RouteFocusDirect   Route = "focus-direct"
	RouteFocusQueue    Route = "focus-queue"
	RouteFocusToast    Route = "focus-toast"
)

const (
	CodeNotifyEnqueueFailed       Code = "notify.enqueue.failed"
	CodeNotifyDeliveryFailed      Code = "notify.delivery.failed"
	CodeNotifyDeliveryUnavailable Code = "notify.delivery.unavailable"
	CodeFocusResolveFailed        Code = "focus.resolve.failed"
	CodeFocusInventoryFailed      Code = "focus.inventory.failed"
	CodeFocusDispatchFailed       Code = "focus.dispatch.failed"
	CodeFocusWindowFailed         Code = "focus.window.failed"
	CodeFocusPaneFailed           Code = "focus.pane.failed"
	CodeFocusOutputFailed         Code = "focus.output.failed"
	CodeFocusRequestFailed        Code = "focus.request.failed"
)

// NotifyFocusRecorder owns the safe Phase 4 terminal transitions for one
// process. Identical safe tuples are emitted at most once per run, preventing
// reconcile/dedupe hot paths from filling the bounded journal with records
// that carry no additional diagnostic information.
type NotifyFocusRecorder struct {
	writer     EventWriter
	runID      string
	version    string
	muxBackend string
	now        func() time.Time
	outcomes   *atomic.Uint64

	mu   sync.Mutex
	seen map[notifyFocusEventKey]bool
}

type notifyFocusEventKey struct {
	component   string
	transition  Transition
	disposition Disposition
	provider    Provider
	category    Category
	route       Route
	code        Code
}

// RecordNotify appends one closed enqueue/delivery transition best-effort.
// ownsTopLevel is true only when this transition is the selected outer CLI
// mutation; secondary/automatic producers must leave it false.
func (r *NotifyFocusRecorder) RecordNotify(transition Transition, disposition Disposition, provider Provider, category Category, route Route, code Code, started time.Time, ownsTopLevel bool) {
	r.record("notify", "notify.transition", transition, disposition, provider, category, route, code, started, ownsTopLevel)
}

// RecordFocus appends one closed focus-request transition best-effort.
func (r *NotifyFocusRecorder) RecordFocus(disposition Disposition, provider Provider, category Category, route Route, code Code, started time.Time) {
	r.record("focus", "focus.transition", TransitionFocusRequest, disposition, provider, category, route, code, started, true)
}

func (r *NotifyFocusRecorder) record(component, name string, transition Transition, disposition Disposition, provider Provider, category Category, route Route, code Code, started time.Time, ownsTopLevel bool) {
	if r == nil {
		return
	}
	key := notifyFocusEventKey{component: component, transition: transition, disposition: disposition, provider: provider, category: category, route: route, code: code}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = make(map[notifyFocusEventKey]bool)
	}
	if owned, ok := r.seen[key]; ok {
		if ownsTopLevel && !owned && r.outcomes != nil {
			r.outcomes.Add(1)
			r.seen[key] = true
		}
		return
	}
	r.seen[key] = ownsTopLevel
	if ownsTopLevel && r.outcomes != nil {
		// Logical ownership precedes append so storage failure cannot produce a
		// duplicate generic top-level outcome.
		r.outcomes.Add(1)
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	event := Event{
		At: now.UTC().Format(time.RFC3339Nano), Level: "info", Component: component,
		Event: name, Result: "success", DurationMS: max(now.Sub(started).Milliseconds(), 0),
		RunID: r.runID, Version: r.version, MuxBackend: r.muxBackend,
		Transition: string(transition), Disposition: string(disposition), Provider: string(provider), Category: string(category), Route: string(route),
	}
	if disposition == DispositionFailed {
		event.Level, event.Result, event.Kind, event.Code = "error", "error", "runtime", string(code)
	}
	if r.writer != nil {
		_ = r.writer.Append(event)
	}
}

// NotifyFocus returns the Phase 4 recorder bound to this process run and its
// shared logical top-level ownership counter.
func (r *LifecycleRecorder) NotifyFocus() *NotifyFocusRecorder {
	if r == nil {
		return nil
	}
	return &NotifyFocusRecorder{
		writer: r.writer, runID: r.runID, version: r.version,
		muxBackend: r.muxBackend, now: time.Now, outcomes: &r.outcomes,
	}
}
