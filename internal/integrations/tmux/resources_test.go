package tmux

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resources"
)

func TestClientListResourcePanesParsesTypedInventory(t *testing.T) {
	t.Parallel()

	output := strings.Join([]string{
		"/tmp/tmux-1000/projmux", "$1", "project", "@2", "editor", "%3", "4242", "/dev/pts/7", "/repo/project", "api", "codex", "fix tests", "zsh", "raw title",
	}, tmuxFieldSep) + "\n"
	client := NewClient(staticRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || len(args) != 4 || args[0] != "list-panes" || args[1] != "-a" || args[2] != "-F" {
			t.Fatalf("resource inventory command = %s %#v", name, args)
		}
		for _, format := range []string{"#{socket_path}", "#{session_id}", "#{window_id}", "#{pane_pid}", "#{pane_tty}", "#{@projmux_project_path}"} {
			if !strings.Contains(args[3], format) {
				t.Fatalf("resource format %q missing from %q", format, args[3])
			}
		}
		return []byte(output), nil
	}))

	got, err := client.ListResourcePanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []resources.PaneInventory{{Socket: "/tmp/tmux-1000/projmux", SessionID: "$1", SessionName: "project", WindowID: "@2", WindowName: "editor", PaneID: "%3", PanePID: 4242, PaneTTY: "/dev/pts/7", ProjectAnchor: "/repo/project", PaneLabel: "api", AIAgent: "codex", AITopic: "fix tests", PaneCommand: "zsh", PaneTitle: "raw title"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListResourcePanes() = %#v, want %#v", got, want)
	}
}

func TestParseResourcePanesPreservesLinkedWindowAnchors(t *testing.T) {
	t.Parallel()

	row := func(sessionID, session, anchor string) string {
		return strings.Join([]string{"", sessionID, session, "@8", "editor", "%9", "900", "/dev/pts/9", anchor, "", "", "", "zsh", "title"}, tmuxEscapedFieldSep)
	}
	got, err := parseResourcePanes([]byte(row("$1", "one", "/repo/one")+"\n"+row("$2", "two", "/repo/two")+"\n"), "projmux")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Socket != "projmux" || got[0].PaneID != got[1].PaneID || got[0].ProjectAnchor == got[1].ProjectAnchor {
		t.Fatalf("linked resource rows = %#v", got)
	}
}

func TestParseResourcePanesRejectsMissingIdentity(t *testing.T) {
	t.Parallel()

	base := []string{"/tmp/s", "$1", "one", "@1", "editor", "%1", "100", "/dev/pts/1", "/repo", "", "", "", "zsh", "title"}
	tests := []struct {
		name  string
		index int
		value string
		want  error
	}{
		{"session", 1, "", errResourceSessionIDRequired},
		{"window", 3, "", errResourceWindowIDRequired},
		{"pane", 5, "", errResourcePaneIDRequired},
		{"pid empty", 6, "", errResourcePanePIDInvalid},
		{"pid zero", 6, "0", errResourcePanePIDInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := append([]string(nil), base...)
			fields[tc.index] = tc.value
			_, err := parseResourcePanes([]byte(strings.Join(fields, tmuxFieldSep)), "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("parseResourcePanes() error = %v, want %v", err, tc.want)
			}
		})
	}
}
