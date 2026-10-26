package app

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	localstate "github.com/crevissepartners/projmux/internal/state"
	"golang.org/x/sys/unix"
)

const claudeTranscriptTailLimit = 256 * 1024

type claudeHookPayload struct {
	AgentID                 string
	AgentType               string
	BackgroundTasksInFlight *int
	EventName               string
	SessionID               string
	CWD                     string
	TranscriptPath          string
	NotificationType        string
	Message                 string
	Prompt                  string
	ToolName                string
	ToolUseID               string
	ToolInput               map[string]any
	ErrorType               string
	ErrorMessage            string
	SubagentType            string
	SubagentID              string
	TeammateName            string
	TeammateID              string
	TeammateContext         string
}

func (c *aiCommand) ingestClaudeHook(data []byte, explicitPane string) error {
	payload, err := parseClaudeHookPayload(data)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Result: "error", Reason: aiIngestFailureReason(aiIngestReasonHookPayloadInvalid, err)})
		return err
	}

	identity, matchReason := c.matchAIHookPane(aiPaneMatchInput{
		ExplicitPane: explicitPane,
		Provider:     aiModeClaude,
		CWD:          payload.CWD,
		SessionID:    payload.SessionID,
	})
	paneID := identity.paneID
	if paneID == "" {
		c.appendAIIngestLog(claudeHookLogEntry("", payload, "ignored", aiIngestRecordReason(matchReason)))
		return nil
	}
	c = c.withAIHookPaneRoute(identity)
	defer c.flushPendingAgentSessionRef(paneID)

	binding, owned, markers := c.markAIHookPaneBinding(paneID, aiModeClaude, payload.CWD, "", payload.SessionID, payload.TranscriptPath)
	metadata := payload.claudeMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderClaude, payload.EventName)

	switch payload.EventName {
	case "SessionStart":
		if _, _, err := c.persistManagedAgentStartupReadiness(paneID, aiModeClaude); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonReadinessWriteFailed, err)))
			return err
		}
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	case "UserPromptSubmit":
		return c.ingestClaudeUserPromptSubmit(paneID, payload, metadata, action)
	case "Notification":
		return c.ingestClaudeNotification(paneID, payload, metadata, action)
	case "PermissionRequest":
		return c.ingestClaudePermissionRequest(paneID, payload, metadata, action)
	case "Stop":
		return c.ingestClaudeStop(paneID, payload, metadata, action, binding, owned)
	case "StopFailure":
		return c.ingestClaudeStopFailure(paneID, payload, metadata, action)
	case "SubagentStop":
		return c.ingestClaudeSubagentStop(paneID, payload, metadata, action)
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied", "ElicitationResult":
		if payload.EventName == "PostToolUse" || payload.EventName == "PostToolUseFailure" {
			c.closeClaudePermissionAnsweredInTerminal(data, payload)
			if answered, err := c.ingestClaudeSubagentApprovalAnswered(paneID, payload, metadata, action, markers); answered {
				return err
			}
		}
		return c.ingestClaudeOperatorDialogClosed(paneID, payload, metadata, action, binding, owned)
	case "PreToolUse", "PostToolBatch", "SubagentStart":
		if c.claudeBackgroundFallback(payload, action, binding, owned) {
			return c.ingestClaudeBackgroundProgress(paneID, payload, metadata, binding, owned)
		}
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	case "UserPromptExpansion", "PreCompact", "PostCompact", "SessionEnd", "Setup", "TaskCreated", "TaskCompleted", "Elicitation", "ConfigChange", "InstructionsLoaded", "WorktreeCreate", "WorktreeRemove", "CwdChanged", "FileChanged":
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	case "TeammateIdle":
		return c.ingestClaudeTeammateIdle(paneID, payload, metadata, action)
	default:
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	}
}

func (c *aiCommand) ingestClaudeUserPromptSubmit(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
		return nil
	}
	if err := c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
		Metadata:  metadata,
		BadgeKind: aiBadgeKindInProgress,
	}); err != nil {
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
		return err
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", ""))
	return nil
}

// ingestClaudeOperatorDialogClosed handles the events that follow an operator
// answer: a tool ran or failed after its permission dialog, a permission was
// denied, or an MCP elicitation returned. Only an Agent still recorded as
// awaiting its operator moves to in_progress, state only; every other Agent
// stays quiet unless current subagent activity repairs a completed projection.
// PostToolUse fires on every tool call, so the
// judgment reads only the binding the hook marking already loaded.
func (c *aiCommand) ingestClaudeOperatorDialogClosed(paneID string, payload claudeHookPayload, metadata map[string]string,
	action aiHookActionResolution, binding managedAgentBinding, owned bool,
) error {
	if !owned || binding.agent.Spec.Provider != aiModeClaude || !claudeAgentAwaitsOperator(binding.agent, c.sessionRefClock()()) {
		if (payload.EventName == "PostToolUse" || payload.EventName == "PostToolUseFailure") && c.claudeBackgroundFallback(payload, action, binding, owned) {
			return c.ingestClaudeBackgroundProgress(paneID, payload, metadata, binding, owned)
		}
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	}
	if err := c.applyAIStatusStateOnly("thinking", paneID, attentionNotifyInput{
		Metadata:  metadata,
		BadgeKind: aiBadgeKindInProgress,
	}); err != nil {
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
		return err
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", ""))
	return nil
}

func (c *aiCommand) ingestClaudeNotification(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
		return nil
	}
	body := formatClaudeNotificationNotifyBody(payload)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:        claudeNotifyID(payload),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindForNotifyCategory(body.Category),
	})
}

func (c *aiCommand) ingestClaudePermissionRequest(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
		return nil
	}
	body := formatClaudePermissionNotifyBody(payload)
	input := attentionNotifyInput{
		ID:        claudePermissionNotifyID(payload),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindApprovalRequired,
	}
	// The set and the approval projection change together under the Pane's
	// approval lock, so a concurrent request or answer always reads the other's
	// finished state.
	var emitErr error
	lockErr := c.withClaudeApprovalLock(paneID, func(current aiHookPaneMarkers) {
		c.recordClaudeApprovalAgentID(paneID, payload.AgentID, current)
		emitErr = c.emitClaudeHookStatus(paneID, payload, action, input)
	})
	if lockErr == nil {
		return emitErr
	}
	// Without the lock the approval is still projected, after the fence: a
	// holder may be judging an older read and overwrite the set, but it never
	// writes the fence, and an answer that wrote busy reads the fence afterwards.
	c.noteAIPaneMarkerWriteFailure(c.setAIPaneOption(paneID, aiPaneApprovalFenceOption, claudeApprovalFenceSet))
	if err := c.emitClaudeHookStatus(paneID, payload, action, input); err != nil {
		return err
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestReasonSubagentApprovalLocked))
	return nil
}

// The pending subagent approvals are a bounded set of agent_ids on an internal
// Pane option. It says which subagents' requests the current approval_required
// projection is waiting on, so a tool that runs under one of them after its
// answer can tell whether anything is still waiting. A Pane that cannot say
// that holds the untracked value, and an untracked or absent set never clears
// a badge: the projection then keeps its earlier behavior.
const (
	claudeApprovalAgentIDsLimit     = 8
	claudeApprovalAgentIDsUntracked = "*"
	claudeApprovalAgentIDsSeparator = ","
)

func claudeApprovalAgentIDTrackable(agentID string) bool {
	if agentID == "" || len(agentID) > 128 {
		return false
	}
	for _, r := range agentID {
		if r <= ' ' || r == 0x7f || r == ',' || r == '*' {
			return false
		}
	}
	return true
}

// parseClaudeApprovalAgentIDs returns the tracked set; tracked is false for
// the untracked value and anything this code did not write.
func parseClaudeApprovalAgentIDs(value string) (ids []string, tracked bool) {
	if value == "" {
		return nil, true
	}
	ids = strings.Split(value, claudeApprovalAgentIDsSeparator)
	if len(ids) > claudeApprovalAgentIDsLimit {
		return nil, false
	}
	for _, id := range ids {
		if !claudeApprovalAgentIDTrackable(id) {
			return nil, false
		}
	}
	return ids, true
}

// claudeApprovalLockWaitLimit bounds how long an approval hook waits for the
// Pane's approval lock. The holder re-reads the markers, updates the set and
// applies one projection; a request's projection includes its notification,
// so the hold is not bounded by this limit. The hook itself runs synchronously
// inside the provider, so a slow holder must not stall it: a request that
// gives up sets the approval fence instead.
const claudeApprovalLockWaitLimit = 2 * time.Second

// The approval fence is a Pane option only a request that could not take the
// approval lock sets, before it projects approval_required. No lock holder
// writes it, so a holder judging an older read cannot erase it. While it is
// set, the Pane is untracked: no answer clears the badge, and an answer that
// already wrote busy restores the approval when it reads the fence afterwards.
// Only a main-thread request, which empties the set under the lock, clears it.
const claudeApprovalFenceSet = "1"

// claudeApprovalLockBuckets bounds the lock files: tmux Pane ids only grow, so
// one file per Pane would grow without end, and lock files are never removed.
// Each Pane maps to exactly one bucket, so its hooks are always serialized;
// unrelated Panes that share a bucket only wait for each other's hold.
const claudeApprovalLockBuckets = 64

type claudeApprovalLockHooks struct {
	contended func()
	afterRead func()
}

// claudeApprovalLockPath names the lock of the exact Pane the hook writes: the
// tmux route its option writes use, and the Pane id on that server.
func (c *aiCommand) claudeApprovalLockPath(paneID string) (string, error) {
	stateDir, err := c.aiStateDir()
	if err != nil {
		return "", err
	}
	route, refusal := c.aiPaneOptionRoute(paneID)
	if refusal != nil {
		return "", errAIPaneWriteUnavailable
	}
	sum := sha256.Sum256([]byte(strings.Join(append(slices.Clone(route.transport.Args()), paneID), "\x00")))
	bucket := binary.BigEndian.Uint32(sum[:4]) % claudeApprovalLockBuckets
	return filepath.Join(stateDir, "claude-approval-locks", fmt.Sprintf("pane-%02d.lock", bucket)), nil
}

// withClaudeApprovalLock runs fn under the Pane's approval lock with markers
// re-read inside it, so fn judges the Pane as the previous holder left it
// rather than as the hook's earlier snapshot saw it.
func (c *aiCommand) withClaudeApprovalLock(paneID string, fn func(aiHookPaneMarkers)) error {
	path, err := c.claudeApprovalLockPath(paneID)
	if err != nil {
		return err
	}
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	var contended func()
	if c.claudeApprovalLockHooks != nil {
		contended = c.claudeApprovalLockHooks.contended
	}
	lock, err := localstate.AcquireFileLock(path, claudeApprovalLockWaitLimit, contended)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}()
	current := c.readAIHookPaneMarkers(paneID)
	if c.claudeApprovalLockHooks != nil && c.claudeApprovalLockHooks.afterRead != nil {
		c.claudeApprovalLockHooks.afterRead()
	}
	fn(current)
	return nil
}

// recordClaudeApprovalAgentID updates the pending set before a PermissionRequest
// projects approval_required, judging the markers read under the approval
// lock. A main-thread request always tries to empty the set and clear the
// approval fence, so no subagent answer can clear the badge it raises. A
// subagent request starts a new set when the Pane was not already awaiting
// approval, joins a tracked set when it was, and otherwise leaves the Pane
// untracked: an approval wait the set does not account for, an overflow, an id
// it cannot hold, or a Pane whose markers could not be read.
func (c *aiCommand) recordClaudeApprovalAgentID(paneID, agentID string, markers aiHookPaneMarkers) {
	current, known := markers.value(aiPaneApprovalAgentIDsOption)
	if agentID == "" {
		if !known || current != "" {
			c.noteAIPaneMarkerWriteFailure(c.clearAIPaneOption(paneID, aiPaneApprovalAgentIDsOption))
		}
		if fence, known := markers.value(aiPaneApprovalFenceOption); !known || fence != "" {
			c.noteAIPaneMarkerWriteFailure(c.clearAIPaneOption(paneID, aiPaneApprovalFenceOption))
		}
		return
	}
	next := claudeApprovalAgentIDsUntracked
	if badge, _ := markers.value(aiPaneBadgeKindOption); known && claudeApprovalAgentIDTrackable(agentID) {
		if normalizeAIBadgeKind(badge) != aiBadgeKindApprovalRequired {
			next = agentID
		} else if ids, tracked := parseClaudeApprovalAgentIDs(current); tracked && len(ids) > 0 {
			if !slices.Contains(ids, agentID) {
				ids = append(ids, agentID)
			}
			if len(ids) <= claudeApprovalAgentIDsLimit {
				next = strings.Join(ids, claudeApprovalAgentIDsSeparator)
			}
		}
	}
	if !known || current != next {
		c.recordAIPaneOption(paneID, aiPaneApprovalAgentIDsOption, next)
	}
}

// claudeApprovalFenced reports whether a request that could not take the
// approval lock may still be waiting: the fence is set, or the read failed.
func claudeApprovalFenced(markers aiHookPaneMarkers) bool {
	fence, known := markers.value(aiPaneApprovalFenceOption)
	return !known || fence != ""
}

// ingestClaudeSubagentApprovalAnswered handles a PostToolUse or
// PostToolUseFailure under a subagent whose permission request the Pane still
// awaits: its request was answered. Only an event whose snapshot shows an
// approval wait takes the approval lock; under it the markers are read again,
// the agent_id leaves the set, and when the set is empty the projection returns
// to busy, state only. Otherwise it stays awaiting approval for the subagents
// that still wait, and so does a tool under a subagent the set does not hold
// while others still wait. answered is false for every other event, which
// keeps its existing handling: main-thread tools, an untracked or empty set
// without a fence, a Pane no longer awaiting approval, and explicit runtime
// quiet overrides. A lock that cannot be taken, a failed read under it, or an
// approval fence leaves the badge as it is.
func (c *aiCommand) ingestClaudeSubagentApprovalAnswered(paneID string, payload claudeHookPayload, metadata map[string]string,
	action aiHookActionResolution, markers aiHookPaneMarkers,
) (answered bool, err error) {
	if payload.AgentID == "" || (action.Action == aiHookActionQuiet && action.Source == aiHookActionSourceRuntime) {
		return false, nil
	}
	if badge, known := markers.value(aiPaneBadgeKindOption); !known || normalizeAIBadgeKind(badge) != aiBadgeKindApprovalRequired {
		return false, nil
	}
	lockErr := c.withClaudeApprovalLock(paneID, func(current aiHookPaneMarkers) {
		// Once the snapshot showed an approval wait, this decides the event
		// unless the wait is one the set does not track: a fence or a failed
		// read, or a set still waiting on other subagents, keeps the badge
		// rather than letting the operator-answer handler clear it.
		badge, known := current.value(aiPaneBadgeKindOption)
		if !known || claudeApprovalFenced(current) {
			answered = true
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", aiIngestReasonSubagentApprovalFenced))
			return
		}
		if normalizeAIBadgeKind(badge) != aiBadgeKindApprovalRequired {
			return
		}
		pending, _ := current.value(aiPaneApprovalAgentIDsOption)
		ids, tracked := parseClaudeApprovalAgentIDs(pending)
		if !tracked || len(ids) == 0 {
			return
		}
		answered = true
		if !slices.Contains(ids, payload.AgentID) {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", aiIngestReasonSubagentApprovalPending))
			return
		}
		remaining := slices.DeleteFunc(ids, func(id string) bool { return id == payload.AgentID })
		if len(remaining) > 0 {
			c.recordAIPaneOption(paneID, aiPaneApprovalAgentIDsOption, strings.Join(remaining, claudeApprovalAgentIDsSeparator))
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", aiIngestReasonSubagentApprovalPending))
			return
		}
		c.noteAIPaneMarkerWriteFailure(c.clearAIPaneOption(paneID, aiPaneApprovalAgentIDsOption))
		if err = c.applyAIStatusStateOnly("thinking", paneID, attentionNotifyInput{
			Metadata:  metadata,
			BadgeKind: aiBadgeKindInProgress,
		}); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
			return
		}
		// A request that gave up on the lock sets the fence before it projects
		// approval_required. tmux applies commands in order, so if this read
		// misses the fence, that projection lands after busy; if it finds it,
		// the approval is restored here. Either way the request's wait stays.
		if claudeApprovalFenced(c.readAIHookPaneMarkers(paneID)) {
			if err = c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				Metadata:  metadata,
				BadgeKind: aiBadgeKindApprovalRequired,
			}); err != nil {
				c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
				return
			}
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", aiIngestReasonSubagentApprovalFenced))
			return
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", aiIngestReasonSubagentApprovalAnswered))
	})
	if lockErr != nil {
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", aiIngestReasonSubagentApprovalLocked))
		return true, nil
	}
	return answered, err
}

func (c *aiCommand) ingestClaudeStop(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution, binding managedAgentBinding, owned bool) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
		return nil
	}
	if payload.BackgroundTasksInFlight != nil && *payload.BackgroundTasksInFlight > 0 {
		if owned && claudeAgentAwaitsOperator(binding.agent, c.sessionRefClock()()) {
			c.quietClaudeHook(paneID, payload, "")
			return nil
		}
		return c.ingestClaudeBackgroundProgress(paneID, payload, metadata, binding, owned)
	}
	message := readClaudeTranscriptLastAssistantText(payload.TranscriptPath)
	body := formatClaudeStopNotifyBody(message)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:        claudeStopNotifyID(payload),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindResponseComplete,
	})
}

// A subagent hook can repair an older Stop's completed projection only on
// the exact owned Claude conversation. Explicit quiet overrides win; an
// ordinary main-thread hook and an Agent awaiting its operator stay unchanged.
func (c *aiCommand) claudeBackgroundFallback(payload claudeHookPayload, action aiHookActionResolution, binding managedAgentBinding, owned bool) bool {
	return payload.AgentID != "" && owned && binding.agent.Spec.Provider == aiModeClaude &&
		payload.SessionID != "" && binding.agent.Status.SessionRef.ConversationID() == payload.SessionID &&
		binding.agent.Status.Interaction.Kind == coremetadata.InteractionResponseComplete &&
		!claudeAgentAwaitsOperator(binding.agent, c.sessionRefClock()()) &&
		!(action.Action == aiHookActionQuiet && action.Source == aiHookActionSourceRuntime)
}

var errClaudeBackgroundProgressRejected = errors.New("claude background progress conditions changed")

func (c *aiCommand) ingestClaudeBackgroundProgress(paneID string, payload claudeHookPayload, metadata map[string]string, binding managedAgentBinding, owned bool) error {
	var guard func(*coremetadata.Registry, managedAgentBinding) error
	if owned {
		guard = func(working *coremetadata.Registry, commitBinding managedAgentBinding) error {
			current, ok := working.Agent(binding.agent.Metadata.UID)
			pane, paneOK := working.Pane(binding.paneUID)
			if commitBinding.agent.Metadata.UID != binding.agent.Metadata.UID || commitBinding.paneUID != binding.paneUID ||
				commitBinding.runtimeID != binding.runtimeID || !ok || !paneOK || current.Spec.Provider != aiModeClaude ||
				current.Status.Phase != coremetadata.PhaseRunning || current.Status.PaneRef != binding.paneUID ||
				pane.Status.Activation.AgentUID != binding.agent.Metadata.UID ||
				pane.Status.Activation.RuntimeID != paneID || pane.Status.Activation.Generation != binding.generation ||
				current.Status.SessionRef.ConversationID() != binding.agent.Status.SessionRef.ConversationID() ||
				agentInteractionAwaitsOperator(current.Status.Interaction.Kind) ||
				(payload.EventName != "Stop" && current.Status.Interaction.Kind != coremetadata.InteractionResponseComplete) {
				return errClaudeBackgroundProgressRejected
			}
			return nil
		}
	}
	if err := c.applyAIStatusInternalWithActivationPolicy("thinking", paneID, attentionNotifyInput{
		Metadata: metadata, BadgeKind: aiBadgeKindInProgress,
	}, false, false, string(coremetadata.InteractionSourceProviderHook), true, true, guard); err != nil {
		if errors.Is(err, errClaudeBackgroundProgressRejected) {
			c.quietClaudeHook(paneID, payload, "")
			return nil
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
		return err
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", ""))
	return nil
}

func (c *aiCommand) ingestClaudeStopFailure(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
		return nil
	}
	body := formatClaudeStopFailureNotifyBody(payload)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:       claudeExtraNotifyID(payload, "stop-failure", payload.ErrorType, payload.ErrorMessage),
		Text:     body.Text,
		Severity: body.Severity,
		Metadata: mergeAINotifyBodyMetadata(metadata, body),
		Force:    true,
	})
}

func (c *aiCommand) ingestClaudeSubagentStop(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionNotify {
		body := formatClaudeSubagentStopNotifyBody(payload)
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "subagent-stop", payload.SubagentType, payload.SubagentID),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: mergeAINotifyBodyMetadata(metadata, body),
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
			return err
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "notify", ""))
		return nil
	}
	if action.Action == aiHookActionState {
		if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "subagent-stop", payload.SubagentType, payload.SubagentID),
			Text:     formatClaudeSubagentStopNotifyBody(payload).Text,
			Severity: notify.SeverityInfo,
			Metadata: mergeAINotifyBodyMetadata(metadata, formatClaudeSubagentStopNotifyBody(payload)),
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
			return err
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", aiIngestRecordReason(aiHookStateReason(action))))
		return nil
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", aiIngestReasonHighVolumeEvent))
	return nil
}

func (c *aiCommand) ingestClaudeTeammateIdle(paneID string, payload claudeHookPayload, metadata map[string]string, action aiHookActionResolution) error {
	if action.Action == aiHookActionQuiet {
		c.quietClaudeHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
		return nil
	}
	body := formatClaudeTeammateIdleNotifyBody(payload)
	return c.emitClaudeHookStatus(paneID, payload, action, attentionNotifyInput{
		ID:        claudeExtraNotifyID(payload, "teammate-idle", payload.TeammateName, payload.TeammateID, payload.TeammateContext),
		Text:      body.Text,
		Severity:  body.Severity,
		Metadata:  mergeAINotifyBodyMetadata(metadata, body),
		Force:     true,
		BadgeKind: aiBadgeKindResponseComplete,
	})
}

// emitClaudeHookStatus applies the state-only vs state+notify split shared by
// most claude hook handlers and writes the matching ingest log entry.
func (c *aiCommand) emitClaudeHookStatus(paneID string, payload claudeHookPayload, action aiHookActionResolution, input attentionNotifyInput) error {
	if action.Action == aiHookActionState {
		if err := c.applyAIStatusStateOnly("waiting", paneID, input); err != nil {
			c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
			return err
		}
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "state", aiIngestRecordReason(aiHookStateReason(action))))
		return nil
	}
	if err := c.applyAIStatusWithNotify("waiting", paneID, input); err != nil {
		c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "error", aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err)))
		return err
	}
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "notify", ""))
	return nil
}

func claudeHookLogEntry(paneID string, payload claudeHookPayload, result string, reason aiIngestReason) aiIngestLogEntry {
	return claudeHookDiagnosticFields(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: result, Reason: reason, Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID}, payload)
}

// quietClaudeHook only records the quiet outcome. Every caller is reached from
// ingestClaudeHook, which already marked the Pane before dispatching.
func (c *aiCommand) quietClaudeHook(paneID string, payload claudeHookPayload, reason aiIngestReason) {
	c.appendAIIngestLog(claudeHookLogEntry(paneID, payload, "quiet", reason))
}

// Retain only the count. Unknown or malformed task data is compatibility
// input, not a reason to reject the hook or leave a session permanently busy.
func claudeBackgroundTasksInFlight(value any) int {
	tasks, _ := value.([]any)
	count := 0
	for _, item := range tasks {
		task, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := task["type"].(string)
		status, _ := task["status"].(string)
		if (kind == "subagent" || kind == "workflow") && (status == "running" || status == "pending") {
			count++
		}
	}
	return count
}

func parseClaudeHookPayload(data []byte) (claudeHookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return claudeHookPayload{}, fmt.Errorf("parse claude hook payload: %w", err)
	}
	payload := claudeHookPayload{
		AgentID:          firstString(raw, "agent_id"),
		AgentType:        firstString(raw, "agent_type"),
		EventName:        firstString(raw, "hook_event_name", "event_name"),
		SessionID:        firstString(raw, "session_id", "session-id"),
		CWD:              firstString(raw, "cwd", "workspace", "project_dir"),
		TranscriptPath:   firstString(raw, "transcript_path", "transcriptPath"),
		NotificationType: firstString(raw, "notification_type", "notificationType"),
		Message:          firstString(raw, "message", "text"),
		Prompt:           firstString(raw, "prompt", "user_prompt"),
		ToolName:         firstString(raw, "tool_name", "toolName"),
		ToolUseID:        firstString(raw, "tool_use_id", "toolUseID", "id"),
		ErrorType:        firstString(raw, "error_type", "errorType", "failure_type", "failureType"),
		ErrorMessage:     firstString(raw, "error_message", "errorMessage", "message", "reason"),
		SubagentType:     firstString(raw, "subagent_type", "subagentType", "agent_type", "agentType"),
		SubagentID:       firstString(raw, "subagent_id", "subagentId", "agent_id", "agentId"),
		TeammateName:     firstString(raw, "teammate_name", "teammateName", "teammate"),
		TeammateID:       firstString(raw, "teammate_id", "teammateId"),
		TeammateContext:  firstString(raw, "teammate_context", "teammateContext", "context", "reason", "message"),
	}
	if tasks, present := raw["background_tasks"]; present {
		count := claudeBackgroundTasksInFlight(tasks)
		payload.BackgroundTasksInFlight = &count
	}
	if payload.CWD == "" {
		payload.CWD = firstNestedString(raw["workspace"], "cwd", "path")
	}
	if payload.Message == "" {
		payload.Message = firstNestedString(raw["notification"], "message", "text")
	}
	if payload.NotificationType == "" {
		payload.NotificationType = firstNestedString(raw["notification"], "notification_type", "type")
	}
	if payload.ToolName == "" {
		payload.ToolName = firstNestedString(raw["tool"], "name", "tool_name")
	}
	if payload.ToolUseID == "" {
		payload.ToolUseID = firstNestedString(raw["tool"], "id", "tool_use_id")
	}
	if payload.ErrorType == "" {
		payload.ErrorType = firstNestedString(raw["error"], "type", "name", "code")
	}
	if payload.ErrorMessage == "" {
		payload.ErrorMessage = firstNestedString(raw["error"], "message", "text", "reason")
	}
	if payload.SubagentType == "" {
		payload.SubagentType = firstNestedString(raw["subagent"], "type", "name", "kind")
	}
	if payload.SubagentID == "" {
		payload.SubagentID = firstNestedString(raw["subagent"], "id", "subagent_id", "agent_id")
	}
	if payload.TeammateName == "" {
		payload.TeammateName = firstNestedString(raw["teammate"], "name", "type", "kind")
	}
	if payload.TeammateID == "" {
		payload.TeammateID = firstNestedString(raw["teammate"], "id", "teammate_id")
	}
	if payload.TeammateContext == "" {
		payload.TeammateContext = firstNestedString(raw["teammate"], "context", "status", "reason", "message")
	}
	payload.ToolInput = mapFromAny(raw["tool_input"])
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["input"])
	}
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["tool"])
		delete(payload.ToolInput, "name")
		delete(payload.ToolInput, "tool_name")
		delete(payload.ToolInput, "id")
		delete(payload.ToolInput, "tool_use_id")
	}
	return payload, nil
}

func (p claudeHookPayload) claudeMetadata() map[string]string {
	metadata := map[string]string{
		notify.MetaAgent:    aiModeClaude,
		notify.MetaEvent:    p.EventName,
		"session_id":        p.SessionID,
		"cwd":               p.CWD,
		"transcript_path":   p.TranscriptPath,
		"notification_type": p.NotificationType,
		"prompt":            truncateRunes(p.Prompt, 60),
		"tool_name":         p.ToolName,
		"tool_use_id":       p.ToolUseID,
		"error_type":        p.ErrorType,
		"error_message":     truncateRunes(p.ErrorMessage, 160),
		"subagent_type":     p.SubagentType,
		"subagent_id":       p.SubagentID,
		"teammate_name":     p.TeammateName,
		"teammate_id":       p.TeammateID,
		"teammate_context":  truncateRunes(p.TeammateContext, 160),
	}
	for key, value := range p.ToolInput {
		if text := stringFromAny(value); text != "" {
			metadata["tool_input."+key] = truncateRunes(text, 160)
		}
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func claudeNotifyID(p claudeHookPayload) string {
	parts := []string{"ai", "claude", "notification"}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.NotificationType); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.Message); value != "" {
		parts = append(parts, truncateRunes(value, 40))
	}
	return strings.Join(parts, ":")
}

func claudePermissionNotifyID(p claudeHookPayload) string {
	sessionID := strings.TrimSpace(p.SessionID)
	toolUseID := strings.TrimSpace(p.ToolUseID)
	switch {
	case sessionID != "" && toolUseID != "":
		return "ai:claude:permission:" + sessionID + ":" + toolUseID
	case toolUseID != "":
		return "ai:claude:permission:" + toolUseID
	default:
		return ""
	}
}

func claudeStopNotifyID(p claudeHookPayload) string {
	if sessionID := strings.TrimSpace(p.SessionID); sessionID != "" {
		return "ai:claude:stop:" + sessionID
	}
	return ""
}

func claudeExtraNotifyID(p claudeHookPayload, kind string, values ...string) string {
	parts := []string{"ai", "claude", kind}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	for _, value := range values {
		if trimmed := truncateRunes(value, 40); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, ":")
}

func readClaudeTranscriptLastAssistantText(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size <= 0 {
		return ""
	}
	start := int64(0)
	if size > claudeTranscriptTailLimit {
		start = size - claudeTranscriptTailLimit
	}
	buf := make([]byte, size-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(buf)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if text := claudeAssistantTextFromJSONLine(lines[i]); text != "" {
			return text
		}
	}
	return ""
}

func claudeAssistantTextFromJSONLine(line string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return ""
	}
	if strings.EqualFold(stringFromAny(raw["role"]), "assistant") {
		return claudeContentText(raw["content"])
	}
	message := mapFromAny(raw["message"])
	if strings.EqualFold(stringFromAny(message["role"]), "assistant") {
		return claudeContentText(message["content"])
	}
	return ""
}

func claudeContentText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		for _, item := range v {
			if text := firstNestedString(item, "text"); text != "" {
				return text
			}
			if text := stringFromAny(item); text != "" {
				return text
			}
		}
	case map[string]any:
		return firstString(v, "text", "content")
	}
	return ""
}
