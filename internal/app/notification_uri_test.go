package app

import (
	"strings"
	"testing"
)

func TestBuildFocusURI_RoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		pane   string
		socket string
	}{
		{"basic", "%8", "/tmp/tmux-1000/projmux"},
		{"no socket", "%12", ""},
		{"socket with spaces", "%3", "/tmp/tmux 1000/default"},
		{"unicode socket", "%5", "/tmp/tmux-한글/소켓"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uri := buildFocusURI(tc.pane, tc.socket)
			if uri == "" {
				t.Fatalf("buildFocusURI returned empty for pane=%q socket=%q", tc.pane, tc.socket)
			}
			parsed, err := parseFocusURI(uri)
			if err != nil {
				t.Fatalf("parseFocusURI(%q) error: %v", uri, err)
			}
			if parsed.PaneID != tc.pane {
				t.Fatalf("PaneID = %q, want %q", parsed.PaneID, tc.pane)
			}
			if parsed.Socket != tc.socket {
				t.Fatalf("Socket = %q, want %q", parsed.Socket, tc.socket)
			}
			if parsed.Source != focusURISourceDef {
				t.Fatalf("Source = %q, want %q", parsed.Source, focusURISourceDef)
			}
		})
	}
}

func TestBuildFocusURI_EmptyPaneReturnsEmpty(t *testing.T) {
	t.Parallel()

	if got := buildFocusURI("", "/sock"); got != "" {
		t.Fatalf("buildFocusURI with empty pane = %q, want empty", got)
	}
	if got := buildFocusURI("   ", "/sock"); got != "" {
		t.Fatalf("buildFocusURI with whitespace pane = %q, want empty", got)
	}
}

func TestBuildFocusURI_SchemeAndHost(t *testing.T) {
	t.Parallel()

	uri := buildFocusURI("%1", "/s")
	if !strings.HasPrefix(uri, "projmux://focus?") {
		t.Fatalf("uri = %q, want projmux://focus? prefix", uri)
	}
}

func TestParseFocusURI_RejectsWrongScheme(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://focus?pane_id=%251",
		"https://focus?pane_id=%251",
		"vscode://focus?pane_id=%251",
		"projmuxx://focus?pane_id=%251",
	} {
		if _, err := parseFocusURI(raw); err == nil {
			t.Fatalf("parseFocusURI(%q) accepted wrong scheme", raw)
		}
	}
}

func TestParseFocusURI_RejectsWrongHost(t *testing.T) {
	t.Parallel()

	if _, err := parseFocusURI("projmux://launch?pane_id=%251"); err == nil {
		t.Fatal("parseFocusURI accepted non-focus host")
	}
}

func TestParseFocusURI_RejectsMissingPaneID(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"projmux://focus",
		"projmux://focus?",
		"projmux://focus?socket=/tmp/sock",
		"projmux://focus?pane_id=",
		"projmux://focus?pane_id=%20%20",
	} {
		if _, err := parseFocusURI(raw); err == nil {
			t.Fatalf("parseFocusURI(%q) accepted missing pane_id", raw)
		}
	}
}

func TestParseFocusURI_DecodesURLEncodedSocket(t *testing.T) {
	t.Parallel()

	uri := "projmux://focus?pane_id=%258&socket=%2Ftmp%2Ftmux-1000%2Fprojmux"
	got, err := parseFocusURI(uri)
	if err != nil {
		t.Fatalf("parseFocusURI: %v", err)
	}
	if got.PaneID != "%8" {
		t.Fatalf("PaneID = %q, want %q", got.PaneID, "%8")
	}
	if got.Socket != "/tmp/tmux-1000/projmux" {
		t.Fatalf("Socket = %q, want /tmp/tmux-1000/projmux", got.Socket)
	}
	if got.Source != focusURISourceDef {
		t.Fatalf("Source = %q, want %q", got.Source, focusURISourceDef)
	}
}

func TestParseFocusURI_DefaultSourceWhenAbsent(t *testing.T) {
	t.Parallel()

	got, err := parseFocusURI("projmux://focus?pane_id=%251")
	if err != nil {
		t.Fatalf("parseFocusURI: %v", err)
	}
	if got.Source != focusURISourceDef {
		t.Fatalf("Source = %q, want default %q", got.Source, focusURISourceDef)
	}
}

func TestParseFocusURI_IgnoresUnknownParams(t *testing.T) {
	t.Parallel()

	uri := "projmux://focus?pane_id=%251&socket=/s&future=hello&another=world"
	got, err := parseFocusURI(uri)
	if err != nil {
		t.Fatalf("parseFocusURI rejected extra params: %v", err)
	}
	if got.PaneID != "%1" || got.Socket != "/s" {
		t.Fatalf("got = %#v, want pane=%%1 socket=/s", got)
	}
}

func TestParseFocusURI_CustomSourcePreserved(t *testing.T) {
	t.Parallel()

	got, err := parseFocusURI("projmux://focus?pane_id=%251&source=custom-handler")
	if err != nil {
		t.Fatalf("parseFocusURI: %v", err)
	}
	if got.Source != "custom-handler" {
		t.Fatalf("Source = %q, want custom-handler", got.Source)
	}
}

func TestParseFocusURI_OpaqueFormAccepted(t *testing.T) {
	t.Parallel()

	// Some Windows shells hand us `projmux:focus?...` without the `//`
	// authority delimiter; net/url parses this as opaque. Accept it so we
	// don't reject a click just because of an authority-stripping shell.
	got, err := parseFocusURI("projmux:focus?pane_id=%251&socket=/s")
	if err != nil {
		t.Fatalf("parseFocusURI(opaque) error: %v", err)
	}
	if got.PaneID != "%1" || got.Socket != "/s" {
		t.Fatalf("got = %#v, want pane=%%1 socket=/s", got)
	}
}

func TestParseFocusURI_EmptyInput(t *testing.T) {
	t.Parallel()

	if _, err := parseFocusURI(""); err == nil {
		t.Fatal("parseFocusURI(\"\") accepted empty input")
	}
	if _, err := parseFocusURI("   "); err == nil {
		t.Fatal("parseFocusURI whitespace accepted empty input")
	}
}
