package config

import (
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
