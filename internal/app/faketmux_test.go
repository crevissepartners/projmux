package app

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// fakeTmux is an in-memory tmux server for the materialization tests.
//
// It models exactly the surface the create routes use -- sessions, windows,
// panes, scoped options, and the three list/read formats they emit -- and it
// records every argv. That makes two otherwise awkward properties directly
// assertable: that a create never issues a focus-moving command, and that a
// rollback removes exactly the objects the operation created.
type fakeTmux struct {
	// mu makes the server safe for the concurrency suite, where several create
	// operations race for the on-disk registry lock while sharing one runtime.
	mu       sync.Mutex
	sessions []*fakeTmuxSession
	calls    [][]string
	nextID   int
	// fail injects a failure for the first command whose argv contains every
	// token of the trigger. It fires once unless failAlways is set.
	fail        []string
	failMessage string
	failed      bool
	// failAlways keeps the trigger armed. A one-shot trigger cannot model a
	// query that is simply unavailable -- reconcile reads some inventories more
	// than once per pass, and the second read would then succeed and hide the
	// fail-closed behavior the test is checking.
	failAlways bool
}

type fakeTmuxSession struct {
	id      string
	name    string
	opts    map[string]string
	windows []*fakeTmuxWindow
}

type fakeTmuxWindow struct {
	id    string
	name  string
	opts  map[string]string
	panes []*fakeTmuxPane
}

type fakeTmuxPane struct {
	id      string
	opts    map[string]string
	command string
}

func newFakeTmux() *fakeTmux { return &fakeTmux{} }

func (f *fakeTmux) mint(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%d", prefix, f.nextID)
}

func (f *fakeTmux) session(name string) *fakeTmuxSession {
	name = strings.TrimPrefix(strings.TrimSpace(name), "=")
	name = strings.TrimSuffix(name, ":")
	for _, s := range f.sessions {
		if s.name == name || s.id == name {
			return s
		}
	}
	return nil
}

func (f *fakeTmux) sessionNames() map[string]bool {
	out := map[string]bool{}
	for _, s := range f.sessions {
		out[s.name] = true
	}
	return out
}

// addSession creates a session with one window holding one pane, the way tmux
// `new-session` does.
func (f *fakeTmux) addSession(name string) *fakeTmuxSession {
	session := &fakeTmuxSession{id: f.mint("$"), name: name, opts: map[string]string{}}
	window := &fakeTmuxWindow{id: f.mint("@"), name: "tmux", opts: map[string]string{}}
	window.panes = append(window.panes, &fakeTmuxPane{id: f.mint("%"), opts: map[string]string{}})
	session.windows = append(session.windows, window)
	f.sessions = append(f.sessions, session)
	return session
}

func (f *fakeTmux) window(target string) (*fakeTmuxSession, *fakeTmuxWindow) {
	target = strings.TrimSpace(target)
	for _, s := range f.sessions {
		for _, w := range s.windows {
			if w.id == target {
				return s, w
			}
		}
	}
	return nil, nil
}

func (f *fakeTmux) pane(target string) (*fakeTmuxSession, *fakeTmuxWindow, *fakeTmuxPane) {
	target = strings.TrimSpace(target)
	for _, s := range f.sessions {
		for _, w := range s.windows {
			for _, p := range w.panes {
				if p.id == target {
					return s, w, p
				}
			}
		}
	}
	return nil, nil, nil
}

// paneCount returns the number of live panes across every session.
func (f *fakeTmux) paneCount() int {
	total := 0
	for _, s := range f.sessions {
		for _, w := range s.windows {
			total += len(w.panes)
		}
	}
	return total
}

// windowCount returns the number of live windows across every session.
func (f *fakeTmux) windowCount() int {
	total := 0
	for _, s := range f.sessions {
		total += len(s.windows)
	}
	return total
}

// state renders a stable description of the whole server for before/after
// comparisons.
func (f *fakeTmux) state() string {
	var b strings.Builder
	for _, s := range f.sessions {
		fmt.Fprintf(&b, "session %s %s %v\n", s.id, s.name, s.opts)
		for _, w := range s.windows {
			fmt.Fprintf(&b, "  window %s %s %v\n", w.id, w.name, w.opts)
			for _, p := range w.panes {
				fmt.Fprintf(&b, "    pane %s %v cmd=%q\n", p.id, p.opts, p.command)
			}
		}
	}
	return b.String()
}

// argvContains reports whether any recorded call contains token.
func (f *fakeTmux) argvContains(token string) bool {
	return slices.ContainsFunc(f.calls, func(call []string) bool {
		return slices.Contains(call, token)
	})
}

func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// Run answers one tmux invocation.
func (f *fakeTmux) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != "tmux" {
		return nil, fmt.Errorf("fake tmux: unexpected binary %q", name)
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	if (!f.failed || f.failAlways) && len(f.fail) > 0 && containsAll(args, f.fail) {
		f.failed = true
		message := f.failMessage
		if message == "" {
			message = "injected tmux failure"
		}
		// The real runner returns an *exec.ExitError here, which is what makes
		// the exit-code suppression trap in cmd/projmux reachable at all.
		return nil, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), &exec.ExitError{}, message)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("fake tmux: empty argv")
	}
	switch args[0] {
	case "new-window":
		return f.runNewWindow(args)
	case "split-window":
		return f.runSplitWindow(args)
	case "list-windows":
		return f.runListWindows(args)
	case "list-panes":
		return f.runListPanes(args)
	case "display-message":
		return f.runDisplayMessage(args)
	case "set-option":
		return f.runSetOption(args)
	case "rename-window":
		return f.runRenameWindow(args)
	case "kill-session", "kill-window", "kill-pane":
		return f.runKill(args)
	case "list-sessions":
		var b strings.Builder
		for _, s := range f.sessions {
			fmt.Fprintf(&b, "%s\n", s.name)
		}
		return []byte(b.String()), nil
	default:
		return nil, fmt.Errorf("fake tmux: unsupported command %q", args[0])
	}
}

func containsAll(args, want []string) bool {
	for _, token := range want {
		if !slices.Contains(args, token) {
			return false
		}
	}
	return true
}

func (f *fakeTmux) runNewWindow(args []string) ([]byte, error) {
	session := f.session(flagValue(args, "-t"))
	if session == nil {
		return nil, fmt.Errorf("fake tmux: new-window: no session %q", flagValue(args, "-t"))
	}
	window := &fakeTmuxWindow{id: f.mint("@"), name: flagValue(args, "-n"), opts: map[string]string{}}
	window.panes = append(window.panes, &fakeTmuxPane{
		id:      f.mint("%"),
		opts:    map[string]string{},
		command: strings.Join(trailingCommand(args), " "),
	})
	session.windows = append(session.windows, window)
	return []byte(window.id + "\n"), nil
}

func (f *fakeTmux) runSplitWindow(args []string) ([]byte, error) {
	_, window, _ := f.pane(flagValue(args, "-t"))
	if window == nil {
		return nil, fmt.Errorf("fake tmux: split-window: no pane %q", flagValue(args, "-t"))
	}
	pane := &fakeTmuxPane{
		id:      f.mint("%"),
		opts:    map[string]string{},
		command: strings.Join(trailingCommand(args), " "),
	}
	window.panes = append(window.panes, pane)
	return []byte(pane.id + "\n"), nil
}

// trailingCommand returns the shell-command tail of a new-window/split-window
// argv: everything after the last recognized option pair.
func trailingCommand(args []string) []string {
	valued := map[string]bool{"-t": true, "-c": true, "-n": true, "-F": true}
	i := 1
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		if valued[arg] {
			i += 2
			continue
		}
		i++
	}
	return args[i:]
}

func (f *fakeTmux) runListWindows(args []string) ([]byte, error) {
	// `-a` is the server-wide window inventory the mirrored-uid Window lookup
	// reads. It ignores `-t` entirely, exactly like tmux.
	if slices.Contains(args, "-a") {
		format := flagValue(args, "-F")
		var b strings.Builder
		for _, s := range f.sessions {
			for _, w := range s.windows {
				b.WriteString(renderFormat(format, s, w, nil))
				b.WriteString("\n")
			}
		}
		return []byte(b.String()), nil
	}
	session := f.session(flagValue(args, "-t"))
	if session == nil {
		return nil, fmt.Errorf("fake tmux: list-windows: no session %q", flagValue(args, "-t"))
	}
	format := flagValue(args, "-F")
	var b strings.Builder
	for _, w := range session.windows {
		b.WriteString(renderFormat(format, session, w, nil))
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

func (f *fakeTmux) runListPanes(args []string) ([]byte, error) {
	// `-a` is the server-wide inventory the mirrored-uid lookup and the
	// dead-pane sweep both read. It ignores `-t` entirely, exactly like tmux.
	if slices.Contains(args, "-a") {
		format := flagValue(args, "-F")
		var b strings.Builder
		for _, s := range f.sessions {
			for _, w := range s.windows {
				for _, p := range w.panes {
					b.WriteString(renderFormat(format, s, w, p))
					b.WriteString("\n")
				}
			}
		}
		return []byte(b.String()), nil
	}
	target := flagValue(args, "-t")
	session, window := f.window(target)
	if window == nil {
		if s := f.session(target); s != nil && len(s.windows) > 0 {
			session, window = s, s.windows[0]
		}
	}
	if window == nil {
		return nil, fmt.Errorf("fake tmux: list-panes: no window %q", target)
	}
	format := flagValue(args, "-F")
	var b strings.Builder
	for _, p := range window.panes {
		b.WriteString(renderFormat(format, session, window, p))
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

func (f *fakeTmux) runDisplayMessage(args []string) ([]byte, error) {
	target := flagValue(args, "-t")
	format := flagValue(args, "-F")
	if session, window, pane := f.pane(target); pane != nil {
		return []byte(renderFormat(format, session, window, pane) + "\n"), nil
	}
	if session, window := f.window(target); window != nil {
		return []byte(renderFormat(format, session, window, nil) + "\n"), nil
	}
	if session := f.session(target); session != nil {
		return []byte(renderFormat(format, session, nil, nil) + "\n"), nil
	}
	return nil, fmt.Errorf("fake tmux: display-message: no target %q", target)
}

func (f *fakeTmux) runSetOption(args []string) ([]byte, error) {
	target := flagValue(args, "-t")
	rest := optionAssignment(args)
	if len(rest) != 2 {
		return nil, fmt.Errorf("fake tmux: set-option: cannot parse %v", args)
	}
	switch {
	case slices.Contains(args, "-p"):
		if _, _, pane := f.pane(target); pane != nil {
			pane.opts[rest[0]] = rest[1]
			return nil, nil
		}
	case slices.Contains(args, "-w"):
		if _, window := f.window(target); window != nil {
			window.opts[rest[0]] = rest[1]
			return nil, nil
		}
	default:
		if session := f.session(target); session != nil {
			session.opts[rest[0]] = rest[1]
			return nil, nil
		}
	}
	return nil, fmt.Errorf("fake tmux: set-option: no target %q", target)
}

// optionAssignment returns the trailing "<option> <value>" pair of a set-option
// argv.
func optionAssignment(args []string) []string {
	valued := map[string]bool{"-t": true}
	i := 1
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		if valued[arg] {
			i += 2
			continue
		}
		i++
	}
	return args[i:]
}

func (f *fakeTmux) runRenameWindow(args []string) ([]byte, error) {
	target := flagValue(args, "-t")
	_, window := f.window(target)
	if window == nil {
		return nil, fmt.Errorf("fake tmux: rename-window: no window %q", target)
	}
	window.name = args[len(args)-1]
	return nil, nil
}

func (f *fakeTmux) runKill(args []string) ([]byte, error) {
	target := flagValue(args, "-t")
	switch args[0] {
	case "kill-session":
		for i, s := range f.sessions {
			if s.id == target || s.name == target {
				f.sessions = slices.Delete(f.sessions, i, i+1)
				return nil, nil
			}
		}
	case "kill-window":
		for _, s := range f.sessions {
			for i, w := range s.windows {
				if w.id == target {
					s.windows = slices.Delete(s.windows, i, i+1)
					return nil, nil
				}
			}
		}
	case "kill-pane":
		for _, s := range f.sessions {
			for _, w := range s.windows {
				for i, p := range w.panes {
					if p.id == target {
						w.panes = slices.Delete(w.panes, i, i+1)
						return nil, nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("fake tmux: %s: no target %q", args[0], target)
}

// renderFormat expands the tmux format tokens the create routes actually emit.
//
// The separator handling mirrors real tmux: a format carries the escaped `\037`
// spelling and the output carries it back verbatim, which is what both tmux 3.5a
// and 3.6 do. A fake that echoed a raw 0x1F would hide the parsing bug that
// spelling exists to avoid.
func renderFormat(format string, session *fakeTmuxSession, window *fakeTmuxWindow, pane *fakeTmuxPane) string {
	fields := strings.Split(format, tmuxRowSepFormat)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		token := strings.TrimSuffix(strings.TrimPrefix(field, "#{"), "}")
		switch {
		case token == "session_id" && session != nil:
			out = append(out, session.id)
		case token == "session_name" && session != nil:
			out = append(out, session.name)
		case token == "window_id" && window != nil:
			out = append(out, window.id)
		case token == "window_name" && window != nil:
			out = append(out, window.name)
		case token == "pane_id" && pane != nil:
			out = append(out, pane.id)
		case strings.HasPrefix(token, "@"):
			out = append(out, scopedOption(token, session, window, pane))
		default:
			out = append(out, "")
		}
	}
	return strings.Join(out, tmuxRowSepFormat)
}

// scopedOption reads a projmux option from the narrowest scope the caller
// addressed, which is how tmux resolves a pane/window/session option format.
func scopedOption(token string, session *fakeTmuxSession, window *fakeTmuxWindow, pane *fakeTmuxPane) string {
	if pane != nil {
		if value, ok := pane.opts[token]; ok {
			return value
		}
	}
	if window != nil {
		if value, ok := window.opts[token]; ok {
			return value
		}
	}
	if session != nil {
		if value, ok := session.opts[token]; ok {
			return value
		}
	}
	return ""
}
