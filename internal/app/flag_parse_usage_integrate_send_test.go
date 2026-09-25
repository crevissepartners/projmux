package app

import (
	"errors"
	"flag"
	"io"
	"path/filepath"
	"testing"
)

// TestAgentIntegrateAndMessageSendFlagParseErrorsAreUsageErrors pins the
// public `agent integrate <kind>` and `agent message send` routes to exit 2 on
// a flag error, driven through the agent dispatcher so the route spelling is
// covered, while --help still passes flag.ErrHelp through unchanged.
func TestAgentIntegrateAndMessageSendFlagParseErrorsAreUsageErrors(t *testing.T) {
	// A parse failure must return before any provider config, registry or
	// message store is touched; isolate every root anyway so a regression
	// cannot write into the real home.
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	run := func(argv []string) error {
		cmd := &agentCommand{ai: &aiCommand{}}
		return cmd.Run(argv, io.Discard, io.Discard)
	}

	cases := []struct {
		name     string
		argv     []string
		wantText string
	}{
		{"integrate claude unknown flag", []string{"integrate", "claude", "--zz-bogus-flag"}, "flag provided but not defined: -zz-bogus-flag"},
		{"integrate claude bad bool", []string{"integrate", "claude", "--dry-run=maybe"}, `invalid boolean value "maybe" for -dry-run: parse error`},
		{"integrate codex unknown flag", []string{"integrate", "codex", "--zz-bogus-flag"}, "flag provided but not defined: -zz-bogus-flag"},
		{"integrate codex bad bool", []string{"integrate", "codex", "--remove=maybe"}, `invalid boolean value "maybe" for -remove: parse error`},
		{"integrate tmux-bell unknown flag", []string{"integrate", "tmux-bell", "--zz-bogus-flag"}, "flag provided but not defined: -zz-bogus-flag"},
		{"integrate tmux-bell bad bool", []string{"integrate", "tmux-bell", "--dry-run=maybe"}, `invalid boolean value "maybe" for -dry-run: parse error`},
		{"integrate antigravity unknown flag", []string{"integrate", "antigravity", "--zz-bogus-flag"}, "flag provided but not defined: -zz-bogus-flag"},
		{"integrate antigravity bad bool", []string{"integrate", "antigravity", "--remove=maybe"}, `invalid boolean value "maybe" for -remove: parse error`},
		{"message send unknown flag", []string{"message", "send", "uid:agt-x", "--zz-bogus-flag", "--", "text"}, "flag provided but not defined: -zz-bogus-flag"},
		{"message send value-less flag", []string{"message", "send", "uid:agt-x", "--ttl", "--", "text"}, "flag needs an argument: -ttl"},
		{"message send value-less source", []string{"message", "send", "uid:agt-x", "--source", "--", "text"}, "flag needs an argument: -source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.argv)
			if err == nil {
				t.Fatalf("Run(%q) error = nil, want a flag parse error", tc.argv)
			}
			if !IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v (%T), want a usage error", tc.argv, err, err)
			}
			if got := err.Error(); got != tc.wantText {
				t.Fatalf("Run(%q) error text = %q, want %q", tc.argv, got, tc.wantText)
			}
		})
	}

	for _, argv := range [][]string{
		{"integrate", "claude", "--help"},
		{"integrate", "codex", "-h"},
		{"integrate", "tmux-bell", "--help"},
		{"integrate", "antigravity", "-help"},
		{"message", "send", "--help", "--", "text"},
	} {
		t.Run("help "+argv[0]+" "+argv[1], func(t *testing.T) {
			err := run(argv)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("Run(%q) error = %v, want flag.ErrHelp", argv, err)
			}
			if IsUsageError(err) {
				t.Fatalf("Run(%q) help classified as a usage error", argv)
			}
		})
	}
}
