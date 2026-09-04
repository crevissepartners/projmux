package selector

import (
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

var fixtureClock = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

// builder assembles a structurally legal registry for selector tests.
//
// It records the name reservations the resource model requires and derives each
// Window's anchor/default shell refs, then runs the real Registry.Validate. That keeps the
// fixtures honest: a selector test can never pass against a registry shape the
// production model would reject.
type builder struct {
	t        *testing.T
	registry metadata.Registry
}

func newBuilder(t *testing.T) *builder {
	t.Helper()
	return &builder{t: t, registry: metadata.NewRegistry()}
}

func (b *builder) reserve(scope string, kind metadata.Kind, name, uid string) {
	b.registry.NameReservations = append(b.registry.NameReservations, metadata.NameReservation{
		Scope: scope, Kind: kind, Name: name, UID: uid,
	})
}

func (b *builder) meta(uid, name, _ string, owner *metadata.OwnerRef, labels map[string]string) metadata.ObjectMeta {
	return metadata.ObjectMeta{
		UID:       uid,
		Name:      name,
		Labels:    labels,
		OwnerRef:  owner,
		CreatedAt: fixtureClock,
	}
}

// project adds a Project. session may be nil (never bound to a runtime).
func (b *builder) project(uid, name, displayName, root string, session *metadata.SessionProjection, missingRoot bool) {
	project := metadata.Project{
		APIVersion: metadata.APIVersion,
		Kind:       metadata.KindProject,
		Metadata:   b.meta(uid, name, displayName, nil, nil),
		Spec:       metadata.ProjectSpec{Root: root},
		Status:     metadata.ProjectStatus{Session: session},
	}
	if missingRoot {
		project.Status.Conditions = []metadata.Condition{{
			Type:             metadata.ConditionMissingRoot,
			Status:           metadata.ConditionTrue,
			Reason:           "RootDisappeared",
			FirstObservedAt:  fixtureClock,
			LastTransitionAt: fixtureClock,
		}}
	}
	b.registry.Projects = append(b.registry.Projects, project)
	b.reserve("", metadata.KindProject, name, uid)
}

func (b *builder) window(uid, name, projectUID string, labels map[string]string) {
	owner := &metadata.OwnerRef{Kind: metadata.KindProject, UID: projectUID}
	b.registry.Windows = append(b.registry.Windows, metadata.Window{
		APIVersion: metadata.APIVersion,
		Kind:       metadata.KindWindow,
		Metadata:   b.meta(uid, name, "", owner, labels),
	})
	b.reserve(projectUID, metadata.KindWindow, name, uid)
}

func (b *builder) rootForWindow(windowUID string) string {
	window, ok := b.registry.Window(windowUID)
	if !ok || window.Metadata.OwnerRef == nil {
		panic("fixture Window does not exist or has no root owner: " + windowUID)
	}
	return window.Metadata.OwnerRef.UID
}

func (b *builder) shellPane(uid, name, displayName, windowUID, cwd string, labels map[string]string) {
	owner := &metadata.OwnerRef{Kind: metadata.KindWindow, UID: windowUID}
	b.registry.Panes = append(b.registry.Panes, metadata.Pane{
		APIVersion: metadata.APIVersion,
		Kind:       metadata.KindPane,
		Metadata:   b.meta(uid, name, displayName, owner, labels),
		Spec:       metadata.PaneSpec{Role: metadata.PaneRoleShell, CWD: cwd, Command: name},
	})
	b.reserve(b.rootForWindow(windowUID), metadata.KindPane, name, uid)
}

func (b *builder) agentWithPane(agentUID, agentName, windowUID, paneUID, paneName string, labels map[string]string) {
	agentOwner := &metadata.OwnerRef{Kind: metadata.KindWindow, UID: windowUID}
	b.registry.Agents = append(b.registry.Agents, metadata.Agent{
		APIVersion: metadata.APIVersion,
		Kind:       metadata.KindAgent,
		Metadata:   b.meta(agentUID, agentName, "", agentOwner, nil),
		Spec:       metadata.AgentSpec{Provider: agentName},
		Status: metadata.AgentStatus{
			Phase:            metadata.PhaseRunning,
			PaneRef:          paneUID,
			LastTransitionAt: fixtureClock,
		},
	})
	rootUID := b.rootForWindow(windowUID)
	b.reserve(rootUID, metadata.KindAgent, agentName, agentUID)

	paneOwner := &metadata.OwnerRef{Kind: metadata.KindAgent, UID: agentUID}
	b.registry.Panes = append(b.registry.Panes, metadata.Pane{
		APIVersion: metadata.APIVersion,
		Kind:       metadata.KindPane,
		Metadata:   b.meta(paneUID, paneName, "", paneOwner, labels),
		Spec:       metadata.PaneSpec{Role: metadata.PaneRoleAgent},
	})
	b.reserve(rootUID, metadata.KindPane, paneName, paneUID)
}

// paneLessAgent adds an Agent that owns no managed Pane: the shape every
// released, pending, or failed Agent has, because the registry clears
// status.paneRef on every non-Running transition.
func (b *builder) paneLessAgent(agentUID, agentName, windowUID string, phase metadata.AgentPhase) {
	agentOwner := &metadata.OwnerRef{Kind: metadata.KindWindow, UID: windowUID}
	b.registry.Agents = append(b.registry.Agents, metadata.Agent{
		APIVersion: metadata.APIVersion,
		Kind:       metadata.KindAgent,
		Metadata:   b.meta(agentUID, agentName, "", agentOwner, nil),
		Spec:       metadata.AgentSpec{Provider: agentName},
		Status:     metadata.AgentStatus{Phase: phase, LastTransitionAt: fixtureClock},
	})
	b.reserve(b.rootForWindow(windowUID), metadata.KindAgent, agentName, agentUID)
}

// build derives the schema-v2 canonical Project/Window shell anchors and
// validates the result.
func (b *builder) build() metadata.Registry {
	b.t.Helper()
	for i := range b.registry.Windows {
		window := &b.registry.Windows[i]
		for _, pane := range b.registry.Panes {
			if pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == metadata.KindWindow &&
				pane.Metadata.OwnerRef.UID == window.Metadata.UID && pane.Spec.Role == metadata.PaneRoleShell {
				window.Spec.AnchorPaneRef = pane.Metadata.UID
				break
			}
		}
		if window.Spec.AnchorPaneRef == "" {
			uid := "pan-anchor-" + window.Metadata.UID
			b.shellPane(uid, "shell-anchor", "", window.Metadata.UID, "", nil)
			window.Spec.AnchorPaneRef = uid
		}
	}
	for i := range b.registry.Projects {
		project := &b.registry.Projects[i]
		for _, window := range b.registry.Windows {
			if window.Metadata.OwnerRef != nil && window.Metadata.OwnerRef.Kind == metadata.KindProject && window.Metadata.OwnerRef.UID == project.Metadata.UID {
				project.Spec.PrimaryWindowRef = window.Metadata.UID
				break
			}
		}
		if project.Spec.PrimaryWindowRef == "" {
			windowUID := "win-anchor-" + project.Metadata.UID
			paneUID := "pan-anchor-" + project.Metadata.UID
			b.window(windowUID, "window-anchor", project.Metadata.UID, nil)
			b.shellPane(paneUID, "shell-anchor", "", windowUID, project.Spec.Root, nil)
			project.Spec.PrimaryWindowRef = windowUID
			window, _ := b.registry.Window(windowUID)
			window.Spec.AnchorPaneRef = paneUID
		}
	}
	if err := b.registry.Validate(); err != nil {
		b.t.Fatalf("selector fixture is not a valid registry: %v", err)
	}
	return b.registry
}

// standardRegistry is the shared Phase 2 fixture. It deliberately contains
// every shape the selector contract has an opinion about:
//
//   - Duplicate displayName. "alpha" and "beta" both display as "projmux", and
//     the Pane named "log" displays as "zsh". Any resolution that consulted
//     displayName would return the wrong resource here.
//   - Offline. "beta" has a session projection with Live=false.
//   - MissingRoot. "gone" carries the MissingRoot condition.
//   - Root scope. The Window name "main" and Pane name "zsh" repeat across
//     Projects, which is legal because descendant names are unique only inside
//     their Project or ControlSession root.
//   - A managed Pane owned by an Agent rather than by its Window.
func standardRegistry(t *testing.T) metadata.Registry {
	t.Helper()
	b := newBuilder(t)

	b.project("prj-alpha", "alpha", "projmux", "/srv/alpha", &metadata.SessionProjection{Name: "alpha", Live: true}, false)
	b.project("prj-beta", "beta", "projmux", "/srv/beta", &metadata.SessionProjection{Name: "beta", Live: false}, false)
	b.project("prj-gone", "gone", "gone", "/srv/gone", nil, true)

	b.window("win-alpha-main", "main", "prj-alpha", map[string]string{"tier": "primary"})
	b.window("win-alpha-review", "review", "prj-alpha", nil)
	b.window("win-beta-main", "main", "prj-beta", nil)
	b.window("win-gone-main", "main", "prj-gone", nil)

	b.shellPane("pan-alpha-zsh", "zsh", "zsh", "win-alpha-main", "/srv/alpha", map[string]string{"role": "shell", "tier": "primary"})
	// displayName "zsh" duplicates the Pane above on purpose: a selector that
	// fell back to displayName would match this Pane for `--pane zsh`.
	b.shellPane("pan-alpha-log", "log", "zsh", "win-alpha-main", "/srv/alpha/logs", map[string]string{"role": "sidecar"})
	b.shellPane("pan-alpha-review-zsh", "review-zsh", "zsh", "win-alpha-review", "/srv/alpha", map[string]string{"role": "shell"})
	b.shellPane("pan-beta-zsh", "zsh", "zsh", "win-beta-main", "/srv/beta", map[string]string{"role": "shell"})
	b.shellPane("pan-gone-zsh", "zsh", "zsh", "win-gone-main", "/srv/gone", map[string]string{"role": "shell"})

	b.agentWithPane("agt-alpha-codex", "codex", "win-alpha-main", "pan-alpha-codex", "codex-pane",
		map[string]string{"role": "agent", "tier": "primary"})

	return b.build()
}

// observing builds a live-tmux observation from literal uid lists. It is the
// fake observer of the status-derivation tests: the machine is stated as the
// set of uids a live tmux object still mirrors, never as a stored bool.
func observing(windows, panes []string) metadata.RuntimeObservation {
	set := func(uids []string) map[string]bool {
		out := make(map[string]bool, len(uids))
		for _, uid := range uids {
			out[uid] = true
		}
		return out
	}
	return metadata.RuntimeObservation{Windows: set(windows), Panes: set(panes)}
}

// liveAlphaObservation is the observation that matches standardRegistry's
// intent: the machine is running exactly the Windows and Panes of the Project
// "alpha", whose session projection also says live. Nothing under the offline
// "beta" or the MissingRoot "gone" is mirrored anywhere.
func liveAlphaObservation() metadata.RuntimeObservation {
	return observing(
		[]string{"win-alpha-main", "win-alpha-review"},
		[]string{"pan-alpha-zsh", "pan-alpha-log", "pan-alpha-review-zsh", "pan-alpha-codex"},
	)
}

// mustRef parses a selector occurrence or fails the test.
func mustRef(t *testing.T, kind metadata.Kind, raw string) Ref {
	t.Helper()
	ref, err := ParseRef(kind, raw)
	if err != nil {
		t.Fatalf("ParseRef(%s, %q) error = %v", kind, raw, err)
	}
	return ref
}

// mustLabel parses a label condition or fails the test.
func mustLabel(t *testing.T, raw string) Label {
	t.Helper()
	label, err := ParseLabel(raw)
	if err != nil {
		t.Fatalf("ParseLabel(%q) error = %v", raw, err)
	}
	return label
}

func names(matches []Match) []string {
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.Name)
	}
	return out
}
