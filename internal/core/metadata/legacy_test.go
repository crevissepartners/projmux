package metadata

import (
	"reflect"
	"slices"
	"testing"
)

func TestLegacyWindowNameSeedExcludesEveryRuntimeAttribute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		window LegacyWindow
	}{
		{
			name:   "automatic-rename off does not use window_name or pane label",
			window: LegacyWindow{Name: "build", AutomaticRename: false, Panes: []LegacyPane{{Label: "editor"}}},
		},
		{
			name:   "automatic-rename on does not use pane label",
			window: LegacyWindow{Name: "zsh", AutomaticRename: true, Panes: []LegacyPane{{Label: "editor", Provider: "codex", Command: "zsh"}}},
		},
		{
			name:   "provider is not a Window name input",
			window: LegacyWindow{Name: "codex", AutomaticRename: true, Panes: []LegacyPane{{Provider: "Codex", Topic: "refactor", Command: "node"}}},
		},
		{
			name:   "known shell is not a Window name input",
			window: LegacyWindow{Name: "vim", AutomaticRename: true, Panes: []LegacyPane{{Command: "node"}, {Command: "fish"}}},
		},
		{
			name:   "topic and raw title remain excluded",
			window: LegacyWindow{Name: "x", AutomaticRename: true, Panes: []LegacyPane{{Topic: "refactor naming", Title: "~/src/projmux", Command: "node"}}},
		},
		{
			name:   "empty observation uses the same literal base",
			window: LegacyWindow{Name: "", AutomaticRename: false, Panes: []LegacyPane{{Command: "bash"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := LegacyWindowNameSeed(tt.window); got != FallbackWindowNameBase {
				t.Fatalf("got %q, want the literal fallback %q", got, FallbackWindowNameBase)
			}
		})
	}
}

func TestLegacyPaneNameSeedUsesTheExistingPaneLabelFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		pane  LegacyPane
		shell string
		want  string
	}{
		{name: "existing pane label is the seed", pane: LegacyPane{Label: "logs", Command: "tail"}, shell: "/bin/zsh", want: "logs"},
		{name: "command basename is next", pane: LegacyPane{Command: "/usr/bin/tail -f"}, shell: "/bin/zsh", want: "tail"},
		{name: "configured shell basename is next", pane: LegacyPane{}, shell: "/bin/zsh", want: "zsh"},
		{name: "pane literal is the last resort", pane: LegacyPane{}, shell: "", want: "pane"},
		{name: "topic is never a pane name seed", pane: LegacyPane{Topic: "refactor"}, shell: "", want: "pane"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := LegacyPaneNameSeed(tt.pane, tt.shell); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

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
					{Provider: "codex", Topic: "refactor naming", Command: "codex", CWD: "/src/projmux"},
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

	if result.Project.Metadata.Name != "projmux" {
		t.Fatalf("project name = %q", result.Project.Metadata.Name)
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
	if !equalStrings(windowNames, []string{"window", "window-1"}) {
		t.Fatalf("window names = %v, want window/window-1", windowNames)
	}
	for i, want := range []string{"editor", "zsh"} {
		window, ok := reg.Window(result.Windows[i].UID)
		if !ok || window.Metadata.DisplayName != want {
			t.Fatalf("window %d displayName = %q, want observed window_name %q", i, window.Metadata.DisplayName, want)
		}
	}

	var paneNames []string
	for _, pane := range result.Panes {
		paneNames = append(paneNames, pane.Name)
	}
	if !equalStrings(paneNames, []string{"nvim", "codex-pane", "zsh"}) {
		t.Fatalf("pane names = %v, want nvim/codex-pane/zsh", paneNames)
	}

	if len(result.Agents) != 1 || result.Agents[0].Name != "codex" {
		t.Fatalf("agents = %+v, want one codex agent", result.Agents)
	}
	agent, _ := reg.Agent(result.Agents[0].UID)
	if agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != result.Agents[0].PaneUID {
		t.Fatalf("imported agent status = %+v", agent.Status)
	}
	if agent.Metadata.Annotations[AnnotationAgentTopic] != "refactor naming" {
		t.Fatalf("topic must land in annotations, not in the name: %+v", agent.Metadata.Annotations)
	}

	// The derived display title is secondary; the pane name stays primary.
	managed, _ := reg.Pane(result.Agents[0].PaneUID)
	if managed.Metadata.Name != "codex-pane" || managed.Status.DisplayTitle != "refactor naming" {
		t.Fatalf("managed pane = %q / %q", managed.Metadata.Name, managed.Status.DisplayTitle)
	}
	shellPane, _ := reg.Pane(result.Panes[2].UID)
	if shellPane.Status.DisplayTitle != "zsh" {
		t.Fatalf("shell pane display title = %q, want zsh", shellPane.Status.DisplayTitle)
	}
}

func TestLegacyImportProjectsDisplayNameWithoutRenamingAnExistingWindow(t *testing.T) {
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
	if after.Metadata.DisplayName != "runtime-after" {
		t.Fatalf("displayName = %q, want the observed runtime name", after.Metadata.DisplayName)
	}
	if !reflect.DeepEqual(reg.NameReservations, wantReservations) {
		t.Fatalf("display projection changed name reservations:\nbefore=%+v\nafter=%+v", wantReservations, reg.NameReservations)
	}
}

func TestDuplicateLegacyProjectAndAgentImportsGetTheLowestFreeSuffix(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/a/projmux": true, "/b/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()

	agentWindow := LegacyWindow{
		Name:            "agents",
		AutomaticRename: false,
		Panes: []LegacyPane{
			{Provider: "codex", Command: "codex"},
			{Provider: "codex", Command: "codex"},
			{Provider: "claude", Command: "claude"},
			{Provider: "mystery", Command: "mystery"},
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

	// Duplicate Project migration: same basename, different roots, distinct
	// uids and suffixed names.
	if first.Project.Metadata.UID == second.Project.Metadata.UID {
		t.Fatal("duplicate project migration must not merge uids")
	}
	if first.Project.Metadata.Name != "projmux" || second.Project.Metadata.Name != "projmux-1" {
		t.Fatalf("project names = %q/%q", first.Project.Metadata.Name, second.Project.Metadata.Name)
	}
	if first.Project.Metadata.DisplayName != second.Project.Metadata.DisplayName {
		t.Fatalf("display names must be allowed to duplicate: %q vs %q", first.Project.Metadata.DisplayName, second.Project.Metadata.DisplayName)
	}

	// Duplicate Agent migration: two codex agents in one window.
	var firstAgents []string
	for _, agent := range first.Agents {
		firstAgents = append(firstAgents, agent.Name)
	}
	if !equalStrings(firstAgents, []string{"codex", "codex-1", "claude", "agent"}) {
		t.Fatalf("agent names = %v, want codex/codex-1/claude/agent", firstAgents)
	}

	// Agent scope is the owning window, so the second project's window
	// restarts the suffix sequence.
	var secondAgents []string
	for _, agent := range second.Agents {
		secondAgents = append(secondAgents, agent.Name)
	}
	if !equalStrings(secondAgents, []string{"codex", "codex-1", "claude", "agent"}) {
		t.Fatalf("second window agent names = %v", secondAgents)
	}

	// Managed pane names follow their agent names.
	var paneNames []string
	for _, pane := range first.Panes {
		paneNames = append(paneNames, pane.Name)
	}
	if !equalStrings(paneNames, []string{"codex-pane", "codex-1-pane", "claude-pane", "agent-pane"}) {
		t.Fatalf("managed pane names = %v", paneNames)
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
	if window.Spec.PrimaryPaneRef != first.Panes[0].UID {
		t.Fatalf("primaryPaneRef = %q, want the original %q", window.Spec.PrimaryPaneRef, first.Panes[0].UID)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
}
