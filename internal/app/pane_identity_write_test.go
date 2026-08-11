package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestPaneIdentityActionWriteMatrix(t *testing.T) {
	t.Parallel()

	topicSet := testAICommand(t.TempDir())
	if err := topicSet.Run([]string{"topic", "set", "AI topic", "--pane", "%9"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("ai topic set error = %v", err)
	}
	topicClear := testAICommand(t.TempDir())
	if err := topicClear.Run([]string{"topic", "clear", "--pane", "%9"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("ai topic clear error = %v", err)
	}
	renameRunner := &recordingTmuxRunner{}
	if err := (&tmuxCommand{runner: renameRunner}).Run([]string{"rename-pane", "%9", "user label"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("tmux rename-pane error = %v", err)
	}

	catalog := defaultKeyBindingCatalog()
	canonical, _ := keyBindingActionByID(catalog, "rename-pane-label")
	alias, _ := keyBindingActionByID(catalog, "rename-pane-topic")

	tests := []struct {
		name      string
		commands  string
		allowed   []string
		forbidden []string
	}{
		{name: "user rename helper", commands: recordedTmuxCallsText(renameRunner.calls), allowed: []string{paneLabelOption}, forbidden: []string{aiPaneTopicOption, aiPaneTopicManualOption, "select-pane -T"}},
		{name: "canonical rename action", commands: canonical.TmuxBody, allowed: []string{paneLabelOption}, forbidden: []string{aiPaneTopicOption, aiPaneTopicManualOption, "select-pane -T", "pane_title"}},
		{name: "deprecated topic alias", commands: alias.TmuxBody, allowed: []string{paneLabelOption}, forbidden: []string{aiPaneTopicOption, aiPaneTopicManualOption, "select-pane -T", "pane_title"}},
		{name: "AI topic set", commands: recordedAICommandsText(cmdRecorder(topicSet).commands), allowed: []string{aiPaneTopicOption, aiPaneTopicManualOption}, forbidden: []string{paneLabelOption, "select-pane -T"}},
		{name: "AI topic clear", commands: recordedAICommandsText(cmdRecorder(topicClear).commands), allowed: []string{aiPaneTopicOption, aiPaneTopicManualOption}, forbidden: []string{paneLabelOption, "select-pane -T"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, allowed := range tt.allowed {
				if !strings.Contains(tt.commands, allowed) {
					t.Fatalf("commands = %q, want allowed target %q", tt.commands, allowed)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(tt.commands, forbidden) {
					t.Fatalf("commands = %q, contains forbidden target %q", tt.commands, forbidden)
				}
			}
		})
	}

	for _, forbiddenID := range []string{"rename-pane-title", "rename-pane-raw-title", "set-pane-title"} {
		if action, ok := keyBindingActionByID(catalog, forbiddenID); ok {
			t.Fatalf("raw-title user action %q unexpectedly resolves to %#v", forbiddenID, action)
		}
	}
}

func recordedTmuxCallsText(calls []recordedTmuxCall) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, call.name+" "+strings.Join(call.args, " "))
	}
	return strings.Join(parts, "\n")
}

func recordedAICommandsText(commands []recordedAICommand) string {
	parts := make([]string, 0, len(commands))
	for _, command := range commands {
		parts = append(parts, command.name+" "+strings.Join(command.args, " "))
	}
	return strings.Join(parts, "\n")
}
