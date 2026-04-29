package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClassifyProbeInput(t *testing.T) {
	t.Parallel()

	keyAlt1 := probeKey{Label: "Alt-1", Action: "Open sidebar (User4)", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"}
	keyCtrlShiftR := probeKey{Label: "Ctrl-Shift-R", Action: "No projmux binding by default"}

	cases := []struct {
		name       string
		key        probeKey
		input      []byte
		wantStatus probeKeyStatus
	}{
		{name: "plain alt-1", key: keyAlt1, input: []byte("\x1b1"), wantStatus: probeStatusPlain},
		{name: "csiu alt-1", key: keyAlt1, input: []byte("\x1b[9005u"), wantStatus: probeStatusCSIu},
		{name: "arrow key", key: keyAlt1, input: []byte("\x1b[A"), wantStatus: probeStatusUnknown},
		{name: "empty input", key: keyAlt1, input: nil, wantStatus: probeStatusTimeout},
		{name: "no plain, csi-u missing too", key: keyCtrlShiftR, input: []byte("\x1b[1;5R"), wantStatus: probeStatusUnknown},
		{name: "no plain, empty", key: keyCtrlShiftR, input: nil, wantStatus: probeStatusTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classifyProbeInput(tc.key, tc.input)
			if got.Status != tc.wantStatus {
				t.Fatalf("classifyProbeInput(%q) status = %q, want %q (reason=%q)", tc.input, got.Status, tc.wantStatus, got.Reason)
			}
			if got.Status != probeStatusTimeout && len(got.Sequence) == 0 {
				t.Fatalf("classifyProbeInput should preserve sequence for non-timeout result")
			}
			if got.Reason == "" {
				t.Fatalf("classifyProbeInput should always populate a reason; got empty for %q", tc.name)
			}
		})
	}
}

func TestClassifyProbeInputDoesNotAliasInput(t *testing.T) {
	t.Parallel()

	key := probeKey{Label: "Alt-1", Plain: "\x1b1", CSIu: "\x1b[9005u"}
	src := []byte("\x1b1")
	res := classifyProbeInput(key, src)
	src[0] = 'X'
	if string(res.Sequence) != "\x1b1" {
		t.Fatalf("classifyProbeInput must copy bytes; mutated to %q", res.Sequence)
	}
}

func TestRenderProbeStatusContainsSequence(t *testing.T) {
	t.Parallel()

	res := classifyProbeInput(probeKey{Label: "Alt-1", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"}, []byte("\x1b[9005u"))
	rendered := renderProbeStatus(res)
	if !strings.Contains(rendered, "csi-u") {
		t.Fatalf("expected csi-u marker, got %q", rendered)
	}
	if !strings.Contains(rendered, "User4") {
		t.Fatalf("expected User4 reference, got %q", rendered)
	}
}

func TestVisibleEscape(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":             "\"\"",
		"a":            "a",
		"\x1b1":        "\\x1b1",
		"\x1b[9005u":   "\\x1b[9005u",
		"\r":           "\\r",
		"\n":           "\\n",
		"\t":           "\\t",
		"\x01":         "\\x01",
		"\x7f":         "\\x7f",
		"\x1b[1;4D":    "\\x1b[1;4D",
		"abc\x1b[Adef": "abc\\x1b[Adef",
	}
	for in, want := range cases {
		if got := visibleEscape(in); got != want {
			t.Errorf("visibleEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectTerminal(t *testing.T) {
	t.Parallel()

	type lookup map[string]string

	cases := []struct {
		name     string
		env      lookup
		wantSlug string
	}{
		{
			name:     "ghostty via term_program",
			env:      lookup{"TERM_PROGRAM": "ghostty"},
			wantSlug: "ghostty",
		},
		{
			name:     "ghostty via resources dir",
			env:      lookup{"GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app/Contents/Resources"},
			wantSlug: "ghostty",
		},
		{
			name:     "wezterm",
			env:      lookup{"TERM_PROGRAM": "WezTerm"},
			wantSlug: "wezterm",
		},
		{
			name:     "kitty",
			env:      lookup{"KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty"},
			wantSlug: "kitty",
		},
		{
			name:     "iterm2 via term_program",
			env:      lookup{"TERM_PROGRAM": "iTerm.app"},
			wantSlug: "iterm2",
		},
		{
			name:     "iterm2 via lc_terminal",
			env:      lookup{"LC_TERMINAL": "iTerm2"},
			wantSlug: "iterm2",
		},
		{
			name:     "alacritty",
			env:      lookup{"ALACRITTY_WINDOW_ID": "1234"},
			wantSlug: "alacritty",
		},
		{
			name:     "windows terminal",
			env:      lookup{"WT_SESSION": "abc-123"},
			wantSlug: "windows-terminal",
		},
		{
			name:     "foot",
			env:      lookup{"TERM": "foot"},
			wantSlug: "foot",
		},
		{
			name:     "vscode",
			env:      lookup{"TERM_PROGRAM": "vscode"},
			wantSlug: "vscode",
		},
		{
			name:     "unknown",
			env:      lookup{"TERM": "xterm-256color"},
			wantSlug: "unknown",
		},
		{
			name:     "completely empty",
			env:      lookup{},
			wantSlug: "unknown",
		},
		{
			name:     "tmux multiplexer leak",
			env:      lookup{"TERM": "tmux-256color"},
			wantSlug: "multiplexer",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookup := func(k string) string { return tc.env[k] }
			got := detectTerminal(lookup)
			if got.Slug != tc.wantSlug {
				t.Fatalf("detectTerminal slug = %q, want %q (info=%+v)", got.Slug, tc.wantSlug, got)
			}
			if got.Display() == "" {
				t.Fatal("Display() returned empty string")
			}
		})
	}
}

func TestRenderProbeSummaryFlagsFailures(t *testing.T) {
	t.Parallel()

	terminal := terminalInfo{Slug: "ghostty", Name: "Ghostty", Source: "TERM_PROGRAM", Raw: "ghostty"}
	results := []probeResult{
		{Key: probeKey{Label: "Alt-1", Action: "sidebar"}, Status: probeStatusPlain, Sequence: []byte("\x1b1"), Reason: "ok"},
		{Key: probeKey{Label: "Alt-2", Action: "session-popup"}, Status: probeStatusTimeout, Reason: "no bytes"},
		{Key: probeKey{Label: "Ctrl-N", Action: "new-window"}, Status: probeStatusUnknown, Sequence: []byte("\x1b[1;5R"), Reason: "different sequence"},
	}

	var buf bytes.Buffer
	renderProbeSummary(&buf, terminal, results)
	out := buf.String()

	for _, want := range []string{
		"Pass / Fail   : 1 / 2",
		"Failures:",
		"Alt-2",
		"Ctrl-N",
		"projmux init ghostty",
		"Ghostty:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderProbeSummaryAllPass(t *testing.T) {
	t.Parallel()

	terminal := terminalInfo{Slug: "kitty", Name: "kitty", Source: "KITTY_WINDOW_ID", Raw: "1"}
	results := []probeResult{
		{Key: probeKey{Label: "Alt-1"}, Status: probeStatusPlain, Sequence: []byte("\x1b1")},
		{Key: probeKey{Label: "Alt-2"}, Status: probeStatusCSIu, Sequence: []byte("\x1b[9003u")},
	}
	var buf bytes.Buffer
	renderProbeSummary(&buf, terminal, results)
	out := buf.String()

	if !strings.Contains(out, "All probed keys reach this process") {
		t.Fatalf("expected success summary, got:\n%s", out)
	}
	if strings.Contains(out, "Failures:") {
		t.Fatalf("expected no failure block, got:\n%s", out)
	}
}

func TestSetupCommandRunNonInteractive(t *testing.T) {
	t.Parallel()

	cmd := newSetupCommand()
	cmd.lookupEnv = func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--non-interactive"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run --non-interactive error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"Detected terminal:",
		"Expected key sequences:",
		"Alt-1",
		"\\x1b[9005u",
		"Ctrl-N",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("non-interactive output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestSetupCommandRunRejectsPositionalArgs(t *testing.T) {
	t.Parallel()

	cmd := newSetupCommand()
	cmd.lookupEnv = func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"--non-interactive", "extra"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error for positional args, got nil")
	}
}

func TestSetupCommandRunInteractiveUsesProbeReader(t *testing.T) {
	t.Parallel()

	keys := []probeKey{
		{Label: "Alt-1", Action: "sidebar", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"},
		{Label: "Alt-2", Action: "session-popup", Plain: "\x1b2", CSIu: "\x1b[9003u", UserKey: "User2"},
		{Label: "Ctrl-N", Action: "new-window", Plain: "\x0e", CSIu: "\x1b[9008u", UserKey: "User7"},
	}
	queue := [][]byte{
		[]byte("\x1b1"),
		[]byte("\x1b[9003u"),
		nil,
	}

	cmd := newSetupCommand()
	cmd.defaultKeys = keys
	cmd.lookupEnv = func(string) string { return "" }
	cmd.enterRaw = func() (func() error, error) {
		return func() error { return nil }, nil
	}
	idx := 0
	cmd.readKey = func(timeout time.Duration) ([]byte, error) {
		seq := queue[idx]
		idx++
		if seq == nil {
			return nil, errProbeTimeout
		}
		return seq, nil
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"OK plain",
		"OK csi-u",
		"MISS timeout",
		"Pass / Fail   : 2 / 1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("interactive output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestSetupCommandRunInteractivePropagatesReadError(t *testing.T) {
	t.Parallel()

	keys := []probeKey{
		{Label: "Alt-1", Plain: "\x1b1", CSIu: "\x1b[9005u"},
	}
	cmd := newSetupCommand()
	cmd.defaultKeys = keys
	cmd.lookupEnv = func(string) string { return "" }
	cmd.enterRaw = func() (func() error, error) {
		return func() error { return nil }, nil
	}
	wantErr := errors.New("explode")
	cmd.readKey = func(timeout time.Duration) ([]byte, error) {
		return nil, wantErr
	}
	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestDefaultProbeKeysCoverSpec(t *testing.T) {
	t.Parallel()

	keys := defaultProbeKeys()
	got := sortedProbeLabels(keys)
	want := []string{
		"Alt-1", "Alt-2", "Alt-3", "Alt-4", "Alt-5",
		"Alt-Shift-Left", "Alt-Shift-Right",
		"Ctrl-M", "Ctrl-N", "Ctrl-Shift-L", "Ctrl-Shift-M", "Ctrl-Shift-R",
	}
	if len(got) != len(want) {
		t.Fatalf("default probe key count mismatch: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default probe key[%d] = %q, want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}
