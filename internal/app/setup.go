package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// setupCommand walks the operator through a per-key delivery probe so they can
// see exactly which projmux bindings reach tmux and which are swallowed by the
// host terminal before tmux ever sees them.
type setupCommand struct {
	stdin       io.Reader
	lookupEnv   func(string) string
	now         func() time.Time
	enterRaw    func() (restore func() error, err error)
	readKey     func(timeout time.Duration) ([]byte, error)
	openTTY     func() (*os.File, func() error, error)
	defaultKeys []probeKey
}

func newSetupCommand() *setupCommand {
	c := &setupCommand{
		stdin:       os.Stdin,
		lookupEnv:   os.Getenv,
		now:         time.Now,
		defaultKeys: defaultProbeKeys(),
	}
	c.openTTY = openControllingTTY
	c.enterRaw = func() (func() error, error) {
		return enterTTYRawMode(c.lookupEnv)
	}
	c.readKey = func(timeout time.Duration) ([]byte, error) {
		return readKeySequence(os.Stdin, timeout)
	}
	return c
}

// probeKeyStatus categorises how the key reached this process (or did not).
type probeKeyStatus string

const (
	probeStatusPlain   probeKeyStatus = "plain"
	probeStatusCSIu    probeKeyStatus = "csi-u"
	probeStatusUnknown probeKeyStatus = "unknown"
	probeStatusTimeout probeKeyStatus = "timeout"
)

// probeKey describes a single key the user is asked to press.
type probeKey struct {
	// Label is the human-readable name shown to the user (e.g. "Alt-1").
	Label string
	// Action describes the projmux behaviour bound to the key, used in the
	// summary report.
	Action string
	// Plain is the byte sequence the terminal sends when no CSI-u layer is
	// in play (e.g. "\x1b1" for Alt-1). Empty means projmux does not have a
	// direct plain bind for this key (Ctrl-Shift-{R,L}, etc).
	Plain string
	// CSIu is the CSI-u sequence the terminal sends once the user has
	// configured an `ESC[NNNNu` mapping, matching the user-keys table tmux
	// is configured with.
	CSIu string
	// UserKey is the tmux user-key index the CSI-u sequence resolves to
	// (e.g. "User4" for 9005u). Empty when no CSI-u escape route exists.
	UserKey string
}

// probeResult captures what we observed when the user pressed (or failed to
// press) a probed key.
type probeResult struct {
	Key      probeKey
	Status   probeKeyStatus
	Sequence []byte
	Reason   string
}

// defaultProbeKeys returns the keys the setup probe checks. Sequences match
// the user-keys table emitted by tmuxStandaloneConfig / tmuxAppKeyBindings.
func defaultProbeKeys() []probeKey {
	return []probeKey{
		{Label: "Alt-1", Action: "Open sidebar (User4)", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"},
		{Label: "Alt-2", Action: "Open session popup (User2)", Plain: "\x1b2", CSIu: "\x1b[9003u", UserKey: "User2"},
		{Label: "Alt-3", Action: "Open sessionizer (User3)", Plain: "\x1b3", CSIu: "\x1b[9004u", UserKey: "User3"},
		{Label: "Alt-4", Action: "AI split picker right (User5)", Plain: "\x1b4", CSIu: "\x1b[9006u", UserKey: "User5"},
		{Label: "Alt-5", Action: "AI split settings (User6)", Plain: "\x1b5", CSIu: "\x1b[9007u", UserKey: "User6"},
		{Label: "Ctrl-N", Action: "New window (User7)", Plain: "\x0e", CSIu: "\x1b[9008u", UserKey: "User7"},
		{Label: "Ctrl-Shift-R", Action: "No projmux binding by default", Plain: "", CSIu: "", UserKey: ""},
		{Label: "Ctrl-Shift-L", Action: "No projmux binding by default", Plain: "", CSIu: "", UserKey: ""},
		{Label: "Ctrl-M", Action: "Rename window (User10)", Plain: "\r", CSIu: "\x1b[9011u", UserKey: "User10"},
		{Label: "Ctrl-Shift-M", Action: "AI topic prompt (User11)", Plain: "", CSIu: "\x1b[9012u", UserKey: "User11"},
		{Label: "Alt-Shift-Left", Action: "Previous window (User8)", Plain: "\x1b[1;4D", CSIu: "\x1b[9009u", UserKey: "User8"},
		{Label: "Alt-Shift-Right", Action: "Next window (User9)", Plain: "\x1b[1;4C", CSIu: "\x1b[9010u", UserKey: "User9"},
	}
}

const (
	defaultProbeTimeout = 5 * time.Second
)

// Run executes the setup probe.
func (c *setupCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", defaultProbeTimeout, "per-key wait timeout (e.g. 5s)")
	nonInteractive := fs.Bool("non-interactive", false, "skip TTY raw probe; just print the expected key map")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("setup does not accept positional arguments")
	}

	if *nonInteractive {
		return c.printExpectedMap(stdout)
	}

	if c.lookupEnv != nil && c.lookupEnv("TMUX") != "" {
		fmt.Fprintln(stdout, "warning: running inside tmux. tmux already consumes keys; quit tmux and rerun in your raw terminal for accurate results.")
		fmt.Fprintln(stdout)
	}

	terminal := detectTerminal(c.lookupEnv)
	fmt.Fprintf(stdout, "Detected terminal: %s\n", terminal.Display())
	fmt.Fprintln(stdout, "We will ask you to press a series of keys. Each has a", timeout.String(), "window.")
	fmt.Fprintln(stdout, "Press the key, or wait for the timeout to mark it as undelivered. Ctrl-C aborts.")
	fmt.Fprintln(stdout)

	restore, err := c.enterRaw()
	if err != nil {
		return fmt.Errorf("enter raw TTY mode: %w", err)
	}
	defer func() {
		if restore != nil {
			_ = restore()
		}
	}()

	results := make([]probeResult, 0, len(c.defaultKeys))
	for _, key := range c.defaultKeys {
		fmt.Fprintf(stdout, "Press %-16s (%s) ... ", key.Label, key.Action)
		seq, readErr := c.readKey(*timeout)
		if readErr != nil && !errors.Is(readErr, errProbeTimeout) {
			fmt.Fprintln(stdout, "aborted")
			return readErr
		}
		res := classifyProbeInput(key, seq)
		results = append(results, res)
		fmt.Fprintln(stdout, renderProbeStatus(res))
	}

	if restore != nil {
		_ = restore()
		restore = nil
	}

	fmt.Fprintln(stdout)
	renderProbeSummary(stdout, terminal, results)
	return nil
}

func (c *setupCommand) printExpectedMap(stdout io.Writer) error {
	terminal := detectTerminal(c.lookupEnv)
	fmt.Fprintf(stdout, "Detected terminal: %s\n\n", terminal.Display())
	fmt.Fprintln(stdout, "Expected key sequences:")
	for _, key := range c.defaultKeys {
		plain := visibleEscape(key.Plain)
		csiu := visibleEscape(key.CSIu)
		fmt.Fprintf(stdout, "  %-16s plain=%-12s csi-u=%-12s -> %s\n", key.Label, plain, csiu, key.Action)
	}
	return nil
}

// classifyProbeInput inspects the bytes captured for a single keystroke and
// classifies whether the terminal delivered the plain sequence projmux
// expects, the CSI-u escape sequence, an unrelated sequence (different action
// in the host terminal), or nothing at all.
func classifyProbeInput(key probeKey, seq []byte) probeResult {
	res := probeResult{Key: key, Sequence: append([]byte(nil), seq...)}
	if len(seq) == 0 {
		res.Status = probeStatusTimeout
		res.Reason = "no bytes received within timeout (terminal likely swallowed the key)"
		return res
	}
	got := string(seq)
	if key.Plain != "" && got == key.Plain {
		res.Status = probeStatusPlain
		res.Reason = "tmux's plain bind handles this directly"
		return res
	}
	if key.CSIu != "" && got == key.CSIu {
		res.Status = probeStatusCSIu
		res.Reason = "terminal already routes the key via CSI-u (" + key.UserKey + ")"
		return res
	}
	res.Status = probeStatusUnknown
	res.Reason = "received an unexpected sequence " + visibleEscape(got) + "; terminal probably bound this key to its own action"
	return res
}

func renderProbeStatus(res probeResult) string {
	switch res.Status {
	case probeStatusPlain:
		return "OK plain (" + visibleEscape(string(res.Sequence)) + ")"
	case probeStatusCSIu:
		return "OK csi-u (" + visibleEscape(string(res.Sequence)) + " -> " + res.Key.UserKey + ")"
	case probeStatusTimeout:
		return "MISS timeout"
	default:
		return "MISS unknown (" + visibleEscape(string(res.Sequence)) + ")"
	}
}

func renderProbeSummary(w io.Writer, terminal terminalInfo, results []probeResult) {
	fmt.Fprintln(w, "Summary")
	fmt.Fprintln(w, "-------")
	fmt.Fprintf(w, "Terminal      : %s\n", terminal.Display())
	pass, fail := 0, 0
	for _, r := range results {
		if r.Status == probeStatusPlain || r.Status == probeStatusCSIu {
			pass++
		} else {
			fail++
		}
	}
	fmt.Fprintf(w, "Pass / Fail   : %d / %d (of %d)\n", pass, fail, len(results))
	fmt.Fprintln(w)

	if fail == 0 {
		fmt.Fprintln(w, "All probed keys reach this process. Your terminal will not block projmux bindings.")
		return
	}

	fmt.Fprintln(w, "Failures:")
	for _, r := range results {
		if r.Status == probeStatusPlain || r.Status == probeStatusCSIu {
			continue
		}
		label := r.Key.Label
		if r.Key.Action != "" {
			label += " [" + r.Key.Action + "]"
		}
		fmt.Fprintf(w, "  - %-32s %s: %s\n", label, r.Status, r.Reason)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	if hint := terminal.RemediationHint(); hint != "" {
		fmt.Fprintln(w, "  -", hint)
	}
	fmt.Fprintln(w, "  - For each failing key, bind it in your terminal to send the matching CSI-u sequence")
	fmt.Fprintln(w, "    (see `projmux setup --non-interactive` for the table).")
	fmt.Fprintln(w, "  - If your terminal supports automated init, try: projmux init", terminal.Slug)
}

// terminalInfo carries the best-effort terminal identification we managed to
// derive from environment variables.
type terminalInfo struct {
	Slug    string
	Name    string
	Source  string
	Raw     string
	Notable string
}

func (t terminalInfo) Display() string {
	if t.Name == "" {
		return "unknown"
	}
	src := t.Source
	if src == "" {
		src = "env"
	}
	suffix := ""
	if t.Notable != "" {
		suffix = " (" + t.Notable + ")"
	}
	return t.Name + " [" + src + "=" + t.Raw + "]" + suffix
}

// RemediationHint returns a short suggestion tailored to the detected
// terminal, when projmux ships init flow knowledge for it.
func (t terminalInfo) RemediationHint() string {
	switch t.Slug {
	case "ghostty":
		return "Ghostty: add `keybind = alt+one=csi:9005u` style entries to ~/.config/ghostty/config"
	case "wezterm":
		return "WezTerm: map keys to `Action SendString` with the CSI-u escape codes in wezterm.lua"
	case "kitty":
		return "kitty: add `map alt+1 send_text all \\x1b[9005u` (and friends) to kitty.conf"
	case "iterm2":
		return "iTerm2: Profiles > Keys > add per-key Send Escape Sequence entries (e.g. [9005u for Alt-1)"
	case "alacritty":
		return "Alacritty: extend `key_bindings:` in alacritty.toml with `chars = \"\\u001b[9005u\"` style entries"
	case "windows-terminal":
		return "Windows Terminal: edit settings.json `actions` with `sendInput` mappings for the CSI-u sequences"
	case "foot":
		return "foot: define [key-bindings] entries that emit the CSI-u escapes via custom keymap"
	case "vscode":
		return "VS Code terminal swallows many shortcuts; consider running projmux in an external terminal"
	}
	return ""
}

// detectTerminal performs a best-effort identification of the host terminal
// using common environment variables. Order matters: TERM_PROGRAM is the most
// authoritative on macOS-style terminals, LC_TERMINAL is iTerm2's fallback,
// and TERM only catches a few corner cases like foot.
func detectTerminal(lookup func(string) string) terminalInfo {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	get := func(key string) string { return strings.TrimSpace(lookup(key)) }

	checks := []struct {
		key      string
		match    func(string) (terminalInfo, bool)
		fallback bool
	}{
		{key: "GHOSTTY_RESOURCES_DIR", match: func(v string) (terminalInfo, bool) {
			if v == "" {
				return terminalInfo{}, false
			}
			return terminalInfo{Slug: "ghostty", Name: "Ghostty", Source: "GHOSTTY_RESOURCES_DIR", Raw: v}, true
		}},
		{key: "TERM_PROGRAM", match: func(v string) (terminalInfo, bool) {
			lower := strings.ToLower(v)
			switch {
			case lower == "":
				return terminalInfo{}, false
			case strings.Contains(lower, "ghostty"):
				return terminalInfo{Slug: "ghostty", Name: "Ghostty", Source: "TERM_PROGRAM", Raw: v}, true
			case strings.Contains(lower, "wezterm"):
				return terminalInfo{Slug: "wezterm", Name: "WezTerm", Source: "TERM_PROGRAM", Raw: v}, true
			case strings.Contains(lower, "iterm"):
				return terminalInfo{Slug: "iterm2", Name: "iTerm2", Source: "TERM_PROGRAM", Raw: v}, true
			case strings.Contains(lower, "vscode"):
				return terminalInfo{Slug: "vscode", Name: "VS Code", Source: "TERM_PROGRAM", Raw: v, Notable: "embedded terminal"}, true
			case strings.Contains(lower, "apple_terminal"):
				return terminalInfo{Slug: "apple-terminal", Name: "Apple Terminal", Source: "TERM_PROGRAM", Raw: v}, true
			case strings.Contains(lower, "tabby"):
				return terminalInfo{Slug: "tabby", Name: "Tabby", Source: "TERM_PROGRAM", Raw: v}, true
			case strings.Contains(lower, "rio"):
				return terminalInfo{Slug: "rio", Name: "Rio", Source: "TERM_PROGRAM", Raw: v}, true
			}
			return terminalInfo{Slug: "unknown", Name: v, Source: "TERM_PROGRAM", Raw: v}, true
		}},
		{key: "LC_TERMINAL", match: func(v string) (terminalInfo, bool) {
			lower := strings.ToLower(v)
			switch {
			case lower == "":
				return terminalInfo{}, false
			case strings.Contains(lower, "iterm"):
				return terminalInfo{Slug: "iterm2", Name: "iTerm2", Source: "LC_TERMINAL", Raw: v}, true
			case strings.Contains(lower, "wezterm"):
				return terminalInfo{Slug: "wezterm", Name: "WezTerm", Source: "LC_TERMINAL", Raw: v}, true
			}
			return terminalInfo{Slug: "unknown", Name: v, Source: "LC_TERMINAL", Raw: v}, true
		}},
		{key: "KITTY_WINDOW_ID", match: func(v string) (terminalInfo, bool) {
			if v == "" {
				return terminalInfo{}, false
			}
			return terminalInfo{Slug: "kitty", Name: "kitty", Source: "KITTY_WINDOW_ID", Raw: v}, true
		}},
		{key: "ALACRITTY_WINDOW_ID", match: func(v string) (terminalInfo, bool) {
			if v == "" {
				return terminalInfo{}, false
			}
			return terminalInfo{Slug: "alacritty", Name: "Alacritty", Source: "ALACRITTY_WINDOW_ID", Raw: v}, true
		}},
		{key: "WT_SESSION", match: func(v string) (terminalInfo, bool) {
			if v == "" {
				return terminalInfo{}, false
			}
			return terminalInfo{Slug: "windows-terminal", Name: "Windows Terminal", Source: "WT_SESSION", Raw: v}, true
		}},
		{key: "TERM", match: func(v string) (terminalInfo, bool) {
			lower := strings.ToLower(v)
			switch {
			case lower == "":
				return terminalInfo{}, false
			case strings.HasPrefix(lower, "foot"):
				return terminalInfo{Slug: "foot", Name: "foot", Source: "TERM", Raw: v}, true
			case strings.HasPrefix(lower, "rxvt"):
				return terminalInfo{Slug: "rxvt", Name: "rxvt", Source: "TERM", Raw: v}, true
			case strings.HasPrefix(lower, "screen") || strings.HasPrefix(lower, "tmux"):
				return terminalInfo{Slug: "multiplexer", Name: lower, Source: "TERM", Raw: v, Notable: "multiplexer in TERM; run probe in your real terminal"}, true
			}
			return terminalInfo{}, false
		}},
	}

	for _, c := range checks {
		raw := get(c.key)
		if info, ok := c.match(raw); ok {
			return info
		}
	}
	if t := get("TERM"); t != "" {
		return terminalInfo{Slug: "unknown", Name: t, Source: "TERM", Raw: t}
	}
	return terminalInfo{Slug: "unknown", Name: "unknown"}
}

// visibleEscape renders byte sequences with control characters escaped so the
// summary is readable on a normal terminal (e.g. "\x1b[9005u" instead of
// emitting a literal escape that re-triggers the user's terminal).
func visibleEscape(s string) string {
	if s == "" {
		return "\"\""
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range []byte(s) {
		switch {
		case r == 0x1b:
			b.WriteString("\\x1b")
		case r == '\r':
			b.WriteString("\\r")
		case r == '\n':
			b.WriteString("\\n")
		case r == '\t':
			b.WriteString("\\t")
		case r < 0x20 || r == 0x7f:
			b.WriteString(fmt.Sprintf("\\x%02x", r))
		default:
			b.WriteByte(r)
		}
	}
	return b.String()
}

var errProbeTimeout = errors.New("probe key read timed out")

// readKeySequence reads a single keystroke worth of bytes from the provided
// stdin within timeout. It uses a short post-first-byte drain window to coal
// CSI/multi-byte sequences (\x1b[9005u, \x1b[1;4D, ...) into one read.
func readKeySequence(stdin io.Reader, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type chunk struct {
		buf []byte
		err error
	}

	results := make(chan chunk, 1)
	go func() {
		// Read one byte first so we have something to time-bound against.
		first := make([]byte, 1)
		n, err := stdin.Read(first)
		if err != nil || n == 0 {
			results <- chunk{nil, err}
			return
		}
		out := append([]byte(nil), first[:n]...)
		// Drain whatever else arrived as part of the same escape sequence
		// using a short blocking read with a per-byte budget. We use a
		// small fixed buffer and stop as soon as the next read does not
		// produce data within ~30ms.
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer drainCancel()

		more := make(chan chunk, 1)
		go func() {
			buf := make([]byte, 31)
			n, err := stdin.Read(buf)
			more <- chunk{buf[:n], err}
		}()

		select {
		case extra := <-more:
			out = append(out, extra.buf...)
			results <- chunk{out, extra.err}
		case <-drainCtx.Done():
			results <- chunk{out, nil}
		}
	}()

	select {
	case r := <-results:
		if r.err != nil && len(r.buf) == 0 {
			return nil, r.err
		}
		return r.buf, nil
	case <-ctx.Done():
		return nil, errProbeTimeout
	}
}

// enterTTYRawMode flips the controlling TTY to raw mode using the standard
// `stty` utility, returning a restore callback that resets it on exit. We
// rely on `stty` rather than a raw-mode dependency to avoid pulling in an
// extra module while still working on every supported POSIX target.
func enterTTYRawMode(lookupEnv func(string) string) (func() error, error) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if _, err := exec.LookPath("stty"); err != nil {
		return nil, fmt.Errorf("stty utility not found: %w", err)
	}
	saved, err := runSttyOn("-g")
	if err != nil {
		return nil, err
	}
	saved = strings.TrimSpace(saved)
	if _, err := runSttyOn("raw", "-echo", "-icanon", "min", "1", "time", "0"); err != nil {
		return nil, err
	}
	restore := func() error {
		if saved == "" {
			return nil
		}
		_, err := runSttyOn(saved)
		return err
	}
	return restore, nil
}

func runSttyOn(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("stty %s failed: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// openControllingTTY is a placeholder hook used in tests; the production probe
// flow reads from os.Stdin directly. Keeping it as an injected function keeps
// the door open for future unit-level integration testing.
func openControllingTTY() (*os.File, func() error, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() error { return f.Close() }
	return f, cleanup, nil
}

// sortedProbeLabels is a tiny helper used in tests to assert label ordering
// independent of map iteration; exported via package-private call sites.
func sortedProbeLabels(keys []probeKey) []string {
	labels := make([]string, 0, len(keys))
	for _, k := range keys {
		labels = append(labels, k.Label)
	}
	sort.Strings(labels)
	return labels
}
