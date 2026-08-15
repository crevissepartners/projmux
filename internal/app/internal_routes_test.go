package app

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/theme"
)

// generatedConfigBinary is the binary path every generated-config assertion in
// this file renders with. It is distinctive enough that a scan for it finds
// every projmux invocation the config emits and nothing else.
const generatedConfigBinary = "/usr/bin/projmux"

// preNamespaceInternalRoutes is the closed set of top-level tokens the internal
// isolation Phase relocated. A newly installed binary must not emit any of them
// into generated tmux config, popup payloads, or hook payloads; every one of
// them must still be dispatchable, because a tmux server that is already
// running holds config a previously installed binary generated.
var preNamespaceInternalRoutes = []string{
	"key-broker",
	"popup-wait-key",
	"preview",
	"session-popup",
	"status",
	"statusbar",
	"tmux",
}

// projmuxInvocation matches one `'<bin>' <token> [<token>]` occurrence inside a
// generated config line or popup payload. Generated config quotes only the
// binary path; the exec payload builders quote every argument, so the route
// tokens are matched with the surrounding quotes optional.
var projmuxInvocation = regexp.MustCompile(
	`'` + regexp.QuoteMeta(generatedConfigBinary) + `' '?([a-z0-9][a-z0-9-]*)'?(?: '?([a-z0-9][a-z0-9-]*)'?)?`)

// generatedProjmuxRoutes returns every distinct projmux route the supplied
// generated text invokes, as "<token>" or "<token> <token>".
func generatedProjmuxRoutes(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range projmuxInvocation.FindAllStringSubmatch(text, -1) {
		route := match[1]
		if match[2] != "" {
			route += " " + match[2]
		}
		if seen[route] {
			continue
		}
		seen[route] = true
		out = append(out, route)
	}
	sort.Strings(out)
	return out
}

// generatedConfigSurfaces returns every machine-generated projmux payload this
// binary writes: the two tmux configs plus the popup and helper payloads that
// are built at runtime rather than rendered into the config file.
func generatedConfigSurfaces(t *testing.T) map[string]string {
	t.Helper()

	decorations := statusbarDecorationSet{
		Cwd:    config.StatusbarDecorationSymbol,
		Git:    config.StatusbarDecorationSymbol,
		Notify: config.StatusbarDecorationSymbol,
	}
	effective := theme.ResolveTheme(theme.ThemeConfig{})
	catalog := defaultKeyBindingCatalog()
	surfaces := map[string]string{
		// Live resources on is the non-default branch, so rendering it here is
		// what keeps the `status resources` segment inside the scan.
		"standalone config": tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(
			generatedConfigBinary, decorations, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode,
			config.LiveResourcesOn, catalog, false, effective),
		"app config": tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(
			generatedConfigBinary, "/bin/zsh", decorations, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode,
			config.LiveResourcesOn, catalog, false, effective),
	}

	popupPreview, err := inttmux.BuildPopupPreviewCommand(generatedConfigBinary, "dev")
	if err != nil {
		t.Fatalf("BuildPopupPreviewCommand: %v", err)
	}
	surfaces["popup preview payload"] = popupPreview

	pickerPreview, err := inttmux.BuildSessionPopupPreviewCommand(generatedConfigBinary)
	if err != nil {
		t.Fatalf("BuildSessionPopupPreviewCommand: %v", err)
	}
	surfaces["picker preview payload"] = pickerPreview

	for _, subcommand := range []string{"cycle-pane", "cycle-window"} {
		cycle, err := inttmux.BuildSessionPopupCycleCommand(generatedConfigBinary, subcommand, "next")
		if err != nil {
			t.Fatalf("BuildSessionPopupCycleCommand(%s): %v", subcommand, err)
		}
		surfaces["picker "+subcommand+" payload"] = cycle
	}

	hookTrust, err := buildHookTrustPopupArgs(generatedConfigBinary, "/tmp/request.json", "/tmp/decision.txt", hookTrustPopupTarget{})
	if err != nil {
		t.Fatalf("buildHookTrustPopupArgs: %v", err)
	}
	surfaces["hook trust popup payload"] = strings.Join(hookTrust, " ")

	surfaces["statusbar any-key close payload"] = statusbarPopupCommand("body", generatedConfigBinary)

	return surfaces
}

// TestGeneratedConfigEmitsNoPreNamespaceInternalRoute is acceptance criterion 2
// of the internal isolation Phase: a newly installed binary must generate tmux
// config and payloads that reach plumbing only through the hidden `internal`
// namespace.
//
// The scan is exhaustive rather than a list of expected substrings. It finds
// every projmux invocation the generated text contains and judges each one, so
// a plumbing call added later in a spelling nobody thought to assert still
// fails here.
func TestGeneratedConfigEmitsNoPreNamespaceInternalRoute(t *testing.T) {
	t.Parallel()

	relocated := map[string]bool{}
	for _, route := range preNamespaceInternalRoutes {
		relocated[route] = true
	}

	for name, body := range generatedConfigSurfaces(t) {
		routes := generatedProjmuxRoutes(body)
		if len(routes) == 0 {
			t.Fatalf("%s: found no projmux invocation to audit; the scan is not seeing the payload", name)
		}
		for _, route := range routes {
			leading, _, _ := strings.Cut(route, " ")
			if relocated[leading] {
				t.Errorf("%s invokes the pre-namespace internal route %q; generated payloads must use `internal %s`", name, route, route)
			}
			// Every emitted route must exist in the CLI manifest, so a typo in a
			// relocated spelling fails here rather than at runtime inside tmux.
			if _, _, ok := cli.Resolve(strings.Fields(route)); !ok {
				t.Errorf("%s invokes %q, which is not a route in the CLI manifest", name, route)
			}
		}
	}
}

// TestGeneratedConfigReachesEveryRelocatedPlumbingRouteThroughTheInternalNamespace
// is the positive half: the relocation must not have silently dropped a call
// site. Each relocated namespace that the generated surfaces used before is
// still reached, now through `internal`.
func TestGeneratedConfigReachesEveryRelocatedPlumbingRouteThroughTheInternalNamespace(t *testing.T) {
	t.Parallel()

	joined := strings.Join(mapValues(generatedConfigSurfaces(t)), "\n")
	for _, want := range []string{
		"' internal status project",
		// Both HUD segments now receive a tmux format instead of a literal
		// cell count, so the expected substring is built from the same
		// derivation the generator uses.
		"' internal status notify --max-width " + statusbarNotifyBudgetFormat(),
		"' internal status usage --max-width " + statusbarUsageBudgetFormat(),
		"' internal status kube",
		"' internal status git",
		"' internal status resources",
		"' internal statusbar click ",
		"' internal statusbar usage-refresh",
		"' internal tmux popup-toggle --client ",
		"' internal tmux rebalance-panes",
		"' internal tmux autosave-session-state --quiet",
		"' internal popup-wait-key",
		"' internal tmux hook-trust-prompt --request ",
		"' 'internal' 'session-popup' 'preview'",
		"' 'internal' 'session-popup' 'cycle-pane'",
		"' 'internal' 'session-popup' 'cycle-window'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("generated payloads no longer contain %q", want)
		}
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, m[key])
	}
	return out
}

// TestInternalNamespaceSharesTheHandlerInstanceOfEveryRelocatedRoute is
// acceptance criterion 3 stated as an identity rather than as a comparison of
// two captured outputs.
//
// Config generated by a previously installed binary keeps invoking the
// pre-namespace spellings, so those must behave exactly as before. Proving that
// by diffing stdout, stderr, and the exit code of two live runs would only
// cover the argv the test happened to pick. Holding the same handler *instance*
// makes it structural: there is one implementation, so there is nothing for the
// two spellings to differ on.
func TestInternalNamespaceSharesTheHandlerInstanceOfEveryRelocatedRoute(t *testing.T) {
	t.Parallel()

	app := New()
	if app.internal == nil {
		t.Fatal("the application graph is missing the internal namespace command")
	}
	for name, pair := range map[string][2]rawArgvCommand{
		"tmux":           {app.internal.tmux, app.tmux},
		"status":         {app.internal.status, app.status},
		"statusbar":      {app.internal.statusbar, app.statusbar},
		"preview":        {app.internal.preview, app.preview},
		"session-popup":  {app.internal.sessionPopup, app.sessionPopup},
		"agent-hook":     {app.internal.ai, app.ai},
		"key-broker":     {app.internal.keyBroker, app.keyBroker},
		"popup-wait-key": {app.internal.popupWaitKey, app.popupWaitKey},
	} {
		if pair[0] == nil || pair[1] == nil {
			t.Fatalf("internal %s: one side of the alias is not wired", name)
		}
		if pair[0] != pair[1] {
			t.Errorf("internal %s does not share the handler instance of its pre-namespace route", name)
		}
	}

	// Both spellings must also still be reachable from the Cobra root, which is
	// what actually answers a tmux `run-shell` line.
	handlers := app.routeHandlers()
	for _, token := range append([]string{"internal"}, preNamespaceInternalRoutes...) {
		if _, ok := handlers[token]; !ok {
			t.Errorf("route %q has no handler wired; config from a previously installed binary would fail", token)
		}
	}
}

// TestInternalNamespaceForwardsRawArgvUnchanged is the argv half of the parity
// contract: the relocated spelling hands the existing handler exactly the tail
// the pre-namespace spelling would have received, including `--`, unknown
// flags, and tmux format strings.
func TestInternalNamespaceForwardsRawArgvUnchanged(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		args   []string
		target string
		want   []string
	}{
		{name: "status segment", args: []string{"status", "notify", "--max-width", "80"}, target: "status", want: []string{"notify", "--max-width", "80"}},
		{name: "status usage keeps its flags", args: []string{"status", "usage", "--max-width", "120"}, target: "status", want: []string{"usage", "--max-width", "120"}},
		{name: "statusbar click carries a tmux format", args: []string{"statusbar", "click", "#{mouse_status_range}", "--client", "#{client_tty}"}, target: "statusbar", want: []string{"click", "#{mouse_status_range}", "--client", "#{client_tty}"}},
		{name: "statusbar usage refresh", args: []string{"statusbar", "usage-refresh"}, target: "statusbar", want: []string{"usage-refresh"}},
		{name: "tmux popup toggle", args: []string{"tmux", "popup-toggle", "--client", "/dev/pts/7", "sessionizer"}, target: "tmux", want: []string{"popup-toggle", "--client", "/dev/pts/7", "sessionizer"}},
		{name: "tmux autosave", args: []string{"tmux", "autosave-session-state", "--quiet"}, target: "tmux", want: []string{"autosave-session-state", "--quiet"}},
		{name: "preview cycle", args: []string{"preview", "cycle-pane", "dev", "next"}, target: "preview", want: []string{"cycle-pane", "dev", "next"}},
		{name: "session popup preview", args: []string{"session-popup", "preview", "dev"}, target: "session-popup", want: []string{"preview", "dev"}},
		{name: "key broker", args: []string{"key-broker", "--once"}, target: "key-broker", want: []string{"--once"}},
		{name: "popup wait key", args: []string{"popup-wait-key"}, target: "popup-wait-key", want: []string{}},
		{name: "agent hook ingest", args: []string{"agent-hook", "ingest", "codex-hook"}, target: "ai", want: []string{"ingest", "codex-hook"}},
		{name: "agent hook watch title", args: []string{"agent-hook", "watch-title", "%9"}, target: "ai", want: []string{"watch-title", "%9"}},
		{name: "terminator payload survives", args: []string{"tmux", "rename-pane", "%1", "--", "--help"}, target: "tmux", want: []string{"rename-pane", "%1", "--", "--help"}},
		{name: "unknown flag is relayed rather than pre-judged", args: []string{"status", "--bogus"}, target: "status", want: []string{"--bogus"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			targets := map[string]*recordingArgv{}
			cmd := newInternalCommand()
			for _, name := range []string{"tmux", "status", "statusbar", "preview", "session-popup", "ai", "key-broker", "popup-wait-key"} {
				targets[name] = &recordingArgv{}
			}
			cmd.tmux = targets["tmux"]
			cmd.status = targets["status"]
			cmd.statusbar = targets["statusbar"]
			cmd.preview = targets["preview"]
			cmd.sessionPopup = targets["session-popup"]
			cmd.ai = targets["ai"]
			cmd.keyBroker = targets["key-broker"]
			cmd.popupWaitKey = targets["popup-wait-key"]

			if _, _, err := runRoute(t, cmd, test.args...); err != nil {
				t.Fatalf("internal %v error = %v", test.args, err)
			}
			for name, target := range targets {
				if name != test.target {
					if len(target.calls) != 0 {
						t.Fatalf("internal %v also reached the %s handler: %q", test.args, name, target.calls)
					}
					continue
				}
				if len(target.calls) != 1 {
					t.Fatalf("internal %v reached the %s handler %d times, want 1", test.args, name, len(target.calls))
				}
				if got := target.calls[0]; len(got)+len(test.want) > 0 && !reflect.DeepEqual(got, test.want) {
					t.Fatalf("internal %v forwarded %q, want %q", test.args, got, test.want)
				}
			}
		})
	}
}

// TestInternalNamespaceRejectsUnknownSubcommandsAsUsageErrors keeps the
// namespace closed: an unknown namespace or agent-hook verb is exit 2, not a
// silent success, and reaches no handler.
func TestInternalNamespaceRejectsUnknownSubcommandsAsUsageErrors(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"nosuchthing"}, {"agent-hook"}, {"agent-hook", "nosuchthing"}} {
		target := &recordingArgv{}
		cmd := newInternalCommand()
		cmd.tmux, cmd.status, cmd.statusbar, cmd.preview = target, target, target, target
		cmd.sessionPopup, cmd.ai, cmd.keyBroker, cmd.popupWaitKey = target, target, target, target
		_, _, err := runRoute(t, cmd, args...)
		if err == nil {
			t.Fatalf("internal %v returned no error", args)
		}
		if !IsUsageError(err) {
			t.Fatalf("internal %v error = %v, want a usage error (exit 2)", args, err)
		}
		if len(target.calls) != 0 {
			t.Fatalf("internal %v reached a handler: %q", args, target.calls)
		}
	}
}
