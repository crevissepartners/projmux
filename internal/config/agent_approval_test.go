package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeAgentApprovalSetting prepares one setting file for a load case: missing,
// an unreadable directory in its place, or the given content.
func writeAgentApprovalSetting(t *testing.T, path, content string, missing, dir bool) {
	t.Helper()
	switch {
	case dir:
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	case !missing:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLoadAgentApprovalAnsweringFile holds the approval answering setting:
// only the capture word captures, and every other content, a missing file,
// and an unreadable one are way 1.
func TestLoadAgentApprovalAnsweringFile(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		content string
		missing bool
		dir     bool
		want    AgentApprovalAnswering
	}{
		{name: "missing", missing: true, want: AgentApprovalAnsweringClaude},
		{name: "empty", content: "", want: AgentApprovalAnsweringClaude},
		{name: "claude", content: "claude\n", want: AgentApprovalAnsweringClaude},
		{name: "projmux", content: "projmux", want: AgentApprovalAnsweringProjmux},
		{name: "projmux with whitespace and case", content: "  PROJMUX \n", want: AgentApprovalAnsweringProjmux},
		{name: "on is not a way", content: "on", want: AgentApprovalAnsweringClaude},
		{name: "allow is not a way", content: "allow", want: AgentApprovalAnsweringClaude},
		{name: "garbage", content: "projmux please", want: AgentApprovalAnsweringClaude},
		{name: "unreadable directory", dir: true, want: AgentApprovalAnsweringClaude},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := DefaultPaths(t.TempDir(), "").AgentApprovalAnsweringFile()
			writeAgentApprovalSetting(t, path, test.content, test.missing, test.dir)
			got, err := LoadAgentApprovalAnsweringFile(path)
			if got != test.want {
				t.Fatalf("answering = %q (err %v), want %q", got, err, test.want)
			}
			if test.dir != (err != nil) {
				t.Fatalf("err = %v, want an error only for an unreadable file", err)
			}
		})
	}
	if got, err := LoadAgentApprovalAnsweringFile(""); got != AgentApprovalAnsweringClaude || err != nil {
		t.Fatalf("empty path = %q, %v; want way 1 and no error", got, err)
	}
	paths := DefaultPaths(t.TempDir(), "")
	if paths.AgentApprovalAnsweringFile() != filepath.Join(paths.ConfigDir, "agent-approval-answering") ||
		paths.AgentApprovalWindowSecondsFile() != filepath.Join(paths.ConfigDir, "agent-approval-window-seconds") {
		t.Fatalf("paths = %q, %q", paths.AgentApprovalAnsweringFile(), paths.AgentApprovalWindowSecondsFile())
	}
	if paths.AgentApprovalAnsweringFile() == paths.AgentQuestionAnsweringFile() || paths.AgentApprovalWindowSecondsFile() == paths.AgentQuestionWindowSecondsFile() {
		t.Fatal("the approval setting shares a file with the question setting")
	}
}

// TestLoadAgentApprovalWindowSecondsFile holds the approval window: 60..3600,
// default 900, and no unlimited word; everything else reads as the default.
func TestLoadAgentApprovalWindowSecondsFile(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		content string
		missing bool
		dir     bool
		want    int
	}{
		{name: "missing", missing: true, want: 900},
		{name: "default", content: "900", want: 900},
		{name: "minimum", content: "60", want: 60},
		{name: "maximum", content: "3600\n", want: 3600},
		{name: "surrounding whitespace", content: "  120\n", want: 120},
		{name: "below the minimum", content: "59", want: 900},
		{name: "above the maximum", content: "3601", want: 900},
		{name: "zero", content: "0", want: 900},
		{name: "negative", content: "-5", want: 900},
		{name: "unlimited is not a window", content: "unlimited", want: 900},
		{name: "question unlimited seconds are out of range", content: "604785", want: 900},
		{name: "not a number", content: "abc", want: 900},
		{name: "empty", content: "", want: 900},
		{name: "fraction", content: "12.5", want: 900},
		{name: "unreadable directory", dir: true, want: 900},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := DefaultPaths(t.TempDir(), "").AgentApprovalWindowSecondsFile()
			writeAgentApprovalSetting(t, path, test.content, test.missing, test.dir)
			got, err := LoadAgentApprovalWindowSecondsFile(path)
			if got != test.want {
				t.Fatalf("seconds = %d (err %v), want %d", got, err, test.want)
			}
			if test.dir != (err != nil) {
				t.Fatalf("err = %v, want an error only for an unreadable file", err)
			}
		})
	}
	if got, err := LoadAgentApprovalWindowSecondsFile(""); got != DefaultAgentApprovalWindowSeconds || err != nil {
		t.Fatalf("empty path = %d, %v; want the default and no error", got, err)
	}
}

// TestAgentApprovalHookTimeoutOutlastsTheLongestWindow pins the installed
// timeout to the longest window plus the margin.
func TestAgentApprovalHookTimeoutOutlastsTheLongestWindow(t *testing.T) {
	t.Parallel()

	if AgentApprovalHookTimeoutSeconds != 3615 || AgentApprovalHookTimeoutSeconds <= MaxAgentApprovalWindowSeconds {
		t.Fatalf("hook timeout = %d, want 3600 plus the 15 second margin", AgentApprovalHookTimeoutSeconds)
	}
}

// TestSaveAgentApprovalSettingsRoundTripAndRefuse holds both writers: the
// answering word is normalized, in-range windows round-trip, and an
// out-of-range window is refused without touching the saved value.
func TestSaveAgentApprovalSettingsRoundTripAndRefuse(t *testing.T) {
	t.Parallel()

	paths := DefaultPaths(t.TempDir(), "")
	for _, test := range []struct {
		value   AgentApprovalAnswering
		content string
	}{
		{value: AgentApprovalAnsweringProjmux, content: "projmux\n"},
		{value: " PROJMUX ", content: "projmux\n"},
		{value: "garbage", content: "claude\n"},
	} {
		if err := SaveAgentApprovalAnsweringFile(paths.AgentApprovalAnsweringFile(), test.value); err != nil {
			t.Fatalf("save %q: %v", test.value, err)
		}
		if raw, _ := os.ReadFile(paths.AgentApprovalAnsweringFile()); string(raw) != test.content {
			t.Fatalf("save %q wrote %q, want %q", test.value, raw, test.content)
		}
	}
	path := paths.AgentApprovalWindowSecondsFile()
	for _, seconds := range []int{60, 3600, 900} {
		if err := SaveAgentApprovalWindowSecondsFile(path, seconds); err != nil {
			t.Fatalf("save %d: %v", seconds, err)
		}
		if got, err := LoadAgentApprovalWindowSecondsFile(path); got != seconds || err != nil {
			t.Fatalf("load after save %d = %d, %v", seconds, got, err)
		}
	}
	for _, seconds := range []int{0, 59, 3601, UnlimitedAgentQuestionWindowSeconds} {
		if err := SaveAgentApprovalWindowSecondsFile(path, seconds); err == nil {
			t.Fatalf("save %d succeeded, want a refusal", seconds)
		}
		if raw, _ := os.ReadFile(path); string(raw) != "900\n" {
			t.Fatalf("refused save %d changed the file to %q", seconds, raw)
		}
	}
	if err := SaveAgentApprovalWindowSecondsFile("", 900); !errors.Is(err, ErrHomeDirRequired) {
		t.Fatalf("empty path err = %v, want ErrHomeDirRequired", err)
	}
}
