package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentQuestionWindowSecondsFile(t *testing.T) {
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
		{name: "maximum", content: "3600", want: 3600},
		{name: "surrounding whitespace", content: "  120\n", want: 120},
		{name: "below the minimum", content: "59", want: 900},
		{name: "above the maximum", content: "3601", want: 900},
		{name: "zero", content: "0", want: 900},
		{name: "negative", content: "-5", want: 900},
		{name: "not a number", content: "abc", want: 900},
		{name: "empty", content: "", want: 900},
		{name: "exponent", content: "1e3", want: 900},
		{name: "fraction", content: "12.5", want: 900},
		{name: "unlimited", content: "unlimited\n", want: UnlimitedAgentQuestionWindowSeconds},
		{name: "unlimited in any case with whitespace", content: "  Unlimited \n", want: UnlimitedAgentQuestionWindowSeconds},
		{name: "unlimited as seconds is out of range", content: "604785", want: 900},
		{name: "unreadable directory", dir: true, want: 900},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := DefaultPaths(t.TempDir(), "").AgentQuestionWindowSecondsFile()
			switch {
			case test.dir:
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case !test.missing:
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(test.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := LoadAgentQuestionWindowSecondsFile(path)
			if got != test.want {
				t.Fatalf("seconds = %d (err %v), want %d", got, err, test.want)
			}
			if test.dir != (err != nil) {
				t.Fatalf("err = %v, want an error only for an unreadable file", err)
			}
		})
	}

	if got, err := LoadAgentQuestionWindowSecondsFile(""); got != DefaultAgentQuestionWindowSeconds || err != nil {
		t.Fatalf("empty path = %d, %v; want the default and no error", got, err)
	}
}

// TestLoadAgentQuestionAnsweringFile holds the answering setting: only the
// way-2 word is way 2, and every other content, a missing file, and an
// unreadable one are way 1.
func TestLoadAgentQuestionAnsweringFile(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		content string
		missing bool
		dir     bool
		want    AgentQuestionAnswering
	}{
		{name: "missing", missing: true, want: AgentQuestionAnsweringClaude},
		{name: "empty", content: "", want: AgentQuestionAnsweringClaude},
		{name: "whitespace only", content: " \n\t", want: AgentQuestionAnsweringClaude},
		{name: "claude", content: "claude", want: AgentQuestionAnsweringClaude},
		{name: "projmux", content: "projmux", want: AgentQuestionAnsweringProjmux},
		{name: "projmux with surrounding whitespace", content: "  projmux\n", want: AgentQuestionAnsweringProjmux},
		{name: "uppercase projmux", content: "PROJMUX", want: AgentQuestionAnsweringProjmux},
		{name: "uppercase claude", content: "Claude", want: AgentQuestionAnsweringClaude},
		{name: "on is not a way", content: "on", want: AgentQuestionAnsweringClaude},
		{name: "a way number is not a way", content: "2", want: AgentQuestionAnsweringClaude},
		{name: "garbage", content: "projmux please", want: AgentQuestionAnsweringClaude},
		{name: "unreadable directory", dir: true, want: AgentQuestionAnsweringClaude},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := DefaultPaths(t.TempDir(), "").AgentQuestionAnsweringFile()
			switch {
			case test.dir:
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case !test.missing:
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(test.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := LoadAgentQuestionAnsweringFile(path)
			if got != test.want {
				t.Fatalf("answering = %q (err %v), want %q", got, err, test.want)
			}
			if test.dir != (err != nil) {
				t.Fatalf("err = %v, want an error only for an unreadable file", err)
			}
		})
	}

	if got, err := LoadAgentQuestionAnsweringFile(""); got != AgentQuestionAnsweringClaude || err != nil {
		t.Fatalf("empty path = %q, %v; want way 1 and no error", got, err)
	}
	if paths := DefaultPaths(t.TempDir(), ""); paths.AgentQuestionAnsweringFile() != filepath.Join(paths.ConfigDir, AgentQuestionAnsweringFileName) {
		t.Fatalf("path = %q", paths.AgentQuestionAnsweringFile())
	}
}

// TestAgentQuestionTimeoutConstantsHoldTheirDerivation pins the hook timeout
// ceiling to seven days, inside a signed 32-bit millisecond timer, and
// Unlimited to that ceiling less the margin, so the hook still ends the
// longest wait before Claude Code does.
func TestAgentQuestionTimeoutConstantsHoldTheirDerivation(t *testing.T) {
	t.Parallel()

	if want := 7 * 24 * 3600; AgentQuestionHookTimeoutSeconds != want {
		t.Fatalf("hook timeout = %d, want %d", AgentQuestionHookTimeoutSeconds, want)
	}
	if int64(AgentQuestionHookTimeoutSeconds)*1000 > 1<<31-1 {
		t.Fatalf("hook timeout %ds does not fit a signed 32-bit millisecond timer", AgentQuestionHookTimeoutSeconds)
	}
	if AgentQuestionHookTimeoutSeconds != 604800 || UnlimitedAgentQuestionWindowSeconds != 604785 {
		t.Fatalf("hook timeout = %d, unlimited = %d", AgentQuestionHookTimeoutSeconds, UnlimitedAgentQuestionWindowSeconds)
	}
	if UnlimitedAgentQuestionWindowSeconds+AgentQuestionHookTimeoutMarginSeconds != AgentQuestionHookTimeoutSeconds {
		t.Fatal("unlimited plus the margin is not the hook timeout")
	}
	if MaxAgentQuestionWindowSeconds+AgentQuestionHookTimeoutMarginSeconds > AgentQuestionHookTimeoutSeconds {
		t.Fatal("the longest bounded window outlasts the hook timeout")
	}
}

// TestSaveAgentQuestionWindowSecondsFileRoundTripsAndRefusesOutOfRange holds
// the window writer: in-range seconds and Unlimited round-trip through the
// loader, Unlimited is stored as the word, and anything else is refused
// without touching the saved value.
func TestSaveAgentQuestionWindowSecondsFileRoundTripsAndRefusesOutOfRange(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", AgentQuestionWindowSecondsFileName)
	for _, test := range []struct {
		seconds int
		content string
	}{
		{seconds: 60, content: "60\n"},
		{seconds: 3600, content: "3600\n"},
		{seconds: 900, content: "900\n"},
		{seconds: UnlimitedAgentQuestionWindowSeconds, content: "unlimited\n"},
	} {
		if err := SaveAgentQuestionWindowSecondsFile(path, test.seconds); err != nil {
			t.Fatalf("save %d: %v", test.seconds, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != test.content {
			t.Fatalf("save %d wrote %q (%v), want %q", test.seconds, raw, err, test.content)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("save %d mode = %v (%v), want 0644", test.seconds, info.Mode().Perm(), err)
		}
		if got, err := LoadAgentQuestionWindowSecondsFile(path); got != test.seconds || err != nil {
			t.Fatalf("load after save %d = %d, %v", test.seconds, got, err)
		}
	}

	for _, seconds := range []int{0, -1, 59, 3601, UnlimitedAgentQuestionWindowSeconds - 1, AgentQuestionHookTimeoutSeconds} {
		if err := SaveAgentQuestionWindowSecondsFile(path, seconds); err == nil {
			t.Fatalf("save %d succeeded, want a refusal", seconds)
		}
		if raw, _ := os.ReadFile(path); string(raw) != "unlimited\n" {
			t.Fatalf("refused save %d changed the file to %q", seconds, raw)
		}
	}
	if err := SaveAgentQuestionWindowSecondsFile("", 900); !errors.Is(err, ErrHomeDirRequired) {
		t.Fatalf("empty path err = %v, want ErrHomeDirRequired", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %d entries (%v), want only the saved file", len(entries), err)
	}
}

// TestSaveAgentQuestionAnsweringFileNormalizes holds the answering writer:
// only the way-2 word is written as way 2, and whatever is saved loads back
// as the same way.
func TestSaveAgentQuestionAnsweringFileNormalizes(t *testing.T) {
	t.Parallel()

	path := DefaultPaths(t.TempDir(), "").AgentQuestionAnsweringFile()
	for _, test := range []struct {
		value   AgentQuestionAnswering
		content string
		want    AgentQuestionAnswering
	}{
		{value: AgentQuestionAnsweringProjmux, content: "projmux\n", want: AgentQuestionAnsweringProjmux},
		{value: " PROJMUX ", content: "projmux\n", want: AgentQuestionAnsweringProjmux},
		{value: AgentQuestionAnsweringClaude, content: "claude\n", want: AgentQuestionAnsweringClaude},
		{value: "garbage", content: "claude\n", want: AgentQuestionAnsweringClaude},
		{value: "", content: "claude\n", want: AgentQuestionAnsweringClaude},
	} {
		if err := SaveAgentQuestionAnsweringFile(path, test.value); err != nil {
			t.Fatalf("save %q: %v", test.value, err)
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != test.content {
			t.Fatalf("save %q wrote %q (%v), want %q", test.value, raw, err, test.content)
		}
		if got, err := LoadAgentQuestionAnsweringFile(path); got != test.want || err != nil {
			t.Fatalf("load after save %q = %q, %v", test.value, got, err)
		}
	}
	if err := SaveAgentQuestionAnsweringFile(" ", AgentQuestionAnsweringProjmux); !errors.Is(err, ErrHomeDirRequired) {
		t.Fatalf("empty path err = %v, want ErrHomeDirRequired", err)
	}
}
