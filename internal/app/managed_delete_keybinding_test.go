package app

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
)

// tmux 3.6's compiled-in `prefix x` and `prefix &` bodies, exactly as
// `list-keys -T prefix x` and `list-keys -T prefix &` print them on an isolated
// `tmux -f /dev/null` server. They are spelled out here instead of read from the
// production constants so the renderer is held to tmux rather than to itself.
const (
	tmux36StockPrefixX         = `confirm-before -p "kill-pane #P? (y/n)" kill-pane`
	tmux36StockPrefixAmpersand = `confirm-before -p "kill-window #W? (y/n)" kill-window`
)

type managedDeleteKeyContract struct {
	id, canonical, chord, guard, target, stock string
}

func managedDeleteKeyContracts() []managedDeleteKeyContract {
	return []managedDeleteKeyContract{
		{id: "delete-pane", canonical: "pane.delete", chord: "x", guard: "#{@projmux_pane_uid}", target: "pane", stock: tmux36StockPrefixX},
		{id: "delete-window", canonical: "window.delete", chord: "&", guard: "#{@projmux_window_uid}", target: "window", stock: tmux36StockPrefixAmpersand},
	}
}

// managedDeleteBody is the exact generated body for one managed close action.
func managedDeleteBody(bin string, contract managedDeleteKeyContract) string {
	return `if-shell -F "` + contract.guard + `" { run-shell "` + tmuxPaneEnvPrefix + tmuxShellQuote(bin) +
		" internal tmux delete-confirm --client #{client_tty} --anchor #{pane_id} " + contract.target + `" } { ` + contract.stock + ` }`
}

func countConfigLines(rendered, want string) int {
	count := 0
	for line := range strings.SplitSeq(rendered, "\n") {
		if line == want {
			count++
		}
	}
	return count
}

func configLineIndex(rendered, want string) int {
	for index, line := range strings.Split(rendered, "\n") {
		if line == want {
			return index
		}
	}
	return -1
}

func TestManagedDeleteKeyBindingsRenderMirrorGuardedCanonicalRouteWithStockFallback(t *testing.T) {
	t.Parallel()

	const bin = "/usr/local/bin/projmux"
	app := tmuxAppConfig(bin, "/bin/sh", config.StatusbarDecorationOff)
	standalone := tmuxStandaloneConfig(bin, config.StatusbarDecorationOff)
	for _, contract := range managedDeleteKeyContracts() {
		action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), contract.id)
		if !ok {
			t.Fatalf("catalog is missing managed close action %q", contract.id)
		}
		if action.CanonicalID != contract.canonical || action.Scope != keyBindingScopeApp || action.PrefixChord != contract.chord ||
			action.TmuxKind != tmuxBindingManagedDelete || !keyBindingEditable(action) || len(keyBindingEffectivePlainChords(action)) != 0 {
			t.Fatalf("managed close action %q = %#v, want app-scoped editable prefix %q action %q", contract.id, action, contract.chord, contract.canonical)
		}
		if _, protected := keyBindingProtectedActionReason(action); protected {
			t.Fatalf("managed close action %q is read only in Settings", contract.id)
		}

		line := "bind-key " + contract.chord + " " + managedDeleteBody(bin, contract)
		if got := countConfigLines(app, line); got != 1 {
			t.Fatalf("app config has %d exact %s binding line(s), want 1: %s\n%s", got, contract.id, line, app)
		}
		unbind := configLineIndex(app, "unbind-key -q "+contract.chord)
		if unbind < 0 || unbind > configLineIndex(app, line) {
			t.Fatalf("app config must unbind %q before binding it (unbind=%d bind=%d)", contract.chord, unbind, configLineIndex(app, line))
		}
		// The default keymap owns the stock key, so nothing is restored.
		if strings.Contains(app, "bind-key "+contract.chord+" "+contract.stock) {
			t.Fatalf("app config restored the stock %q body while the managed action still owns it", contract.chord)
		}
		// The standalone ~/.tmux.conf snippet never touches either stock key.
		for _, forbidden := range []string{"delete-confirm", "bind-key " + contract.chord + " ", "unbind-key -q " + contract.chord + "\n"} {
			if strings.Contains(standalone, forbidden) {
				t.Fatalf("standalone config contains %q; its prefix %s must stay tmux stock", forbidden, contract.chord)
			}
		}
	}
}

func TestManagedDeleteKeymapChangeAndDisableReachGeneratedConfig(t *testing.T) {
	t.Parallel()

	const bin = "/usr/local/bin/projmux"
	contracts := managedDeleteKeyContracts()
	pane, window := contracts[0], contracts[1]
	decorations := statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff)

	parsed, err := parseKeymapFile("keymap.toml", "schema_version = 2\n\n[bindings.\"pane.delete\"]\nprefix = \"X\"\n\n[bindings.\"window.delete\"]\nprefix = \"\"\n")
	if err != nil {
		t.Fatalf("parse keymap: %v", err)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("merge keymap: %v", err)
	}
	app := tmuxAppConfigWithKeymap(bin, "/bin/sh", decorations, merged, true)

	remapped := "bind-key X " + managedDeleteBody(bin, pane)
	if got := countConfigLines(app, remapped); got != 1 {
		t.Fatalf("remapped pane.delete binding count = %d, want 1: %s\n%s", got, remapped, app)
	}
	if strings.Contains(app, "bind-key x if-shell") || strings.Contains(app, managedDeleteWindowRoute) {
		t.Fatalf("a vacated or disabled key kept its managed binding:\n%s", app)
	}
	// A key the managed action no longer owns returns to tmux's own binding,
	// which is also what an already running server gets on the next source.
	restoreX := "bind-key x " + pane.stock
	restoreAmpersand := "bind-key & " + window.stock
	for _, restore := range []string{restoreX, restoreAmpersand} {
		if got := countConfigLines(app, restore); got != 1 {
			t.Fatalf("stock restore %q count = %d, want 1\n%s", restore, got, app)
		}
	}
	for _, unbind := range []string{"unbind-key -q x", "unbind-key -q X", "unbind-key -q &"} {
		at := configLineIndex(app, unbind)
		if at < 0 || at > configLineIndex(app, restoreX) || at > configLineIndex(app, restoreAmpersand) || at > configLineIndex(app, remapped) {
			t.Fatalf("%q must precede every restore and managed bind line (at=%d)\n%s", unbind, at, app)
		}
	}
	if configLineIndex(app, restoreX) > configLineIndex(app, remapped) {
		t.Fatal("stock restores must precede managed binds so a managed bind on the same key wins")
	}

	// Settings adds root-table keys; each runs the same branching body.
	withKey, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), keymapFile{Bindings: map[string]keymapOverride{
		"pane.delete": {KeysSet: true, Keys: []string{"M-F9"}},
	}})
	if err != nil {
		t.Fatalf("merge added key: %v", err)
	}
	appWithKey := tmuxAppConfigWithKeymap(bin, "/bin/sh", decorations, withKey, true)
	for _, want := range []string{"bind-key -n M-F9 " + managedDeleteBody(bin, pane), "bind-key x " + managedDeleteBody(bin, pane)} {
		if got := countConfigLines(appWithKey, want); got != 1 {
			t.Fatalf("added-key config line %q count = %d, want 1", want, got)
		}
	}
	if strings.Contains(appWithKey, restoreX) {
		t.Fatal("an added key must not vacate the stock prefix key")
	}

	// Two managed actions on one prefix key would silently shadow each other.
	ampersand := "&"
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), keymapFile{Bindings: map[string]keymapOverride{
		"pane.delete": {Prefix: &ampersand},
	}}); err == nil || !strings.Contains(err.Error(), `prefix key "&" is bound to both`) {
		t.Fatalf("duplicate managed prefix key error = %v, want a prefix conflict", err)
	}
}

func TestManagedDeleteCatalogSurfaceRowsMapBothDirections(t *testing.T) {
	t.Parallel()

	rows := map[string]runtimeMutationSurface{}
	for _, row := range runtimeMutationSurfaces {
		rows[row.ID] = row
	}
	app := tmuxAppConfig("/usr/local/bin/projmux", "/bin/sh", config.StatusbarDecorationOff)
	for _, test := range []struct {
		contract managedDeleteKeyContract
		route    string
		verb     runtimeMutationVerb
		handler  string
	}{
		{contract: managedDeleteKeyContracts()[0], route: managedDeletePaneRoute, verb: mutationKillPane, handler: "internal tmux pane-menu kill"},
		{contract: managedDeleteKeyContracts()[1], route: managedDeleteWindowRoute, verb: mutationKillWindow, handler: "tmuxWindowDeleteRuntime"},
	} {
		id := "catalog." + test.contract.canonical
		row, ok := rows[id]
		if !ok {
			t.Fatalf("closed surface table has no %s row", id)
		}
		if row.Disposition != runtimeMutationSurfacePlanned || row.PlanVerb != string(test.verb) || row.LegacyID != test.contract.id ||
			!strings.HasPrefix(row.Handler, "internal tmux delete-confirm "+test.contract.target) || !strings.Contains(row.Handler, test.handler) {
			t.Fatalf("surface row %s = %#v, want planned %s through delete-confirm to %s", id, row, test.verb, test.handler)
		}
		// artifact -> row: the generated route appears once and names this row's handler.
		if got := strings.Count(app, test.route); got != 1 {
			t.Fatalf("app config carries %q %d time(s), want 1", test.route, got)
		}
		// row -> artifact: the row's legacy alias is the shipped action rendering that route.
		action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), row.LegacyID)
		if !ok || action.CanonicalID != test.contract.canonical || action.TmuxBody != test.route {
			t.Fatalf("surface row %s does not point back at a catalog action rendering %q: %#v", id, test.route, action)
		}
	}
	delegating := 0
	for _, row := range runtimeMutationSurfaces {
		if strings.Contains(row.Handler, "delete-confirm") {
			delegating++
			if row.PlanVerb != string(mutationKillPane) && row.PlanVerb != string(mutationKillWindow) {
				t.Fatalf("delete-confirm surface %s plans %q, want only the canonical delete verbs", row.ID, row.PlanVerb)
			}
		}
	}
	if got := strings.Count(app, "internal tmux delete-confirm"); delegating != 2 || got != delegating {
		t.Fatalf("delete-confirm artifacts=%d rows=%d, want exactly 2 each", got, delegating)
	}
}

func TestTmuxDeleteConfirmIssuesOneLocalizedPromptToTheExactClient(t *testing.T) {
	t.Parallel()

	const bin = "/opt/proj mux/bin/projmux"
	for _, test := range []struct {
		name, target, locale string
		key                  i18n.Key
		command, route       string
	}{
		{name: "pane en-US", target: "pane", locale: "en-US", key: i18n.Key("tmux.confirm.delete_pane"),
			command: `run-shell "'/opt/proj mux/bin/projmux' internal tmux pane-menu --client '/dev/pts/7' kill %19"`, route: interactiveRoutePaneMenu},
		{name: "pane ko-KR", target: "pane", locale: "ko-KR", key: i18n.Key("tmux.confirm.delete_pane"),
			command: `run-shell "'/opt/proj mux/bin/projmux' internal tmux pane-menu --client '/dev/pts/7' kill %19"`, route: interactiveRoutePaneMenu},
		// The Window intent carries the press-time anchor as TMUX_PANE: canonical
		// delete window takes its mutation route authority from it, and a job
		// confirm-before starts has no pane of its own.
		{name: "window en-US", target: "window", locale: "en-US", key: i18n.Key("tmux.confirm.delete_window"),
			command: `run-shell "TMUX_PANE=%19 '/opt/proj mux/bin/projmux' internal tmux window-delete --client '/dev/pts/7' --anchor %19"`, route: interactiveRouteWindowDelete},
		{name: "window ko-KR", target: "window", locale: "ko-KR", key: i18n.Key("tmux.confirm.delete_window"),
			command: `run-shell "TMUX_PANE=%19 '/opt/proj mux/bin/projmux' internal tmux window-delete --client '/dev/pts/7' --anchor %19"`, route: interactiveRouteWindowDelete},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingTmuxRunner{}
			deletes := 0
			cmd := &tmuxCommand{
				runner:     runner,
				executable: func() (string, error) { return bin, nil },
				lookupEnv: func(key string) string {
					if key == i18n.LocaleEnvName {
						return test.locale
					}
					return ""
				},
				paneMenuDelete: func(string, io.Writer, io.Writer) error { deletes++; return nil },
				windowDelete:   func(string, io.Writer, io.Writer) error { deletes++; return nil },
			}
			var stdout, stderr bytes.Buffer
			if err := cmd.Run([]string{"delete-confirm", "--client", "/dev/pts/7", "--anchor", "%19", test.target}, &stdout, &stderr); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("delete-confirm wrote to the foreground job: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if deletes != 0 {
				t.Fatalf("delete-confirm deleted %d resource(s) before the operator answered", deletes)
			}
			prompt, err := i18n.NewLocalizer(i18n.Locale(test.locale)).Text(test.key)
			if err != nil || string(prompt.Locale()) != test.locale {
				t.Fatalf("prompt %s for %s: locale=%q err=%v", test.key, test.locale, prompt.Locale(), err)
			}
			want := []recordedTmuxCall{{name: "tmux", args: []string{
				"confirm-before", "-b", "-t", "/dev/pts/7", "-p", prompt.String(), test.command,
			}}}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("tmux calls = %#v, want exactly one confirm-before %#v", runner.calls, want)
			}
			for _, arg := range runner.calls[0].args {
				for token := range strings.FieldsSeq(arg) {
					if closedTmuxTopologyMutationVerbs[strings.Trim(token, `"'`)] {
						t.Fatalf("delete-confirm handed tmux raw topology verb %q: %#v", token, runner.calls)
					}
				}
			}
			// The confirmed command is argv the output guard owns, and exactly one
			// runtime ledger row classifies it.
			guarded, _, ok := matchInteractiveRunShellRoute(interactiveArgvFromGeneratedCommand(test.command), func(string) string { return "" })
			if !ok || guarded.ID != test.route {
				t.Fatalf("confirmed command resolves to guard route %q (ok=%t), want %q", guarded.ID, ok, test.route)
			}
			var hits []string
			for _, row := range runShellOutputLedger() {
				if row.Surface == runShellSurfaceRuntime && strings.Contains(test.command, row.Match) {
					hits = append(hits, row.ID)
					if row.Route != test.route || row.Channel != runShellChannelExactClientMessage {
						t.Fatalf("ledger row %s classifies the confirmed command as route=%q channel=%q", row.ID, row.Route, row.Channel)
					}
				}
			}
			if len(hits) != 1 {
				t.Fatalf("confirmed command matches runtime ledger rows %v, want exactly one", hits)
			}
		})
	}
}

func TestTmuxManagedDeleteIntentsRefuseMalformedArgvWithZeroTmuxCalls(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"delete-confirm", "--anchor", "%19", "pane"},
		{"delete-confirm", "--client", "/dev/pts/7", "--anchor", "active", "pane"},
		{"delete-confirm", "--client", "/dev/pts/7", "--anchor", "%19"},
		{"delete-confirm", "--client", "/dev/pts/7", "--anchor", "%19", "session"},
		{"delete-confirm", "--client", "/dev/pts/7", "--anchor", "%19", "pane", "window"},
		{"window-delete", "--client", "/dev/pts/7"},
		{"window-delete", "--anchor", "%19"},
		{"window-delete", "--client", "/dev/pts/7", "--anchor", "%19", "extra"},
	} {
		runner := &recordingTmuxRunner{}
		deletes := 0
		cmd := &tmuxCommand{
			runner:         runner,
			executable:     func() (string, error) { return "/usr/local/bin/projmux", nil },
			lookupEnv:      func(string) string { return "" },
			paneMenuDelete: func(string, io.Writer, io.Writer) error { deletes++; return nil },
			windowDelete:   func(string, io.Writer, io.Writer) error { deletes++; return nil },
		}
		if err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("Run(%v) accepted malformed argv", args)
		}
		if len(runner.calls) != 0 || deletes != 0 {
			t.Fatalf("Run(%v) reached tmux=%#v deletes=%d, want zero", args, runner.calls, deletes)
		}
	}
}

func TestTmuxWindowDeleteIntentReportsCanonicalResultToTheExactClient(t *testing.T) {
	t.Parallel()

	var anchors []string
	runner := &recordingTmuxRunner{}
	cmd := &tmuxCommand{
		runner: runner,
		windowDelete: func(anchor string, stdout, _ io.Writer) error {
			anchors = append(anchors, anchor)
			_, _ = io.WriteString(stdout, "delete window: deleting 1 window and 2 descendant resources\nwindow/api uid=win-api\n")
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"window-delete", "--client", "/dev/pts/7", "--anchor", "%19"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(anchors, []string{"%19"}) {
		t.Fatalf("canonical Window delete anchors = %v, want [%%19]", anchors)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("window-delete wrote to the foreground job: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	want := []recordedTmuxCall{{name: "tmux", args: []string{
		"display-message", "-c", "/dev/pts/7", "-d", "10000", "projmux delete window: deleting 1 window and 2 descendant resources",
	}}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want the canonical delete summary on the exact client %#v", runner.calls, want)
	}
}

func TestTmuxWindowDeleteIntentRefusalIsShownWithoutRawFallback(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{}
	cmd := &tmuxCommand{
		runner: runner,
		windowDelete: func(_ string, _, stderr io.Writer) error {
			_, _ = io.WriteString(stderr, "the active tmux window maps to no registry Window")
			return errors.New("delete window refused")
		},
	}
	if err := cmd.Run([]string{"window-delete", "--client", "/dev/pts/8", "--anchor", "%21"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("a displayed canonical refusal escaped as an invisible exit code: %v", err)
	}
	if len(runner.calls) != 1 || !containsAll(runner.calls[0].args, []string{"display-message", "-c", "/dev/pts/8"}) {
		t.Fatalf("refusal was not shown to the exact client as one message: %#v", runner.calls)
	}
	message := runner.calls[0].args[len(runner.calls[0].args)-1]
	for _, want := range []string{"Delete Window failed", "delete window refused", "maps to no registry Window"} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal message = %q, want it to contain %q", message, want)
		}
	}
	for _, arg := range runner.calls[0].args[:len(runner.calls[0].args)-1] {
		if closedTmuxTopologyMutationVerbs[arg] {
			t.Fatalf("refusal fell back to raw tmux verb %q: %#v", arg, runner.calls)
		}
	}
}
