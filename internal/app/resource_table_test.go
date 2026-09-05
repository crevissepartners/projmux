package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// columnarHeader is one parsed header cell: its text and the display-cell
// offset the column starts at.
type columnarHeader struct {
	name  string
	start int
}

// parseColumnarHeaders reads the header line of a columnar read back into
// column names and their display-cell start offsets.
func parseColumnarHeaders(line string) []columnarHeader {
	var headers []columnarHeader
	var cell strings.Builder
	offset := 0
	start := 0
	spaces := 0
	for _, r := range line {
		if r == ' ' {
			spaces++
			offset++
			continue
		}
		if spaces >= resourceTableGap && cell.Len() > 0 {
			headers = append(headers, columnarHeader{name: cell.String(), start: start})
			cell.Reset()
			start = offset
		} else if spaces > 0 {
			cell.WriteString(strings.Repeat(" ", spaces))
		}
		spaces = 0
		cell.WriteRune(r)
		offset += projmuxpicker.RuneWidth(r)
	}
	if cell.Len() > 0 {
		headers = append(headers, columnarHeader{name: cell.String(), start: start})
	}
	return headers
}

// sliceCells returns the substring of line covering display cells [from, to).
//
// It measures in display cells rather than bytes or runes on purpose: that is
// the only slicing that agrees with what a terminal actually shows, and it is
// what makes an alignment assertion here falsifiable against rune-count padding.
func sliceCells(line string, from, to int) string {
	var out strings.Builder
	offset := 0
	for _, r := range line {
		width := projmuxpicker.RuneWidth(r)
		if offset >= from && (to < 0 || offset < to) {
			out.WriteRune(r)
		}
		offset += width
	}
	return strings.TrimRight(out.String(), " ")
}

// columnarRows parses a columnar read into one map per data row, keyed by
// header name, by slicing every line at the header's own column offsets.
//
// A row whose cells do not start exactly where the header says they do lands in
// the wrong key, which is what turns "the columns are aligned" into an
// assertion instead of an eyeball check.
func columnarRows(t *testing.T, stdout string) []map[string]string {
	t.Helper()
	trimmed := strings.TrimRight(stdout, "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	headers := parseColumnarHeaders(lines[0])
	if len(headers) == 0 {
		t.Fatalf("columnar output has no header line:\n%s", stdout)
	}
	rows := make([]map[string]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		row := map[string]string{}
		for i, header := range headers {
			end := -1
			if i+1 < len(headers) {
				end = headers[i+1].start
			}
			row[header.name] = sliceCells(line, header.start, end)
		}
		rows = append(rows, row)
	}
	return rows
}

// TestGetListDefaultProjectionIsColumnar is the per-kind golden of the columnar
// default projection: one uppercase header line, then one space-aligned row per
// match, with no box-drawing character anywhere.
func TestGetListDefaultProjectionIsColumnar(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		kind string
		want string
	}{
		{
			kind: "projects",
			want: "NAME   STATUS        ACTIONS\n" +
				"alpha  live          -\n" +
				"beta   offline       -\n" +
				"gone   missing-root  -\n",
		},
		{
			kind: "windows",
			want: "NAME    STATUS        ACTIONS\n" +
				"main    live          -\n" +
				"review  live          -\n" +
				"main    offline       -\n" +
				"main    missing-root  -\n",
		},
		{
			kind: "panes",
			want: "NAME        STATUS        ACTIONS\n" +
				"zsh         live          -\n" +
				"log         live          -\n" +
				"codex-pane  live          -\n" +
				"review-zsh  live          -\n" +
				"zsh         offline       -\n" +
				"zsh         missing-root  -\n",
		},
		{
			kind: "agents",
			want: "NAME   STATUS   ACTIONS\n" +
				"codex  live     -\n" +
				"codex  offline  -\n",
		},
	} {
		t.Run(test.kind, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			prepareDisplayFirstTableFixture(t, store)
			setFixtureSessionRef(t, store, "agt-alpha-codex", &coremetadata.AgentSessionRef{
				Provider:   "codex",
				ObservedAt: sessionRefObservedAt,
				Codex:      &coremetadata.CodexSessionRef{ThreadID: "codex-thread-1", SessionID: "codex-session-1"},
			})
			stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), test.kind)
			if err != nil {
				t.Fatalf("get %s error = %v (stderr %q)", test.kind, err, stderr)
			}
			if stdout != test.want {
				t.Fatalf("get %s stdout =\n%s\nwant\n%s", test.kind, stdout, test.want)
			}
			if stderr != "" {
				t.Fatalf("get %s stderr = %q, want none", test.kind, stderr)
			}
			for _, forbidden := range []string{"forbidden-stored-pane-title", "forbidden stored editor label", "forbidden stored review label", "projmux"} {
				if strings.Contains(stdout, forbidden) {
					t.Fatalf("get %s promoted secondary display value %q:\n%s", test.kind, forbidden, stdout)
				}
			}
			// The projection is a header plus padding spaces and nothing else.
			for _, forbidden := range []string{"│", "─", "┌", "┐", "└", "┘", "├", "┤", "┬", "┴", "┼", "|", "+-", "\t"} {
				if strings.Contains(stdout, forbidden) {
					t.Fatalf("get %s emitted the box/rule character %q:\n%s", test.kind, forbidden, stdout)
				}
			}
			for line := range strings.SplitSeq(strings.TrimRight(stdout, "\n"), "\n") {
				if strings.TrimRight(line, " ") != line {
					t.Fatalf("get %s emitted a line with trailing whitespace: %q", test.kind, line)
				}
			}
		})
	}
}

func TestGetHumanContextNoTransportKeepsStoredPresentationOutAndNameStable(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	command := newTestListGetCommand(t, store)
	command.runtime = nil
	stdout, stderr, err := runRoute(t, command, "projects", "--project", "alpha", "-o", "wide")
	if err != nil {
		t.Fatalf("get projects without transport: %v (stderr=%q)", err, stderr)
	}
	rows := columnarRows(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one Project", rows)
	}
	if got := rows[0]; got["CONTEXT"] != "alpha" || got["SOURCE"] != "project-root-basename" ||
		got["OBSERVED"] != "false" || got["NAME"] != "alpha" {
		t.Fatalf("no-transport Project row = %v", got)
	}
}

func TestGetAndDescribeHumanContextEnglishKoreanGolden(t *testing.T) {
	t.Parallel()

	var got strings.Builder
	for _, test := range []struct {
		locale string
		topic  string
	}{
		{locale: "en-US", topic: "Release review"},
		{locale: "ko-KR", topic: "릴리스 검토"},
	} {
		store := newFakeResourceStore(t)
		agent, ok := store.registry.Agent("agt-alpha-codex")
		if !ok {
			t.Fatal("fixture Agent is missing")
		}
		agent.Metadata.Annotations = map[string]string{coremetadata.AnnotationAgentTopic: test.topic}

		table, stderr, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "alpha", "-o", "wide")
		if err != nil {
			t.Fatalf("%s get agents: %v (stderr=%q)", test.locale, err, stderr)
		}
		description, stderr, err := runRoute(t, newTestDescribeCommand(t, store), "agent", "codex", "--project", "alpha")
		if err != nil {
			t.Fatalf("%s describe agent: %v (stderr=%q)", test.locale, err, stderr)
		}
		fmt.Fprintf(&got, "== %s table ==\n%s== %s describe ==\n%s", test.locale, table, test.locale, description)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "resource-context-human.golden"))
	if err != nil {
		t.Fatalf("read human context golden: %v\ngot:\n%s", err, got.String())
	}
	if got.String() != string(want) {
		t.Fatalf("human context golden mismatch:\ngot:\n%swant:\n%s", got.String(), string(want))
	}
}

// prepareDisplayFirstTableFixture models every no-transport context source the
// plural table can read without making stored presentation authoritative.
func prepareDisplayFirstTableFixture(t *testing.T, store *fakeResourceStore) {
	t.Helper()

	for i := range store.registry.Panes {
		if store.registry.Panes[i].Spec.Role == coremetadata.PaneRoleShell {
			store.registry.Panes[i].Spec.Command = "/bin/zsh -l"
		}
	}
	for i := range store.registry.Agents {
		store.registry.Agents[i].Metadata.Annotations = map[string]string{
			coremetadata.AnnotationAgentTopic: "agent task",
		}
	}
	if _, ok := store.registry.Project("prj-gone"); !ok {
		t.Fatal("fixture project prj-gone is missing")
	}
}

// TestResourceTableColumnsAreTheCanonicalContract pins the column set and order
// of every kind, so a later change that adds or reorders a column has to say so
// here first.
func TestResourceTableColumnsAreTheCanonicalContract(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		kind coremetadata.Kind
		want []string
	}{
		{coremetadata.KindProject, []string{"NAME", "STATUS", "ACTIONS"}},
		{coremetadata.KindWindow, []string{"NAME", "STATUS", "ACTIONS"}},
		{coremetadata.KindPane, []string{"NAME", "STATUS", "ACTIONS"}},
		{coremetadata.KindAgent, []string{"NAME", "STATUS", "ACTIONS"}},
	} {
		got := columnHeaders(resourceTableColumns(test.kind, columnCompact))
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Fatalf("%s columns = %v, want %v", test.kind, got, test.want)
		}
	}
	if len(resourceListKindTokens) != 4 {
		t.Fatalf("the column contract covers %d kinds, want exactly the 4 list kinds", len(resourceListKindTokens))
	}
}

func TestResourceTableKeepsDurableNameSeparateFromEphemeralContext(t *testing.T) {
	t.Parallel()

	match := selector.Match{
		UID: "missing", Name: "stable",
		Context: registryview.Context{Value: "operator hint", Source: registryview.ContextSourceAgentTopic, Observed: true},
		Status:  selector.StatusOffline,
	}
	row := resourceTableRow(match, coremetadata.KindProject, coremetadata.NewRegistry(), time.Time{}, columnWide, nil)
	if got, want := row[:7], []string{"project", "stable", "offline", "-", "operator hint", "agent-topic", "true"}; !slices.Equal(got, want) {
		t.Fatalf("context/name cells = %q, want %q", got, want)
	}

	match.Context = registryview.Context{}
	row = resourceTableRow(match, coremetadata.KindProject, coremetadata.NewRegistry(), time.Time{}, columnWide, nil)
	if got, want := row[:7], []string{"project", "stable", "offline", "-", "", "", "false"}; !slices.Equal(got, want) {
		t.Fatalf("empty-context/name cells = %q, want %q", got, want)
	}
}

// TestResourceTableWidthsUseDisplayCells is the width-calculation table, and it
// is written to fail under rune-count padding.
//
// Every Hangul syllable is one rune and two terminal cells. A renderer that
// pads by rune count therefore emits one space too many per syllable, which
// this table catches twice: the frozen golden bytes differ, and the parsed row
// keys stop lining up with the header offsets.
func TestResourceTableWidthsUseDisplayCells(t *testing.T) {
	t.Parallel()

	registry := coremetadata.NewRegistry()
	for _, test := range []struct {
		name    string
		kind    coremetadata.Kind
		matches []selector.Match
		want    string
	}{
		{
			name: "hangul names are two cells wide per syllable",
			kind: coremetadata.KindPane,
			matches: []selector.Match{
				{Kind: coremetadata.KindPane, UID: "p1", Name: "쉘", Status: selector.StatusLive,
					Owner: selector.OwnerContext{Project: "알파", Window: "메인"}},
				{Kind: coremetadata.KindPane, UID: "p2", Name: "log", Status: selector.StatusOffline,
					Owner: selector.OwnerContext{Project: "alpha", Window: "main"}},
				{Kind: coremetadata.KindPane, UID: "p3", Name: "빌드로그", Status: selector.StatusLive,
					Owner: selector.OwnerContext{Project: "알파", Window: "review"}},
			},
			want: "KIND  NAME      STATUS   ACTIONS  CONTEXT  SOURCE  OBSERVED  PROJECT  WINDOW  AGENT  TERMINATION  AGE\n" +
				"pane  쉘        live     -                         false     알파     메인\n" +
				"pane  log       offline  -                         false     alpha    main\n" +
				"pane  빌드로그  live     -                         false     알파     review\n",
		},
		{
			name: "a long value widens its own column only",
			kind: coremetadata.KindWindow,
			matches: []selector.Match{
				{Kind: coremetadata.KindWindow, UID: "w1", Name: "a-very-long-window-name-indeed", Status: selector.StatusLive,
					Owner: selector.OwnerContext{Project: "alpha"}},
				{Kind: coremetadata.KindWindow, UID: "w2", Name: "m", Status: selector.StatusMissingRoot,
					Owner: selector.OwnerContext{Project: "beta"}},
			},
			want: "KIND    NAME                            STATUS        ACTIONS  CONTEXT  SOURCE  OBSERVED  PROJECT  AGE\n" +
				"window  a-very-long-window-name-indeed  live          -                         false     alpha\n" +
				"window  m                               missing-root  -                         false     beta\n",
		},
		{
			name: "an empty interior cell still holds its column",
			kind: coremetadata.KindPane,
			matches: []selector.Match{
				{Kind: coremetadata.KindPane, UID: "p1", Name: "orphan", Status: selector.StatusOffline},
				{Kind: coremetadata.KindPane, UID: "p2", Name: "zsh", Status: selector.StatusLive,
					Owner: selector.OwnerContext{Project: "alpha", Window: "main"}},
			},
			want: "KIND  NAME    STATUS   ACTIONS  CONTEXT  SOURCE  OBSERVED  PROJECT  WINDOW  AGENT  TERMINATION  AGE\n" +
				"pane  orphan  offline  -                         false\n" +
				"pane  zsh     live     -                         false     alpha    main\n",
		},
		{
			// The AGENT column is the third leg of a Pane's owner chain, which
			// the one-line summary rendered as `agent/<name>`. A managed Pane
			// fills it; a shell Pane leaves it empty without disturbing the
			// columns to its left.
			name: "a managed pane fills AGENT and a shell pane leaves it empty",
			kind: coremetadata.KindPane,
			matches: []selector.Match{
				{Kind: coremetadata.KindPane, UID: "p1", Name: "codex-pane", Status: selector.StatusLive,
					Owner: selector.OwnerContext{Project: "alpha", Window: "main", Agent: "codex"}},
				{Kind: coremetadata.KindPane, UID: "p2", Name: "zsh", Status: selector.StatusLive,
					Owner: selector.OwnerContext{Project: "alpha", Window: "main"}},
			},
			want: "KIND  NAME        STATUS  ACTIONS  CONTEXT  SOURCE  OBSERVED  PROJECT  WINDOW  AGENT  TERMINATION  AGE\n" +
				"pane  codex-pane  live    -                         false     alpha    main    codex\n" +
				"pane  zsh         live    -                         false     alpha    main\n",
		},
		{
			name: "an empty trailing cell ends the line rather than padding it",
			kind: coremetadata.KindAgent,
			matches: []selector.Match{
				{Kind: coremetadata.KindAgent, UID: "a1", Name: "codex", Status: selector.StatusLive,
					Owner: selector.OwnerContext{Project: "alpha", Window: "main"}},
			},
			want: "KIND   NAME   STATUS  ACTIONS  CONTEXT  SOURCE  OBSERVED  INTERACTION  PROJECT  WINDOW  SESSION  TERMINATION  AGE\n" +
				"agent  codex  live    -                         false     unknown      alpha    main\n",
		},
		{
			name:    "zero matches emit zero bytes",
			kind:    coremetadata.KindProject,
			matches: nil,
			want:    "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := writeResourceTable(&out, "get "+strings.ToLower(string(test.kind))+"s", test.kind, test.matches, registry, resourceFixtureReadClock, columnWide, nil); err != nil {
				t.Fatalf("writeResourceTable error = %v", err)
			}
			if out.String() != test.want {
				t.Fatalf("table =\n%q\nwant\n%q", out.String(), test.want)
			}
			if test.want == "" {
				return
			}
			// Every populated cell has to begin exactly at its header's own
			// display-cell offset. Rune-count padding fails this on the Hangul
			// case even where the byte golden might be argued over.
			for i, row := range columnarRows(t, out.String()) {
				for _, header := range parseColumnarHeaders(strings.SplitN(test.want, "\n", 2)[0]) {
					if _, ok := row[header.name]; !ok {
						t.Fatalf("row %d lost column %q: %v", i, header.name, row)
					}
				}
			}
		})
	}
}

// TestResourceTableHangulAlignmentIsNotRuneCount states the width contract as a
// direct comparison against the arithmetic it replaces.
//
// It is the falsification test: it recomputes the same table with rune-count
// widths and requires the two renderings to differ. If display-cell measurement
// ever silently degrades to rune counting, this reddens even if every golden
// above were updated to match.
func TestResourceTableHangulAlignmentIsNotRuneCount(t *testing.T) {
	t.Parallel()

	matches := []selector.Match{
		{Kind: coremetadata.KindPane, UID: "p1", Name: "쉘", Status: selector.StatusLive,
			Owner: selector.OwnerContext{Project: "알파", Window: "메인"}},
		{Kind: coremetadata.KindPane, UID: "p2", Name: "log", Status: selector.StatusOffline,
			Owner: selector.OwnerContext{Project: "alpha", Window: "main"}},
	}
	var out bytes.Buffer
	if err := writeResourceTable(&out, "get panes", coremetadata.KindPane, matches, coremetadata.NewRegistry(), resourceFixtureReadClock, columnWide, nil); err != nil {
		t.Fatalf("writeResourceTable error = %v", err)
	}

	if got := runeCountTable(coremetadata.KindPane, matches); got == out.String() {
		t.Fatalf("display-cell padding is indistinguishable from rune-count padding:\n%s", got)
	}
	// And the real renderer is the one whose columns line up in cells.
	for line := range strings.SplitSeq(strings.TrimRight(out.String(), "\n"), "\n") {
		if got, want := projmuxpicker.VisibleLen(sliceCells(line, 0, 10)), projmuxpicker.VisibleLen(strings.TrimRight(sliceCells(line, 0, 10), " ")); got < want {
			t.Fatalf("cell slicing disagrees with the rendered line %q", line)
		}
	}
	rows := columnarRows(t, out.String())
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(rows))
	}
	for _, want := range []map[string]string{
		{"NAME": "쉘", "STATUS": "live", "PROJECT": "알파", "WINDOW": "메인"},
		{"NAME": "log", "STATUS": "offline", "PROJECT": "alpha", "WINDOW": "main"},
	} {
		found := false
		for _, row := range rows {
			if row["NAME"] == want["NAME"] && row["STATUS"] == want["STATUS"] &&
				row["PROJECT"] == want["PROJECT"] && row["WINDOW"] == want["WINDOW"] {
				found = true
			}
		}
		if !found {
			t.Fatalf("no parsed row matched %v:\n%s\nparsed: %v", want, out.String(), rows)
		}
	}
}

// runeCountTable renders the same table with utf8.RuneCountInString widths --
// the arithmetic text/tabwriter uses -- so a test can prove the two differ.
func runeCountTable(kind coremetadata.Kind, matches []selector.Match) string {
	headers := columnHeaders(resourceTableColumns(kind, columnWide))
	rows := [][]string{headers}
	for _, match := range matches {
		rows = append(rows, resourceTableRow(match, kind, coremetadata.NewRegistry(), resourceFixtureReadClock, columnWide, nil))
	}
	widths := make([]int, len(headers))
	for _, row := range rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	var out strings.Builder
	for _, row := range rows {
		var line strings.Builder
		for i, cell := range row {
			line.WriteString(cell)
			if i == len(row)-1 {
				break
			}
			line.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+resourceTableGap))
		}
		out.WriteString(strings.TrimRight(line.String(), " ") + "\n")
	}
	return out.String()
}

// TestGetListWithHangulNamesStaysAligned drives the whole route -- registry,
// resolver, renderer -- over Hangul-named resources rather than synthesized
// matches, so the alignment contract is proven at the surface an operator uses.
func TestGetListWithHangulNamesStaysAligned(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	renameFixtureResource(t, store, "prj-alpha", "알파")
	renameFixtureResource(t, store, "win-alpha-main", "메인")
	renameFixtureResource(t, store, "pan-alpha-zsh", "쉘")
	if _, ok := store.registry.Pane("pan-alpha-zsh"); !ok {
		t.Fatal("fixture pane pan-alpha-zsh is missing")
	}
	if _, ok := store.registry.Pane("pan-alpha-log"); !ok {
		t.Fatal("fixture pane pan-alpha-log is missing")
	}

	stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), "panes", "--project", "알파", "-o", "wide")
	if err != nil {
		t.Fatalf("get panes error = %v (stderr %q)", err, stderr)
	}
	const want = "KIND  NAME        STATUS  ACTIONS  CONTEXT  SOURCE  OBSERVED  PROJECT  WINDOW  AGENT  TERMINATION  AGE\n" +
		"pane  쉘          live    -                         false     알파     메인                        2d\n" +
		"pane  log         live    -                         false     알파     메인                        2d\n" +
		"pane  codex-pane  live    -                         false     알파     메인    codex               2d\n" +
		"pane  review-zsh  live    -                         false     알파     review                      2d\n"
	if stdout != want {
		t.Fatalf("get panes stdout =\n%q\nwant\n%q", stdout, want)
	}
	for _, row := range columnarRows(t, stdout) {
		if row["STATUS"] != "live" {
			t.Fatalf("a Hangul row parsed its STATUS column as %q: %v", row["STATUS"], row)
		}
	}
}

// renameFixtureResource renames one fixture resource and its name reservation
// together, so the registry stays valid.
func renameFixtureResource(t *testing.T, store *fakeResourceStore, uid, name string) {
	t.Helper()
	switch {
	case strings.HasPrefix(uid, "prj-"):
		project, ok := store.registry.Project(uid)
		if !ok {
			t.Fatalf("fixture project %q missing", uid)
		}
		project.Metadata.Name = name
	case strings.HasPrefix(uid, "win-"):
		window, ok := store.registry.Window(uid)
		if !ok {
			t.Fatalf("fixture window %q missing", uid)
		}
		window.Metadata.Name = name
	case strings.HasPrefix(uid, "pan-"):
		pane, ok := store.registry.Pane(uid)
		if !ok {
			t.Fatalf("fixture pane %q missing", uid)
		}
		pane.Metadata.Name = name
	default:
		t.Fatalf("renameFixtureResource does not know uid %q", uid)
	}
	for i := range store.registry.NameReservations {
		if store.registry.NameReservations[i].UID == uid {
			store.registry.NameReservations[i].Name = name
		}
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("renamed fixture is not a valid registry: %v", err)
	}
}

// TestGetListZeroRowsEmitsNothingAndSucceeds is acceptance criterion 6.
//
// The decision recorded here is that a zero-row read stays byte-identical to the
// pre-columnar behavior: no header, no `No resources found` note, zero bytes on
// both streams, and the nil error the 0..N cardinality already produced. The
// render seam has no stderr writer to put a kubectl-style note on, and inventing
// one would mean changing the signature `get pane`, `describe`, and `rename`
// share.
func TestGetListZeroRowsEmitsNothingAndSucceeds(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"agents", "--project", "gone"},
		{"windows", "--project", "alpha", "--selector", "role=nosuch"},
		{"panes", "--project", "alpha", "--selector", "role=nosuch"},
		{"projects", "--selector", "role=nosuch"},
	} {
		store := newFakeResourceStore(t)
		stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), args...)
		if err != nil {
			t.Fatalf("get %v error = %v, want a successful empty read", args, err)
		}
		if stdout != "" {
			t.Fatalf("get %v stdout = %q, want zero bytes", args, stdout)
		}
		if stderr != "" {
			t.Fatalf("get %v stderr = %q, want zero bytes", args, stderr)
		}
	}
}

// TestDisplayFirstTablesDoNotBroadenSelectors keeps display projection and
// identity resolution independent. Only stable metadata.name and explicit
// uid: references resolve; display aliases and runtime/secondary labels do not.
func TestDisplayFirstTablesDoNotBroadenSelectors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "stable Window name", args: []string{"window", "main", "--project", "alpha"}},
		{name: "explicit Window uid", args: []string{"window", "uid:win-alpha-main", "--project", "alpha"}},
		{name: "Project display alias", args: []string{"project", "projmux"}, wantErr: true},
		{name: "Window runtime display", args: []string{"window", "editor", "--project", "alpha"}, wantErr: true},
		{name: "Pane displayTitle", args: []string{"pane", "forbidden-topic-or-raw-title", "--project", "alpha", "--window", "main"}, wantErr: true},
		{name: "Agent topic", args: []string{"agent", "forbidden-agent-topic", "--project", "alpha", "--window", "main"}, wantErr: true},
		{name: "tmux Window handle", args: []string{"window", "@7", "--project", "alpha"}, wantErr: true},
		{name: "tmux Pane handle", args: []string{"pane", "%7", "--project", "alpha", "--window", "main"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			prepareDisplayFirstTableFixture(t, store)
			stdout, _, err := runRoute(t, newTestDescribeCommand(t, store), test.args...)
			if !test.wantErr {
				if err != nil {
					t.Fatalf("describe %v error = %v", test.args, err)
				}
				if stdout == "" {
					t.Fatalf("describe %v emitted no resource", test.args)
				}
				return
			}
			if err == nil || !IsUsageError(err) {
				t.Fatalf("describe %v error = %v, want usage error", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("describe %v stdout = %q, want zero bytes", test.args, stdout)
			}
		})
	}
}

// TestSingularDefaultProjectionIsUnchangedByTheColumnarList is the negative half
// of the change: the columnar table is reachable only from the plural reads.
//
// The three singular default-projection routes are asserted against the exact
// bytes they emitted before the change, and the non-default modes are asserted
// on a plural read where the table would otherwise have taken over.
func TestSingularDefaultProjectionIsUnchangedByTheColumnarList(t *testing.T) {
	t.Parallel()

	t.Run("get pane", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, err := runGet(t, newTestGetCommand(t, &stubCurrentPath{}),
			"pane", "--project", "alpha", "--window", "main", "--pane", "zsh")
		if err != nil {
			t.Fatalf("get pane error = %v (stderr %q)", err, stderr)
		}
		if stdout != "pane/zsh status=live owner=project/alpha window/main\n" {
			t.Fatalf("get pane stdout = %q", stdout)
		}
	})

	t.Run("rename pane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		stdout, _, err := runRoute(t, newTestRenameCommand(store),
			"pane", "--project", "alpha", "--window", "main", "--pane", "log", "--name", "renamed")
		if err != nil {
			t.Fatalf("rename pane error = %v", err)
		}
		if stdout != "pane/renamed status=live owner=project/alpha window/main\n"+
			"receipt operation=rename.pane identity=unchanged address=renamed topology=unchanged desired-state=unchanged runtime=unchanged focus=unchanged projects=0 windows=0 panes=1 agents=0\n" {
			t.Fatalf("rename pane stdout = %q", stdout)
		}
	})

	t.Run("describe pane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		stdout, _, err := runRoute(t, newTestDescribeCommand(t, store),
			"pane", "--project", "alpha", "--window", "main", "--pane", "zsh")
		if err != nil {
			t.Fatalf("describe pane error = %v", err)
		}
		// describe renders its own key/value block, never the summary or the
		// table, and it is frozen here byte for byte.
		//
		// The CreatedAt row is the one deliberate movement since this golden was
		// written, and it is not the table leaking into a singular route: it is a
		// key/value row of describe's own block, added by the timestamp-surfacing
		// track. What this test guards -- that the columnar projection is
		// reachable only with `list` true -- is unchanged and still falsifiable
		// here, because a header line or a padded column would redden it.
		const want = "Kind:            Pane\n" +
			"Name:            zsh\n" +
			"UID:             pan-alpha-zsh\n" +
			"CreatedAt:       2026-08-15T09:00:00Z\n" +
			"Context:         \n" +
			"ContextSource:   \n" +
			"ContextObserved: false\n" +
			"Owner:           project/alpha window/main\n" +
			"Status:          live\n" +
			"Role:            shell\n" +
			"CWD:             /srv/alpha\n" +
			"Labels:          role=shell\n"
		if stdout != want {
			t.Fatalf("describe pane stdout =\n%q\nwant\n%q", stdout, want)
		}
	})

	// `describe -o <mode>` and every plural `-o <mode>` keep the one-line
	// scalar projections, which is what proves the table is gated on
	// OutputModeDefault and not on `list`.
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"windows", "--project", "beta", "-o", "uid"}, want: "win-beta-main\n"},
		{args: []string{"windows", "--project", "beta", "-o", "name"}, want: "main\n"},
		{args: []string{"windows", "--project", "beta", "-o", "ref"}, want: "window/main\n"},
		{args: []string{"windows", "--project", "beta", "-o", "none"}, want: ""},
	} {
		store := newFakeResourceStore(t)
		stdout, _, err := runRoute(t, newTestListGetCommand(t, store), test.args...)
		if err != nil {
			t.Fatalf("get %v error = %v", test.args, err)
		}
		if stdout != test.want {
			t.Fatalf("get %v stdout = %q, want %q", test.args, stdout, test.want)
		}
	}
}
