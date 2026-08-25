package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// runShellOccurrence is one `run-shell` tmux will run, lifted back out of the
// generated config.
type runShellOccurrence struct {
	Surface    runShellSurface
	Command    string
	Background bool
	Line       string
}

// extractRunShellOccurrences finds every `run-shell` in a generated config.
//
// A generated line can carry several: the pane context menu packs four into one
// `display-menu`. Each occurrence therefore owns the text from its own
// `run-shell` up to the next one, which is what keeps the redirect and
// exit-guard assertions pointed at the right command.
func extractRunShellOccurrences(t *testing.T, generated string) []runShellOccurrence {
	t.Helper()
	var occurrences []runShellOccurrence
	for line := range strings.SplitSeq(generated, "\n") {
		if !strings.Contains(line, "run-shell") {
			continue
		}
		starts := []int{}
		for offset := 0; ; {
			idx := strings.Index(line[offset:], "run-shell")
			if idx < 0 {
				break
			}
			starts = append(starts, offset+idx)
			offset += idx + len("run-shell")
		}
		surface := runShellSurfaceOf(line[:starts[0]])
		for i, start := range starts {
			end := len(line)
			if i+1 < len(starts) {
				end = starts[i+1]
			}
			command := line[start:end]
			occurrences = append(occurrences, runShellOccurrence{
				Surface:    surface,
				Command:    command,
				Background: strings.HasPrefix(strings.TrimPrefix(command, "run-shell"), " -b"),
				Line:       line,
			})
		}
	}
	return occurrences
}

func runShellSurfaceOf(prefix string) runShellSurface {
	switch {
	case strings.Contains(prefix, "MouseDown3Pane"):
		return runShellSurfacePaneMenu
	case strings.Contains(prefix, "MouseDown1Status"), strings.Contains(prefix, "-T projmux-status"):
		return runShellSurfaceStatusbar
	case strings.Contains(prefix, "set-hook"):
		return runShellSurfaceHook
	case strings.Contains(prefix, "bind-key"):
		return runShellSurfaceKeybinding
	default:
		return runShellSurfaceStartup
	}
}

// keyBindingCatalogWithEveryChordBound gives every catalog action a chord.
//
// Several actions -- Window create and rename among them, the two this track
// was opened for -- ship with no default chord and are bound from keymap.toml,
// so a sweep of the stock config would never see the producers they render. The
// ledger is a contract about the renderer, not about one operator's keymap, so
// the sweep binds them all.
func keyBindingCatalogWithEveryChordBound() []keyBindingAction {
	catalog := defaultKeyBindingCatalog()
	for i := range catalog {
		if len(keyBindingEffectivePlainChords(catalog[i])) != 0 {
			continue
		}
		catalog[i].PlainChords = []string{fmt.Sprintf("F%d", i+1)}
	}
	return catalog
}

func generatedConfigsUnderTest() map[string]string {
	const bin = "/opt/projmux/bin/projmux"
	decorations := statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff)
	catalog := keyBindingCatalogWithEveryChordBound()
	return map[string]string{
		"standalone": tmuxStandaloneConfigWithKeymap(bin, decorations, catalog, false),
		"app":        tmuxAppConfigWithKeymap(bin, "/bin/zsh", decorations, catalog, false),
	}
}

// TestRunShellOutputLedgerClassifiesEveryGeneratedProducer is the closed-set
// gate. tmux draws a foreground `run-shell` job's output over the pane the
// operator is working in, so an unclassified producer is not a documentation
// gap -- it is the next overlay. Every occurrence in the generated config must
// resolve to exactly one ledger row, and every row that claims to be generated
// must appear.
func TestRunShellOutputLedgerClassifiesEveryGeneratedProducer(t *testing.T) {
	t.Parallel()

	matched := map[string]int{}
	for name, generated := range generatedConfigsUnderTest() {
		for _, occurrence := range extractRunShellOccurrences(t, generated) {
			var hits []runShellProducer
			for _, row := range runShellOutputLedger() {
				if row.Surface == occurrence.Surface && strings.Contains(occurrence.Command, row.Match) {
					hits = append(hits, row)
				}
			}
			if len(hits) != 1 {
				ids := make([]string, 0, len(hits))
				for _, hit := range hits {
					ids = append(ids, hit.ID)
				}
				t.Fatalf("%s config: %q resolved to %d ledger rows %v, want exactly 1; classify the producer in runShellOutputLedger",
					name, occurrence.Command, len(hits), ids)
			}
			row := hits[0]
			matched[row.ID]++
			if row.Background != occurrence.Background {
				t.Fatalf("%s config: %s declares Background=%t, generated %q", name, row.ID, row.Background, occurrence.Command)
			}
			if row.Redirected && !strings.Contains(occurrence.Command, ">/dev/null 2>&1") {
				t.Fatalf("%s config: %s declares an explicit redirect, generated %q", name, row.ID, occurrence.Command)
			}
			if row.ExitGuarded && !strings.Contains(occurrence.Command, "|| true") {
				t.Fatalf("%s config: %s declares an exit guard, generated %q", name, row.ID, occurrence.Command)
			}
		}
	}

	for _, row := range runShellOutputLedger() {
		if row.Surface == runShellSurfaceRuntime || row.RuntimeInstalled {
			continue
		}
		if matched[row.ID] == 0 {
			t.Fatalf("ledger row %s matched no generated producer; retire the row or fix its Match", row.ID)
		}
	}
}

// TestRunShellOutputLedgerHoldsTheNoOverlayContract asserts C-1 on the ledger
// itself: nothing is classified as an overlay, every foreground interactive
// producer routes through the guard that consumes stdout, stderr, and the exit
// status, and every hook keeps its redirect.
func TestRunShellOutputLedgerHoldsTheNoOverlayContract(t *testing.T) {
	t.Parallel()

	guarded := map[string]bool{}
	for _, route := range interactiveRunShellRoutes() {
		guarded[route.ID] = true
	}

	seen := map[string]bool{}
	for _, row := range runShellOutputLedger() {
		if seen[row.ID] {
			t.Fatalf("duplicate ledger id %s", row.ID)
		}
		seen[row.ID] = true

		if row.Channel == runShellChannelForbiddenOverlay {
			t.Fatalf("%s is classified as %s: a generated producer may never reach tmux view-mode", row.ID, row.Channel)
		}
		switch row.Channel {
		case runShellChannelIntentionalUI, runShellChannelSilent, runShellChannelRedirect, runShellChannelExactClientMessage:
		default:
			t.Fatalf("%s has unknown channel %q", row.ID, row.Channel)
		}

		if row.Route != "" && !guarded[row.Route] {
			t.Fatalf("%s names route %q, which no interactiveRunShellRoutes entry owns", row.ID, row.Route)
		}
		if !row.Background && row.Surface != runShellSurfaceRuntime {
			switch {
			case row.Route != "":
			case row.Redirected && row.ExitGuarded:
			case row.ControlSentinel:
			default:
				t.Fatalf("%s runs in the foreground without a guarded route, a redirect plus exit guard, or sentinel status: tmux would paint its result", row.ID)
			}
		}
		if row.Surface == runShellSurfaceHook && row.Channel != runShellChannelRedirect {
			t.Fatalf("%s is a hook classified as %s; hooks are machine convergence and stay redirected", row.ID, row.Channel)
		}
	}

	// The control-sentinel allowlist is closed. It is the one row the C-1
	// contract puts out of scope, and it stays a single, named, non-UI signal.
	var sentinels []string
	for _, row := range runShellOutputLedger() {
		if row.ControlSentinel {
			sentinels = append(sentinels, row.ID)
		}
	}
	if len(sentinels) != 1 || sentinels[0] != "runtime.quit-refusal-sentinel" {
		t.Fatalf("control sentinel allowlist = %v, want exactly [runtime.quit-refusal-sentinel]", sentinels)
	}
}

// TestRunShellSourceSitesAreClosed keeps the ledger honest against the source
// rather than only against today's generated output. A producer added behind a
// flag, a new hook, or a second context menu would otherwise never appear in
// the config sweep at its default settings.
func TestRunShellSourceSitesAreClosed(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	sites := runShellSourceSites()
	used := make([]bool, len(sites))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// This file and the ledger it enforces both spell `run-shell` in prose
		// and in match tokens; neither emits one.
		if name == "run_shell_output_ledger.go" || name == "interactive_run_shell.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for lineNo, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, "run-shell") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			// The most specific registration wins: one line can match both a
			// family snippet and the exact producer's own.
			hit, ambiguous := -1, false
			for i, site := range sites {
				if site.File != name || !strings.Contains(line, site.Snippet) {
					continue
				}
				switch {
				case hit < 0 || len(site.Snippet) > len(sites[hit].Snippet):
					hit, ambiguous = i, false
				case len(site.Snippet) == len(sites[hit].Snippet):
					ambiguous = true
				}
			}
			if ambiguous {
				t.Fatalf("%s:%d matches two registered sites of equal specificity; make the snippets exact", name, lineNo+1)
			}
			if hit < 0 {
				t.Fatalf("%s:%d emits a run-shell that no ledger site covers:\n\t%s\nregister it in runShellSourceSites and classify it in runShellOutputLedger", name, lineNo+1, trimmed)
			}
			used[hit] = true
		}
	}
	for i, site := range sites {
		if !used[i] {
			t.Fatalf("registered run-shell site %s %q matched no source line; retire it", site.File, site.Snippet)
		}
	}
}

// TestGeneratedInteractiveProducersInvokeGuardedRoutes ties the two halves
// together: the argv a generated interactive binding actually emits has to be
// argv the guard recognizes. A route renamed on one side and not the other
// would silently fall back to raw `run-shell` output.
func TestGeneratedInteractiveProducersInvokeGuardedRoutes(t *testing.T) {
	t.Parallel()

	for name, generated := range generatedConfigsUnderTest() {
		for _, occurrence := range extractRunShellOccurrences(t, generated) {
			if occurrence.Background {
				continue
			}
			var row runShellProducer
			for _, candidate := range runShellOutputLedger() {
				if candidate.Surface == occurrence.Surface && strings.Contains(occurrence.Command, candidate.Match) {
					row = candidate
					break
				}
			}
			if row.Route == "" {
				continue
			}
			argv := interactiveArgvFromGeneratedCommand(occurrence.Command)
			route, _, ok := matchInteractiveRunShellRoute(argv, func(string) string { return "/dev/pts/9" })
			if !ok || route.ID != row.Route {
				t.Fatalf("%s config: %s emits argv %v, which the guard resolves to %q (matched=%t), want %q",
					name, row.ID, argv, route.ID, ok, row.Route)
			}
		}
	}
}

// interactiveArgvFromGeneratedCommand recovers the projmux argv from one
// generated `run-shell` command. The binary path is quoted and may be preceded
// by the binding's env prefix, so the argv starts after the quoted path.
func interactiveArgvFromGeneratedCommand(command string) []string {
	_, after, found := strings.Cut(command, "bin/projmux'")
	if !found {
		return nil
	}
	fields := strings.Fields(after)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, `"`)
		if field == "}" || field == "" {
			break
		}
		out = append(out, field)
	}
	return out
}
