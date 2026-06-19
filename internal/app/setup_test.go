package app

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClassifyProbeInput(t *testing.T) {
	t.Parallel()

	keyAlt1 := probeKey{Label: "Alt-1", Action: "Open sidebar", Plain: "\x1b1"}
	keyCtrlShiftR := probeKey{Label: "Ctrl-Shift-R", Action: "No projmux binding by default"}

	cases := []struct {
		name       string
		key        probeKey
		input      []byte
		wantStatus probeKeyStatus
	}{
		{name: "plain alt-1", key: keyAlt1, input: []byte("\x1b1"), wantStatus: probeStatusPlain},
		{name: "legacy app csi-u alt-1 is not a success path", key: keyAlt1, input: []byte("\x1b[9900u"), wantStatus: probeStatusUnknown},
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

	key := probeKey{Label: "Alt-1", Plain: "\x1b1"}
	src := []byte("\x1b1")
	res := classifyProbeInput(key, src)
	src[0] = 'X'
	if string(res.Sequence) != "\x1b1" {
		t.Fatalf("classifyProbeInput must copy bytes; mutated to %q", res.Sequence)
	}
}

func TestRenderProbeStatusContainsSequence(t *testing.T) {
	t.Parallel()

	res := classifyProbeInput(probeKey{Label: "Alt-1", Plain: "\x1b1"}, []byte("\x1b[9900u"))
	rendered := renderProbeStatus(res)
	if !strings.Contains(rendered, "MISS unknown") {
		t.Fatalf("expected unknown marker, got %q", rendered)
	}
	if !strings.Contains(rendered, "\\x1b[9900u") {
		t.Fatalf("expected captured sequence, got %q", rendered)
	}
}

func TestVisibleEscape(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":             "\"\"",
		"a":            "a",
		"\x1b1":        "\\x1b1",
		"\x1b[9900u":   "\\x1b[9900u",
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
			name:     "windows terminal via wsl distro",
			env:      lookup{"WSL_DISTRO_NAME": "Ubuntu"},
			wantSlug: "windows-terminal",
		},
		{
			name:     "windows terminal via wsl interop",
			env:      lookup{"WSL_INTEROP": "/run/WSL/1_interop"},
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
		{Key: probeKey{Label: "Alt-2", Action: "notify-sidebar"}, Status: probeStatusTimeout, Reason: "no bytes"},
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
		{Key: probeKey{Label: "Alt-2"}, Status: probeStatusPlain, Sequence: []byte("\x1b2")},
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

func TestSuggestedPlainChordForSequence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		seq  []byte
		want string
		ok   bool
	}{
		{name: "alt printable", seq: []byte("\x1ba"), want: "M-a", ok: true},
		{name: "alt digit", seq: []byte("\x1b7"), want: "M-7", ok: true},
		{name: "control byte", seq: []byte{0x01}, want: "C-a", ok: true},
		{name: "printable key", seq: []byte("p"), want: "p", ok: true},
		{name: "printable uppercase key", seq: []byte("P"), want: "P", ok: true},
		{name: "catalog plain sequence", seq: []byte("\x1b[1;4D"), want: "M-S-Left", ok: true},
		{name: "enter is ambiguous", seq: []byte("\r"), ok: false},
		{name: "space is not a keymap chord", seq: []byte(" "), ok: false},
		{name: "unsupported printable config char", seq: []byte(`"`), ok: false},
		{name: "raw multi-byte printable sequence", seq: []byte("pa"), ok: false},
		{name: "arrow is not a plain tmux chord", seq: []byte("\x1b[A"), ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := suggestedPlainChordForSequence(tc.seq)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("suggestedPlainChordForSequence(%q) = %q, %v; want %q, %v", tc.seq, got, ok, tc.want, tc.ok)
			}
		})
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
		"Ctrl-N",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("non-interactive output missing %q\nfull:\n%s", want, out)
		}
	}
	if strings.Contains(out, "9900u") || strings.Contains(out, "User") {
		t.Fatalf("non-interactive output should not mention app escape/User keys:\n%s", out)
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
		{Label: "Alt-1", Action: "sidebar", Plain: "\x1b1"},
		{Label: "Alt-2", Action: "notify-sidebar", Plain: "\x1b2"},
		{Label: "Ctrl-N", Action: "new-window", Plain: "\x0e"},
	}
	queue := [][]byte{
		[]byte("\x1b1"),
		[]byte("\x1b[9901u"),
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
		"MISS unknown",
		"MISS timeout",
		"Pass / Fail   : 1 / 2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("interactive output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestSetupCommandRunInteractivePropagatesReadError(t *testing.T) {
	t.Parallel()

	keys := []probeKey{
		{Label: "Alt-1", Plain: "\x1b1"},
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

func TestSetupProbeControllingTTYKeyReadsTTYFile(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer r.Close()
	if _, err := w.Write([]byte("\x1b1")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	var opened bool
	cmd := &setupCommand{
		openTTY: func() (*os.File, func() error, error) {
			opened = true
			return r, func() error { return nil }, nil
		},
		enterRaw: func() (func() error, error) {
			return func() error { return nil }, nil
		},
	}
	res, err := cmd.probeControllingTTYKey(probeKey{Label: "Alt-1", Plain: "\x1b1"}, time.Second)
	if err != nil {
		t.Fatalf("probeControllingTTYKey() error = %v", err)
	}
	if !opened {
		t.Fatalf("probeControllingTTYKey() did not open controlling TTY")
	}
	if res.Status != probeStatusPlain {
		t.Fatalf("probeControllingTTYKey() status = %q, want plain", res.Status)
	}
}

func TestDefaultProbeKeysCoverSpec(t *testing.T) {
	t.Parallel()

	keys := defaultProbeKeys()
	got := sortedProbeLabels(keys)
	want := []string{
		"Alt-1", "Alt-2", "Alt-3", "Alt-4", "Alt-5", "Alt-6",
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
	alt3 := probeKeyByLabel(keys, "Alt-3")
	if alt3.Action != "Recent windows" || alt3.ActionID != "RecentWindows:Open" || alt3.PlainChord != "M-3" {
		t.Fatalf("Alt-3 probe = %#v, want RecentWindows:Open recent windows M-3 probe", alt3)
	}
	cmd := newSetupCommand()
	cmd.defaultKeys = keys
	cmd.lookupEnv = func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--non-interactive"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run --non-interactive error = %v", err)
	}
	out := stdout.String()
	for _, banned := range []string{"9900u", "User", "CSI-u"} {
		if strings.Contains(out, banned) {
			t.Fatalf("default probe key output should not include legacy route %q:\n%s", banned, out)
		}
	}
}

func probeKeyByLabel(keys []probeKey, label string) probeKey {
	for _, key := range keys {
		if key.Label == label {
			return key
		}
	}
	return probeKey{}
}
