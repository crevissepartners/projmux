package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The tmux call budget of one `projmux create window` transaction.
//
// The fixture reproduces the measured live shape: an app-owned server reached
// through an inherited TMUX receipt, the target Project's session, the Home
// ControlSession, and N other registered Project sessions whose tmux mirrors
// already match the Registry. The command is wired through newCreateCommandOn,
// so the route bind, the reconciler, and the typed metadata mirror are the
// production ones; only the Registry store, workdir discovery, and the
// supervisor binary are test seams.
//
// Caps are pinned at the fake-runner measurement. A change that adds a tmux
// call to this transaction fails here with the count, the cap, the overage, and
// a per-command breakdown.
//
// Measured on the same fixture before the identity re-proof work (main
// 5081a399): 225 calls and 112 identity reads with one other Project session,
// 345 and 208 with three, so 60 calls per extra session. The live trace of the
// same shape was 221 calls and 112 identity reads.
const (
	// createWindowCallBudget is the whole transaction with one other Project
	// session open.
	createWindowCallBudget = 115
	// createWindowIdentityBudget counts #{socket_path}, #{pid}, @projmux_app and
	// the logical socket-name marker reads with one other Project session open.
	createWindowIdentityBudget = 42
	// createWindowPerSessionBudget is what one more converged, registered
	// Project session open on the server may add to the whole create: both
	// reconcile passes together.
	createWindowPerSessionBudget = 2
)

const callBudgetSocket = "/tmp/fake-tmux/default"

type callBudgetFixture struct {
	create *createCommand
	store  *fakeResourceStore
	tmux   *fakeTmux
}

// addCallBudgetProject registers one Project with one Window and one shell Pane
// and, when live, opens its session with every mirror already converged.
func addCallBudgetProject(registry *coremetadata.Registry, tmux *fakeTmux, dirs map[string]bool, name string) {
	projectUID, windowUID, paneUID := "prj-"+name, "win-"+name+"-main", "pan-"+name+"-shell"
	root := "/srv/" + name
	dirs[root] = true
	registry.Projects = append(registry.Projects, coremetadata.Project{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: name, CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: root},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: name, Live: true}},
	})
	registry.NameReservations = append(registry.NameReservations,
		coremetadata.NameReservation{Scope: "", Kind: coremetadata.KindProject, Name: name, UID: projectUID})
	addFixtureCanonicalShell(registry, projectUID, windowUID, paneUID, root)

	session := tmux.addSession(name)
	session.opts[tmuxopts.ProjectUIDSession] = projectUID
	session.opts[tmuxopts.ProjectNameSession] = name
	session.opts[tmuxopts.ProjectPathSession] = root
	window := session.windows[0]
	window.name = "main"
	window.opts[tmuxopts.WindowUID] = windowUID
	window.opts[tmuxopts.WindowName] = "main"
	window.opts[tmuxopts.AutomaticRenameWindow] = "off"
	pane := window.panes[0]
	pane.opts[tmuxopts.PaneUID] = paneUID
	pane.opts[tmuxopts.PaneName] = "shell"
	pane.opts[tmuxopts.PaneOwnerKind] = string(coremetadata.KindWindow)
	pane.opts[tmuxopts.PaneOwnerUID] = windowUID
	pane.opts[tmuxopts.PaneRole] = string(coremetadata.PaneRoleShell)
}

// addCallBudgetHome registers the Home ControlSession and opens its session.
func addCallBudgetHome(registry *coremetadata.Registry, tmux *fakeTmux) {
	const controlUID, windowUID, paneUID = "ctl-home", "win-home-control", "pan-home-control"
	owner := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry.ControlSessions = append(registry.ControlSessions, coremetadata.ControlSession{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindControlSession,
		Metadata: coremetadata.ObjectMeta{UID: controlUID, Name: "home", CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ControlSessionSpec{Session: "home"},
	})
	registry.Windows = append(registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "control", OwnerRef: owner(coremetadata.KindControlSession, controlUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: paneUID},
	})
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "zsh", OwnerRef: owner(coremetadata.KindWindow, windowUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv"},
	})
	registry.NameReservations = append(registry.NameReservations,
		coremetadata.NameReservation{Scope: "", Kind: coremetadata.KindControlSession, Name: "home", UID: controlUID},
		coremetadata.NameReservation{Scope: controlUID, Kind: coremetadata.KindWindow, Name: "control", UID: windowUID},
		coremetadata.NameReservation{Scope: controlUID, Kind: coremetadata.KindPane, Name: "zsh", UID: paneUID},
	)
	session := tmux.addSession("home")
	session.opts[tmuxopts.SessionRole] = resourcegraph.ControlSessionRole
	session.windows[0].opts[tmuxopts.WindowUID] = windowUID
	session.windows[0].panes[0].opts[tmuxopts.PaneUID] = paneUID
}

// newCallBudgetFixture builds the measured server with others converged
// non-target Project sessions open next to the target and Home.
func newCallBudgetFixture(t *testing.T, others int) callBudgetFixture {
	t.Helper()
	return newCallBudgetFixtureWith(t, others, false)
}

// newCallBudgetFixtureWith is newCallBudgetFixture whose first other Project
// session, when staleMirror is set, lacks its Project-name mirror, so each
// create's first reconcile pass has one typed metadata mirror write to make.
func newCallBudgetFixtureWith(t *testing.T, others int, staleMirror bool) callBudgetFixture {
	t.Helper()
	tmux := newFakeTmux()
	tmux.socketPath = callBudgetSocket
	registry := coremetadata.NewRegistry()
	store := &fakeResourceStore{dirs: map[string]bool{"/srv": true}, now: resourceFixtureClock}
	addCallBudgetHome(&registry, tmux)
	addCallBudgetProject(&registry, tmux, store.dirs, "target")
	for i := 1; i <= others; i++ {
		addCallBudgetProject(&registry, tmux, store.dirs, fmt.Sprintf("other%d", i))
	}
	if staleMirror && others > 0 {
		delete(tmux.session("other1").opts, tmuxopts.ProjectNameSession)
	}
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("call budget fixture is not a valid Registry: %v", err)
	}
	store.registry = registry

	lookupEnv := func(key string) string {
		switch key {
		case "TMUX":
			return callBudgetSocket + "," + tmux.serverPID + ",0"
		case "TMUX_PANE":
			return "%1"
		case "SHELL":
			return "/bin/zsh"
		}
		return ""
	}
	create := newCreateCommandOn(tmux, lookupEnv)
	create.store = store.store()
	create.runtime.warn = testWarnWriter{t}
	create.runtime.executable = func() (string, error) { return testSupervisorBinary, nil }
	create.sessionNameFor = filepath.Base
	create.newGeneration = testGenerationSequence()
	create.newOperationID = func() (string, error) { return "op-test", nil }
	create.now = func() time.Time { return testCreateOperationClock }
	create.homeDir = func() (string, error) { return t.TempDir(), nil }
	// The production bind rebuilds the reconciler; keep it off this machine's
	// real workdir discovery and home-derived session names.
	for _, binder := range []*func(context.Context) error{&create.bindRuntime, &create.bindExplicitRuntime} {
		bind := *binder
		*binder = func(ctx context.Context) error {
			if err := bind(ctx); err != nil {
				return err
			}
			create.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
			create.reconciler.sessionNameFor = filepath.Base
			return nil
		}
	}
	return callBudgetFixture{create: create, store: store, tmux: tmux}
}

type callBudgetMeasurement struct {
	calls, identity int
	breakdown       map[string]int
	trace           []string
}

func callBudgetCommandKey(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	key := argv[0]
	switch argv[0] {
	case "display-message":
		format := flagValue(argv, "-F")
		if flagValue(argv, "-t") == "" {
			return key + " " + format
		}
		return key + " -t " + format
	case "show-options":
		return key + " " + strings.Join(argv[1:], " ")
	case "list-windows", "list-panes", "list-sessions":
		if slices.Contains(argv, "-a") {
			return key + " -a " + flagValue(argv, "-F")
		}
		if flagValue(argv, "-t") != "" {
			return key + " -t " + flagValue(argv, "-F")
		}
		return key + " " + flagValue(argv, "-F")
	}
	return key
}

func measureCreateWindowCalls(t *testing.T, others int) callBudgetMeasurement {
	t.Helper()
	fixture := newCallBudgetFixture(t, others)
	start := len(fixture.tmux.calls)
	stdout, stderr, err := runRoute(t, fixture.create, "window", "--project", "uid:prj-target", "--name", "probe", "-o", "uid")
	if err != nil {
		t.Fatalf("create window with %d other Project sessions: %v (stderr %q)", others, err, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("create window printed no uid")
	}
	calls := fixture.tmux.calls[start:]
	measured := callBudgetMeasurement{calls: len(calls), identity: routeIdentityReads(calls), breakdown: map[string]int{}}
	for _, call := range calls {
		measured.breakdown[callBudgetCommandKey(tmuxCommandArgv(call))]++
		measured.trace = append(measured.trace, strings.Join(call, " "))
	}
	return measured
}

func (m callBudgetMeasurement) report() string {
	keys := make([]string, 0, len(m.breakdown))
	for key := range m.breakdown {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if m.breakdown[a] != m.breakdown[b] {
			return m.breakdown[b] - m.breakdown[a]
		}
		return strings.Compare(a, b)
	})
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "  %4d  %s\n", m.breakdown[key], key)
	}
	return b.String()
}

func (m callBudgetMeasurement) numberedTrace() string {
	var b strings.Builder
	for i, line := range m.trace {
		fmt.Fprintf(&b, "  %3d  %s\n", i+1, line)
	}
	return b.String()
}

func TestCreateWindowStaysWithinItsTmuxCallBudget(t *testing.T) {
	t.Parallel()

	one := measureCreateWindowCalls(t, 1)
	three := measureCreateWindowCalls(t, 3)
	perSession := (three.calls - one.calls) / 2
	t.Logf("create window: total=%d identity=%d (N=1); total=%d identity=%d (N=3); per extra Project session=%d",
		one.calls, one.identity, three.calls, three.identity, perSession)
	if testing.Verbose() {
		t.Logf("N=1 breakdown:\n%s\nN=1 trace:\n%s", one.report(), one.numberedTrace())
	}

	over := func(name string, got, budget int, m callBudgetMeasurement) {
		t.Helper()
		if got > budget {
			t.Errorf("%s = %d, cap %d, over by %d\nper-command breakdown:\n%s", name, got, budget, got-budget, m.report())
		}
	}
	over("create window tmux calls (N=1)", one.calls, createWindowCallBudget, one)
	over("create window identity reads (N=1)", one.identity, createWindowIdentityBudget, one)
	over("create window tmux calls added per extra Project session", perSession, createWindowPerSessionBudget, three)
	if (three.calls-one.calls)%2 != 0 {
		t.Errorf("N=3 minus N=1 = %d calls is not an even per-session cost\nN=3 breakdown:\n%s", three.calls-one.calls, three.report())
	}
}
