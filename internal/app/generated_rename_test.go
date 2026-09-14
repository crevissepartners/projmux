package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// generatedRenameTestClient is the exact client every generated rename test
// invocation names; its status messages are the route's only visible output.
const generatedRenameTestClient = "/dev/pts/9"

// canonicalFixtureRenamer is the public rename owner wired onto a canonical
// intent fixture's Registry and fake tmux server, the way newRenameCommand is
// wired onto the inherited run-shell environment in production.
func canonicalFixtureRenamer(fx canonicalRootFixture) *renameCommand {
	lookupEnv := func(key string) string {
		if key == "TMUX" {
			return fx.tmux.socketPath + "," + fx.tmux.serverPID + ",0"
		}
		return ""
	}
	return &renameCommand{
		store:      fx.store.store(),
		mirror:     inheritedResourceMutationMirror(lookupEnv, fx.tmux),
		runtime:    liveAlphaRuntime(),
		tmuxRunner: fx.tmux,
		lookupEnv:  lookupEnv,
	}
}

// runGeneratedRename runs one generated rename exactly as the binding invokes
// it: the exact client and anchor in argv, the response in the here-document
// body on stdin, and the production canonical intents behind the route.
func runGeneratedRename(t *testing.T, fx canonicalRootFixture, kind coremetadata.Kind, anchor, response string) {
	t.Helper()
	route := "window-rename"
	if kind == coremetadata.KindPane {
		route = "pane-rename"
	}
	// Production binds the exact app route in ensureRuntimeRoute from the
	// inherited run-shell environment; the fixture states the same authority.
	fx.create.runtime.expectedSocketPath = fx.tmux.socketPath
	fx.create.runtime.socketName = defaultAppSocket
	fx.create.runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: fx.tmux.serverPID}
	cmd := &tmuxCommand{
		runner: fx.tmux,
		stdin:  strings.NewReader(generatedRenameResponseSentinel + response + "\n"),
		windowRename: func(intent windowRenameIntent, stdout, stderr io.Writer) error {
			return fx.create.renameWindowFromIntent(intent, canonicalFixtureRenamer(fx), stdout, stderr)
		},
		paneRename: func(intent paneRenameIntent, stdout, stderr io.Writer) error {
			return fx.create.renamePaneFromIntent(intent, canonicalFixtureRenamer(fx), stdout, stderr)
		},
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{route, "--client", generatedRenameTestClient, "--anchor", anchor, generatedRenameStdinFlag}, &stdout, &stderr); err != nil {
		t.Fatalf("%s did not converge onto the exact client: %v", route, err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("%s wrote to the foreground job: stdout=%q stderr=%q", route, stdout.String(), stderr.String())
	}
}

func generatedRenameClientMessages(fx canonicalRootFixture) []string {
	var messages []string
	for _, message := range fx.tmux.clientMessages {
		if message.client == generatedRenameTestClient {
			messages = append(messages, message.text)
		}
	}
	return messages
}

// tmuxStateWritesSince lists every recorded tmux call after from that changes
// server state. A client status message is feedback, not state, and reads are
// allowed: a refusal may have to observe the runtime before it can refuse.
func tmuxStateWritesSince(calls [][]string, from int) []string {
	readOnly := map[string]bool{
		"display-message": true, "list-sessions": true, "list-windows": true, "list-panes": true,
		"show-options": true, "show-environment": true, "has-session": true, "list-clients": true,
	}
	var writes []string
	for _, call := range calls[from:] {
		argv := tmuxCommandArgv(call)
		if len(argv) == 0 {
			continue
		}
		if argv[0] == "display-message" && flagValue(argv, "-F") == "" && flagValue(argv, "-c") != "" {
			continue
		}
		if !readOnly[argv[0]] {
			writes = append(writes, strings.Join(argv, " "))
		}
	}
	return writes
}

func TestGeneratedRenameNameInputRules(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		response      string
		wantName      string
		wantRequested bool
		wantErr       []string
		wantNoUsable  bool
	}{
		{response: "", wantRequested: false},
		{response: "   ", wantRequested: false},
		{response: "\t", wantRequested: false},
		{response: "notes", wantName: "notes", wantRequested: true},
		{response: "-flag-shaped", wantName: "-flag-shaped", wantRequested: true},
		{response: "a b", wantRequested: true, wantErr: []string{`usable name "a-b"`, "unsupported character", "nothing was changed"}},
		{response: " lead", wantRequested: true, wantErr: []string{`usable name "lead"`, "leading or trailing whitespace"}},
		{response: "trail ", wantRequested: true, wantErr: []string{`usable name "trail"`, "leading or trailing whitespace"}},
		{response: "x'y", wantRequested: true, wantErr: []string{`usable name "x-y"`}},
		{response: "$(touch pwned)", wantRequested: true, wantErr: []string{`usable name "touch-pwned"`}},
		{response: "50%#{session_name}", wantRequested: true, wantErr: []string{`usable name "50-session_name"`}},
		{response: "..", wantRequested: true, wantErr: []string{"is reserved", "nothing was changed"}, wantNoUsable: true},
	} {
		t.Run(test.response, func(t *testing.T) {
			t.Parallel()
			name, requested, err := generatedRenameName(test.response)
			if requested != test.wantRequested || name != test.wantName {
				t.Fatalf("generatedRenameName(%q) = (%q, %t), want (%q, %t)", test.response, name, requested, test.wantName, test.wantRequested)
			}
			if len(test.wantErr) == 0 {
				if err != nil {
					t.Fatalf("generatedRenameName(%q) error = %v", test.response, err)
				}
				return
			}
			if err == nil || !IsUsageError(err) {
				t.Fatalf("generatedRenameName(%q) error = %v, want a usage refusal", test.response, err)
			}
			for _, want := range test.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not contain %q", err.Error(), want)
				}
			}
			if test.wantNoUsable && strings.Contains(err.Error(), "usable name") {
				t.Fatalf("refusal %q offers a usable name SanitizeNameBase does not produce", err.Error())
			}
		})
	}
}

// TestGeneratedWindowRenameCommitsRegistryNameMirrorAndDisplay is the Window
// half of the defect: the key and menu rename used to change only tmux's
// window_name, so Continue rebuilt the session under the old Registry name.
func TestGeneratedWindowRenameCommitsRegistryNameMirrorAndDisplay(t *testing.T) {
	for _, control := range []bool{false, true} {
		rootName := "Project"
		if control {
			rootName = "ControlSession"
		}
		t.Run(rootName, func(t *testing.T) {
			fx := canonicalFixture(t, control)
			session, window, _ := fx.tmux.pane(fx.originID)
			// Make the managed Window the session's first (index 0) Window, the
			// one new-session creates and Continue names from the Registry.
			if session != nil {
				session.windows = slices.DeleteFunc(session.windows, func(candidate *fakeTmuxWindow) bool {
					return candidate != window && candidate.opts[tmuxopts.WindowUID] == ""
				})
			}
			if session == nil || len(session.windows) == 0 || session.windows[0] != window {
				t.Fatalf("fixture origin Window is not its session's first Window:\n%s", fx.tmux.state())
			}
			runGeneratedRename(t, fx, coremetadata.KindWindow, fx.originID, "renamed-window")

			stored, ok := fx.store.registry.Window(fx.windowUID)
			if !ok || stored.Metadata.Name != "renamed-window" {
				t.Fatalf("Registry Window %s name = %q, want renamed-window", fx.windowUID, stored.Metadata.Name)
			}
			if got := window.opts[tmuxopts.WindowName]; got != "renamed-window" {
				t.Fatalf("%s = %q, want renamed-window", tmuxopts.WindowName, got)
			}
			if window.name != "renamed-window" {
				t.Fatalf("tmux window_name = %q, want renamed-window", window.name)
			}
			if window.opts[tmuxopts.WindowUID] != fx.windowUID {
				t.Fatalf("rename moved the Window identity mirror: %q", window.opts[tmuxopts.WindowUID])
			}
			if got := generatedRenameClientMessages(fx); !reflect.DeepEqual(got, []string{windowRenamedMessage + "renamed-window"}) {
				t.Fatalf("client messages = %q, want the one success line", got)
			}
			// The display rename happens only after the Registry commit: the
			// commit's own stable-name mirror write precedes rename-window.
			stableAt, displayAt := -1, -1
			for index, call := range fx.tmux.calls {
				argv := tmuxCommandArgv(call)
				switch {
				case len(argv) > 0 && argv[0] == "set-option" && slices.Contains(argv, tmuxopts.WindowName) && stableAt < 0:
					stableAt = index
				case len(argv) > 0 && argv[0] == "rename-window" && displayAt < 0:
					displayAt = index
				}
			}
			if stableAt < 0 || displayAt < 0 || stableAt > displayAt {
				t.Fatalf("stable-name mirror at %d, display rename at %d; want the committed mirror first", stableAt, displayAt)
			}
		})
	}
}

// TestGeneratedPaneRenameCommitsRegistryNameAndLabel is the Pane half: the key
// used to set only @projmux_pane_label, which Continue then restored from the
// unchanged Registry name.
func TestGeneratedPaneRenameCommitsRegistryNameAndLabel(t *testing.T) {
	for _, test := range []struct {
		name    string
		control bool
		agent   bool
	}{
		{name: "Project shell Pane"},
		{name: "ControlSession shell Pane", control: true},
		{name: "Agent Pane with a launcher-given name", agent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := canonicalFixture(t, test.control)
			anchor := fx.originID
			_, window, livePane := fx.tmux.pane(anchor)
			if test.agent {
				stored, ok := fx.store.registry.Pane("pan-alpha-codex")
				if !ok || stored.Metadata.OwnerRef == nil || stored.Metadata.OwnerRef.Kind != coremetadata.KindAgent || stored.Metadata.Name != "codex-pane" {
					t.Fatalf("fixture Agent Pane = %+v, want the launcher-named Agent-owned Pane", stored)
				}
				livePane = newFakeTmuxPane(fx.tmux.mint("%"))
				livePane.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
				livePane.opts[tmuxopts.PaneName] = "codex-pane"
				window.panes = append(window.panes, livePane)
				anchor = livePane.id
			}
			paneUID := livePane.opts[tmuxopts.PaneUID]
			runGeneratedRename(t, fx, coremetadata.KindPane, anchor, "review-notes")

			stored, ok := fx.store.registry.Pane(paneUID)
			if !ok || stored.Metadata.Name != "review-notes" {
				t.Fatalf("Registry Pane %s name = %q, want review-notes", paneUID, stored.Metadata.Name)
			}
			if got := livePane.opts[tmuxopts.PaneName]; got != "review-notes" {
				t.Fatalf("%s = %q, want review-notes", tmuxopts.PaneName, got)
			}
			if livePane.opts[tmuxopts.PaneUID] != paneUID {
				t.Fatalf("rename moved the Pane identity mirror: %q", livePane.opts[tmuxopts.PaneUID])
			}
			if test.agent {
				agent, _ := fx.store.registry.Agent("agt-alpha-codex")
				if stored.Metadata.OwnerRef == nil || stored.Metadata.OwnerRef.UID != "agt-alpha-codex" || agent.Metadata.Name != "codex" {
					t.Fatalf("Agent Pane rename changed ownership or the Agent name: pane=%+v agent=%+v", stored.Metadata, agent.Metadata)
				}
			}
			if got := generatedRenameClientMessages(fx); !reflect.DeepEqual(got, []string{paneRenamedMessage + "review-notes"}) {
				t.Fatalf("client messages = %q, want the one success line", got)
			}
		})
	}
}

// TestGeneratedRenameRefusalsWriteNothing holds the input rules and the D-3
// conflict rule for both routes: every refusal reaches the exact client as one
// line with its reason, and writes neither the Registry nor tmux state.
func TestGeneratedRenameRefusalsWriteNothing(t *testing.T) {
	for _, kind := range []coremetadata.Kind{coremetadata.KindWindow, coremetadata.KindPane} {
		label, conflict := "Rename Window", "review"
		if kind == coremetadata.KindPane {
			label, conflict = "Rename Pane", "log"
		}
		for _, test := range []struct {
			name      string
			response  string
			unmanaged bool
			want      []string
			forbid    []string
		}{
			{name: "space", response: "a b", want: []string{label + " failed", `usable name "a-b"`, "nothing was changed"}},
			{name: "leading whitespace", response: " lead", want: []string{`usable name "lead"`, "leading or trailing whitespace"}},
			{name: "trailing whitespace", response: "trail ", want: []string{`usable name "trail"`, "leading or trailing whitespace"}},
			{name: "shell quote break", response: "x'y", want: []string{`usable name "x-y"`, `unsupported character "'"`}},
			{name: "double quote", response: `a"b`, want: []string{`usable name "a-b"`}},
			{name: "command substitution", response: "$(touch pwned)", want: []string{`usable name "touch-pwned"`}},
			{name: "semicolon", response: "a;b", want: []string{`usable name "a-b"`}},
			{name: "percent and format", response: "50%#{session_name}", want: []string{`usable name "50-session_name"`, `"50%#{session_name}"`}},
			{name: "reserved without a usable name", response: "..", want: []string{"is reserved", "nothing was changed"}, forbid: []string{"usable name"}},
			{name: "blank", response: "", want: []string{"projmux " + label + renameUnchangedMessageSuffix}, forbid: []string{"failed"}},
			{name: "whitespace only", response: "   ", want: []string{"projmux " + label + renameUnchangedMessageSuffix}, forbid: []string{"failed"}},
			{name: "name conflict", response: conflict, want: []string{label + " failed", "already used by", "nothing was changed"}},
			{name: "unmanaged anchor", response: "fine-name", unmanaged: true, want: []string{label + " failed", "exact UI origin was lost"}},
		} {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				fx := canonicalFixture(t, false)
				anchor := fx.originID
				if test.unmanaged {
					_, window, _ := fx.tmux.pane(fx.originID)
					unmanaged := newFakeTmuxPane(fx.tmux.mint("%"))
					window.panes = append(window.panes, unmanaged)
					anchor = unmanaged.id
				}
				_, window, pane := fx.tmux.pane(fx.originID)
				beforeRegistry := fx.store.snapshot()
				beforeWindow, beforeStable, beforeLabel := window.name, window.opts[tmuxopts.WindowName], pane.opts[tmuxopts.PaneName]
				beforeCalls := len(fx.tmux.calls)

				runGeneratedRename(t, fx, kind, anchor, test.response)

				if fx.store.writes != 0 || fx.store.snapshot() != beforeRegistry {
					t.Fatalf("refusal wrote the Registry: writes=%d", fx.store.writes)
				}
				if writes := tmuxStateWritesSince(fx.tmux.calls, beforeCalls); len(writes) != 0 {
					t.Fatalf("refusal wrote tmux state: %q", writes)
				}
				if window.name != beforeWindow || window.opts[tmuxopts.WindowName] != beforeStable || pane.opts[tmuxopts.PaneName] != beforeLabel {
					t.Fatalf("refusal changed a live name: window_name=%q stable=%q label=%q", window.name, window.opts[tmuxopts.WindowName], pane.opts[tmuxopts.PaneName])
				}
				messages := generatedRenameClientMessages(fx)
				if len(messages) != 1 {
					t.Fatalf("client messages = %q, want exactly one line", messages)
				}
				message := messages[0]
				if strings.ContainsAny(message, "\n\r") {
					t.Fatalf("client message spans lines: %q", message)
				}
				for _, want := range test.want {
					if !strings.Contains(message, tmuxLiteralMessage(want)) {
						t.Fatalf("client message %q does not carry %q", message, want)
					}
				}
				for _, forbid := range test.forbid {
					if strings.Contains(message, forbid) {
						t.Fatalf("client message %q must not carry %q", message, forbid)
					}
				}
			})
		}
	}
}

// TestGeneratedRenameRouteRequiresTheIntactHereDocumentBody pins the stdin
// contract between the generated binding and the route, and that a blank
// response never reaches the canonical intent.
func TestGeneratedRenameRouteRequiresTheIntactHereDocumentBody(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		args      []string
		stdin     string
		wantCalls []string
		want      string
	}{
		{name: "intact body", stdin: "name=notes\n", wantCalls: []string{"notes"}, want: paneRenamedMessage + "notes"},
		{name: "intact body keeps the raw response", stdin: "name= a b \n", wantCalls: []string{" a b "}, want: paneRenamedMessage + " a b "},
		{name: "blank body", stdin: "name=\n", want: "projmux Rename Pane" + renameUnchangedMessageSuffix},
		{name: "whitespace body", stdin: "name=   \n", want: "projmux Rename Pane" + renameUnchangedMessageSuffix},
		{name: "missing sentinel", stdin: "notes\n", want: "projmux Rename Pane failed: the rename response was not delivered intact; nothing was changed"},
		{name: "missing newline", stdin: "name=notes", want: "projmux Rename Pane failed: the rename response was not delivered intact; nothing was changed"},
		{name: "positional producer", args: []string{"--", "notes"}, wantCalls: []string{"notes"}, want: paneRenamedMessage + "notes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingTmuxRunner{}
			var calls []string
			cmd := &tmuxCommand{
				runner: runner,
				stdin:  strings.NewReader(test.stdin),
				paneRename: func(intent paneRenameIntent, _, _ io.Writer) error {
					if intent.anchorPaneID != "%9" || intent.targetClient != "/dev/pts/2" {
						t.Errorf("intent = %+v, want the exact anchor and client", intent)
					}
					calls = append(calls, intent.response)
					return nil
				},
			}
			args := []string{"pane-rename", "--client", "/dev/pts/2", "--anchor", "%9"}
			if test.args != nil {
				args = append(args, test.args...)
			} else {
				args = append(args, generatedRenameStdinFlag)
			}
			if err := cmd.Run(args, io.Discard, io.Discard); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("intent responses = %q, want %q", calls, test.wantCalls)
			}
			want := []recordedTmuxCall{{name: "tmux", args: []string{"display-message", "-c", "/dev/pts/2", "-d", "10000", tmuxLiteralMessage(strings.Join(strings.Fields(test.want), " "))}}}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("tmux calls = %#v, want %#v", runner.calls, want)
			}
		})
	}

	for _, args := range [][]string{
		{"pane-rename", "--client", "/dev/pts/2", "--anchor", "%9"},
		{"pane-rename", "--client", "/dev/pts/2", "--anchor", "%9", generatedRenameStdinFlag, "--", "notes"},
		{"pane-rename", "--anchor", "%9", generatedRenameStdinFlag},
		{"window-rename", "--client", "/dev/pts/2", "--anchor", "9", generatedRenameStdinFlag},
	} {
		called := false
		cmd := &tmuxCommand{
			runner:       &recordingTmuxRunner{},
			stdin:        strings.NewReader("name=notes\n"),
			paneRename:   func(paneRenameIntent, io.Writer, io.Writer) error { called = true; return nil },
			windowRename: func(windowRenameIntent, io.Writer, io.Writer) error { called = true; return nil },
		}
		err := cmd.Run(args, io.Discard, io.Discard)
		if err == nil || called || !strings.Contains(err.Error(), "requires --client <key> --anchor <%pane>") {
			t.Fatalf("Run(%q) = %v called=%t, want a usage refusal before any intent", args, err, called)
		}
	}
}

// TestTmuxClientMessagesAreDeliveredLiterally pins the exact argv of both
// client message writers. tmux expands a display-message argument as a
// strftime format and as a tmux format, so without escaping `<%pane>` reached
// the client as `<AMane>` and a quoted `#{session_name}` as a session name.
func TestTmuxClientMessagesAreDeliveredLiterally(t *testing.T) {
	t.Parallel()

	message := `projmux Rename Pane failed: usable name "50-x"; name: name "50%#{x}" contains an unsupported character "%" in <%pane> ##`
	literal := `projmux Rename Pane failed: usable name "50-x"; name: name "50%%##{x}" contains an unsupported character "%%" in <%%pane> ####`

	intentRunner := &recordingTmuxRunner{}
	if err := (&tmuxCommand{runner: intentRunner}).displayPaneMenuMessage("/dev/pts/2", message); err != nil {
		t.Fatal(err)
	}
	guardRunner := &recordingTmuxRunner{}
	if err := displayInteractiveRunShellMessage(guardRunner, "/dev/pts/2", message); err != nil {
		t.Fatal(err)
	}
	want := []recordedTmuxCall{{name: "tmux", args: []string{"display-message", "-c", "/dev/pts/2", "-d", "10000", literal}}}
	for name, runner := range map[string]*recordingTmuxRunner{"intent": intentRunner, "guard": guardRunner} {
		if !reflect.DeepEqual(runner.calls, want) {
			t.Fatalf("%s message argv = %#v, want %#v", name, runner.calls, want)
		}
	}
	if got := tmuxLiteralMessage(windowRenamedMessage + "notes"); got != windowRenamedMessage+"notes" {
		t.Fatalf("a message without %% or # changed: %q", got)
	}
}

// TestGeneratedRenameBindingsCarryTheResponseOnlyInAQuotedHereDocument pins the
// rendered command both rename keys and the Window menu Rename item run.
func TestGeneratedRenameBindingsCarryTheResponseOnlyInAQuotedHereDocument(t *testing.T) {
	t.Parallel()

	const bin = "/usr/local/bin/projmux"
	catalog := defaultKeyBindingCatalog()
	for id, route := range map[string]string{"rename-window": windowRenameRoute, paneRenameActionID: paneRenameRoute} {
		action, ok := keyBindingActionByID(catalog, id)
		if !ok || action.TmuxKind != tmuxBindingPromptRunProjmux {
			t.Fatalf("%s = %+v, want a prompt-run-projmux action", id, action)
		}
		wantBody := route + ` --name-stdin <<'PROJMUX_RENAME_RESPONSE'\nname=%%%\nPROJMUX_RENAME_RESPONSE`
		if action.TmuxBody != wantBody {
			t.Fatalf("%s body = %q, want %q", id, action.TmuxBody, wantBody)
		}
		runShell := "run-shell " + tmuxConfigQuote(tmuxPaneEnvPrefix+tmuxShellQuote(bin)+" "+action.TmuxBody)
		rendered := renderTmuxBindingBody(bin, action)
		if want := "command-prompt " + action.TmuxPromptArgs + " " + tmuxConfigQuote(runShell); rendered != want {
			t.Fatalf("%s rendered = %q, want %q", id, rendered, want)
		}
		// The only `%` is the escaped response placeholder, so no Pane handle
		// or other placeholder can take the response instead.
		if strings.Count(rendered, "%") != 3 || strings.Contains(rendered, "'%%'") || strings.Contains(rendered, "%1") {
			t.Fatalf("%s rendered placeholders = %q, want exactly one %%%%%% in the here-document", id, rendered)
		}
	}
	if pane, _ := keyBindingActionByID(catalog, paneRenameActionID); pane.Semantics.ResultKind != "rename the focused Pane in the Registry" ||
		strings.Contains(strings.ToLower(pane.Description), "clear") {
		t.Fatalf("Pane rename copy = (%q, %q), want a Registry rename", pane.Semantics.ResultKind, pane.Description)
	}
}

// tmuxPromptTemplateReplace is tmux 3.6 cmd_template_replace (cmd.c) for the
// first prompt response: every `%1` and the first `%%` are replaced, and a
// trailing third `%` escapes `"`, `\`, `$`, `;` and `~` with a backslash.
func tmuxPromptTemplateReplace(template, response string) string {
	const quote = "\"\\$;~"
	var b strings.Builder
	replaced := false
	for i := 0; i < len(template); {
		ch := template[i]
		i++
		if ch != '%' {
			b.WriteByte(ch)
			continue
		}
		if i >= len(template) || template[i] != '1' {
			if i >= len(template) || template[i] != '%' || replaced {
				b.WriteByte(ch)
				continue
			}
			replaced = true
		}
		i++
		quoted := i < len(template) && template[i] == '%'
		if quoted {
			i++
		}
		for j := 0; j < len(response); j++ {
			if quoted && strings.IndexByte(quote, response[j]) >= 0 {
				b.WriteByte('\\')
			}
			b.WriteByte(response[j])
		}
	}
	return b.String()
}

// TestGeneratedRenameBindingDeliversHostileResponsesVerbatimThroughRealTmux
// drives the rendered rename bindings through an isolated real tmux server.
// source-file parses the generated bind-key as the config does; the prompt
// itself needs an attached client, so the test applies tmux's own template
// replacement (tmuxPromptTemplateReplace) to the exact run-shell command the
// renderer quoted and hands the result to source-file, which parses it,
// format-expands it in run-shell, and runs it with /bin/sh. The recorder
// stands in for projmux and keeps its argv and stdin.
func TestGeneratedRenameBindingDeliversHostileResponsesVerbatimThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "prn-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	socket := filepath.Join(root, "s")
	tmux := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		return command.CombinedOutput()
	}
	if out, err := tmux("new-session", "-d", "-s", "rename-transport"); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _, _ = tmux("kill-server") })

	recorder := filepath.Join(root, "rec")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >'" + root + "/argv'\ncat >'" + root + "/stdin.tmp' && mv '" + root + "/stdin.tmp' '" + root + "/stdin'\n"
	if err := os.WriteFile(recorder, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	responses := []struct {
		response string
		want     string
	}{
		{response: "renamed"},
		{response: "x'y"},
		{response: `a"b`},
		{response: "$(touch " + root + "/pwned-dollar)"},
		{response: "`touch " + root + "/pwned-backtick`"},
		{response: "x; touch " + root + "/pwned-semicolon"},
		{response: "a b"},
		{response: " lead"},
		{response: "trail "},
		{response: `back\slash`},
		{response: "~home"},
		{response: "%1 and %% and %%%"},
		{response: generatedRenameHeredocDelimiter},
		{response: ""},
		{response: "   "},
		// The documented residual: run-shell format-expands before any shell.
		{response: "#{session_name}", want: "rename-transport"},
	}
	catalog := defaultKeyBindingCatalog()
	for _, id := range []string{"rename-window", paneRenameActionID} {
		action, _ := keyBindingActionByID(catalog, id)
		conf := filepath.Join(root, "bind.conf")
		if err := os.WriteFile(conf, []byte("bind-key -T projmux-rename-test r "+renderTmuxBindingBody(recorder, action)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := tmux("source-file", conf); err != nil || len(bytes.TrimSpace(out)) != 0 {
			t.Fatalf("%s generated bind-key does not parse: %v: %s", id, err, out)
		}
		template := "run-shell " + tmuxConfigQuote(tmuxPaneEnvPrefix+tmuxShellQuote(recorder)+" "+action.TmuxBody)
		for _, test := range responses {
			_ = os.Remove(filepath.Join(root, "stdin"))
			confirm := filepath.Join(root, "confirm.conf")
			if err := os.WriteFile(confirm, []byte(tmuxPromptTemplateReplace(template, test.response)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := tmux("source-file", confirm); err != nil {
				t.Fatalf("%s response %q: confirmed template failed: %v: %s", id, test.response, err, out)
			}
			var stdin []byte
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if stdin, err = os.ReadFile(filepath.Join(root, "stdin")); err == nil {
					break
				}
			}
			if err != nil {
				t.Fatalf("%s response %q never reached the recorder: %v", id, test.response, err)
			}
			got, err := (renameIntentArgs{fromStdin: true}).readResponse(bytes.NewReader(stdin))
			want := test.response
			if test.want != "" {
				want = test.want
			}
			if err != nil || got != want {
				t.Fatalf("%s response %q arrived as %q (stdin %q, err %v), want %q", id, test.response, got, stdin, err, want)
			}
			argv, _ := os.ReadFile(filepath.Join(root, "argv"))
			if !strings.Contains(string(argv), strings.TrimPrefix(strings.Fields(action.TmuxBody)[2], "")) || !strings.HasSuffix(strings.TrimSpace(string(argv)), generatedRenameStdinFlag) {
				t.Fatalf("%s argv = %q, want the route with only --name-stdin after the anchor", id, argv)
			}
		}
	}
	matches, _ := filepath.Glob(filepath.Join(root, "pwned-*"))
	if len(matches) != 0 {
		t.Fatalf("a shell executed part of a rename response: %v", matches)
	}
}
