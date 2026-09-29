package tmuxexec

import (
	"context"
	"slices"
	"testing"
)

func TestArgsAddsUTF8FlagToTmuxCommandClients(t *testing.T) {
	cases := []struct {
		name string
		prog string
		args []string
		want []string
	}{
		{"format read", "tmux", []string{"list-panes", "-s", "-F", "#{pane_id}\x1f#{session_id}"}, []string{"-u", "list-panes", "-s", "-F", "#{pane_id}\x1f#{session_id}"}},
		{"socket name and config", "tmux", []string{"-L", "projmux", "-f", "/tmp/c", "show-options", "-gqv", "@projmux_app"}, []string{"-L", "projmux", "-f", "/tmp/c", "-u", "show-options", "-gqv", "@projmux_app"}},
		{"socket path", "tmux", []string{"-S", "/tmp/s", "display-message", "-p", "#{socket_path}"}, []string{"-S", "/tmp/s", "-u", "display-message", "-p", "#{socket_path}"}},
		{"absolute tmux path", "/usr/bin/tmux", []string{"list-sessions"}, []string{"-u", "list-sessions"}},
		{"detached new-session", "tmux", []string{"-L", "projmux", "new-session", "-d", "-P", "-F", "#{session_id}", "-s", "p"}, []string{"-L", "projmux", "-u", "new-session", "-d", "-P", "-F", "#{session_id}", "-s", "p"}},
		{"detached new-session with joined flags", "tmux", []string{"new", "-dP", "-s", "p"}, []string{"-u", "new", "-dP", "-s", "p"}},
		{"switch-client is a command client", "tmux", []string{"switch-client", "-t", "=p"}, []string{"-u", "switch-client", "-t", "=p"}},
		{"already UTF-8", "tmux", []string{"-u", "list-sessions"}, []string{"-u", "list-sessions"}},
		{"already UTF-8 joined", "tmux", []string{"-2u", "list-sessions"}, []string{"-2u", "list-sessions"}},
		{"attach-session", "tmux", []string{"-L", "projmux", "-f", "/tmp/c", "attach-session", "-t", "=p"}, []string{"-L", "projmux", "-f", "/tmp/c", "attach-session", "-t", "=p"}},
		{"attach alias", "tmux", []string{"attach", "-t", "p"}, []string{"attach", "-t", "p"}},
		{"a alias", "tmux", []string{"a"}, []string{"a"}},
		{"attached new-session", "tmux", []string{"new-session", "-s", "p"}, []string{"new-session", "-s", "p"}},
		{"new-session value is not a flag", "tmux", []string{"new-session", "-s", "-d"}, []string{"new-session", "-s", "-d"}},
		{"attach-or-create new-session", "tmux", []string{"new-session", "-A", "-d", "-s", "p"}, []string{"new-session", "-A", "-d", "-s", "p"}},
		{"no command", "tmux", []string{"-L", "projmux"}, []string{"-L", "projmux"}},
		{"version", "tmux", []string{"-V"}, []string{"-V"}},
		{"not tmux", "git", []string{"-C", "/tmp", "status"}, []string{"-C", "/tmp", "status"}},
		{"name ending in tmux", "psmux", []string{"list-sessions"}, []string{"list-sessions"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.args)
			got := Args(tc.prog, tc.args)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Args(%q, %q) = %q, want %q", tc.prog, tc.args, got, tc.want)
			}
			if !slices.Equal(tc.args, original) {
				t.Fatalf("Args modified the caller's args: %q, was %q", tc.args, original)
			}
		})
	}
}

func TestCommandsCarryArgs(t *testing.T) {
	want := []string{"tmux", "-u", "list-sessions"}
	if got := Command("tmux", "list-sessions").Args; !slices.Equal(got, want) {
		t.Fatalf("Command argv = %q, want %q", got, want)
	}
	if got := CommandContext(context.Background(), "tmux", "list-sessions").Args; !slices.Equal(got, want) {
		t.Fatalf("CommandContext argv = %q, want %q", got, want)
	}
	if got := Command("stty", "-g").Args; !slices.Equal(got, []string{"stty", "-g"}) {
		t.Fatalf("Command changed a non-tmux argv: %q", got)
	}
}
