package codexappserver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentprogress"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestDecodeProgressPlanDropsStepAndExplanationWithNinetyNineCap(t *testing.T) {
	t.Parallel()
	steps := make([]map[string]string, 120)
	for i := range steps {
		status := "pending"
		if i < 40 {
			status = "completed"
		} else if i < 50 {
			status = "inProgress"
		}
		steps[i] = map[string]string{"status": status, "step": "PRIVATE STEP /repo/secret"}
	}
	raw, _ := json.Marshal(map[string]any{"threadId": "thread-1", "turnId": "turn-1", "plan": steps, "explanation": "PRIVATE EXPLANATION"})
	event, recognized, err := DecodeProgressEvent(Notification{Method: "turn/plan/updated", Params: raw}, time.Unix(1, 0))
	if err != nil || !recognized {
		t.Fatalf("decode = %+v/%t/%v", event, recognized, err)
	}
	if event.PlanCompleted != 40 || event.PlanInProgress != 10 || event.PlanTotal != 99 || !event.PlanTruncated {
		t.Fatalf("plan projection = %+v", event)
	}
	assertBoundedProgressEventHasNoContentFields(t, event)
	if text := fmt.Sprintf("%+v", event); strings.Contains(text, "PRIVATE") || strings.Contains(text, "/repo") {
		t.Fatalf("bounded event retained content: %s", text)
	}
}

func TestDecodeProgressDiffScansAtMostBudgetAndSaturatesHeaders(t *testing.T) {
	t.Parallel()
	var diff strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&diff, "diff --git a/PRIVATE-%d b/PRIVATE-%d\n", i, i)
	}
	raw, _ := json.Marshal(map[string]any{"threadId": "thread-1", "turnId": "turn-1", "diff": diff.String()})
	event, recognized, err := DecodeProgressEvent(Notification{Method: "turn/diff/updated", Params: raw}, time.Unix(1, 0))
	if err != nil || !recognized || event.ChangedFiles != 999 || !event.FilesTruncated {
		t.Fatalf("diff projection = %+v/%t/%v", event, recognized, err)
	}
	huge, _ := json.Marshal(map[string]any{"threadId": "thread-1", "turnId": "turn-1", "diff": strings.Repeat("x", maxProgressDiffScanBytes+1)})
	event, _, err = DecodeProgressEvent(Notification{Method: "turn/diff/updated", Params: huge}, time.Unix(1, 0))
	if err != nil || event.ChangedFiles != 0 || !event.FilesTruncated {
		t.Fatalf("size projection = %+v/%v", event, err)
	}
	assertBoundedProgressEventHasNoContentFields(t, event)
}

func TestDecodeProgressItemUsesOnlyClosedDiscriminatorMapping(t *testing.T) {
	t.Parallel()
	cases := map[string]coremetadata.AgentProgressActivity{
		"plan": coremetadata.ProgressPlanning, "commandExecution": coremetadata.ProgressCommand,
		"fileChange": coremetadata.ProgressFileChange, "mcpToolCall": coremetadata.ProgressTool,
		"webSearch": coremetadata.ProgressWebSearch, "collabAgentToolCall": coremetadata.ProgressDelegation,
		"imageGeneration": coremetadata.ProgressImage, "enteredReviewMode": coremetadata.ProgressReview,
		"contextCompaction": coremetadata.ProgressCompaction, "futurePrivateThing": coremetadata.ProgressOther,
	}
	for itemType, want := range cases {
		raw, _ := json.Marshal(map[string]any{"threadId": "thread-1", "turnId": "turn-1", "startedAtMs": 1, "item": map[string]any{
			"id": "item-1", "type": itemType, "command": "rm PRIVATE", "cwd": "/repo/private", "output": "SECRET",
		}})
		event, recognized, err := DecodeProgressEvent(Notification{Method: "item/started", Params: raw}, time.Unix(1, 0))
		if err != nil || !recognized || event.Activity != want {
			t.Fatalf("%s = %+v/%t/%v, want %s", itemType, event, recognized, err, want)
		}
		if itemType == "futurePrivateThing" && event.UnknownIncrement != 1 {
			t.Fatalf("unknown increment = %d", event.UnknownIncrement)
		}
		assertBoundedProgressEventHasNoContentFields(t, event)
	}
}

func TestDecodeProgressTurnUsesNestedTurnIDAndTerminalStatus(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"threadId":"thread-1","turnId":"wrong","turn":{"id":"turn-1","status":"interrupted","startedAt":10.5,"items":[{"type":"reasoning","text":"PRIVATE"}],"error":{"message":"PRIVATE"}}}`)
	event, recognized, err := DecodeProgressEvent(Notification{Method: "turn/completed", Params: raw}, time.Unix(20, 0))
	if err != nil || !recognized || event.Kind != agentprogress.EventTurnTerminal || event.TurnRef != "turn-1" {
		t.Fatalf("turn projection = %+v/%t/%v", event, recognized, err)
	}
	assertBoundedProgressEventHasNoContentFields(t, event)
}

func assertBoundedProgressEventHasNoContentFields(t *testing.T, event agentprogress.Event) {
	t.Helper()
	typ := reflect.TypeOf(event)
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		for _, forbidden := range []string{"prompt", "reason", "step", "path", "command", "tool", "output", "diff", "model", "effort", "name", "content"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("bounded reducer input has forbidden field %q", typ.Field(i).Name)
			}
		}
	}
}
