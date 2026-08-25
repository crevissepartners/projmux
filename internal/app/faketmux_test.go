package app

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
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
	// clientMessages records the exact-client status messages interactive tmux
	// actions converge on, in order.
	clientMessages []fakeTmuxClientMessage
	// fail injects a failure for the first command whose argv contains every
	// token of the trigger. It fires once unless failAlways is set.
	fail        []string
	failMessage string
	failed      bool
	// failAfterMutation models tmux lifecycle-hook failures: tmux has already
	// applied the requested mutation and produced its normal output, then returns
	// the hook's non-zero status and diagnostic in the same combined output.
	failAfterMutation bool
	// afterNewWindow and newWindowResult are deterministic attribution-race
	// seams. The callback may add/move runtime objects after tmux created the
	// requested Window; the result hook may return stale or foreign handles.
	afterNewWindow    func(*fakeTmux, *fakeTmuxSession, *fakeTmuxWindow, *fakeTmuxPane)
	newWindowResult   func(*fakeTmuxSession, *fakeTmuxWindow, *fakeTmuxPane) string
	afterListSessions func(*fakeTmux)
	// beforeOwnerInventory fires once, just before the first server-wide
	// `list-windows -a` is served. That is exactly the plan-to-execute boundary
	// where a topology owner guard reads, so a test can move a runtime object
	// after planning committed to it and before any mutation runs.
	beforeOwnerInventory func(*fakeTmux)
	// appMarker is the server-global @projmux_app value. It defaults to the
	// app-owned marker because every fixture in this package models a server
	// projmux started; a standalone fixture clears it.
	appMarker    string
	socketName   string
	serverAbsent bool
	// socketPath is what `display-message -p '#{socket_path}'` answers, which
	// is the controller kernel's socket guard.
	socketPath string
	serverPID  string
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
	env     map[string]string
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
	left    int
	top     int
	width   int
	height  int
}

func newFakeTmuxPane(id string) *fakeTmuxPane {
	return &fakeTmuxPane{id: id, opts: map[string]string{}, width: 80, height: 24}
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{appMarker: "1", socketName: defaultAppSocket, socketPath: "/tmp/fake-tmux/default", serverPID: "4242"}
}

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
	session := &fakeTmuxSession{id: f.mint("$"), name: name, opts: map[string]string{}, env: map[string]string{}}
	window := &fakeTmuxWindow{id: f.mint("@"), name: "tmux", opts: map[string]string{}}
	window.panes = append(window.panes, newFakeTmuxPane(f.mint("%")))
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

// tmuxCommandArgv returns the command portion while preserving fakeTmux.calls'
// exact route evidence. Legacy behavior tests generally care about the tmux
// verb; Phase 10 route tests inspect the original call directly.
func tmuxCommandArgv(call []string) []string {
	if len(call) >= 3 && (call[0] == "-L" || call[0] == "-S") {
		return call[2:]
	}
	return call
}

// settingsLiveTestRunner supplies the exact route observations required by the
// production Settings reload boundary while preserving the narrow historical
// test seam that records only the eventual tmux mutation.
type settingsLiveTestRunner struct {
	run        func(string, ...string) error
	output     func(string, ...string) ([]byte, error)
	socketPath string
}

func (r *settingsLiveTestRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	argv := tmuxCommandArgv(args)
	if r.socketPath == "" {
		switch {
		case len(args) >= 2 && args[0] == "-S":
			r.socketPath = args[1]
		case len(args) >= 2 && args[0] == "-L":
			r.socketPath = "/tmp/tmux-test/" + args[1]
		default:
			r.socketPath = "/tmp/tmux-test/projmux"
		}
	}
	joined := strings.Join(argv, " ")
	switch {
	case strings.Contains(joined, "#{socket_path}") && strings.Contains(joined, "#{pid}") && strings.Contains(joined, "#{session_id}"):
		return []byte(strings.Join([]string{r.socketPath, "4242", "$1", "@1", "%1"}, tmuxRowSep) + "\n"), nil
	case strings.Contains(joined, "display-message -p -F #{socket_path}"):
		return []byte(r.socketPath + "\n"), nil
	case strings.Contains(joined, "display-message -p -F #{pid}"):
		return []byte("4242\n"), nil
	case strings.Contains(joined, "show-options -gqv "+tmuxopts.AppGlobal):
		return []byte("1\n"), nil
	case strings.Contains(joined, "show-options -gqv "+runtimeMutationSocketNameOption):
		return []byte(defaultAppSocket + "\n"), nil
	case strings.Contains(joined, "show-options -gqv "+tmuxSequenceRootsOption),
		strings.Contains(joined, "show-options -gqv "+tmuxSequenceTablesOption):
		if r.output != nil {
			return r.output(name, argv...)
		}
		return nil, nil
	}
	if r.run != nil {
		return nil, r.run(name, argv...)
	}
	return nil, nil
}

func wireSettingsLiveTestRunner(cmd *settingsCommand) {
	originalLookup := cmd.lookupEnv
	path := "/tmp/tmux-test/projmux"
	if originalLookup != nil {
		if inherited := strings.TrimSpace(originalLookup("TMUX")); inherited != "" {
			candidate := strings.Split(inherited, ",")[0]
			if filepath.IsAbs(candidate) && filepath.Clean(candidate) == candidate {
				path = candidate
			}
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "TMUX":
					return path + ",4242,0"
				case runtimeMutationAnchorPaneEnv:
					return "%1"
				}
				return originalLookup(name)
			}
		}
	}
	cmd.tmuxRunner = &settingsLiveTestRunner{run: cmd.runCommand, output: cmd.runOutput, socketPath: path}
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
	shouldFail := (!f.failed || f.failAlways) && len(f.fail) > 0 && containsAll(args, f.fail)
	if shouldFail && !f.failAfterMutation {
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
	// Explicit runtime mutation routes precede the command with -L/-S. Keep the
	// recorded argv exact, then dispatch the same server model after the route.
	if len(args) >= 3 && (args[0] == "-L" || args[0] == "-S") {
		args = args[2:]
	}
	if len(args) >= 3 && args[0] == "-f" {
		args = args[2:]
	}
	if f.serverAbsent && (len(args) == 0 || args[0] != "new-session") {
		return nil, appTypedCommandFailure{failure: inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "no server running on " + f.socketPath,
		}}
	}
	if len(args) > 0 && args[0] == "new-session" {
		f.serverAbsent = false
	}
	var out []byte
	var err error
	switch args[0] {
	case "new-session":
		out, err = f.runNewSession(args)
	case "new-window":
		out, err = f.runNewWindow(args)
	case "split-window":
		out, err = f.runSplitWindow(args)
	case "list-windows":
		return f.runListWindows(args)
	case "list-panes":
		return f.runListPanes(args)
	case "display-message":
		return f.runDisplayMessage(args)
	case "resize-pane":
		return f.runResizePane(args)
	case "set-option":
		out, err = f.runSetOption(args)
	case "set-environment":
		return f.runSetEnvironment(args)
	case "show-environment":
		return f.runShowEnvironment(args)
	case "rename-window":
		return f.runRenameWindow(args)
	case "kill-session", "kill-window", "kill-pane":
		return f.runKill(args)
	case "show-options":
		return f.runShowOptions(args)
	case "list-sessions":
		var b strings.Builder
		format := flagValue(args, "-F")
		if format == "" {
			format = "#{session_name}"
		}
		for _, s := range f.sessions {
			var window *fakeTmuxWindow
			var pane *fakeTmuxPane
			if len(s.windows) > 0 {
				window = s.windows[0]
				if len(window.panes) > 0 {
					pane = window.panes[0]
				}
			}
			fmt.Fprintf(&b, "%s\n", renderFormat(format, s, window, pane))
		}
		if f.afterListSessions != nil {
			callback := f.afterListSessions
			f.afterListSessions = nil
			callback(f)
		}
		return []byte(b.String()), nil
	default:
		return nil, fmt.Errorf("fake tmux: unsupported command %q", args[0])
	}
	if err != nil {
		return out, err
	}
	if shouldFail {
		f.failed = true
		message := f.failMessage
		if message == "" {
			message = "injected tmux failure"
		}
		return append(out, []byte("'exit 7' returned 7: "+message+"\n")...),
			fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), &exec.ExitError{}, message)
	}
	return out, nil
}

func (f *fakeTmux) runNewSession(args []string) ([]byte, error) {
	name := flagValue(args, "-s")
	if strings.TrimSpace(name) == "" || f.session(name) != nil {
		return nil, fmt.Errorf("fake tmux: new-session target %q is missing or already exists", name)
	}
	session := f.addSession(name)
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-e" {
			continue
		}
		key, value, ok := strings.Cut(args[i+1], "=")
		if ok {
			session.env[key] = value
		}
	}
	format := flagValue(args, "-F")
	if format == "" {
		format = "#{session_id}"
	}
	return []byte(renderFormat(format, session, session.windows[0], session.windows[0].panes[0]) + "\n"), nil
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
	pane := newFakeTmuxPane(f.mint("%"))
	pane.command = strings.Join(trailingCommand(args), " ")
	window.panes = append(window.panes, pane)
	session.windows = append(session.windows, window)
	if f.afterNewWindow != nil {
		f.afterNewWindow(f, session, window, pane)
	}
	if f.newWindowResult != nil {
		return []byte(f.newWindowResult(session, window, pane) + "\n"), nil
	}
	format := flagValue(args, "-F")
	if format == "" {
		format = "#{window_id}"
	}
	return []byte(renderFormat(format, session, window, pane) + "\n"), nil
}

func (f *fakeTmux) runSplitWindow(args []string) ([]byte, error) {
	_, window, _ := f.pane(flagValue(args, "-t"))
	if window == nil {
		return nil, fmt.Errorf("fake tmux: split-window: no pane %q", flagValue(args, "-t"))
	}
	_, _, anchor := f.pane(flagValue(args, "-t"))
	if anchor == nil {
		return nil, fmt.Errorf("fake tmux: split-window: no anchor pane %q", flagValue(args, "-t"))
	}
	pane := newFakeTmuxPane(f.mint("%"))
	pane.command = strings.Join(trailingCommand(args), " ")
	if slices.Contains(args, "-v") {
		pane.left, pane.width = anchor.left, anchor.width
		pane.height = max(1, (anchor.height-1)/2)
		anchor.height = max(1, anchor.height-pane.height-1)
		pane.top = anchor.top + anchor.height + 1
	} else {
		pane.top, pane.height = anchor.top, anchor.height
		pane.width = max(1, (anchor.width-1)/2)
		anchor.width = max(1, anchor.width-pane.width-1)
		pane.left = anchor.left + anchor.width + 1
	}
	window.panes = append(window.panes, pane)
	return []byte(pane.id + "\n"), nil
}

func (f *fakeTmux) runResizePane(args []string) ([]byte, error) {
	_, _, pane := f.pane(flagValue(args, "-t"))
	if pane == nil {
		return nil, fmt.Errorf("fake tmux: resize-pane: no pane %q", flagValue(args, "-t"))
	}
	if raw := flagValue(args, "-x"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &pane.width); err != nil {
			return nil, fmt.Errorf("fake tmux: resize-pane: invalid width %q", raw)
		}
	}
	if raw := flagValue(args, "-y"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &pane.height); err != nil {
			return nil, fmt.Errorf("fake tmux: resize-pane: invalid height %q", raw)
		}
	}
	return nil, nil
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
		if f.beforeOwnerInventory != nil {
			inject := f.beforeOwnerInventory
			f.beforeOwnerInventory = nil
			inject(f)
		}
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
	if slices.Contains(args, "-s") {
		session := f.session(target)
		if session == nil {
			return nil, fmt.Errorf("fake tmux: list-panes: no session %q", target)
		}
		format := flagValue(args, "-F")
		var b strings.Builder
		for _, window := range session.windows {
			for _, pane := range window.panes {
				b.WriteString(renderFormat(format, session, window, pane))
				b.WriteString("\n")
			}
		}
		return []byte(b.String()), nil
	}
	session, window := f.window(target)
	if paneSession, paneWindow, pane := f.pane(target); pane != nil {
		session, window = paneSession, paneWindow
	}
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

// runShowOptions answers the server-global reads the resolved graph takes. Only
// the @projmux_app marker is modeled; anything else reads as absent, exactly
// like tmux's -v output for an unset user option.
func (f *fakeTmux) runShowOptions(args []string) ([]byte, error) {
	option := args[len(args)-1]
	if slices.Contains(args, "-wqv") {
		_, window := f.window(flagValue(args, "-t"))
		if window == nil {
			return nil, fmt.Errorf("fake tmux: show-options: no window %q", flagValue(args, "-t"))
		}
		return []byte(window.opts[option] + "\n"), nil
	}
	if slices.Contains(args, "-pqv") {
		_, _, pane := f.pane(flagValue(args, "-t"))
		if pane == nil {
			return nil, fmt.Errorf("fake tmux: show-options: no pane %q", flagValue(args, "-t"))
		}
		return []byte(pane.opts[option] + "\n"), nil
	}
	if slices.Contains(args, "-qv") {
		session := f.session(flagValue(args, "-t"))
		if session == nil {
			return nil, fmt.Errorf("fake tmux: show-options: no session %q", flagValue(args, "-t"))
		}
		return []byte(session.opts[option] + "\n"), nil
	}
	if !slices.Contains(args, "-gv") && !slices.Contains(args, "-gqv") {
		return nil, fmt.Errorf("fake tmux: show-options: unsupported argv %v", args)
	}
	if option == tmuxopts.AppGlobal {
		return []byte(f.appMarker + "\n"), nil
	}
	if option == runtimeMutationSocketNameOption {
		return []byte(f.socketName + "\n"), nil
	}
	return []byte("\n"), nil
}

// fakeTmuxClientMessage is one `display-message -c <client> ... <text>` write.
type fakeTmuxClientMessage struct {
	client string
	text   string
}

func (f *fakeTmux) runDisplayMessage(args []string) ([]byte, error) {
	target := flagValue(args, "-t")
	format := flagValue(args, "-F")
	// A client-scoped message with no format is a status write, not a read:
	// this is where every interactive action's bounded result lands.
	if client := flagValue(args, "-c"); client != "" && format == "" && len(args) > 0 {
		f.clientMessages = append(f.clientMessages, fakeTmuxClientMessage{client: client, text: args[len(args)-1]})
		return nil, nil
	}
	// A targetless display-message is a server-scope read. `#{socket_path}` is
	// the only one the app takes, and it is the controller's socket guard.
	if target == "" {
		if len(args) > 0 && args[len(args)-1] == "#{socket_path}" {
			return []byte(f.socketPath + "\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "#{pid}" {
			return []byte(f.serverPID + "\n"), nil
		}
		return nil, fmt.Errorf("fake tmux: display-message: no target %q", target)
	}
	if session, window, pane := f.pane(target); pane != nil {
		return []byte(f.renderFormat(format, session, window, pane) + "\n"), nil
	}
	if session, window := f.window(target); window != nil {
		return []byte(renderFormat(format, session, window, nil) + "\n"), nil
	}
	if session := f.session(target); session != nil {
		return []byte(renderFormat(format, session, nil, nil) + "\n"), nil
	}
	return nil, fmt.Errorf("fake tmux: display-message: no target %q", target)
}

func (f *fakeTmux) renderFormat(format string, session *fakeTmuxSession, window *fakeTmuxWindow, pane *fakeTmuxPane) string {
	fields := strings.Split(format, tmuxRowSepFormat)
	for index, field := range fields {
		switch field {
		case "#{socket_path}":
			fields[index] = f.socketPath
		case "#{pid}":
			fields[index] = f.serverPID
		default:
			fields[index] = renderFormat(field, session, window, pane)
		}
	}
	return strings.Join(fields, tmuxRowSepFormat)
}

func (f *fakeTmux) runSetOption(args []string) ([]byte, error) {
	target := flagValue(args, "-t")
	rest := optionAssignment(args)
	unset := slices.Contains(args, "-u")
	if (!unset && len(rest) != 2) || (unset && len(rest) != 1) {
		return nil, fmt.Errorf("fake tmux: set-option: cannot parse %v", args)
	}
	value := ""
	if !unset {
		value = rest[1]
	}
	switch {
	case slices.Contains(args, "-g") || slices.Contains(args, "-gq"):
		if unset {
			switch rest[0] {
			case tmuxopts.AppGlobal:
				f.appMarker = ""
			case runtimeMutationSocketNameOption:
				f.socketName = ""
			}
		} else {
			switch rest[0] {
			case tmuxopts.AppGlobal:
				f.appMarker = value
			case runtimeMutationSocketNameOption:
				f.socketName = value
			}
		}
		return nil, nil
	case slices.Contains(args, "-p"):
		if _, _, pane := f.pane(target); pane != nil {
			if unset {
				delete(pane.opts, rest[0])
			} else {
				pane.opts[rest[0]] = value
			}
			return nil, nil
		}
	case slices.Contains(args, "-w"):
		if _, window := f.window(target); window != nil {
			if unset {
				delete(window.opts, rest[0])
			} else {
				window.opts[rest[0]] = value
			}
			return nil, nil
		}
	default:
		if session := f.session(target); session != nil {
			if unset {
				delete(session.opts, rest[0])
			} else {
				session.opts[rest[0]] = value
			}
			return nil, nil
		}
	}
	return nil, fmt.Errorf("fake tmux: set-option: no target %q", target)
}

func (f *fakeTmux) runSetEnvironment(args []string) ([]byte, error) {
	session := f.session(flagValue(args, "-t"))
	if session == nil {
		return nil, fmt.Errorf("fake tmux: set-environment: no session %q", flagValue(args, "-t"))
	}
	name := args[len(args)-2]
	if slices.Contains(args, "-u") {
		name = args[len(args)-1]
		delete(session.env, name)
		return nil, nil
	}
	session.env[name] = args[len(args)-1]
	return nil, nil
}

func (f *fakeTmux) runShowEnvironment(args []string) ([]byte, error) {
	session := f.session(flagValue(args, "-t"))
	if session == nil {
		return nil, fmt.Errorf("fake tmux: show-environment: no session %q", flagValue(args, "-t"))
	}
	var b strings.Builder
	for name, value := range session.env {
		fmt.Fprintf(&b, "%s=%s\n", name, value)
	}
	return []byte(b.String()), nil
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
		case token == "window_index" && session != nil && window != nil:
			out = append(out, fmt.Sprintf("%d", slices.Index(session.windows, window)))
		case token == "window_name" && window != nil:
			out = append(out, window.name)
		case token == "automatic-rename" && window != nil:
			out = append(out, window.opts[token])
		case token == "pane_id" && pane != nil:
			out = append(out, pane.id)
		case token == "pane_index" && window != nil && pane != nil:
			out = append(out, fmt.Sprintf("%d", slices.Index(window.panes, pane)))
		case token == "pane_left" && pane != nil:
			out = append(out, fmt.Sprintf("%d", pane.left))
		case token == "pane_top" && pane != nil:
			out = append(out, fmt.Sprintf("%d", pane.top))
		case token == "pane_width" && pane != nil:
			out = append(out, fmt.Sprintf("%d", pane.width))
		case token == "pane_height" && pane != nil:
			out = append(out, fmt.Sprintf("%d", pane.height))
		case token == tmuxopts.RemainOnExitPane && pane != nil:
			if enabled, ok := exactTmuxBoolean(pane.opts[token]); ok && enabled {
				out = append(out, "1")
			} else {
				out = append(out, "0")
			}
		case token == "pane_dead" && pane != nil:
			out = append(out, "0")
		case strings.HasPrefix(token, "@"):
			out = append(out, scopedOption(token, session, window, pane))
		case strings.HasPrefix(token, "E:") && session != nil:
			out = append(out, session.env[strings.TrimPrefix(token, "E:")])
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
