package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestColumnCatalogCompletenessBijectionAndExactProfiles(t *testing.T) {
	t.Parallel()
	matrix := []struct {
		surface             columnSurface
		kind, compact, wide string
	}{
		{columnResourceCLI, "Project", "KIND NAME STATUS ACTIONS", "KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED AGE"},
		{columnResourceCLI, "Window", "KIND NAME STATUS ACTIONS", "KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED PROJECT AGE"},
		{columnResourceCLI, "Pane", "KIND NAME STATUS ACTIONS", "KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED PROJECT WINDOW AGENT TERMINATION AGE"},
		{columnResourceCLI, "Agent", "KIND NAME STATUS ACTIONS", "KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED INTERACTION PROJECT WINDOW SESSION TERMINATION AGE"},
		{columnRegistryPicker, "", "KIND NAME STATUS ACTIONS", "KIND NAME STATUS PROGRESS TERMINATION ACTIONS RUNTIME UID"},
		{columnRuntimeCLI, "session", "SESSION NAME CLASS", "SESSION NAME CLASS UID RESOURCE REASON"},
		{columnRuntimeCLI, "window", "WINDOW SESSION NAME CLASS", "WINDOW SESSION NAME CLASS UID RESOURCE REASON"},
		{columnRuntimeCLI, "pane", "PANE WINDOW TITLE CLASS", "PANE WINDOW TITLE CLASS UID RESOURCE REASON"},
		{columnRuntimePicker, "", "KIND ID IN NAME CLASS", "KIND ID IN NAME CLASS RESOURCE REASON"},
	}
	if len(columnCatalog) != len(matrix) {
		t.Fatalf("catalog surfaces = %d, want %d", len(columnCatalog), len(matrix))
	}
	// Compare the typed ID declarations to their actual capabilities. A new ID
	// with no consumer, a duplicate ID, or an untyped literal fails this audit.
	syntax, err := parser.ParseFile(token.NewFileSet(), "column_profiles.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[columnField]bool{}
	ast.Inspect(syntax, func(n ast.Node) bool {
		value, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		typ, ok := value.Type.(*ast.Ident)
		if !ok || typ.Name != "columnField" {
			return true
		}
		for _, expr := range value.Values {
			literal, ok := expr.(*ast.BasicLit)
			if !ok {
				t.Fatal("field ID must be a typed literal")
			}
			id, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if declared[columnField(id)] {
				t.Fatalf("duplicate field ID %q", id)
			}
			declared[columnField(id)] = true
		}
		return true
	})
	used := map[columnField]bool{}
	for _, entry := range matrix {
		wide := columnsFor(entry.surface, entry.kind, columnWide)
		compact := columnsFor(entry.surface, entry.kind, columnCompact)
		for _, profile := range []struct {
			name    columnProfile
			columns []columnSpec
			want    string
		}{{columnCompact, compact, entry.compact}, {columnWide, wide, entry.wide}} {
			if got := strings.Join(columnHeaders(profile.columns), " "); got != profile.want {
				t.Errorf("%s/%s/%s = %q, want %q", entry.surface, entry.kind, profile.name, got, profile.want)
			}
			seen := map[columnField]bool{}
			for _, column := range profile.columns {
				if !declared[column.field] || seen[column.field] {
					t.Fatalf("%s/%s/%s orphan or duplicate %q", entry.surface, entry.kind, profile.name, column.field)
				}
				seen[column.field], used[column.field] = true, true
			}
		}
		next := 0
		for _, column := range wide {
			if next < len(compact) && compact[next] == column {
				next++
			}
		}
		if next != len(compact) {
			t.Fatalf("%s/%s compact is not an ordered wide subset", entry.surface, entry.kind)
		}
		// Callers must not be able to mutate the catalog through a selected profile.
		wide[0].header = "mutated"
		if columnsFor(entry.surface, entry.kind, columnWide)[0].header == "mutated" {
			t.Fatal("mutable catalog escaped")
		}
	}
	if !reflect.DeepEqual(declared, used) {
		t.Fatalf("field declaration/capability bijection failed: declared=%v used=%v", declared, used)
	}
	if columnsFor(columnResourceCLI, "missing", columnCompact) != nil || columnsFor(columnResourceCLI, "Project", "auto") != nil {
		t.Fatal("undeclared surface/profile accepted")
	}
}

func TestColumnConsumersHaveNoIndependentHeaderLiteralAuthority(t *testing.T) {
	t.Parallel()
	for _, filename := range []string{"resource_routes.go", "registry_navigation_view.go", "runtime_diagnostics.go", "runtime_diagnostics_view.go"} {
		syntax, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(syntax, func(n ast.Node) bool {
			literal, ok := n.(*ast.CompositeLit)
			if !ok || len(literal.Elts) < 3 {
				return true
			}
			headers := 0
			for _, elt := range literal.Elts {
				value, ok := elt.(*ast.BasicLit)
				if !ok || value.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(value.Value)
				if err != nil {
					t.Fatal(err)
				}
				if text != "" && strings.Trim(text, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" {
					headers++
				}
			}
			if headers >= 3 {
				t.Errorf("%s retains an independent header literal at %d", filename, literal.Pos())
			}
			return true
		})
	}
}

// columnFixture runs the production four-query observer over a deterministic
// Registry with live, offline, missing-root and ControlSession-owned resources.
func columnFixture(t *testing.T) (*getCommand, *fakeResourceStore, *fakeTmux, *fakeTmux, resourcegraph.Graph) {
	t.Helper()
	store := newFakeResourceStore(t)
	prepareDisplayFirstTableFixture(t, store)
	setFixtureSessionRef(t, store, "agt-alpha-codex", &coremetadata.AgentSessionRef{
		Provider: "codex", ObservedAt: sessionRefObservedAt,
		Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-" + strings.Repeat("long-value-", 12), SessionID: "session-original"},
	})
	store.registry.Agents[0].Metadata.Annotations[coremetadata.AnnotationAgentTopic] = "릴리스 검토 " + strings.Repeat("full context ", 12)
	meta := func(uid, name string, kind coremetadata.Kind, owner string) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, CreatedAt: resourceFixtureClock, OwnerRef: &coremetadata.OwnerRef{Kind: kind, UID: owner}}
	}
	store.registry.ControlSessions = []coremetadata.ControlSession{{APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindControlSession, Metadata: coremetadata.ObjectMeta{UID: "ctl-home", Name: "home", CreatedAt: resourceFixtureClock}, Spec: coremetadata.ControlSessionSpec{Session: "Home"}}}
	store.registry.Windows = append(store.registry.Windows, coremetadata.Window{APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow, Metadata: meta("win-home", "home-window", coremetadata.KindControlSession, "ctl-home"), Spec: coremetadata.WindowSpec{AnchorPaneRef: "pan-home"}})
	store.registry.Panes = append(store.registry.Panes, coremetadata.Pane{APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane, Metadata: meta("pan-home", "home-shell", coremetadata.KindWindow, "win-home"), Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, Command: "/bin/zsh"}})
	store.registry.NameReservations = append(store.registry.NameReservations,
		coremetadata.NameReservation{Kind: coremetadata.KindControlSession, Name: "home", UID: "ctl-home"},
		coremetadata.NameReservation{Scope: "ctl-home", Kind: coremetadata.KindWindow, Name: "home-window", UID: "win-home"},
		coremetadata.NameReservation{Scope: "ctl-home", Kind: coremetadata.KindPane, Name: "home-shell", UID: "pan-home"})
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	primary := newFakeTmux()
	primary.appMarker = "1"
	primary.socketPath = "/tmp/column-fixture/primary"
	alpha := primary.addSession("alpha")
	alpha.opts[tmuxopts.ProjectUIDSession] = "prj-alpha"
	alpha.opts[tmuxopts.ProjectNameSession] = "alpha"
	alpha.windows[0].opts[tmuxopts.WindowUID] = "win-alpha-main"
	alpha.windows[0].name = "live editor " + strings.Repeat("창", 60)
	alpha.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-alpha-zsh"
	alpha.windows[0].panes[0].title = "live shell " + strings.Repeat("full title ", 12)
	agentPane := newFakeTmuxPane(primary.mint("%"))
	agentPane.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
	alpha.windows[0].panes = append(alpha.windows[0].panes, agentPane)
	home := primary.addSession("Home")
	home.opts[tmuxopts.SessionRole] = resourcegraph.ControlSessionRole
	home.windows[0].opts[tmuxopts.WindowUID] = "win-home"
	home.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-home"
	sibling := newFakeTmux()
	sibling.addSession("untouched")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": primary, "-L\x00sibling": sibling}}
	reader := &runtimeDiagnosticsReader{runner: runner, loadRegistry: store.store().load}
	reader.observe = func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
		return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
	}
	// readSnapshot normally inherits TMUX; make that transport explicit in the
	// fixture without putting a real caller socket into the test environment.
	reader.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/column-fixture/primary,1,0"
		}
		return ""
	}
	runner.servers["-S\x00/tmp/column-fixture/primary"] = primary
	transport, err := reader.transport(runtimeTransportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	graph := resourcegraph.Resolve(store.registry, reader.observe(context.Background(), transport))
	primary.calls = nil
	command := newTestListGetCommand(t, store)
	command.reads = runtimeResourceReadLookup(reader)
	command.runtimeDiag = reader
	return command, store, primary, sibling, graph
}

func TestColumnProfilesExactOutputAndSnapshotParity(t *testing.T) {
	var golden strings.Builder
	for _, kind := range []string{"projects", "windows", "panes", "agents"} {
		for _, mode := range []string{"default", "wide", "json", "metadata", "uid", "name", "ref", "none"} {
			command, store, primary, sibling, graph := columnFixture(t)
			before, _ := json.Marshal(store.registry)
			args := []string{kind}
			if kind != "projects" {
				args = append(args, "-A")
			}
			if mode != "default" {
				args = append(args, "-o", mode)
			}
			stdout, stderr, err := runRoute(t, command, args...)
			if err != nil || stderr != "" {
				t.Fatalf("%v: %v stderr=%q", args, err, stderr)
			}
			assertColumnReadCounters(t, store, primary, sibling, before)
			fmt.Fprintf(&golden, "== registry %s %s ==\n", kind, mode)
			if mode == "default" || mode == "wide" {
				golden.WriteString(stdout)
				rows := columnarRows(t, stdout)
				navigation := map[string]registryview.Row{}
				for _, row := range registryview.Build(registryview.Input{Graph: graph}).Rows {
					navigation[row.UID] = row
				}
				names, _, err := runRoute(t, command, append([]string{kind, "-o", "uid"}, func() []string {
					if kind != "projects" {
						return []string{"-A"}
					}
					return nil
				}()...)...)
				if err != nil {
					t.Fatal(err)
				}
				ids := strings.Fields(names)
				if len(rows) != len(ids) {
					t.Fatal("profile changed selector cardinality")
				}
				for i, row := range rows {
					want := runtimeCell(registryNavigationActionList(navigation[ids[i]]))
					fixed := map[string]string{"prj-alpha": "open,delete", "prj-beta": "start,delete", "prj-gone": "rebind,delete", "agt-alpha-codex": "open,delete", "pan-alpha-zsh": "open,delete", "pan-alpha-log": "start,delete", "win-home": "-", "pan-home": "-"}
					if expected, ok := fixed[ids[i]]; ok && want != expected {
						t.Fatalf("action fixture %s=%q want %q", ids[i], want, expected)
					}
					if row["ACTIONS"] != want {
						t.Fatalf("%s %s actions=%q want=%q", kind, ids[i], row["ACTIONS"], want)
					}
					if mode == "default" && len(strings.Fields(strings.Split(strings.TrimSpace(stdout), "\n")[i+1])) != 4 {
						t.Fatal("compact row is not four shell fields")
					}
				}
			} else {
				fmt.Fprintf(&golden, "sha256 %x\n", sha256.Sum256([]byte(stdout)))
			}
		}
	}
	for _, kind := range []string{"sessions", "windows", "panes"} {
		for _, mode := range []string{"default", "wide", "json", "none"} {
			command, store, primary, sibling, _ := columnFixture(t)
			before, _ := json.Marshal(store.registry)
			args := []string{kind, "--socket", "primary"}
			if mode != "default" {
				args = append(args, "-o", mode)
			}
			stdout, stderr, err := runGetRuntime(t, command, args...)
			if err != nil || stderr != "" {
				t.Fatalf("runtime %v: %v stderr=%q", args, err, stderr)
			}
			assertColumnReadCounters(t, store, primary, sibling, before)
			fmt.Fprintf(&golden, "== runtime %s %s ==\n", kind, mode)
			if mode == "json" || mode == "none" {
				fmt.Fprintf(&golden, "sha256 %x\n", sha256.Sum256([]byte(stdout)))
			} else {
				golden.WriteString(stdout)
			}
		}
	}
	_, _, _, _, graph := columnFixture(t)
	view := registryview.Build(registryview.Input{Graph: graph})
	registryPicker := registryNavigationView{locale: i18n.FallbackLocale, view: view, rows: view.Rows, now: resourceFixtureReadClock, profile: columnWide}
	runtimePicker := runtimeDiagnosticsView{locale: i18n.FallbackLocale, hostMode: string(graph.HostMode), transport: graph.Transport, rows: runtimediag.Rows(graph), profile: columnWide}
	for _, entry := range []struct {
		name  string
		value any
	}{{"registry picker wide", registryPicker.entries()}, {"runtime picker wide", runtimePicker.entries()}} {
		encoded, err := json.MarshalIndent(entry.value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&golden, "== %s ==\n%s\n", entry.name, encoded)
	}
	path := filepath.Join("testdata", "column-profiles.golden")
	if os.Getenv("UPDATE_COLUMNAR_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(golden.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if golden.String() != string(expected) {
		t.Fatalf("column profile exact output differs from %s", path)
	}
}

func assertColumnReadCounters(t *testing.T, store *fakeResourceStore, primary, sibling *fakeTmux, before []byte) {
	t.Helper()
	after, _ := json.Marshal(store.registry)
	if !bytes.Equal(before, after) || store.writes != 0 || store.transactions != 0 || store.reads != 1 {
		t.Fatalf("Registry read/write drift: reads=%d writes=%d transactions=%d", store.reads, store.writes, store.transactions)
	}
	var verbs []string
	for _, call := range primary.calls {
		if len(call) > 0 {
			verbs = append(verbs, call[0])
		}
	}
	if !slices.Equal(verbs, []string{"show-options", "list-sessions", "list-windows", "list-panes"}) || len(sibling.calls) != 0 {
		t.Fatalf("extra observation/write: primary=%v sibling=%v", primary.calls, sibling.calls)
	}
}

func TestColumnProfilesDoNotSelectFromWidthAndWideIsUnbounded(t *testing.T) {
	for _, width := range []string{"80", "120"} {
		t.Setenv("COLUMNS", width)
		command, _, _, _, _ := columnFixture(t)
		stdout, _, err := runRoute(t, command, "agents", "-A", "--output", "wide")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout, "codex:thread-"+strings.Repeat("long-value-", 12)) || !strings.Contains(stdout, "릴리스 검토 "+strings.TrimSpace(strings.Repeat("full context ", 12))) {
			t.Fatalf("width %s clipped wide values", width)
		}
		command, _, _, _, _ = columnFixture(t)
		stdout, _, err = runRoute(t, command, "agents", "-A")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(strings.Fields(strings.Split(stdout, "\n")[0]), []string{"KIND", "NAME", "STATUS", "ACTIONS"}) {
			t.Fatalf("width %s selected a different default", width)
		}
	}
}

func TestColumnActionsReuseUnavailableSnapshotAndEmptyListsStayEmpty(t *testing.T) {
	for _, mode := range []string{"default", "wide"} {
		command, store, primary, sibling, _ := columnFixture(t)
		command.runtimeDiag.lookupEnv = func(string) string { return "" }
		args := []string{"projects"}
		if mode == "wide" {
			args = append(args, "-o", mode)
		}
		stdout, stderr, err := runRoute(t, command, args...)
		if err != nil || stderr != "" {
			t.Fatalf("unavailable %s: %v stderr=%q", mode, err, stderr)
		}
		rows := columnarRows(t, stdout)
		for i, want := range []string{"start,delete", "start,delete", "rebind,delete"} {
			if rows[i]["ACTIONS"] != want {
				t.Fatalf("unavailable %s row %d actions=%q want=%q", mode, i, rows[i]["ACTIONS"], want)
			}
		}
		if store.reads != 1 || store.writes != 0 || store.transactions != 0 || len(primary.calls) != 0 || len(sibling.calls) != 0 {
			t.Fatal("unavailable action projection read or wrote a server")
		}
		command, store, primary, sibling, _ = columnFixture(t)
		before, _ := json.Marshal(store.registry)
		args = append(args, "--selector", "role=never-matches")
		stdout, stderr, err = runRoute(t, command, args...)
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("empty %s: stdout=%q stderr=%q err=%v", mode, stdout, stderr, err)
		}
		assertColumnReadCounters(t, store, primary, sibling, before)
	}
}
