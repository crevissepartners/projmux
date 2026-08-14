package app

import (
	"errors"
	"strings"
	"time"

	corefocus "github.com/crevissepartners/projmux/internal/core/focus"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

type notifyDiagnosticLabels struct {
	provider diagnostics.Provider
	category diagnostics.Category
}

func notifyLabels(source string, metadata map[string]string) notifyDiagnosticLabels {
	provider := diagnostics.ProviderOther
	switch strings.ToLower(strings.TrimSpace(source)) {
	case notify.SourceAI:
		provider = notifyProvider(strings.TrimSpace(metadata[notify.MetaAgent]))
		if provider == diagnostics.ProviderOther {
			provider = diagnostics.ProviderAI
		}
	case notify.SourceK8s:
		provider = diagnostics.ProviderK8s
	case notify.SourceGit:
		provider = diagnostics.ProviderGit
	case notify.SourceExternal:
		provider = diagnostics.ProviderExternal
	}
	return notifyDiagnosticLabels{provider: provider, category: notifyCategory(strings.TrimSpace(metadata[notify.MetaCategory]))}
}

func notifyProvider(raw string) diagnostics.Provider {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "claude":
		return diagnostics.ProviderClaude
	case "codex":
		return diagnostics.ProviderCodex
	case "antigravity":
		return diagnostics.ProviderAntigravity
	case "ai":
		return diagnostics.ProviderAI
	case "k8s":
		return diagnostics.ProviderK8s
	case "git":
		return diagnostics.ProviderGit
	case "external":
		return diagnostics.ProviderExternal
	case "projmux":
		return diagnostics.ProviderProjmux
	default:
		return diagnostics.ProviderOther
	}
}

func notifyCategory(raw string) diagnostics.Category {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "approval_required":
		return diagnostics.CategoryApprovalRequired
	case "input_required":
		return diagnostics.CategoryInputRequired
	case "response_complete", "response_ready":
		return diagnostics.CategoryResponseComplete
	case "error":
		return diagnostics.CategoryError
	case "subagent_stopped":
		return diagnostics.CategorySubagentStopped
	case "teammate_waiting":
		return diagnostics.CategoryTeammateWaiting
	case "selection_required":
		return diagnostics.CategorySelectionRequired
	case "confirmation_required":
		return diagnostics.CategoryConfirmationRequired
	case "session_ready":
		return diagnostics.CategorySessionReady
	default:
		return diagnostics.CategoryOther
	}
}

func recordNotifyEnqueue(recorder *diagnostics.NotifyFocusRecorder, in notify.PushInput, result notify.PushResult, operationErr error, started time.Time, ownsTopLevel bool) {
	if recorder == nil {
		return
	}
	labels := notifyLabels(in.Source, in.Metadata)
	disposition := diagnostics.DispositionQueued
	code := diagnostics.Code("")
	if operationErr != nil {
		disposition, code = diagnostics.DispositionFailed, diagnostics.CodeNotifyEnqueueFailed
	} else if result.Replaced {
		disposition = diagnostics.DispositionDeduplicated
	}
	recorder.RecordNotify(diagnostics.TransitionNotifyEnqueue, disposition, labels.provider, labels.category, diagnostics.RouteQueue, code, started, ownsTopLevel)
}

type notifyDeliveryDiagnostics struct {
	recorder     *diagnostics.NotifyFocusRecorder
	provider     diagnostics.Provider
	category     diagnostics.Category
	ownsTopLevel bool
}

func (d notifyDeliveryDiagnostics) record(disposition diagnostics.Disposition, route diagnostics.Route, code diagnostics.Code, started time.Time) {
	if d.recorder == nil {
		return
	}
	provider := d.provider
	if provider == "" {
		provider = diagnostics.ProviderOther
	}
	category := d.category
	if category == "" {
		category = diagnostics.CategoryOther
	}
	d.recorder.RecordNotify(diagnostics.TransitionNotifyDelivery, disposition, provider, category, route, code, started, d.ownsTopLevel)
}

type focusTelemetry struct {
	opts   focusOptions
	target corefocus.Target
	socket string

	provider diagnostics.Provider
	category diagnostics.Category
	route    diagnostics.Route
}

func newFocusTelemetry(opts focusOptions, target corefocus.Target, socket string) focusTelemetry {
	provider := diagnostics.ProviderProjmux
	switch strings.ToLower(strings.TrimSpace(opts.Source)) {
	case "ai":
		provider = diagnostics.ProviderAI
	case "external":
		provider = diagnostics.ProviderExternal
	}
	category := diagnostics.CategoryOther
	switch strings.ToLower(strings.TrimSpace(opts.Kind)) {
	case "segment-click":
		category = diagnostics.CategorySegmentClick
	case "toast-click":
		category = diagnostics.CategoryToastClick
	case "row-select":
		category = diagnostics.CategoryRowSelect
	case "group-select":
		category = diagnostics.CategoryGroupSelect
	case "reply-ready":
		category = diagnostics.CategoryReplyReady
	case "busy-cleared":
		category = diagnostics.CategoryBusyCleared
	case "custom":
		category = diagnostics.CategoryCustom
	}
	route := diagnostics.RouteFocusDirect
	switch strings.ToLower(strings.TrimSpace(opts.Source)) {
	case "status-bar", "notify-sidebar", "os-notification":
		route = diagnostics.RouteFocusQueue
	case "toast":
		route = diagnostics.RouteFocusToast
	}
	return focusTelemetry{opts: opts, target: target, socket: socket, provider: provider, category: category, route: route}
}

func focusDiagnosticOutcome(res focusResult, operationErr error) (diagnostics.Disposition, diagnostics.Code) {
	if operationErr == nil {
		switch {
		case res.Dispatch == "notify-only":
			return diagnostics.DispositionNotifyOnly, ""
		case strings.Contains(res.Fallback, "session-only"):
			return diagnostics.DispositionSessionOnly, ""
		case strings.Contains(res.Fallback, "window-only"):
			return diagnostics.DispositionWindowOnly, ""
		default:
			return diagnostics.DispositionFocused, ""
		}
	}
	switch res.Reason {
	case "output-failed":
		return diagnostics.DispositionFailed, diagnostics.CodeFocusOutputFailed
	case "session-unresolved", "window-id-unresolved", "pane-id-unresolved":
		if res.Reason == "window-id-unresolved" {
			return diagnostics.DispositionFailed, diagnostics.CodeFocusWindowFailed
		}
		if res.Reason == "pane-id-unresolved" {
			return diagnostics.DispositionFailed, diagnostics.CodeFocusPaneFailed
		}
		return diagnostics.DispositionFailed, diagnostics.CodeFocusResolveFailed
	case "list-clients-failed":
		return diagnostics.DispositionFailed, diagnostics.CodeFocusInventoryFailed
	case "switch-client-failed":
		return diagnostics.DispositionFailed, diagnostics.CodeFocusDispatchFailed
	}
	if strings.Contains(operationErr.Error(), "focus: list-sessions:") {
		return diagnostics.DispositionFailed, diagnostics.CodeFocusInventoryFailed
	}
	var coded focusExitError
	if errors.As(operationErr, &coded) {
		return diagnostics.DispositionFailed, diagnostics.CodeFocusResolveFailed
	}
	return diagnostics.DispositionFailed, diagnostics.CodeFocusRequestFailed
}
