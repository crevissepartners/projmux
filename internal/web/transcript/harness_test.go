package transcript

import (
	"strings"
	"testing"
)

const subagentNotification = "<task-notification>\n<task-id>ae27</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n" +
	"<output-file>/tmp/tasks/ae27.output</output-file>\n<status>completed</status>\n" +
	"<summary>Agent \"Map tmux, transcripts\" finished</summary>\n" +
	"<note>A task-notification fires each time this agent stops.</note>\n" +
	"<result>This agent's report was delivered to you as a message.\n</result>\n" +
	"<usage><subagent_tokens>126600</subagent_tokens><tool_uses>41</tool_uses><duration_ms>218072</duration_ms></usage>\n" +
	"</task-notification>"

const commandNotification = "<task-notification>\n<task-id>bl4a</task-id>\n<tool-use-id>toolu_2</tool-use-id>\n" +
	"<output-file>/tmp/tasks/bl4a.output</output-file>\n<status>completed</status>\n" +
	"<summary>Background command \"Wait for make test\" completed (exit code 2)</summary>\n</task-notification>"

func TestTaskNotificationsBecomeOneStructuredTurn(t *testing.T) {
	record := map[string]any{
		"type":    "user",
		"origin":  map[string]any{"kind": "task-notification"},
		"message": map[string]any{"role": "user", "content": subagentNotification},
	}
	turn, ok := parseClaudeLine(record)
	if !ok || turn.Kind != "task" || turn.Role != "system" || turn.Text != "" || turn.Task == nil {
		t.Fatalf("subagent notification = %v %+v", ok, turn)
	}
	task := *turn.Task
	if task.Kind != "agent" || task.Name != "Map tmux, transcripts" || task.Status != "completed" ||
		task.ToolUses != 41 || task.DurationMs != 218072 || task.Tokens != 126600 ||
		task.ID != "ae27" || task.OutputFile != "/tmp/tasks/ae27.output" || task.ExitCode != nil || task.Failed() {
		t.Fatalf("task = %+v", task)
	}

	// A background command, queued while the session was busy.
	queued := map[string]any{
		"type":       "attachment",
		"attachment": map[string]any{"type": "queued_command", "prompt": commandNotification},
	}
	turn, ok = parseClaudeLine(queued)
	if !ok || turn.Task == nil || turn.Task.Kind != "command" || turn.Task.Name != "Wait for make test" ||
		turn.Task.ExitCode == nil || *turn.Task.ExitCode != 2 || !turn.Task.Failed() {
		t.Fatalf("command notification = %v %+v %+v", ok, turn, turn.Task)
	}
}

func TestSubagentReportDropsTheFrameAndIndent(t *testing.T) {
	text := "Another Claude session sent a message:\n<agent-message from=\"ae27\">\n" +
		"[Subagent hand-back] The text below is the final report. The report follows:\n" +
		"  ## Findings\n  \n  - one\n    - nested\n</agent-message>\n\nThat \"other Claude session\" is an agent working inside this session."
	record := map[string]any{
		"type":    "user",
		"isMeta":  true,
		"origin":  map[string]any{"kind": "peer"},
		"message": map[string]any{"role": "user", "content": text},
	}
	turn, ok := parseClaudeLine(record)
	if !ok || turn.Kind != "report" || turn.Report == nil || turn.Report.From != "ae27" {
		t.Fatalf("report = %v %+v", ok, turn)
	}
	if turn.Text != "## Findings\n\n- one\n  - nested" {
		t.Fatalf("report text = %q", turn.Text)
	}
}

func TestSystemRemindersAreDropped(t *testing.T) {
	record := map[string]any{
		"type": "user",
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok\n<system-reminder>\nnote for the model\n</system-reminder>"},
			map[string]any{"type": "text", "text": "<system-reminder>only this</system-reminder>"},
		}},
	}
	turn, ok := parseClaudeLine(record)
	if !ok || turn.Text != "" || len(turn.Tools) != 1 || turn.Tools[0].Result != "ok" {
		t.Fatalf("turn = %v %+v", ok, turn)
	}
	// A message that was nothing but a reminder is not shown at all.
	only := map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": "<system-reminder>x</system-reminder>"},
	}
	if _, ok := parseClaudeLine(only); ok {
		t.Fatal("a reminder-only message was shown")
	}
}

// A message sent mid-turn with a picture is queued as a content array. Only
// the string form was read, so such messages vanished.
func TestQueuedMessageWithAnImageIsKept(t *testing.T) {
	record := map[string]any{
		"type": "attachment",
		"attachment": map[string]any{"type": "queued_command", "prompt": []any{
			map[string]any{"type": "text", "text": "[Image #1] this does not respond"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "AAAA"}},
		}},
	}
	turn, ok := parseClaudeLine(record)
	if !ok || turn.Role != "user" || !strings.Contains(turn.Text, "does not respond") || turn.Images != 1 {
		t.Fatalf("turn = %v %+v", ok, turn)
	}
}
