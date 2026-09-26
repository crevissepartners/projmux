package app

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// TestConfigAgentApprovalsSetThenShowMatchesHookLoaders stores both values in
// one call and proves the bare read and the permission hook's own loaders see
// them from their own files, apart from the agent-questions files.
func TestConfigAgentApprovalsSetThenShowMatchesHookLoaders(t *testing.T) {
	t.Parallel()

	cmd := centralSettingsTestCommand(t)
	if got := runConfigRoute(t, cmd, "agent-approvals"); got != "answering claude window 900\n" {
		t.Fatalf("default read = %q", got)
	}
	if got := runConfigRoute(t, cmd, "agent-approvals", "--answering", "PROJMUX", "--window", "120"); got != "answering projmux window 120\n" {
		t.Fatalf("store = %q", got)
	}
	if got := runConfigRoute(t, cmd, "agent-approvals"); got != "answering projmux window 120\n" {
		t.Fatalf("read after store = %q", got)
	}
	_, paths := centralSettingsFiles(t, cmd)
	if claudePermissionAnsweringFromPaths(paths) != config.AgentApprovalAnsweringProjmux || claudePermissionWindowFromPaths(paths).Seconds() != 120 {
		t.Fatal("the hook loaders do not see the stored values")
	}
	for _, question := range []string{paths.AgentQuestionAnsweringFile(), paths.AgentQuestionWindowSecondsFile()} {
		if _, err := os.Stat(question); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("agent-approvals wrote the question setting %s: %v", question, err)
		}
	}
}

// TestConfigAgentApprovalsRejectsInvalidWithoutWriting holds the validation:
// an unknown way, an out-of-range window, the unlimited word, and a
// positional argument are usage errors that write nothing.
func TestConfigAgentApprovalsRejectsInvalidWithoutWriting(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--answering", "allow"},
		{"--answering", ""},
		{"--answering", "projmux", "--window", "59"},
		{"--answering", "projmux", "--window", "3601"},
		{"--answering", "projmux", "--window", "unlimited"},
		{"--window", ""},
		{"extra"},
		{"--bogus"},
	} {
		cmd := centralSettingsTestCommand(t)
		_, _, err := runRoute(t, cmd, append([]string{"agent-approvals"}, args...)...)
		if !IsUsageError(err) || !strings.Contains(err.Error(), "config agent-approvals") {
			t.Fatalf("%q err = %v, want a config agent-approvals usage error", args, err)
		}
		_, paths := centralSettingsFiles(t, cmd)
		for _, path := range []string{paths.AgentApprovalAnsweringFile(), paths.AgentApprovalWindowSecondsFile()} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%q wrote %s: %v", args, path, err)
			}
		}
	}
}
