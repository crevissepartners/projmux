package claudesettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/hookcatalog"
)

// SettingsRelativePath is Claude Code's user settings file under the home
// directory.
const SettingsRelativePath = ".claude/settings.json"

// The commands and markers of the hooks projmux writes into the settings
// file. A marker inside a command is how integration recognizes its own
// wiring, since JSON settings cannot carry comments.
const (
	HookManagedMarker  = "projmux-managed:claude-hook:v1"
	CanonicalHookRoute = "projmux internal agent-hook ingest claude-hook"
	HookCommand        = CanonicalHookRoute + hookcatalog.PaneArgument + " >/dev/null 2>&1 || true # " + HookManagedMarker

	// RegistrationHookCommand execs the hook shell: the registration
	// process's direct parent must be the exact activation-exec process after
	// it exec'd Claude. An unmanaged nested Claude may inherit the private
	// activation environment, but cannot pass this producer check for its own
	// SessionStart.
	RegistrationHookCommand = "exec projmux internal claude-endpoint-register >/dev/null 2>&1 # " + HookManagedMarker

	// QuestionManagedMarker owns the one AskUserQuestion PreToolUse entry
	// that lets `projmux agent question answer` answer an opted-in Claude
	// Agent's question. Unlike the ingest command, its stdout reaches Claude
	// Code: that is where the answer is handed over.
	QuestionManagedMarker = "projmux-managed:claude-question:v1"
	// QuestionHookRoute is the hidden `internal` route Claude Code runs as a
	// PreToolUse hook for AskUserQuestion.
	QuestionHookRoute   = "claude-question-hook"
	QuestionHookCommand = "exec projmux internal " + QuestionHookRoute + hookcatalog.PaneArgument + " 2>/dev/null # " + QuestionManagedMarker
	// QuestionHookMatcher limits the entry to the one tool it answers.
	QuestionHookMatcher = "AskUserQuestion"
	// QuestionStatusMessage is what Claude Code shows while the hook holds
	// the question.
	QuestionStatusMessage = "Question held for a projmux answer (projmux agent question answer); Esc declines"

	// PermissionManagedMarker owns the one PermissionRequest entry that lets
	// `projmux agent approval answer` allow or deny a projmux Claude Agent's
	// permission request. It is a separate entry from the ingest command on
	// the same event, which discards its stdout; this one's stdout reaches
	// Claude Code, because that is where the decision is handed over.
	PermissionManagedMarker = "projmux-managed:claude-permission:v1"
	// PermissionHookRoute is the hidden `internal` route Claude Code runs as a
	// PermissionRequest hook.
	PermissionHookRoute   = "claude-permission-hook"
	PermissionHookCommand = "exec projmux internal " + PermissionHookRoute + hookcatalog.PaneArgument + " 2>/dev/null # " + PermissionManagedMarker

	CoordinationHookTimeout        = 5 * time.Second
	PriorCoordinationManagedMarker = "projmux-managed:claude-coordination:v1"
	PriorCoordinationV2Marker      = "projmux-managed:claude-coordination:v2"
	CoordinationManagedMarker      = "projmux-managed:claude-coordination:v3"
	CoordinationHookCommand        = "exec projmux internal claude-message-reply >/dev/null 2>&1 # " + CoordinationManagedMarker
	CoordinationBoundaryCommand    = "exec projmux internal claude-message-boundary >/dev/null 2>&1 # " + CoordinationManagedMarker
)

// LegacyHookRoute is the removed ingest route a marker-owned v0.10.1 file
// still carries.
var LegacyHookRoute = hookcatalog.LegacyIngestCommand("claude-hook")

// Plan is what integrating Claude Code hooks would do to the settings file:
// its current bytes, the bytes it would hold, and the action the command
// reports. Conflict names an unmanaged projmux hook that refuses an install.
// Exists separates a file that does not exist from an empty one.
type Plan struct {
	Path     string
	Current  string
	Next     string
	Action   string
	Changed  bool
	Conflict string
	Exists   bool
}

// ReadState reads the settings file at path with readFile (os.ReadFile when
// nil) and reports whether it exists; a missing file reads as "".
func ReadState(path string, readFile func(string) ([]byte, error)) (string, bool, error) {
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err == nil {
		return string(data), true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("read Claude settings %s: %w", path, err)
}

// PlanHooks plans the managed hooks of current, the settings at path:
// removing them, or reinstalling every event installEvents names (read only
// when an install is planned and nothing conflicts) and, with
// includeCoordination, the coordination, question and permission entries.
// The returned Plan leaves Exists for the caller, which read the file.
func PlanHooks(remove, includeCoordination bool, path, current string, installEvents func() ([]string, error)) (Plan, error) {
	settings, err := ParseSettings(current, path)
	if err != nil {
		return Plan{}, err
	}
	hooks, err := settingsHooks(settings, false)
	if err != nil {
		return Plan{}, err
	}
	cleaned := false
	conflict := ""
	if hooks != nil {
		for event, value := range hooks {
			nextEntries, removed, eventConflict, err := entriesWithoutManaged(value, event, path)
			if err != nil {
				return Plan{}, err
			}
			cleaned = cleaned || removed
			if eventConflict != "" && conflict == "" {
				conflict = eventConflict
			}
			if len(nextEntries) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = nextEntries
			}
		}
	}

	plan := Plan{Path: path, Current: current, Conflict: conflict}
	if remove {
		next, err := EncodeSettings(settings)
		if err != nil {
			return Plan{}, err
		}
		plan.Next = next
		plan.Changed = cleaned && next != current
		if cleaned {
			plan.Action = "removed projmux-managed Claude Code hook wiring from " + path
		} else {
			plan.Action = "no changes: projmux-managed Claude Code hook wiring is not present in " + path
		}
		return plan, nil
	}
	if conflict != "" {
		plan.Action = "would refuse to install Claude Code hook wiring"
		plan.Next = current
		return plan, nil
	}
	hookEvents, err := installEvents()
	if err != nil {
		return Plan{}, err
	}

	hooks, err = settingsHooks(settings, true)
	if err != nil {
		return Plan{}, err
	}
	for _, event := range hookEvents {
		entry := HookManagedEntry()
		if event == "SessionStart" {
			entry["hooks"] = append(entry["hooks"].([]any), map[string]any{
				"type": "command", "command": RegistrationHookCommand, "timeout": 5,
			})
		}
		hooks[event] = append(entrySlice(hooks[event]), entry)
	}
	if includeCoordination {
		// Ingress is an immediate helper push. These short compatibility
		// callbacks observe turn boundaries; Stop text has no reply authority.
		// Only an authenticated explicit public reply can commit a peer response.
		hooks["Stop"] = append(entrySlice(hooks["Stop"]), coordinationManagedEntry())
		hooks["UserPromptSubmit"] = append(entrySlice(hooks["UserPromptSubmit"]), coordinationBoundaryManagedEntry())
		// The question channel rides on explicit integration only, like the
		// coordination callbacks above. It holds nothing open for an Agent that
		// was not opted in with `projmux agent question enable`.
		hooks["PreToolUse"] = append(entrySlice(hooks["PreToolUse"]), questionManagedEntry())
		// The permission hook is its own PermissionRequest entry next to the
		// ingest entry. It decides nothing unless agent-approval-answering is
		// `projmux`.
		hooks["PermissionRequest"] = append(entrySlice(hooks["PermissionRequest"]), permissionManagedEntry())
	}
	next, err := EncodeSettings(settings)
	if err != nil {
		return Plan{}, err
	}
	plan.Next = next
	plan.Changed = next != current
	if plan.Changed {
		plan.Action = "configured Claude Code hooks in " + path
	} else {
		plan.Action = "no changes: Claude Code hooks are already configured in " + path
	}
	return plan, nil
}
// ParseSettings parses settings; blank content is an empty object.
func ParseSettings(content, path string) (map[string]any, error) {
	if strings.TrimSpace(content) == "" {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(content), &settings); err != nil {
		return nil, fmt.Errorf("parse Claude settings %s: %w", path, err)
	}
	if settings == nil {
		return map[string]any{}, nil
	}
	return settings, nil
}
func settingsHooks(settings map[string]any, create bool) (map[string]any, error) {
	value, ok := settings["hooks"]
	if !ok {
		if !create {
			return nil, nil
		}
		hooks := map[string]any{}
		settings["hooks"] = hooks
		return hooks, nil
	}
	hooks, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("Claude settings hooks field must be a JSON object")
	}
	return hooks, nil
}
func entriesWithoutManaged(value any, event, path string) ([]any, bool, string, error) {
	if value == nil {
		return nil, false, "", nil
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, false, "", fmt.Errorf("Claude settings %s hook event %s must be an array", path, event)
	}
	out := make([]any, 0, len(entries))
	removed := false
	conflict := ""
	for _, entryValue := range entries {
		entry, ok := entryValue.(map[string]any)
		if !ok {
			return nil, false, "", fmt.Errorf("Claude settings %s hook event %s contains a non-object matcher entry", path, event)
		}
		hookValues, ok := entry["hooks"].([]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		nextHooks := make([]any, 0, len(hookValues))
		removedQuestion := false
		for _, hookValue := range hookValues {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				nextHooks = append(nextHooks, hookValue)
				continue
			}
			command, _ := hook["command"].(string)
			if hook["type"] == "command" && strings.Contains(command, QuestionManagedMarker) {
				removed, removedQuestion = true, true
				continue
			}
			if hook["type"] == "command" && (strings.Contains(command, HookManagedMarker) || strings.Contains(command, PermissionManagedMarker) ||
				strings.Contains(command, CoordinationManagedMarker) || strings.Contains(command, PriorCoordinationV2Marker) || strings.Contains(command, PriorCoordinationManagedMarker)) {
				removed = true
				continue
			}
			if hook["type"] == "command" && (strings.Contains(command, LegacyHookRoute) || strings.Contains(command, CanonicalHookRoute) ||
				strings.Contains(command, "internal claude-message-wait") || strings.Contains(command, "internal claude-message-reply") || strings.Contains(command, "internal claude-message-boundary") || strings.Contains(command, "internal "+QuestionHookRoute) || strings.Contains(command, "internal "+PermissionHookRoute)) && conflict == "" {
				conflict = fmt.Sprintf("Claude Code hook %s already contains unmanaged projmux ingest command in %s: %s", event, path, command)
			}
			nextHooks = append(nextHooks, hookValue)
		}
		if len(nextHooks) == 0 && len(entry) == 1 {
			continue
		}
		// The question entry is the one managed entry that carries a matcher.
		// Once its command is gone the matcher alone matches nothing, so the
		// entry goes with it.
		if len(nextHooks) == 0 && removedQuestion && entryIsHooksAndMatcherOnly(entry) {
			continue
		}
		if len(nextHooks) == 0 {
			delete(entry, "hooks")
		} else {
			entry["hooks"] = nextHooks
		}
		out = append(out, entry)
	}
	return out, removed, conflict, nil
}
func entrySlice(value any) []any {
	if entries, ok := value.([]any); ok {
		return entries
	}
	return nil
}
// HookManagedEntry is the ingest entry every installed event gets.
func HookManagedEntry() map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": HookCommand,
			},
		},
	}
}

// entryIsHooksAndMatcherOnly reports whether a matcher entry holds
// nothing but its hooks and its matcher.
func entryIsHooksAndMatcherOnly(entry map[string]any) bool {
	for key := range entry {
		if key != "hooks" && key != "matcher" {
			return false
		}
	}
	return true
}

// questionManagedEntry is the AskUserQuestion PreToolUse entry. Its
// command's stdout is not discarded: an answered question's decision is printed
// there. Its timeout is the fixed ceiling config.AgentQuestionHookTimeoutSeconds,
// never read from the window file: the hook rereads the window for every
// question and ends the wait itself, so a changed window applies to the next
// question without re-running integrate. An entry an older projmux installed
// with window plus the margin is removed by its marker and rewritten with the
// ceiling on the next integrate.
func questionManagedEntry() map[string]any {
	return map[string]any{
		"matcher": QuestionHookMatcher,
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       QuestionHookCommand,
				"timeout":       config.AgentQuestionHookTimeoutSeconds,
				"statusMessage": QuestionStatusMessage,
			},
		},
	}
}

// permissionManagedEntry is the PermissionRequest entry of the
// permission hook. It carries no matcher, so it sees every tool's request. Its
// timeout is the fixed ceiling config.AgentApprovalHookTimeoutSeconds, the
// longest window plus a margin, never read from the window file: the hook
// rereads the window for every request and ends the wait itself.
func permissionManagedEntry() map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": PermissionHookCommand,
				"timeout": config.AgentApprovalHookTimeoutSeconds,
			},
		},
	}
}
func coordinationManagedEntry() map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": CoordinationHookCommand,
				"timeout": int(CoordinationHookTimeout / time.Second),
			},
		},
	}
}
func coordinationBoundaryManagedEntry() map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": CoordinationBoundaryCommand,
				"timeout": 5,
			},
		},
	}
}
// EncodeSettings is settings as the command writes it: two-space indented
// JSON with HTML left unescaped and a trailing newline.
func EncodeSettings(settings map[string]any) (string, error) {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(settings); err != nil {
		return "", fmt.Errorf("encode Claude settings: %w", err)
	}
	return out.String(), nil
}
