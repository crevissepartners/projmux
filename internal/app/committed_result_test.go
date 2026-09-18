package app

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// committed_result_test.go is the enforcement of C-1: once a canonical mutation
// has committed, the interactive route returns nil even when the result line
// cannot be shown, and the loss becomes exactly one journal record.
//
// Every case here is deterministic. Making a real tmux server refuse a
// `display-message` on an exact client is not: the failure depends on when the
// client detaches, which is why this contract had no enforcement at all and why
// a fake runner -- not an isolated tmux smoke -- is what closes it.

// countingJournalWriter counts the records a real recorder appends. The
// recorders the routes carry are concrete types, so the fake goes at the only
// seam they have: the journal's append writer.
type countingJournalWriter struct {
	mu     sync.Mutex
	events []diagnostics.Event
}

func (w *countingJournalWriter) Append(event diagnostics.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
	return nil
}

func (w *countingJournalWriter) snapshot() []diagnostics.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]diagnostics.Event(nil), w.events...)
}

// unshownSources returns the site of every unshown-result record, so a case can
// assert the count and the site in one comparison.
func (w *countingJournalWriter) unshownSources() []string {
	var out []string
	for _, event := range w.snapshot() {
		if event.Event == "runtime.surface.unshown" {
			out = append(out, event.Source)
		}
	}
	return out
}

// committedResultJournal is satisfied by both recorders the routes carry.
var (
	_ committedResultJournal = (*diagnostics.AIRecorder)(nil)
	_ committedResultJournal = (*diagnostics.LifecycleRecorder)(nil)
)

// refusingDisplayRunner is a tmux runner that refuses exactly the bounded
// client message and answers every read the routes make before it.
type refusingDisplayRunner struct {
	calls    [][]string
	outputs  map[string]string
	refuse   bool
	refusals int
}

func (r *refusingDisplayRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if isBoundedClientMessage(args) {
		if r.refuse {
			r.refusals++
			return nil, errors.New("injected display-message refusal: no client /dev/pts/9")
		}
		return nil, nil
	}
	if out, ok := r.outputs[strings.Join(args, "\x00")]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("injected read refusal")
}

func (r *refusingDisplayRunner) messages() []string {
	var out []string
	for _, call := range r.calls {
		if isBoundedClientMessage(call) {
			out = append(out, call[len(call)-1])
		}
	}
	return out
}

// committedResultClient is the exact client every case in this file presses
// from, and the one the injected display-message refusal names.
const committedResultClient = "/dev/pts/9"

func isBoundedClientMessage(args []string) bool {
	return len(args) >= 2 && args[0] == "display-message" && args[1] == "-c"
}

// committedSplitFunnel wires the split funnel the way a generated producer does:
// a canonical create seam, the journal recorder production injects, and a runner
// whose bounded client message refuses.
func committedSplitFunnel(t *testing.T, created createdPaneRuntime, notice string, createErr error, refuse bool) (*aiCommand, *refusingDisplayRunner, *countingJournalWriter) {
	t.Helper()
	home := t.TempDir()
	cmd := testAICommand(home)
	// The exact pressing client is what the generated producer carries, and
	// createPaneFromIntent reads it at the funnel rather than from the intent.
	env := map[string]string{"HOME": home, canonicalCreateTargetClientEnv: committedResultClient}
	cmd.lookupEnv = func(name string) string { return env[name] }
	writer := &countingJournalWriter{}
	cmd.operationalDiagnostics = diagnostics.NewLifecycleRecorder(writer, "committed-result-run", "0.15.3", "tmux").AI()
	runner := &refusingDisplayRunner{refuse: refuse}
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		_, err := runner.Run(context.Background(), name, args...)
		return err
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return runner.Run(context.Background(), name, args...)
	}
	cmd.panes = &committedResultCreator{created: created, notice: notice, err: createErr}
	return cmd, runner, writer
}

// committedResultCreator is the canonical create seam: it commits what the case
// says it commits and writes its split start notice where create writes it.
type committedResultCreator struct {
	created createdPaneRuntime
	notice  string
	err     error
	calls   int
}

func (c *committedResultCreator) createFromIntent(_ agentPaneIntent, _ io.Writer, stderr io.Writer) (createdPaneRuntime, error) {
	c.calls++
	if c.notice != "" {
		_, _ = io.WriteString(stderr, c.notice)
	}
	return c.created, c.err
}

// TestCommittedSplitReturnsNilWhenItsResultLineCannotBeShown is C-1 acceptance
// 1 and 3 at the split funnel: both post-commit lines refuse to be shown, the
// route still succeeds, the Pane it created is untouched, and each loss is one
// journal record.
func TestCommittedSplitReturnsNilWhenItsResultLineCannotBeShown(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		created  createdPaneRuntime
		notice   string
		refuse   bool
		wantLine string
		wantSite string
	}{
		{
			// The focus step reads list-clients through the same runner, which
			// refuses every read, so the focus attempt itself fails.
			name:     "a committed split whose focus failure line cannot be shown",
			created:  createdPaneRuntime{paneID: "%42"},
			refuse:   true,
			wantLine: paneCreatedUnfocusedMessage,
			wantSite: string(diagnostics.SurfaceSiteSplitFocus),
		},
		{
			// An empty committed pane id is create's inherited-target path: the
			// focus step has nothing to move, so the notice is the only line.
			name:     "a committed split whose start notice cannot be shown",
			notice:   "split start notice: the requested Pane directory was not used",
			refuse:   true,
			wantLine: "projmux: split start notice",
			wantSite: string(diagnostics.SurfaceSiteSplitNotice),
		},
		{
			name:     "the same split when the line is shown writes no record",
			notice:   "split start notice: the requested Pane directory was not used",
			wantLine: "projmux: split start notice",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, runner, journal := committedSplitFunnel(t, test.created, test.notice, nil, test.refuse)
			creator := cmd.panes.(*committedResultCreator)

			err := cmd.createPaneFromIntent(agentPaneIntent{
				producer: canonicalProducerPaneMenu, placement: "right",
			})

			if err != nil {
				t.Fatalf("createPaneFromIntent() error = %v, want nil: the split committed", err)
			}
			if creator.calls != 1 {
				t.Fatalf("canonical create calls = %d, want exactly one", creator.calls)
			}
			messages := runner.messages()
			if len(messages) != 1 || !strings.Contains(messages[0], test.wantLine) {
				t.Fatalf("client messages = %#v, want one containing %q", messages, test.wantLine)
			}
			// Nothing rolls the Pane back: no kill-pane, no second create.
			for _, call := range runner.calls {
				if len(call) > 0 && (call[0] == "kill-pane" || call[0] == "split-window") {
					t.Fatalf("a display failure mutated runtime: %v", call)
				}
			}
			var wantSites []string
			if test.wantSite != "" {
				wantSites = []string{test.wantSite}
			}
			if got := journal.unshownSources(); !equalStrings(got, wantSites) {
				t.Fatalf("journal unshown sites = %#v, want %#v", got, wantSites)
			}
		})
	}
}

// TestASplitThatNeverCommittedStillReportsItsRefusal is C-1 acceptance 4 at the
// split funnel. Without this boundary, acceptance 1 is indistinguishable from
// swallowing everything: nothing was created, so a refusal nobody saw has no
// channel left but the route's exit status.
func TestASplitThatNeverCommittedStillReportsItsRefusal(t *testing.T) {
	t.Parallel()

	cmd, runner, journal := committedSplitFunnel(t, createdPaneRuntime{}, "", errors.New("injected canonical create refusal"), true)

	err := cmd.createPaneFromIntent(agentPaneIntent{
		producer: canonicalProducerPaneMenu, placement: "right",
	})

	if err == nil {
		t.Fatal("createPaneFromIntent() error = nil, want the refusal: nothing was committed and nobody saw it")
	}
	if !strings.Contains(err.Error(), "injected canonical create refusal") {
		t.Fatalf("error = %v, want the canonical create refusal", err)
	}
	if runner.refusals != 1 {
		t.Fatalf("display attempts = %d, want exactly one", runner.refusals)
	}
	if got := journal.unshownSources(); len(got) != 0 {
		t.Fatalf("journal unshown sites = %#v, want none: a pre-commit refusal is not an unshown result", got)
	}
}

// committedIntentCommand wires the pane-menu and Window intent routes with the
// lifecycle recorder production injects.
func committedIntentCommand(refuse bool) (*tmuxCommand, *refusingDisplayRunner, *countingJournalWriter) {
	writer := &countingJournalWriter{}
	runner := &refusingDisplayRunner{refuse: refuse}
	cmd := &tmuxCommand{
		runner:      runner,
		diagnostics: diagnostics.NewLifecycleRecorder(writer, "committed-result-run", "0.15.3", "tmux"),
	}
	return cmd, runner, writer
}

// TestCommittedPaneMenuAndWindowIntentsReturnNilWhenTheirLineCannotBeShown is
// C-1 acceptance 2 and 3 at the tmuxCommand seam: every post-commit line of the
// pane menu and the Window intents refuses to be shown, each route still
// succeeds, and each loss is one journal record naming its own site.
func TestCommittedPaneMenuAndWindowIntentsReturnNilWhenTheirLineCannotBeShown(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		wire      func(*tmuxCommand)
		args      []string
		wantSites []string
	}{
		{
			name: "a committed pane-menu split whose success line cannot be shown",
			wire: func(cmd *tmuxCommand) {
				cmd.paneMenuCreate = func(agentPaneIntent, io.Writer, io.Writer) (createdPaneRuntime, error) {
					return createdPaneRuntime{}, nil
				}
			},
			args:      []string{"pane-menu", "--client", committedResultClient, "split-right", "%17"},
			wantSites: []string{string(diagnostics.SurfaceSitePaneMenuSplit)},
		},
		{
			name: "a committed pane-menu split whose focus failure line cannot be shown",
			wire: func(cmd *tmuxCommand) {
				cmd.paneMenuCreate = func(agentPaneIntent, io.Writer, io.Writer) (createdPaneRuntime, error) {
					return createdPaneRuntime{paneID: "%42"}, nil
				}
			},
			args:      []string{"pane-menu", "--client", committedResultClient, "split-right", "%17"},
			wantSites: []string{string(diagnostics.SurfaceSitePaneMenuSplit)},
		},
		{
			name: "a committed pane-menu kill whose summary cannot be shown",
			wire: func(cmd *tmuxCommand) {
				cmd.paneMenuDelete = func(_ string, stdout, _ io.Writer) error {
					_, _ = io.WriteString(stdout, "delete pane: deleting 1 pane and 0 descendant resources\n")
					return nil
				}
			},
			args:      []string{"pane-menu", "--client", committedResultClient, "kill", "%19"},
			wantSites: []string{string(diagnostics.SurfaceSitePaneMenuKill)},
		},
		{
			name: "a committed Window whose client could not be moved and whose line cannot be shown",
			wire: func(cmd *tmuxCommand) {
				cmd.windowCreate = func(windowCreateIntent, io.Writer, io.Writer) (createdWindowRuntime, error) {
					return createdWindowRuntime{}, nil
				}
			},
			args:      []string{"window-create", "--client", committedResultClient, "--anchor", "%17"},
			wantSites: []string{string(diagnostics.SurfaceSiteWindowIntent)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, runner, journal := committedIntentCommand(true)
			test.wire(cmd)

			if err := cmd.Run(test.args, io.Discard, io.Discard); err != nil {
				t.Fatalf("Run(%v) error = %v, want nil: the mutation committed", test.args, err)
			}
			if runner.refusals != 1 {
				t.Fatalf("display attempts = %d, want exactly one", runner.refusals)
			}
			for _, call := range runner.calls {
				if len(call) > 0 && (call[0] == "kill-pane" || call[0] == "kill-window" || call[0] == "split-window") {
					t.Fatalf("a display failure mutated runtime: %v", call)
				}
			}
			if got := journal.unshownSources(); !equalStrings(got, test.wantSites) {
				t.Fatalf("journal unshown sites = %#v, want %#v", got, test.wantSites)
			}
		})
	}
}

// TestASuccessfullyShownIntentLineWritesNoRecord is the negative sample of
// acceptance 3: the journal grows only when the line was lost.
func TestASuccessfullyShownIntentLineWritesNoRecord(t *testing.T) {
	t.Parallel()

	cmd, runner, journal := committedIntentCommand(false)
	cmd.paneMenuCreate = func(agentPaneIntent, io.Writer, io.Writer) (createdPaneRuntime, error) {
		return createdPaneRuntime{}, nil
	}

	if err := cmd.Run([]string{"pane-menu", "--client", committedResultClient, "split-right", "%17"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if messages := runner.messages(); len(messages) != 1 || messages[0] != paneMenuCreatedMessage {
		t.Fatalf("client messages = %#v, want one %q", messages, paneMenuCreatedMessage)
	}
	if got := journal.snapshot(); len(got) != 0 {
		t.Fatalf("journal events = %#v, want none", got)
	}
}

// TestAPaneMenuOrWindowIntentThatNeverCommittedStillReportsItsRefusal is
// acceptance 4 at the tmuxCommand seam.
func TestAPaneMenuOrWindowIntentThatNeverCommittedStillReportsItsRefusal(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		wire func(*tmuxCommand)
		args []string
	}{
		{
			name: "the pane-menu create refused",
			wire: func(cmd *tmuxCommand) {
				cmd.paneMenuCreate = func(agentPaneIntent, io.Writer, io.Writer) (createdPaneRuntime, error) {
					return createdPaneRuntime{}, errors.New("injected canonical create refusal")
				}
			},
			args: []string{"pane-menu", "--client", committedResultClient, "split-right", "%17"},
		},
		{
			name: "the pane-menu delete refused",
			wire: func(cmd *tmuxCommand) {
				cmd.paneMenuDelete = func(string, io.Writer, io.Writer) error {
					return errors.New("injected canonical delete refusal")
				}
			},
			args: []string{"pane-menu", "--client", committedResultClient, "kill", "%19"},
		},
		{
			name: "the Window create refused",
			wire: func(cmd *tmuxCommand) {
				cmd.windowCreate = func(windowCreateIntent, io.Writer, io.Writer) (createdWindowRuntime, error) {
					return createdWindowRuntime{}, errors.New("injected canonical Window create refusal")
				}
			},
			args: []string{"window-create", "--client", committedResultClient, "--anchor", "%17"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, runner, journal := committedIntentCommand(true)
			test.wire(cmd)

			err := cmd.Run(test.args, io.Discard, io.Discard)

			if err == nil {
				t.Fatal("Run() error = nil, want the refusal: nothing was committed and nobody saw it")
			}
			if runner.refusals != 1 {
				t.Fatalf("display attempts = %d, want exactly one", runner.refusals)
			}
			if got := journal.unshownSources(); len(got) != 0 {
				t.Fatalf("journal unshown sites = %#v, want none", got)
			}
		})
	}
}

// TestCommittedResultDisplaySitesAreClosed is C-1 acceptance 5: the source sweep
// that makes a new post-commit site fail until it is registered and classified.
//
// It reads the package source the way TestRunShellSourceSitesAreClosed reads it.
// Each occurrence of a result-line call in the interactive adapter layer has to
// match exactly one ledger row, and a committed row has to go through a
// showCommitted* entry point -- which returns nothing, so the display failure
// cannot become the route's exit status.
func TestCommittedResultDisplaySitesAreClosed(t *testing.T) {
	t.Parallel()

	registered := committedResultDisplaySites()
	matched := make([]int, len(registered))
	var unregistered []string

	for _, name := range committedResultSweepFiles() {
		for lineNo, line := range committedResultSourceLines(t, name) {
			if !committedResultLineShowsALine(line) {
				continue
			}
			hits := 0
			for i, row := range registered {
				if row.File == name && strings.Contains(line, row.Snippet) {
					matched[i]++
					hits++
				}
			}
			switch {
			case hits == 0:
				unregistered = append(unregistered, name+":"+strconv.Itoa(lineNo)+": "+strings.TrimSpace(line))
			case hits > 1:
				t.Errorf("%s:%d matches %d ledger rows; snippets must identify one site: %s", name, lineNo, hits, strings.TrimSpace(line))
			}
		}
	}
	if len(unregistered) != 0 {
		t.Fatalf("unclassified result-line call sites:\n  %s\nregister each one in committedResultDisplaySites()", strings.Join(unregistered, "\n  "))
	}
	for i, row := range registered {
		if matched[i] != 1 {
			t.Errorf("ledger row %s %q matched %d source lines, want exactly one", row.File, row.Snippet, matched[i])
		}
	}

	used := map[diagnostics.SurfaceSite]bool{}
	for _, row := range registered {
		switch row.Kind {
		case committedResultCommitted:
			if !strings.Contains(row.Snippet, "showCommitted") {
				t.Errorf("committed row %s %q does not go through the committed-result seam", row.File, row.Snippet)
			}
			if row.Site == "" {
				t.Errorf("committed row %s %q records no journal site", row.File, row.Snippet)
			}
			used[row.Site] = true
		case committedResultPreCommit, committedResultTransport:
			if strings.Contains(row.Snippet, "showCommitted") {
				t.Errorf("%s row %s %q uses the committed-result seam", row.Kind, row.File, row.Snippet)
			}
			if row.Site != "" {
				t.Errorf("%s row %s %q names a journal site", row.Kind, row.File, row.Snippet)
			}
		default:
			t.Errorf("row %s %q has no classification", row.File, row.Snippet)
		}
		if strings.TrimSpace(row.Note) == "" {
			t.Errorf("row %s %q has no note", row.File, row.Snippet)
		}
	}
	// Extending the closed site inventory without wiring the site is the other
	// half: an unused site means a seam nobody reports through.
	for _, site := range diagnostics.SurfaceSites() {
		if !used[site] {
			t.Errorf("surface site %q is in the closed inventory but no registered call site records it", site)
		}
	}
}

// committedResultSourceLines returns the non-comment source lines of one package
// file, keyed by 1-based line number.
func committedResultSourceLines(t *testing.T, name string) map[int]string {
	t.Helper()
	file, err := os.Open(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer func() { _ = file.Close() }()
	lines := map[int]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := scanner.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		lines[lineNo] = line
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return lines
}

func committedResultLineShowsALine(line string) bool {
	for _, token := range committedResultSweepTokens() {
		if strings.Contains(line, token) {
			return true
		}
	}
	return false
}
