package setupcmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClassifyProbeInput(t *testing.T) {
	t.Parallel()

	keyAlt1 := ProbeKey{Label: "Alt-1", Action: "Open sidebar", Plain: "\x1b1"}
	keyCtrlShiftR := ProbeKey{Label: "Ctrl-Shift-R", Action: "No projmux binding by default"}

	cases := []struct {
		name       string
		key        ProbeKey
		input      []byte
		wantStatus ProbeKeyStatus
	}{
		{name: "plain alt-1", key: keyAlt1, input: []byte("\x1b1"), wantStatus: ProbeStatusPlain},
		{name: "legacy app csi-u alt-1 is not a success path", key: keyAlt1, input: []byte("\x1b[9900u"), wantStatus: ProbeStatusUnknown},
		{name: "arrow key", key: keyAlt1, input: []byte("\x1b[A"), wantStatus: ProbeStatusUnknown},
		{name: "empty input", key: keyAlt1, input: nil, wantStatus: ProbeStatusTimeout},
		{name: "no plain, csi-u missing too", key: keyCtrlShiftR, input: []byte("\x1b[1;5R"), wantStatus: ProbeStatusUnknown},
		{name: "no plain, empty", key: keyCtrlShiftR, input: nil, wantStatus: ProbeStatusTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ClassifyProbeInput(tc.key, tc.input)
			if got.Status != tc.wantStatus {
				t.Fatalf("ClassifyProbeInput(%q) status = %q, want %q (reason=%q)", tc.input, got.Status, tc.wantStatus, got.Reason)
			}
			if got.Status != ProbeStatusTimeout && len(got.Sequence) == 0 {
				t.Fatalf("ClassifyProbeInput should preserve sequence for non-timeout result")
			}
			if got.Reason == "" {
				t.Fatalf("ClassifyProbeInput should always populate a reason; got empty for %q", tc.name)
			}
		})
	}
}

func TestClassifyProbeInputDoesNotAliasInput(t *testing.T) {
	t.Parallel()

	key := ProbeKey{Label: "Alt-1", Plain: "\x1b1"}
	src := []byte("\x1b1")
	res := ClassifyProbeInput(key, src)
	src[0] = 'X'
	if string(res.Sequence) != "\x1b1" {
		t.Fatalf("ClassifyProbeInput must copy bytes; mutated to %q", res.Sequence)
	}
}

func TestRenderProbeStatusContainsSequence(t *testing.T) {
	t.Parallel()

	res := ClassifyProbeInput(ProbeKey{Label: "Alt-1", Plain: "\x1b1"}, []byte("\x1b[9900u"))
	rendered := RenderProbeStatus(res)
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
		if got := VisibleEscape(in); got != want {
			t.Errorf("VisibleEscape(%q) = %q, want %q", in, got, want)
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
	results := []ProbeResult{
		{Key: ProbeKey{Label: "Alt-1", Action: "sidebar"}, Status: ProbeStatusPlain, Sequence: []byte("\x1b1"), Reason: "ok"},
		{Key: ProbeKey{Label: "Alt-2", Action: "notify-sidebar"}, Status: ProbeStatusTimeout, Reason: "no bytes"},
		{Key: ProbeKey{Label: "Ctrl-N", Action: "new-window"}, Status: ProbeStatusUnknown, Sequence: []byte("\x1b[1;5R"), Reason: "different sequence"},
	}

	var buf bytes.Buffer
	renderProbeSummary(&buf, terminal, results)
	out := buf.String()

	for _, want := range []string{
		"Pass / Fail   : 1 / 2",
		"Failures:",
		"Alt-2",
		"Ctrl-N",
		"projmux setup terminal ghostty",
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
	results := []ProbeResult{
		{Key: ProbeKey{Label: "Alt-1"}, Status: ProbeStatusPlain, Sequence: []byte("\x1b1")},
		{Key: ProbeKey{Label: "Alt-2"}, Status: ProbeStatusPlain, Sequence: []byte("\x1b2")},
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

func TestSetupCommandRunRejectsPositionalArgs(t *testing.T) {
	t.Parallel()

	cmd := New(nil, nil, os.Getenv)
	cmd.lookupEnv = func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"--non-interactive", "extra"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error for positional args, got nil")
	}
}

func TestSetupCommandRunInteractiveUsesProbeReader(t *testing.T) {
	t.Parallel()

	keys := []ProbeKey{
		{Label: "Alt-1", Action: "sidebar", Plain: "\x1b1"},
		{Label: "Alt-2", Action: "notify-sidebar", Plain: "\x1b2"},
		{Label: "Ctrl-N", Action: "new-window", Plain: "\x0e"},
	}
	queue := [][]byte{
		[]byte("\x1b1"),
		[]byte("\x1b[9901u"),
		nil,
	}

	cmd := New(nil, nil, os.Getenv)
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

	keys := []ProbeKey{
		{Label: "Alt-1", Plain: "\x1b1"},
	}
	cmd := New(nil, nil, os.Getenv)
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

func TestSetupReadProbeKeyTimeoutReopensTTYWithoutStealingNextKey(t *testing.T) {
	t.Parallel()

	firstReader, firstWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("first Pipe() error = %v", err)
	}
	defer firstWriter.Close()
	secondReader, secondWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("second Pipe() error = %v", err)
	}
	defer secondWriter.Close()
	if _, err := secondWriter.Write([]byte("x")); err != nil {
		t.Fatalf("write second key: %v", err)
	}

	opened := 0
	cleaned := 0
	cmd := &Command{
		openTTY: func() (*os.File, func() error, error) {
			opened++
			switch opened {
			case 1:
				return firstReader, func() error {
					cleaned++
					return firstReader.Close()
				}, nil
			case 2:
				return secondReader, func() error {
					cleaned++
					return secondReader.Close()
				}, nil
			default:
				t.Fatalf("openTTY called %d times, want two", opened)
				return nil, nil, errors.New("unexpected open")
			}
		},
	}

	if _, err := cmd.readProbeKeyContext(context.Background(), 20*time.Millisecond); !errors.Is(err, errProbeTimeout) {
		t.Fatalf("first read error = %v, want probe timeout", err)
	}
	got, err := cmd.readProbeKeyContext(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("second read error = %v", err)
	}
	if string(got) != "x" {
		t.Fatalf("second read = %q, want next key x", got)
	}
	if opened != 2 || cleaned != 2 {
		t.Fatalf("TTY lifecycle opened/cleaned = %d/%d, want 2/2", opened, cleaned)
	}
}

func TestSetupReadProbeKeyFallsBackToStdinWhenTTYOpenFails(t *testing.T) {
	t.Parallel()

	var opened bool
	cmd := &Command{
		stdin: strings.NewReader("x"),
		openTTY: func() (*os.File, func() error, error) {
			opened = true
			return nil, nil, errors.New("no controlling tty")
		},
	}
	got, err := cmd.readProbeKeyContext(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("readProbeKeyContext() error = %v", err)
	}
	if !opened {
		t.Fatal("readProbeKeyContext() did not try the controlling TTY")
	}
	if string(got) != "x" {
		t.Fatalf("readProbeKeyContext() = %q, want stdin key x", got)
	}
}

func TestReadKeySequenceTimeoutDoesNotLeaveReaderToStealNextKey(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	if _, err := readKeySequenceContext(context.Background(), reader, 20*time.Millisecond); !errors.Is(err, errProbeTimeout) {
		t.Fatalf("first read error = %v, want probe timeout", err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatalf("write next key: %v", err)
	}
	got, err := readKeySequenceContext(context.Background(), reader, time.Second)
	if err != nil {
		t.Fatalf("second read error = %v", err)
	}
	if string(got) != "x" {
		t.Fatalf("second read = %q, want next key x", got)
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
	cmd := &Command{
		openTTY: func() (*os.File, func() error, error) {
			opened = true
			return r, func() error { return nil }, nil
		},
		enterRaw: func() (func() error, error) {
			return func() error { return nil }, nil
		},
	}
	res, err := cmd.probeControllingTTYKeyContext(context.Background(), ProbeKey{Label: "Alt-1", Plain: "\x1b1"}, time.Second)
	if err != nil {
		t.Fatalf("probeControllingTTYKey() error = %v", err)
	}
	if !opened {
		t.Fatalf("probeControllingTTYKey() did not open controlling TTY")
	}
	if res.Status != ProbeStatusPlain {
		t.Fatalf("probeControllingTTYKey() status = %q, want plain", res.Status)
	}
}
