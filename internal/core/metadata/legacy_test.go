package metadata

import (
	"reflect"
	"slices"
	"testing"
)

func TestLegacyImportBuildsResourcesAndMarksManagedWindowsForAutomaticRenameOff(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()

	result, err := m.ImportLegacySession(&reg, LegacySession{
		Session: "projmux",
		Root:    "/src/projmux",
		Windows: []LegacyWindow{
			{
				Name:            "editor",
				AutomaticRename: false,
				Panes: []LegacyPane{
					{Label: "nvim", Command: "nvim", CWD: "/src/projmux"},
					{Provider: "codex", LaunchAuthorship: "1", Topic: "refactor naming", Command: "codex", CWD: "/src/projmux"},
				},
			},
			{
				Name:            "zsh",
				AutomaticRename: true,
				Panes:           []LegacyPane{{Command: "zsh", Title: "~/src/projmux", CWD: "/src/projmux"}},
			},
		},
	}, "/bin/zsh", "op-import", nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("imported registry is invalid: %v", err)
	}

	if result.Project.Metadata.Name != result.Project.Metadata.UID {
		t.Fatalf("project name = %q, want exact uid %q", result.Project.Metadata.Name, result.Project.Metadata.UID)
	}
	if result.Project.Status.Session == nil || result.Project.Status.Session.Name != "projmux" {
		t.Fatalf("session projection = %+v", result.Project.Status.Session)
	}

	var windowNames []string
	for _, window := range result.Windows {
		windowNames = append(windowNames, window.Name)
		if !window.NeedsAutomaticRenameOff {
			t.Fatalf("managed window %q must be marked for automatic-rename off", window.Name)
		}
	}
	for i, window := range result.Windows {
		if window.Name != window.UID {
			t.Fatalf("window %d name = %q, want exact uid %q", i, window.Name, window.UID)
		}
	}

	var paneNames []string
	for _, pane := range result.Panes {
		paneNames = append(paneNames, pane.Name)
	}
	for i, pane := range result.Panes {
		if pane.Name != pane.UID {
			t.Fatalf("pane %d name = %q, want exact uid %q", i, pane.Name, pane.UID)
		}
	}

	if len(result.Agents) != 1 || result.Agents[0].Name != result.Agents[0].UID {
		t.Fatalf("agents = %+v, want one exact-uid named agent", result.Agents)
	}
	agent, _ := reg.Agent(result.Agents[0].UID)
	if agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != result.Agents[0].PaneUID {
		t.Fatalf("imported agent status = %+v", agent.Status)
	}
	if agent.Metadata.Annotations[AnnotationAgentTopic] != "refactor naming" {
		t.Fatalf("topic must land in annotations, not in the name: %+v", agent.Metadata.Annotations)
	}

	// Runtime topic/title observations are not persisted in Registry identity.
	managed, _ := reg.Pane(result.Agents[0].PaneUID)
	if managed.Metadata.Name != managed.Metadata.UID {
		t.Fatalf("managed pane name = %q, want exact uid %q", managed.Metadata.Name, managed.Metadata.UID)
	}
}

func TestLegacyReobservationPreservesExplicitNameWithoutStoredPresentation(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	first, err := m.ImportLegacySession(&reg, LegacySession{
		Session: "projmux", Root: "/src/projmux",
		Windows: []LegacyWindow{{Name: "runtime-before", Panes: []LegacyPane{{Command: "zsh"}}}},
	}, "/bin/zsh", "op-1", NewBindingMatcher(RuntimeObservation{}))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	windowUID := first.Windows[0].UID
	if _, err := m.RenameWindow(&reg, windowUID, "operator-chosen"); err != nil {
		t.Fatalf("explicit stable-name rename: %v", err)
	}
	before, _ := reg.Window(windowUID)
	wantUID := before.Metadata.UID
	wantName := before.Metadata.Name
	wantOwner := *before.Metadata.OwnerRef
	wantReservations := slices.Clone(reg.NameReservations)

	second, err := m.ImportLegacySession(&reg, LegacySession{
		Session: "projmux", Root: "/src/projmux",
		Windows: []LegacyWindow{{
			Name: "runtime-after", AutomaticRename: true,
			Panes: []LegacyPane{{Label: "lead-roadmap", Command: "fish"}},
		}}}, "/bin/zsh", "op-2", NewBindingMatcher(RuntimeObservation{}))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(second.Windows) != 1 || second.Windows[0].UID != wantUID || second.Windows[0].Origin != ImportAdopted {
		t.Fatalf("second import windows = %+v, want the existing Window adopted", second.Windows)
	}
	after, ok := reg.Window(windowUID)
	if !ok {
		t.Fatalf("Window %q was deleted or re-identified", windowUID)
	}
	if after.Metadata.UID != wantUID || after.Metadata.Name != wantName || after.Metadata.OwnerRef == nil || *after.Metadata.OwnerRef != wantOwner {
		t.Fatalf("existing Window identity changed: before=%+v after=%+v", before.Metadata, after.Metadata)
	}
	if !reflect.DeepEqual(reg.NameReservations, wantReservations) {
		t.Fatalf("runtime observation changed name reservations:\nbefore=%+v\nafter=%+v", wantReservations, reg.NameReservations)
	}
}

func TestLegacyImportsUseExactUIDNamesWithoutNumericSuffixes(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/a/projmux": true, "/b/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()

	agentWindow := LegacyWindow{
		Name:            "agents",
		AutomaticRename: false,
		Panes: []LegacyPane{
			{Provider: "codex", LaunchAuthorship: "1", Command: "codex"},
			{Provider: "codex", LaunchAuthorship: "1", Command: "codex"},
			{Provider: "claude", LaunchAuthorship: "1", Command: "claude"},
			{Provider: "mystery", LaunchAuthorship: "1", Command: "mystery"},
		},
	}

	first, err := m.ImportLegacySession(&reg, LegacySession{Session: "projmux", Root: "/a/projmux", Windows: []LegacyWindow{agentWindow}}, "/bin/zsh", "op-a", nil)
	if err != nil {
		t.Fatalf("import a: %v", err)
	}
	second, err := m.ImportLegacySession(&reg, LegacySession{Session: "projmux-b", Root: "/b/projmux", Windows: []LegacyWindow{agentWindow}}, "/bin/zsh", "op-b", nil)
	if err != nil {
		t.Fatalf("import b: %v", err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after duplicate imports: %v", err)
	}

	// Same-basename Projects remain distinct and every automatic name is its uid.
	if first.Project.Metadata.UID == second.Project.Metadata.UID {
		t.Fatal("duplicate project migration must not merge uids")
	}
	if first.Project.Metadata.Name != first.Project.Metadata.UID || second.Project.Metadata.Name != second.Project.Metadata.UID {
		t.Fatalf("project automatic names are not exact uids: %+v / %+v", first.Project.Metadata, second.Project.Metadata)
	}

	// Duplicate Agent migration: two codex agents in one window.
	var firstAgents []string
	for _, agent := range first.Agents {
		firstAgents = append(firstAgents, agent.Name)
	}
	for _, agent := range first.Agents {
		if agent.Name != agent.UID {
			t.Fatalf("agent name = %q, want exact uid %q", agent.Name, agent.UID)
		}
	}

	// The second root follows the same exact-uid rule.
	var secondAgents []string
	for _, agent := range second.Agents {
		secondAgents = append(secondAgents, agent.Name)
	}
	for _, agent := range second.Agents {
		if agent.Name != agent.UID {
			t.Fatalf("second-root agent name = %q, want exact uid %q", agent.Name, agent.UID)
		}
	}

	// Managed pane automatic names are exact pane uids.
	var paneNames []string
	for _, pane := range first.Panes {
		paneNames = append(paneNames, pane.Name)
	}
	for _, pane := range first.Panes {
		if pane.Name != pane.UID {
			t.Fatalf("managed pane name = %q, want exact uid %q", pane.Name, pane.UID)
		}
	}
}

func TestReimportingAnExactRootReusesTheProjectUIDWithoutDuplicatingTopology(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	legacy := LegacySession{
		Session: "projmux",
		Root:    "/src/projmux",
		Windows: []LegacyWindow{{Name: "editor", Panes: []LegacyPane{{Command: "nvim"}}}},
	}

	first, err := m.ImportLegacySession(&reg, legacy, "/bin/zsh", "op-1", nil)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	windowsAfterFirst := len(reg.Windows)

	// The second observation still reports no uid on the live window, which is
	// what a tmux server restart leaves behind. The Window is adopted, not
	// duplicated -- and, unlike the guard this replaced, it is *reported*, so
	// the caller rewrites its binding.
	second, err := m.ImportLegacySession(&reg, legacy, "/bin/zsh", "op-2", NewBindingMatcher(RuntimeObservation{}))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !second.ProjectReused {
		t.Fatal("re-importing an exact root must reuse the project")
	}
	if second.Project.Metadata.UID != first.Project.Metadata.UID {
		t.Fatalf("uid changed on re-import: %q -> %q", first.Project.Metadata.UID, second.Project.Metadata.UID)
	}
	if len(reg.Windows) != windowsAfterFirst {
		t.Fatalf("re-import duplicated topology: %d -> %d windows", windowsAfterFirst, len(reg.Windows))
	}
	if len(second.Windows) != 1 || second.Windows[0].Origin != ImportAdopted {
		t.Fatalf("second import windows = %+v, want one adopted Window", second.Windows)
	}
	if second.Windows[0].UID != first.Windows[0].UID {
		t.Fatalf("adoption re-identified the Window: %q -> %q", first.Windows[0].UID, second.Windows[0].UID)
	}
	if len(second.Panes) != 1 || second.Panes[0].Origin != ImportAdopted || second.Panes[0].UID != first.Panes[0].UID {
		t.Fatalf("second import panes = %+v, want the same Pane adopted", second.Panes)
	}
	// Adopted objects are not created objects: a rollback of the second import
	// must not delete resources the first one owns.
	if len(second.Created) != 0 {
		t.Fatalf("adoption recorded %v as created", second.Created)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after re-import: %v", err)
	}
}

// TestLegacyImportAdoptsWithinOneProjectAndNeverAcrossProjects is the negative
// case at the import seam.
//
// Both Projects own a Window named `zsh` -- which is the real registry's actual
// shape, nine times over -- and both live sessions report a window with no uid.
// A rule that matched on a name, or that ranged over every Window in the
// registry, would cross here. The structural rule cannot: the candidate set is
// the session's own Project, and the ordinal is taken inside it.
func TestLegacyImportAdoptsWithinOneProjectAndNeverAcrossProjects(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/alpha": true, "/src/beta": true}
	m := testMutator(roots)
	reg := NewRegistry()

	blank := LegacySession{
		Windows: []LegacyWindow{{Name: "zsh", Panes: []LegacyPane{{Command: "zsh"}}}},
	}
	alpha := blank
	alpha.Session, alpha.Root = "alpha", "/src/alpha"
	beta := blank
	beta.Session, beta.Root = "beta", "/src/beta"

	// First pass creates one Window per Project.
	binder := NewBindingMatcher(RuntimeObservation{})
	firstAlpha, err := m.ImportLegacySession(&reg, alpha, "/bin/zsh", "op-1", binder)
	if err != nil {
		t.Fatalf("import alpha: %v", err)
	}
	firstBeta, err := m.ImportLegacySession(&reg, beta, "/bin/zsh", "op-1", binder)
	if err != nil {
		t.Fatalf("import beta: %v", err)
	}
	if len(reg.Windows) != 2 {
		t.Fatalf("windows after the first pass = %d, want 2", len(reg.Windows))
	}

	// Second pass, still with every uid wiped off the machine. Each session
	// adopts its own Project's Window.
	second := NewBindingMatcher(RuntimeObservation{})
	secondAlpha, err := m.ImportLegacySession(&reg, alpha, "/bin/zsh", "op-2", second)
	if err != nil {
		t.Fatalf("re-import alpha: %v", err)
	}
	secondBeta, err := m.ImportLegacySession(&reg, beta, "/bin/zsh", "op-2", second)
	if err != nil {
		t.Fatalf("re-import beta: %v", err)
	}
	if len(reg.Windows) != 2 {
		t.Fatalf("windows after the second pass = %d, want 2; adoption duplicated topology", len(reg.Windows))
	}
	if secondAlpha.Windows[0].UID != firstAlpha.Windows[0].UID {
		t.Fatalf("alpha adopted %q, want its own %q", secondAlpha.Windows[0].UID, firstAlpha.Windows[0].UID)
	}
	if secondBeta.Windows[0].UID != firstBeta.Windows[0].UID {
		t.Fatalf("beta adopted %q, want its own %q", secondBeta.Windows[0].UID, firstBeta.Windows[0].UID)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after cross-project adoption pass: %v", err)
	}
}

// TestALiveWindowCarryingAForeignUIDIsNeverAdopted pins the refusal that keeps
// adoption from re-identifying anything, and the boundary of that refusal.
//
// A uid the registry has never heard of is not a blank, so no existing registry
// Window is ever pointed at it -- the unbound `existing` Window sitting right
// there is what makes that falsifiable. It is still imported, though, because
// projmux itself produces unknown uids: a reconcile rolled back by a pre-create
// hook refusal has already written its allocated uids onto tmux, and tmux
// options are not transactional. Refusing outright would leave those windows
// permanently unmanageable.
//
// A uid that *does* exist but belongs to another Project is the other case
// entirely, and it is refused outright: claiming it would take a binding that
// is genuinely somebody else's.
func TestALiveWindowCarryingAForeignUIDIsNeverAdopted(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/alpha": true, "/src/beta": true}
	m := testMutator(roots)
	reg := NewRegistry()

	// Seed one Window per Project, then wipe every binding off the machine.
	seed := func(session, root string) ImportResult {
		t.Helper()
		result, err := m.ImportLegacySession(&reg, LegacySession{
			Session: session, Root: root,
			Windows: []LegacyWindow{{Name: "existing", Panes: []LegacyPane{{Command: "zsh"}}}},
		}, "/bin/zsh", "op-seed", NewBindingMatcher(RuntimeObservation{}))
		if err != nil {
			t.Fatalf("seed %s: %v", session, err)
		}
		return result
	}
	alpha := seed("alpha", "/src/alpha")
	beta := seed("beta", "/src/beta")

	legacy := LegacySession{
		Session: "alpha",
		Root:    "/src/alpha",
		Windows: []LegacyWindow{
			{Name: "unknown", UID: "win-from-somewhere-else", Panes: []LegacyPane{{Command: "zsh"}}},
			{Name: "stolen", UID: beta.Windows[0].UID, Panes: []LegacyPane{{Command: "zsh"}}},
		},
	}
	result, err := m.ImportLegacySession(&reg, legacy, "/bin/zsh", "op-1", NewBindingMatcher(RuntimeObservation{}))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// The unknown uid minted a Window; it did not take alpha's unbound one.
	if len(result.Windows) != 1 {
		t.Fatalf("reported windows = %+v, want exactly one", result.Windows)
	}
	if result.Windows[0].Origin != ImportCreated {
		t.Fatalf("window origin = %q, want created", result.Windows[0].Origin)
	}
	if result.Windows[0].UID == alpha.Windows[0].UID {
		t.Fatalf("a foreign uid was re-identified onto the existing Window %q", alpha.Windows[0].UID)
	}
	// The uid another Project owns produced nothing at all: no rebind, no
	// adoption, no Window minted beside it, and none of its panes.
	if len(reg.WindowsOf(beta.Project.Metadata.UID)) != 1 {
		t.Fatalf("Project beta's topology changed: %+v", reg.WindowsOf(beta.Project.Metadata.UID))
	}
	if len(reg.WindowsOf(alpha.Project.Metadata.UID)) != 2 {
		t.Fatalf("Project alpha's windows = %d, want its own plus the minted one", len(reg.WindowsOf(alpha.Project.Metadata.UID)))
	}
	if len(result.Panes) != 1 {
		t.Fatalf("reported panes = %+v, want only the minted window's pane", result.Panes)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
}

// TestAnAdoptedWindowStillImportsAPaneItHasNoCandidateFor pins the cascade
// inside an adopted Window: adoption is per object, not per subtree.
func TestAnAdoptedWindowStillImportsAPaneItHasNoCandidateFor(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/alpha": true}
	m := testMutator(roots)
	reg := NewRegistry()

	oneP := LegacySession{
		Session: "alpha", Root: "/src/alpha",
		Windows: []LegacyWindow{{Name: "editor", Panes: []LegacyPane{{Command: "nvim"}}}},
	}
	first, err := m.ImportLegacySession(&reg, oneP, "/bin/zsh", "op-1", NewBindingMatcher(RuntimeObservation{}))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}

	// The operator split a second pane into the same window. The Window is
	// adopted; the extra pane has no candidate left and is imported.
	twoP := oneP
	twoP.Windows = []LegacyWindow{{Name: "editor", Panes: []LegacyPane{{Command: "nvim"}, {Command: "tail"}}}}
	second, err := m.ImportLegacySession(&reg, twoP, "/bin/zsh", "op-2", NewBindingMatcher(RuntimeObservation{}))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(reg.Windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(reg.Windows))
	}
	if len(second.Panes) != 2 {
		t.Fatalf("reported panes = %+v, want two", second.Panes)
	}
	if second.Panes[0].Origin != ImportAdopted || second.Panes[0].UID != first.Panes[0].UID {
		t.Fatalf("first pane = %+v, want the adopted original", second.Panes[0])
	}
	if second.Panes[1].Origin != ImportCreated {
		t.Fatalf("second pane = %+v, want a created Pane", second.Panes[1])
	}
	if want := []string{string(KindPane) + "/" + second.Panes[1].UID}; !equalStrings(second.Created, want) {
		t.Fatalf("created ledger = %v, want only the new Pane %v", second.Created, want)
	}
	// The adopted Window keeps the primary Pane it already named.
	window, _ := reg.Window(first.Windows[0].UID)
	if window.Spec.AnchorPaneRef != first.Panes[0].UID {
		t.Fatalf("anchorPaneRef = %q, want the original %q", window.Spec.AnchorPaneRef, first.Panes[0].UID)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
}
