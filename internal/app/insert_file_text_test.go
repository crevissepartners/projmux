package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/integrations/mux"
)

type insertRecordedCall struct {
	name string
	args []string
}

// insertMuxBackend records every tmux subprocess call so tests can assert the
// exact commands the insert-file-text command issues (and, critically, that no
// clipboard command is ever emitted).
type insertMuxBackend struct {
	calls []insertRecordedCall
	out   []byte
	err   error
}

func (b *insertMuxBackend) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	b.calls = append(b.calls, insertRecordedCall{name: name, args: append([]string(nil), args...)})
	return append([]byte(nil), b.out...), b.err
}

func (b *insertMuxBackend) call(sub string) ([]string, bool) {
	for _, c := range b.calls {
		if len(c.args) > 0 && c.args[0] == sub {
			return c.args, true
		}
	}
	return nil, false
}

// touchedClipboard reports whether any recorded call could reach the OS
// clipboard: a send-keys `-w` (OSC52) or a buffer/clipboard command. Note `-w`
// on display-popup is the width flag, not clipboard, so the check is scoped by
// subcommand.
func (b *insertMuxBackend) touchedClipboard() bool {
	for _, c := range b.calls {
		if len(c.args) == 0 {
			continue
		}
		switch c.args[0] {
		case "send-keys":
			if slices.Contains(c.args, "-w") {
				return true
			}
		case "set-buffer", "set-clipboard", "save-buffer", "load-buffer":
			return true
		}
	}
	return false
}

func newInsertTestCommand(t *testing.T, backend mux.Backend, sources map[string]hooks.InsertFileTextSource, files map[string]string) *insertFileTextCommand {
	t.Helper()
	home := t.TempDir()
	return &insertFileTextCommand{
		runner:      mux.NewRunner(backend),
		loadSources: func() (map[string]hooks.InsertFileTextSource, error) { return sources, nil },
		readFile: func(p string) ([]byte, error) {
			if content, ok := files[p]; ok {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		},
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		executable: func() (string, error) { return "/usr/bin/projmux", nil },
	}
}

func TestInsertFileTextNamedInsertsTrimmedLiteral(t *testing.T) {
	backend := &insertMuxBackend{}
	sources := map[string]hooks.InsertFileTextSource{
		"screenshot": {Path: "/tmp/latest.path", Trim: true},
	}
	files := map[string]string{"/tmp/latest.path": "  /home/es5h/shot.png  \n"}
	cmd := newInsertTestCommand(t, backend, sources, files)

	if err := cmd.Run([]string{"screenshot", "--pane", "%3"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	args, ok := backend.call("send-keys")
	if !ok {
		t.Fatalf("no send-keys call; calls=%#v", backend.calls)
	}
	want := []string{"send-keys", "-t", "%3", "-l", "--", "/home/es5h/shot.png"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("send-keys args = %#v, want %#v", args, want)
	}
	if backend.touchedClipboard() {
		t.Fatalf("clipboard command emitted: %#v", backend.calls)
	}
}

func TestInsertFileTextExpandsHome(t *testing.T) {
	backend := &insertMuxBackend{}
	home := t.TempDir()
	expanded := filepath.Join(home, ".screenshots", "latest.path")
	sources := map[string]hooks.InsertFileTextSource{"s": {Path: "~/.screenshots/latest.path", Trim: true}}
	cmd := &insertFileTextCommand{
		runner:      mux.NewRunner(backend),
		loadSources: func() (map[string]hooks.InsertFileTextSource, error) { return sources, nil },
		readFile: func(p string) ([]byte, error) {
			if p == expanded {
				return []byte("/home/es5h/shot.png"), nil
			}
			return nil, os.ErrNotExist
		},
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		executable: func() (string, error) { return "/usr/bin/projmux", nil },
	}

	if err := cmd.Run([]string{"s", "--pane", "%1"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	args, ok := backend.call("send-keys")
	if !ok {
		t.Fatalf("no send-keys call; calls=%#v", backend.calls)
	}
	if args[len(args)-1] != "/home/es5h/shot.png" {
		t.Fatalf("inserted text = %q, want the file body (proves ~ was expanded)", args[len(args)-1])
	}
}

func TestInsertFileTextTrimOptOutKeepsWhitespace(t *testing.T) {
	backend := &insertMuxBackend{}
	sources := map[string]hooks.InsertFileTextSource{"raw": {Path: "/tmp/raw.txt", Trim: false}}
	files := map[string]string{"/tmp/raw.txt": "  keep  "}
	cmd := newInsertTestCommand(t, backend, sources, files)

	if err := cmd.Run([]string{"raw", "--pane", "%1"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	args, _ := backend.call("send-keys")
	if got := args[len(args)-1]; got != "  keep  " {
		t.Fatalf("inserted text = %q, want untrimmed %q", got, "  keep  ")
	}
}

func TestInsertFileTextMissingSourceShowsMessageAndNeverInserts(t *testing.T) {
	backend := &insertMuxBackend{}
	cmd := newInsertTestCommand(t, backend, map[string]hooks.InsertFileTextSource{}, nil)

	if err := cmd.Run([]string{"nope", "--pane", "%1"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := backend.call("send-keys"); ok {
		t.Fatalf("send-keys should not be called for a missing source")
	}
	args, ok := backend.call("display-message")
	if !ok {
		t.Fatalf("expected a display-message; calls=%#v", backend.calls)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "not found") || !strings.Contains(joined, "nope") {
		t.Fatalf("display-message = %q, want not-found + source name", joined)
	}
	if backend.touchedClipboard() {
		t.Fatalf("clipboard command emitted: %#v", backend.calls)
	}
}

func TestInsertFileTextUnreadableShowsMessage(t *testing.T) {
	backend := &insertMuxBackend{}
	sources := map[string]hooks.InsertFileTextSource{"x": {Path: "/tmp/missing.txt", Trim: true}}
	cmd := newInsertTestCommand(t, backend, sources, nil)

	if err := cmd.Run([]string{"x", "--pane", "%1"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := backend.call("send-keys"); ok {
		t.Fatalf("send-keys should not run for an unreadable source")
	}
	args, ok := backend.call("display-message")
	if !ok || !strings.Contains(strings.Join(args, " "), "unreadable") {
		t.Fatalf("expected an unreadable display-message; calls=%#v", backend.calls)
	}
}

func TestInsertFileTextEmptyAfterTrimShowsMessage(t *testing.T) {
	backend := &insertMuxBackend{}
	sources := map[string]hooks.InsertFileTextSource{"x": {Path: "/tmp/empty.txt", Trim: true}}
	files := map[string]string{"/tmp/empty.txt": "   \n\t"}
	cmd := newInsertTestCommand(t, backend, sources, files)

	if err := cmd.Run([]string{"x", "--pane", "%1"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := backend.call("send-keys"); ok {
		t.Fatalf("send-keys should not run for an empty source")
	}
	args, ok := backend.call("display-message")
	if !ok || !strings.Contains(strings.Join(args, " "), "empty") {
		t.Fatalf("expected an empty display-message; calls=%#v", backend.calls)
	}
}

func TestInsertFileTextZeroSourcesShowsMessage(t *testing.T) {
	backend := &insertMuxBackend{}
	cmd := newInsertTestCommand(t, backend, map[string]hooks.InsertFileTextSource{}, nil)

	// No positional name -> runtime resolution over 0 configured sources.
	if err := cmd.Run([]string{"--pane", "%1"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := backend.call("send-keys"); ok {
		t.Fatalf("send-keys should not run with zero sources")
	}
	if _, ok := backend.call("display-popup"); ok {
		t.Fatalf("no popup should open with zero sources")
	}
	args, ok := backend.call("display-message")
	if !ok || !strings.Contains(strings.Join(args, " "), "no insert-file-text source configured") {
		t.Fatalf("expected a none-configured display-message; calls=%#v", backend.calls)
	}
}

func TestInsertFileTextSingleSourceInsertsDirectly(t *testing.T) {
	backend := &insertMuxBackend{}
	sources := map[string]hooks.InsertFileTextSource{"only": {Path: "/tmp/only.txt", Trim: true}}
	files := map[string]string{"/tmp/only.txt": "value"}
	cmd := newInsertTestCommand(t, backend, sources, files)

	if err := cmd.Run([]string{"--pane", "%5"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := backend.call("display-popup"); ok {
		t.Fatalf("a single source must insert directly, not open a picker popup")
	}
	args, ok := backend.call("send-keys")
	if !ok || args[len(args)-1] != "value" {
		t.Fatalf("expected a direct send-keys of the single source; calls=%#v", backend.calls)
	}
}

func TestInsertFileTextMultipleSourcesOpenPicker(t *testing.T) {
	backend := &insertMuxBackend{}
	sources := map[string]hooks.InsertFileTextSource{
		"a": {Path: "/tmp/a.txt", Trim: true},
		"b": {Path: "/tmp/b.txt", Trim: true},
	}
	cmd := newInsertTestCommand(t, backend, sources, nil)

	// A concrete --pane avoids the active-pane resolution lookup.
	if err := cmd.Run([]string{"--pane", "%9"}, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := backend.call("send-keys"); ok {
		t.Fatalf("N sources must defer to a picker popup, not insert directly")
	}
	args, ok := backend.call("display-popup")
	if !ok {
		t.Fatalf("expected a display-popup for N sources; calls=%#v", backend.calls)
	}
	command := args[len(args)-1]
	if !strings.Contains(command, "insert-file-text --pick") {
		t.Fatalf("popup command = %q, want the --pick re-invocation", command)
	}
	if !strings.Contains(command, "--pane") || !strings.Contains(command, "%9") {
		t.Fatalf("popup command = %q, want the origin pane threaded through", command)
	}
	if backend.touchedClipboard() {
		t.Fatalf("clipboard command emitted: %#v", backend.calls)
	}
}

func TestInsertFileTextCatalogEntryIsOptInAndRebindable(t *testing.T) {
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "InsertFileText")
	if !ok {
		t.Fatal("catalog is missing the InsertFileText action")
	}
	if chords := keyBindingEffectivePlainChords(action); len(chords) != 0 {
		t.Fatalf("default chords = %#v, want empty (opt-in, no default binding)", chords)
	}
	if !keyBindingEditable(action) {
		t.Fatal("InsertFileText must be editable so Settings can rebind it")
	}
	body := renderTmuxBindingBody("/usr/bin/projmux", action)
	if !strings.Contains(body, "insert-file-text --pane #{pane_id}") {
		t.Fatalf("tmux body = %q, want the run-shell insert-file-text binding", body)
	}
	if strings.Contains(body, "-w") {
		t.Fatalf("tmux body = %q, must not carry a clipboard flag", body)
	}
}

func TestInsertFileTextKeymapRoundTrip(t *testing.T) {
	parsed, err := parseKeymapFile("/tmp/keymap.toml", `[bindings.InsertFileText]
keys = ["M-i"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile error: %v", err)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides error: %v", err)
	}
	action, ok := keyBindingActionByID(merged, "InsertFileText")
	if !ok {
		t.Fatal("merged catalog dropped InsertFileText")
	}
	if got := keyBindingEffectivePlainChords(action); len(got) != 1 || got[0] != "M-i" {
		t.Fatalf("effective chords = %#v, want [M-i]", got)
	}
	lines := strings.Join(tmuxBindLines("/bin/projmux", keyBindingCatalogForScopeFrom(merged, keyBindingScopeStandalone)), "\n")
	if !strings.Contains(lines, "bind-key -n M-i run-shell") || !strings.Contains(lines, "insert-file-text --pane #{pane_id}") {
		t.Fatalf("tmux bind lines =\n%s\nwant an M-i run-shell insert-file-text binding", lines)
	}
}
