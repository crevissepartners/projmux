// Package diagnostics provides the private, bounded operational event journal.
// Its schema is deliberately closed: callers cannot attach arbitrary metadata.
package diagnostics

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxMessageRunes = 512

// Event is the complete on-disk operational event schema. It intentionally has
// no argv, environment, routing, payload, or generic metadata field.
type Event struct {
	At                 string `json:"at"`
	Level              string `json:"level"`
	Component          string `json:"component"`
	Event              string `json:"event"`
	Result             string `json:"result"`
	DurationMS         int64  `json:"duration_ms"`
	RunID              string `json:"run_id"`
	Version            string `json:"version"`
	MuxBackend         string `json:"mux_backend"`
	Command            string `json:"command,omitempty"`
	Subcommand         string `json:"subcommand,omitempty"`
	Kind               string `json:"kind,omitempty"`
	Message            string `json:"message,omitempty"`
	Operation          string `json:"operation,omitempty"`
	Code               string `json:"code,omitempty"`
	Source             string `json:"source,omitempty"`
	Transition         string `json:"transition,omitempty"`
	Disposition        string `json:"disposition,omitempty"`
	Provider           string `json:"provider,omitempty"`
	Category           string `json:"category,omitempty"`
	Route              string `json:"route,omitempty"`
	AIKind             string `json:"ai_kind,omitempty"`
	AIResult           string `json:"ai_result,omitempty"`
	Failure            string `json:"failure,omitempty"`
	WindowCount        *int   `json:"window_count,omitempty"`
	PaneCount          *int   `json:"pane_count,omitempty"`
	ShellRecipeCount   *int   `json:"shell_recipe_count,omitempty"`
	AgentRecipeCount   *int   `json:"agent_recipe_count,omitempty"`
	StartupRecipeCount *int   `json:"startup_recipe_count,omitempty"`
	ItemCount          *int   `json:"item_count,omitempty"`
}

// NewRunID creates one opaque correlation ID for a process invocation.
func NewRunID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), os.Getpid())
}

// MuxBackend returns the supported backend enum for operational events.
func MuxBackend() string {
	return "tmux"
}

// SanitizeMessage removes terminal controls, abbreviates the user's home
// directory, and bounds the result. Error kind remains a separate field.
func SanitizeMessage(message, home string) string {
	if home != "" {
		home = strings.TrimRight(home, `/\`)
		if home != "" {
			message = strings.ReplaceAll(message, home, "~")
		}
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.ToValidUTF8(message, "�") {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		if unicode.IsSpace(r) {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	clean := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(clean) <= maxMessageRunes {
		return clean
	}
	runes := []rune(clean)
	return string(runes[:maxMessageRunes-1]) + "…"
}

var (
	allowedLevels     = stringSet("info", "error")
	allowedComponents = stringSet("cli", "runtime", "session-state", "notify", "focus", "ai", "resource")
	allowedEvents     = stringSet("command.outcome", "lifecycle.start", "lifecycle.outcome", "session-state.outcome", "notify.transition", "focus.transition", "ai.watcher.transition", "ai.ingest.outcome")
	allowedResults    = stringSet("started", "success", "error")
	allowedKinds      = stringSet("usage", "exit", "runtime")
	allowedBackends   = stringSet("tmux")
	allowedOperations = stringSet(
		string(OperationSessionCreate),
		string(OperationSessionAttach),
		string(OperationSessionSwitch),
		string(OperationSessionKill),
		string(OperationTmuxApply),
		string(OperationSessionStateSave),
		string(OperationSessionStateAutosave),
		string(OperationSessionStateRestore),
		string(OperationSessionStateDelete),
	)
	allowedCodes = stringSet(
		string(CodeSessionCreateFailed),
		string(CodeSessionAttachFailed),
		string(CodeSessionSwitchFailed),
		string(CodeSessionKillFailed),
		string(CodeTmuxApplyFailed),
		string(CodeTmuxApplySocketUnreachable),
		string(CodeTmuxApplyReloadFailed),
		string(CodeTmuxApplyReloadSkipped),
		string(CodeSessionStateSaveFailed),
		string(CodeSessionStateAutosaveFailed),
		string(CodeSessionStateRestoreFailed),
		string(CodeSessionStateDeleteFailed),
		string(CodeNotifyEnqueueFailed),
		string(CodeNotifyDeliveryFailed),
		string(CodeNotifyDeliveryUnavailable),
		string(CodeFocusResolveFailed),
		string(CodeFocusInventoryFailed),
		string(CodeFocusDispatchFailed),
		string(CodeFocusWindowFailed),
		string(CodeFocusPaneFailed),
		string(CodeFocusOutputFailed),
		string(CodeFocusRequestFailed),
	)
	allowedSessionStateSources = stringSet(
		string(SessionStateSourceManual),
		string(SessionStateSourceSettingsLatest),
		string(SessionStateSourceSettingsNamed),
		string(SessionStateSourceAutosave),
		string(SessionStateSourceStartupLatest),
		string(SessionStateSourceStartupNamed),
		string(SessionStateSourcePrune),
	)
	allowedTransitions  = stringSet(string(TransitionNotifyEnqueue), string(TransitionNotifyDelivery), string(TransitionFocusRequest))
	allowedDispositions = stringSet(
		string(DispositionQueued), string(DispositionDeduplicated), string(DispositionDelivered), string(DispositionSuppressed),
		string(DispositionFocused), string(DispositionNotifyOnly), string(DispositionSessionOnly), string(DispositionWindowOnly), string(DispositionFailed),
	)
	allowedProviders = stringSet(
		string(ProviderClaude), string(ProviderCodex), string(ProviderAntigravity), string(ProviderAI), string(ProviderK8s), string(ProviderGit),
		string(ProviderExternal), string(ProviderProjmux), string(ProviderOther),
	)
	allowedAIProviders = stringSet(string(ProviderCodex), string(ProviderClaude), string(ProviderAntigravity), string(ProviderTmuxBell), string(ProviderOther), string(ProviderAI))
	allowedCategories  = stringSet(
		string(CategoryApprovalRequired), string(CategoryInputRequired), string(CategoryResponseComplete), string(CategoryError),
		string(CategorySubagentStopped), string(CategoryTeammateWaiting), string(CategorySelectionRequired), string(CategoryConfirmationRequired),
		string(CategorySessionReady), string(CategorySegmentClick), string(CategoryToastClick), string(CategoryRowSelect), string(CategoryGroupSelect),
		string(CategoryReplyReady), string(CategoryBusyCleared), string(CategoryCustom), string(CategoryOther),
	)
	allowedRoutes = stringSet(
		string(RouteQueue), string(RouteHook), string(RouteNotifySend), string(RouteWSLToast), string(RouteWSLNotifySend),
		string(RouteDisabled), string(RouteDedupe), string(RouteVisiblePane), string(RouteFocusDirect), string(RouteFocusQueue), string(RouteFocusToast),
	)
	allowedAIKinds = stringSet(
		string(AIKindWatcher), string(AIKindPayload), string(AIKindPrompt), string(AIKindPermission), string(AIKindStop),
		string(AIKindNotification), string(AIKindTool), string(AIKindSession), string(AIKindCompact), string(AIKindSubagent),
		string(AIKindTeammate), string(AIKindStatusline), string(AIKindInvocation), string(AIKindLifecycle), string(AIKindBell), string(AIKindUnknown),
	)
	allowedAIResults = stringSet(
		string(AIResultStarted), string(AIResultPaneGone), string(AIResultHookActive), string(AIResultIgnored), string(AIResultFailed),
	)
	allowedAIFailures = stringSet(
		string(AIFailurePayloadInvalid), string(AIFailurePayloadRead), string(AIFailurePayloadOversized), string(AIFailureTargetInvalid),
		string(AIFailureTargetUnmatched), string(AIFailureUnsupportedEvent), string(AIFailureRoute), string(AIFailureWatcherLaunch), string(AIFailureWatcherState),
	)
)

func sanitizeEvent(in Event, home string) (Event, error) {
	out := in
	if _, ok := allowedLevels[out.Level]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics level")
	}
	if _, ok := allowedComponents[out.Component]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics component")
	}
	if _, ok := allowedEvents[out.Event]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics event")
	}
	if _, ok := allowedResults[out.Result]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics result")
	}
	if err := validateEventShape(out); err != nil {
		return Event{}, err
	}
	if _, ok := allowedBackends[out.MuxBackend]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics mux backend")
	}
	if out.Kind != "" {
		if _, ok := allowedKinds[out.Kind]; !ok {
			return Event{}, fmt.Errorf("invalid diagnostics error kind")
		}
	}
	if out.At == "" {
		return Event{}, fmt.Errorf("missing diagnostics timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, out.At); err != nil {
		return Event{}, fmt.Errorf("invalid diagnostics timestamp")
	}
	if out.RunID == "" || len(out.RunID) > 96 || !safeIdentifier(out.RunID) {
		return Event{}, fmt.Errorf("invalid diagnostics run id")
	}
	if out.DurationMS < 0 {
		out.DurationMS = 0
	}
	if len(out.Version) > 64 || !safeVersion(out.Version) {
		return Event{}, fmt.Errorf("invalid diagnostics version")
	}
	class := Classify([]string{out.Command, out.Subcommand})
	if out.Command != "" && class.Command != out.Command {
		return Event{}, fmt.Errorf("unsafe diagnostics command")
	}
	if out.Subcommand != "" && class.Subcommand != out.Subcommand {
		return Event{}, fmt.Errorf("unsafe diagnostics subcommand")
	}
	out.Message = SanitizeMessage(out.Message, home)
	return out, nil
}

func validateEventShape(event Event) error {
	switch event.Event {
	case "command.outcome":
		if event.Result == "started" || event.Operation != "" || event.Code != "" || event.Source != "" || event.hasCounts() || event.hasNotifyFocusFields() || event.hasAIFields() {
			return fmt.Errorf("invalid command outcome shape")
		}
	case "lifecycle.start":
		if event.Result != "started" || event.Level != "info" || event.Operation == "" || event.Code != "" || event.Kind != "" || event.Message != "" || event.Command != "" || event.Subcommand != "" || event.Source != "" || event.hasCounts() || event.hasNotifyFocusFields() || event.hasAIFields() {
			return fmt.Errorf("invalid lifecycle start shape")
		}
		if !runtimeOperation(Operation(event.Operation)) {
			return fmt.Errorf("invalid lifecycle operation")
		}
	case "lifecycle.outcome":
		if event.Result == "started" || event.Operation == "" || event.Command != "" || event.Subcommand != "" || event.Message != "" || event.Source != "" || event.hasCounts() || event.hasNotifyFocusFields() || event.hasAIFields() {
			return fmt.Errorf("invalid lifecycle outcome shape")
		}
		if event.Result == "error" && (event.Level != "error" || event.Kind != "runtime" || event.Code == "") {
			return fmt.Errorf("invalid lifecycle error shape")
		}
		if event.Result == "success" && (event.Level != "info" || event.Kind != "") {
			return fmt.Errorf("invalid lifecycle success shape")
		}
		if event.Result == "success" && event.Code != "" && event.Code != string(CodeTmuxApplyReloadSkipped) {
			return fmt.Errorf("invalid lifecycle success code")
		}
		if event.Result == "error" && event.Code == string(CodeTmuxApplyReloadSkipped) {
			return fmt.Errorf("invalid lifecycle error code")
		}
		if !runtimeOperation(Operation(event.Operation)) {
			return fmt.Errorf("invalid lifecycle operation")
		}
		if !operationAcceptsCode(Operation(event.Operation), Code(event.Code)) {
			return fmt.Errorf("invalid lifecycle operation code")
		}
	case "session-state.outcome":
		if event.Component != "session-state" || event.Result == "started" || event.Operation == "" || event.Command != "" || event.Subcommand != "" || event.Message != "" || event.hasNotifyFocusFields() || event.hasAIFields() {
			return fmt.Errorf("invalid session-state outcome shape")
		}
		if event.Source != "" {
			if _, ok := allowedSessionStateSources[event.Source]; !ok {
				return fmt.Errorf("invalid session-state source")
			}
		}
		operation := Operation(event.Operation)
		if !sessionStateOperation(operation) {
			return fmt.Errorf("invalid session-state operation")
		}
		if event.Result == "error" {
			if event.Level != "error" || event.Kind != "runtime" || event.Code != string(failureCode(operation)) || event.hasCounts() {
				return fmt.Errorf("invalid session-state error shape")
			}
		} else if event.Result == "success" {
			if event.Level != "info" || event.Kind != "" || event.Code != "" {
				return fmt.Errorf("invalid session-state success shape")
			}
			if operation == OperationSessionStateAutosave {
				return fmt.Errorf("invalid session-state autosave success")
			}
			if operation == OperationSessionStateDelete {
				if event.ItemCount == nil || event.hasSnapshotCounts() {
					return fmt.Errorf("invalid session-state delete counts")
				}
			} else if event.ItemCount != nil || !event.hasAllSnapshotCounts() {
				return fmt.Errorf("invalid session-state snapshot counts")
			}
		}
		if !event.nonNegativeCounts() {
			return fmt.Errorf("invalid session-state count")
		}
	case "notify.transition", "focus.transition":
		if event.Command != "" || event.Subcommand != "" || event.Message != "" || event.Operation != "" || event.Source != "" || event.hasCounts() || event.hasAIFields() {
			return fmt.Errorf("invalid notify/focus transition shape")
		}
		if _, ok := allowedTransitions[event.Transition]; !ok {
			return fmt.Errorf("invalid notify/focus transition")
		}
		if _, ok := allowedDispositions[event.Disposition]; !ok {
			return fmt.Errorf("invalid notify/focus disposition")
		}
		if _, ok := allowedProviders[event.Provider]; !ok {
			return fmt.Errorf("invalid notify/focus provider")
		}
		if _, ok := allowedCategories[event.Category]; !ok {
			return fmt.Errorf("invalid notify/focus category")
		}
		if _, ok := allowedRoutes[event.Route]; !ok {
			return fmt.Errorf("invalid notify/focus route")
		}
		if event.Event == "notify.transition" {
			if event.Component != "notify" || event.Transition == string(TransitionFocusRequest) {
				return fmt.Errorf("invalid notify transition family")
			}
		} else if event.Component != "focus" || event.Transition != string(TransitionFocusRequest) {
			return fmt.Errorf("invalid focus transition family")
		}
		if !notifyFocusTupleMatches(event) {
			return fmt.Errorf("invalid notify/focus transition tuple")
		}
		if event.Disposition == string(DispositionFailed) {
			if event.Level != "error" || event.Result != "error" || event.Kind != "runtime" || event.Code == "" {
				return fmt.Errorf("invalid notify/focus error shape")
			}
		} else if event.Level != "info" || event.Result != "success" || event.Kind != "" || event.Code != "" {
			return fmt.Errorf("invalid notify/focus success shape")
		}
		if !notifyFocusCodeMatches(event) {
			return fmt.Errorf("invalid notify/focus code")
		}
	case "ai.watcher.transition", "ai.ingest.outcome":
		if event.Component != "ai" || event.Command != "" || event.Subcommand != "" || event.Message != "" || event.Operation != "" || event.Code != "" || event.Source != "" || event.hasCounts() || event.Transition != "" || event.Disposition != "" || event.Category != "" || event.Route != "" {
			return fmt.Errorf("invalid ai diagnostic shape")
		}
		if _, ok := allowedAIProviders[event.Provider]; !ok {
			return fmt.Errorf("invalid ai provider")
		}
		if _, ok := allowedAIKinds[event.AIKind]; !ok {
			return fmt.Errorf("invalid ai event kind")
		}
		if _, ok := allowedAIResults[event.AIResult]; !ok {
			return fmt.Errorf("invalid ai result")
		}
		if event.Failure != "" {
			if _, ok := allowedAIFailures[event.Failure]; !ok {
				return fmt.Errorf("invalid ai failure")
			}
		}
		if !aiTupleMatches(event) {
			return fmt.Errorf("invalid ai diagnostic tuple")
		}
	}
	if event.Operation != "" {
		if _, ok := allowedOperations[event.Operation]; !ok {
			return fmt.Errorf("invalid diagnostics operation")
		}
	}
	if event.Code != "" {
		if _, ok := allowedCodes[event.Code]; !ok {
			return fmt.Errorf("invalid diagnostics code")
		}
	}
	return nil
}

func notifyFocusTupleMatches(event Event) bool {
	disposition := Disposition(event.Disposition)
	route := Route(event.Route)
	switch Transition(event.Transition) {
	case TransitionNotifyEnqueue:
		if route != RouteQueue {
			return false
		}
		return disposition == DispositionQueued || disposition == DispositionDeduplicated || disposition == DispositionFailed
	case TransitionNotifyDelivery:
		switch route {
		case RouteDisabled, RouteDedupe, RouteVisiblePane:
			return disposition == DispositionSuppressed
		case RouteHook, RouteNotifySend, RouteWSLToast, RouteWSLNotifySend:
			return disposition == DispositionDelivered || disposition == DispositionFailed
		default:
			return false
		}
	case TransitionFocusRequest:
		switch route {
		case RouteFocusDirect, RouteFocusQueue, RouteFocusToast:
			switch disposition {
			case DispositionFocused, DispositionNotifyOnly, DispositionSessionOnly, DispositionWindowOnly, DispositionFailed:
				return true
			}
		}
	}
	return false
}

func notifyFocusCodeMatches(event Event) bool {
	code := Code(event.Code)
	if event.Disposition != string(DispositionFailed) {
		return code == ""
	}
	switch event.Transition {
	case string(TransitionNotifyEnqueue):
		return code == CodeNotifyEnqueueFailed
	case string(TransitionNotifyDelivery):
		if code == CodeNotifyDeliveryUnavailable {
			return event.Route == string(RouteNotifySend)
		}
		return code == CodeNotifyDeliveryFailed
	case string(TransitionFocusRequest):
		switch code {
		case CodeFocusResolveFailed, CodeFocusInventoryFailed, CodeFocusDispatchFailed, CodeFocusWindowFailed, CodeFocusPaneFailed, CodeFocusOutputFailed, CodeFocusRequestFailed:
			return true
		}
	}
	return false
}

func runtimeOperation(operation Operation) bool {
	switch operation {
	case OperationSessionCreate, OperationSessionAttach, OperationSessionSwitch, OperationSessionKill, OperationTmuxApply:
		return true
	default:
		return false
	}
}

func (event Event) hasSnapshotCounts() bool {
	return event.WindowCount != nil || event.PaneCount != nil || event.ShellRecipeCount != nil || event.AgentRecipeCount != nil || event.StartupRecipeCount != nil
}

func (event Event) hasAllSnapshotCounts() bool {
	return event.WindowCount != nil && event.PaneCount != nil && event.ShellRecipeCount != nil && event.AgentRecipeCount != nil && event.StartupRecipeCount != nil
}

func (event Event) hasCounts() bool { return event.hasSnapshotCounts() || event.ItemCount != nil }

func (event Event) hasNotifyFocusFields() bool {
	return event.Transition != "" || event.Disposition != "" || event.Provider != "" || event.Category != "" || event.Route != ""
}

func (event Event) hasAIFields() bool {
	return event.AIKind != "" || event.AIResult != "" || event.Failure != ""
}

func (event Event) nonNegativeCounts() bool {
	for _, value := range []*int{event.WindowCount, event.PaneCount, event.ShellRecipeCount, event.AgentRecipeCount, event.StartupRecipeCount, event.ItemCount} {
		if value != nil && *value < 0 {
			return false
		}
	}
	return true
}

func operationAcceptsCode(operation Operation, code Code) bool {
	if code == "" {
		return true
	}
	switch operation {
	case OperationSessionCreate:
		// Ensure/restore may create and then activate the session.
		return code == CodeSessionCreateFailed || code == CodeSessionAttachFailed || code == CodeSessionSwitchFailed
	case OperationSessionAttach:
		// Attached-session kill flows first move to a fallback, then kill.
		return code == CodeSessionAttachFailed || code == CodeSessionKillFailed
	case OperationSessionSwitch:
		// In-tmux kill flows first move to a fallback, then kill.
		return code == CodeSessionSwitchFailed || code == CodeSessionKillFailed
	case OperationSessionKill:
		// Auto-attach may prune, ensure/create a fallback, then activate it.
		return code == CodeSessionKillFailed || code == CodeSessionCreateFailed || code == CodeSessionAttachFailed || code == CodeSessionSwitchFailed
	case OperationTmuxApply:
		return code == CodeTmuxApplyFailed || code == CodeTmuxApplySocketUnreachable || code == CodeTmuxApplyReloadFailed || code == CodeTmuxApplyReloadSkipped
	default:
		return false
	}
}

// ValidLevel reports whether value is in the stable severity allowlist.
func ValidLevel(value string) bool {
	_, ok := allowedLevels[value]
	return ok
}

func safeIdentifier(value string) bool {
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func safeVersion(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func stringSet(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}
