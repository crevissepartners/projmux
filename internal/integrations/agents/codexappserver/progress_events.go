package codexappserver

import (
	"bufio"
	"fmt"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentprogress"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const maxProgressDiffScanBytes = 256 << 10

// DecodeProgressEvent is the privacy boundary for Codex plan, diff, item, and
// turn progress. The raw params and all content-bearing fields die in this
// call; its return value contains only bounded scalars and opaque current-turn
// dedupe ids.
func DecodeProgressEvent(notification Notification, observedAt time.Time) (agentprogress.Event, bool, error) {
	method := strings.TrimSpace(notification.Method)
	switch method {
	case "turn/started", "turn/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID          string   `json:"id"`
				Status      string   `json:"status"`
				StartedAt   *float64 `json:"startedAt"`
				CompletedAt *float64 `json:"completedAt"`
				DurationMS  *float64 `json:"durationMs"`
			} `json:"turn"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return agentprogress.Event{}, true, err
		}
		if strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.Turn.ID) == "" {
			return agentprogress.Event{}, true, protocolProgressError()
		}
		event := agentprogress.Event{Kind: agentprogress.EventTurnStarted, TurnRef: strings.TrimSpace(params.Turn.ID), ObservedAt: observedAt.UTC()}
		if params.Turn.StartedAt != nil {
			event.StartedAt = unixSeconds(*params.Turn.StartedAt)
		}
		if method == "turn/completed" {
			if normalizeTurnState(params.Turn.Status) == TurnStateUnknown || normalizeTurnState(params.Turn.Status) == TurnStateInProgress {
				return agentprogress.Event{}, true, protocolProgressError()
			}
			event.Kind = agentprogress.EventTurnTerminal
		} else if normalizeTurnState(params.Turn.Status) != TurnStateInProgress {
			return agentprogress.Event{}, true, protocolProgressError()
		}
		return event, true, nil
	case "turn/plan/updated":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Plan     []struct {
				Status string `json:"status"`
			} `json:"plan"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return agentprogress.Event{}, true, err
		}
		if strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" {
			return agentprogress.Event{}, true, protocolProgressError()
		}
		limit := len(params.Plan)
		truncated := limit > coremetadata.AgentProgressPlanCap
		if truncated {
			limit = coremetadata.AgentProgressPlanCap
		}
		event := agentprogress.Event{Kind: agentprogress.EventPlanUpdated, TurnRef: strings.TrimSpace(params.TurnID), PlanTotal: uint8(limit), PlanTruncated: truncated, ObservedAt: observedAt.UTC()}
		for _, step := range params.Plan[:limit] {
			switch strings.TrimSpace(step.Status) {
			case "completed":
				event.PlanCompleted++
			case "inProgress":
				event.PlanInProgress++
			case "pending":
			default:
				return agentprogress.Event{}, true, protocolProgressError()
			}
		}
		return event, true, nil
	case "turn/diff/updated":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Diff     string `json:"diff"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return agentprogress.Event{}, true, err
		}
		if strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" {
			return agentprogress.Event{}, true, protocolProgressError()
		}
		count, truncated := countDiffHeaders(params.Diff)
		params.Diff = ""
		return agentprogress.Event{Kind: agentprogress.EventDiffUpdated, TurnRef: strings.TrimSpace(params.TurnID), ChangedFiles: count, FilesTruncated: truncated, ObservedAt: observedAt.UTC()}, true, nil
	case "item/started", "item/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Item     struct {
				ID         string   `json:"id"`
				Type       string   `json:"type"`
				Status     string   `json:"status"`
				DurationMS *float64 `json:"durationMs"`
			} `json:"item"`
			StartedAtMS   *float64 `json:"startedAtMs"`
			CompletedAtMS *float64 `json:"completedAtMs"`
		}
		if err := decodeLifecycleParams(notification.Params, &params); err != nil {
			return agentprogress.Event{}, true, err
		}
		if strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" || strings.TrimSpace(params.Item.ID) == "" {
			return agentprogress.Event{}, true, protocolProgressError()
		}
		activity, known := progressActivity(params.Item.Type)
		event := agentprogress.Event{Kind: agentprogress.EventItemStarted, TurnRef: strings.TrimSpace(params.TurnID), ItemRef: strings.TrimSpace(params.Item.ID), Activity: activity, ObservedAt: observedAt.UTC()}
		if !known {
			event.UnknownIncrement = 1
		}
		if method == "item/completed" {
			event.Kind = agentprogress.EventItemCompleted
		}
		return event, true, nil
	default:
		return agentprogress.Event{}, false, nil
	}
}

func progressActivity(itemType string) (coremetadata.AgentProgressActivity, bool) {
	switch strings.TrimSpace(itemType) {
	case "plan":
		return coremetadata.ProgressPlanning, true
	case "commandExecution":
		return coremetadata.ProgressCommand, true
	case "fileChange":
		return coremetadata.ProgressFileChange, true
	case "mcpToolCall", "dynamicToolCall":
		return coremetadata.ProgressTool, true
	case "collabAgentToolCall", "subAgentActivity":
		return coremetadata.ProgressDelegation, true
	case "webSearch":
		return coremetadata.ProgressWebSearch, true
	case "imageView", "imageGeneration":
		return coremetadata.ProgressImage, true
	case "enteredReviewMode", "exitedReviewMode":
		return coremetadata.ProgressReview, true
	case "contextCompaction":
		return coremetadata.ProgressCompaction, true
	case "userMessage", "hookPrompt", "agentMessage", "reasoning", "sleep":
		return coremetadata.ProgressOther, true
	default:
		return coremetadata.ProgressOther, false
	}
}

func countDiffHeaders(diff string) (uint16, bool) {
	input := diff
	scanTruncated := len(input) > maxProgressDiffScanBytes
	if scanTruncated {
		input = input[:maxProgressDiffScanBytes]
		if lastNewline := strings.LastIndexByte(input, '\n'); lastNewline >= 0 {
			input = input[:lastNewline+1]
		} else {
			input = ""
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(input))
	scanner.Buffer(make([]byte, 4096), maxProgressDiffScanBytes)
	count := uint16(0)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "diff --git ") {
			continue
		}
		if count == coremetadata.AgentProgressFilesCap {
			return count, true
		}
		count++
	}
	return count, scanTruncated || scanner.Err() != nil
}

func unixSeconds(value float64) time.Time {
	seconds := int64(value)
	nanos := int64((value - float64(seconds)) * float64(time.Second))
	return time.Unix(seconds, nanos).UTC()
}

func protocolProgressError() error {
	return fmt.Errorf("%w: invalid bounded progress event", ErrProtocol)
}
