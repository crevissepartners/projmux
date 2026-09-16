package transcript

import (
	"regexp"
	"strconv"
	"strings"
)

// Blocks Claude Code writes into a transcript for the model rather than for a
// person: task notifications, subagent reports framed as peer messages, and
// system reminders. Shown raw they are pages of markup and handling rules in
// the operator's column. They are turned into structure the client can render
// in a line, and the rules around them are dropped.

// Task is a background task finishing: a subagent or a background command.
type Task struct {
	ID        string `json:"id,omitempty"`
	ToolUseID string `json:"toolUseID,omitempty"`
	// Kind is "agent" for a subagent, "command" for a background command, and
	// empty when the summary says neither.
	Kind   string `json:"kind,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	// ExitCode is set only when the summary reports one.
	ExitCode   *int   `json:"exitCode,omitempty"`
	Summary    string `json:"summary,omitempty"`
	OutputFile string `json:"outputFile,omitempty"`
	ToolUses   int    `json:"toolUses,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Tokens     int64  `json:"tokens,omitempty"`
}

// Failed reports whether the task ended badly: a failed or killed status, or
// a non-zero exit code.
func (t Task) Failed() bool {
	switch t.Status {
	case "failed", "killed", "error":
		return true
	}
	return t.ExitCode != nil && *t.ExitCode != 0
}

// Report is a subagent's final report, delivered to its parent session.
type Report struct {
	From string `json:"from,omitempty"`
}

const (
	kindTask   = "task"
	kindReport = "report"
)

var (
	systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>\s*`)
	taskSummary    = regexp.MustCompile(`^(Agent|Background command) "(.*)" (\w+)(?: \(exit code (-?\d+)\))?`)
	agentMessage   = regexp.MustCompile(`<agent-message from="([^"]*)">`)
)

// stripReminders removes system-reminder blocks, which the harness appends to
// tool results and messages for the model's benefit.
func stripReminders(text string) string {
	if !strings.Contains(text, "<system-reminder>") {
		return text
	}
	return strings.TrimSpace(systemReminder.ReplaceAllString(text, ""))
}

// harnessTurn recognizes a harness block and turns it into a structured
// turn. ok is false when text is not one.
func harnessTurn(text, at string) (Turn, bool) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "<task-notification>") {
		return Turn{Role: "system", Kind: kindTask, At: at, Task: parseTask(trimmed)}, true
	}
	if strings.Contains(trimmed, "[Subagent hand-back]") {
		if report, from, ok := parseReport(trimmed); ok {
			return Turn{Role: "system", Kind: kindReport, At: at, Text: report, Report: &Report{From: from}}, true
		}
	}
	return Turn{}, false
}

func tag(text, name string) string {
	open, closing := "<"+name+">", "</"+name+">"
	_, after, ok := strings.Cut(text, open)
	if !ok {
		return ""
	}
	rest := after
	before, _, ok := strings.Cut(rest, closing)
	if !ok {
		return ""
	}
	return strings.TrimSpace(before)
}

func parseTask(text string) *Task {
	task := &Task{
		ID:         tag(text, "task-id"),
		ToolUseID:  tag(text, "tool-use-id"),
		Status:     tag(text, "status"),
		Summary:    tag(text, "summary"),
		OutputFile: tag(text, "output-file"),
	}
	if m := taskSummary.FindStringSubmatch(task.Summary); m != nil {
		task.Kind = map[string]string{"Agent": "agent", "Background command": "command"}[m[1]]
		task.Name = m[2]
		if m[4] != "" {
			if code, err := strconv.Atoi(m[4]); err == nil {
				task.ExitCode = &code
			}
		}
	}
	if usage := tag(text, "usage"); usage != "" {
		task.ToolUses, _ = strconv.Atoi(tag(usage, "tool_uses"))
		task.DurationMs, _ = strconv.ParseInt(tag(usage, "duration_ms"), 10, 64)
		task.Tokens, _ = strconv.ParseInt(tag(usage, "subagent_tokens"), 10, 64)
	}
	return task
}

// parseReport cuts a subagent's report out of the peer frame around it. The
// harness indents every line of the report by two spaces, which is undone.
func parseReport(text string) (report, from string, ok bool) {
	m := agentMessage.FindStringSubmatchIndex(text)
	if m == nil {
		return "", "", false
	}
	from = text[m[2]:m[3]]
	body := text[m[1]:]
	if end := strings.Index(body, "</agent-message>"); end >= 0 {
		body = body[:end]
	}
	if start := strings.Index(body, "The report follows:"); start >= 0 {
		body = body[start+len("The report follows:"):]
	}
	lines := strings.Split(strings.Trim(body, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, "  ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), from, true
}
