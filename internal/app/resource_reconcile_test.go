package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestResourceReconcileProjectsAllAgentFieldsFromRegistryAuthority(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	registry := resourceFixtureRegistry(t)
	agent, _ := registry.Agent("agt-alpha-codex")
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic] = "registry topic"
	agent.Status.Interaction = coremetadata.AgentInteraction{
		Kind: coremetadata.InteractionResponseComplete, ObservedAt: now.Add(-time.Minute), Source: "provider-hook",
	}
	server := newFakeTmux()
	pane := server.addSession("alpha").windows[0].panes[0]
	pane.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
	pane.opts[aiPaneTopicOption] = "foreign live topic"
	pane.opts[aiPaneStateOption] = "thinking"
	pane.opts[aiPaneBadgeKindOption] = aiBadgeKindInProgress
	pane.opts[attentionStateOption] = attentionStateBusy

	plan := func(reg coremetadata.Registry) *resourcePlanTmuxRunner {
		recorder := newResourcePlanTmuxRunner(server)
		if err := planResourceAgentProjections(context.Background(), recorder, reg, now); err != nil {
			t.Fatalf("plan Agent projection: %v", err)
		}
		return recorder
	}
	recorder := plan(registry)
	want := map[string]string{
		aiPaneTopicOption:       "registry topic",
		aiPaneTopicManualOption: "on",
		aiPaneStateOption:       "waiting",
		aiPaneBadgeKindOption:   aiBadgeKindResponseComplete,
		attentionStateOption:    attentionStateReply,
	}
	if len(recorder.writes) != len(want) {
		t.Fatalf("writes = %#v, want one for every Agent projection", recorder.writes)
	}
	for _, write := range recorder.writes {
		if want[write.field] != write.after {
			t.Fatalf("write %s = %q, want %q", write.field, write.after, want[write.field])
		}
		if _, err := server.Run(context.Background(), "tmux", write.args...); err != nil {
			t.Fatalf("execute %v: %v", write.args, err)
		}
	}
	if repeat := plan(registry); len(repeat.writes) != 0 {
		t.Fatalf("repeat projection imported or rewrote live values: %#v", repeat.writes)
	}

	// An Offline Agent may retain durable history, but a surviving stale Pane
	// projects current unknown and cannot keep the completed badge live.
	agent.Status.Phase = coremetadata.PhaseOffline
	agent.Status.PaneRef = ""
	recorder = plan(registry)
	for _, write := range recorder.writes {
		if _, err := server.Run(context.Background(), "tmux", write.args...); err != nil {
			t.Fatalf("execute offline clear %v: %v", write.args, err)
		}
	}
	if pane.opts[aiPaneStateOption] != "" {
		t.Fatalf("offline state = %q", pane.opts[aiPaneStateOption])
	}
	for _, field := range []string{aiPaneTopicOption, aiPaneTopicManualOption, aiPaneBadgeKindOption, attentionStateOption} {
		if got := pane.opts[field]; got != "" {
			t.Fatalf("offline field %s retained %q", field, got)
		}
	}
	if agent.Status.Interaction.Kind != coremetadata.InteractionResponseComplete {
		t.Fatalf("reconcile imported/erased durable history: %+v", agent.Status.Interaction)
	}
}

func TestPublicResourceReconcileRetriesExactAgentProjection(t *testing.T) {
	t.Parallel()
	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("bootstrap reconciliation: %v", err)
	}
	store.now = time.Now().UTC()
	mutator := store.mutator()
	window := store.registry.Windows[0]
	agent, err := mutator.CreateAgent(&store.registry, window.Metadata.UID, coremetadata.CreateAgentOptions{
		Provider: "codex", OperationID: "op-agent-reconcile",
	})
	if err != nil {
		t.Fatal(err)
	}
	managed, err := mutator.AttachAgentPane(&store.registry, agent.Metadata.UID, coremetadata.BootstrapPane{
		CWD: store.registry.Projects[0].Spec.Root,
	}, "op-agent-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.SetAgentTopic(&store.registry, agent.Metadata.UID, "public topic"); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.SetAgentInteraction(&store.registry, agent.Metadata.UID, coremetadata.InteractionApprovalRequired, string(coremetadata.InteractionSourceProviderHook)); err != nil {
		t.Fatal(err)
	}
	liveWindow := server.sessions[0].windows[0]
	livePane := newFakeTmuxPane("%agent")
	livePane.opts[tmuxopts.PaneUID] = managed.Metadata.UID
	livePane.opts[aiPaneTopicOption] = "stale"
	livePane.opts[aiPaneStateOption] = "idle"
	liveWindow.panes = append(liveWindow.panes, livePane)

	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("Agent projection preview: %v\n%s", err, preview)
	}
	for _, field := range []string{aiPaneTopicOption, aiPaneTopicManualOption, aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption} {
		if !strings.Contains(preview, `"field": "`+field+`"`) {
			t.Fatalf("public preview missing %s:\n%s", field, preview)
		}
	}

	server.fail = []string{"set-option", aiPaneStateOption}
	failed, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(failed, `"retry": "projmux reconcile resources --socket 'primary'"`) {
		t.Fatalf("projection failure did not expose public exact retry: err=%v\n%s", err, failed)
	}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("Agent projection retry: %v", err)
	}
	if livePane.opts[aiPaneTopicOption] != "public topic" || livePane.opts[aiPaneTopicManualOption] != "on" ||
		livePane.opts[aiPaneStateOption] != "waiting" || livePane.opts[aiPaneBadgeKindOption] != aiBadgeKindApprovalRequired ||
		livePane.opts[attentionStateOption] != attentionStateReply {
		t.Fatalf("Agent projection did not converge: %+v", livePane.opts)
	}
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) {
		t.Fatalf("Agent projection repeat not no-op: err=%v\n%s", err, repeat)
	}
}

func reconcileFixtureReconciler(root, sessionName string) func(tmuxCommandRunner, sessionLister) *registryReconciler {
	return func(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
		mirror := intmetadata.NewMirror(runner)
		return &registryReconciler{
			discoverRoots: func() ([]string, error) { return []string{root}, nil },
			liveSessions:  sessions.ExistingSessions,
			observeLegacy: mirror.ObserveLegacySessionTargets,
			mirror:        mirror,
			mirrorProject: func(context.Context, string, coremetadata.Project) error { return nil },
			mirrorWindow:  mirror.MirrorWindow,
			mirrorPane: func(ctx context.Context, target, _ string, pane coremetadata.Pane) error {
				return mirror.MirrorPane(ctx, target, pane)
			},
			shell: "/bin/zsh",
			sessionNameFor: func(string) string {
				return sessionName
			},
		}
	}
}

func newReconcileFixture(t *testing.T, socketFlag, socketValue string) (*resourceReconcileCommand, *fakeResourceStore, *fakeTmux, *routedTmuxRunner, string) {
	t.Helper()
	root := t.TempDir()
	server := newFakeTmux()
	session := server.addSession("alpha")
	session.opts[inttmux.ProjectPathSessionOption] = root
	store := &fakeResourceStore{registry: coremetadata.NewRegistry(), dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	// Most reconciliation tests exercise repair of Registry-owned identity, not
	// legacy import. Seed that authority explicitly in the fixture; the public
	// command must no longer turn this live D2 topology into Registry rows.
	_, err := store.mutator().ImportLegacySession(&store.registry, coremetadata.LegacySession{
		Session: "alpha",
		Root:    root,
		Windows: []coremetadata.LegacyWindow{{
			Name:  session.windows[0].name,
			Panes: []coremetadata.LegacyPane{{Command: "zsh", CWD: root}},
		}},
	}, "/bin/zsh", "op-fixture-authority", coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{}))
	if err != nil {
		t.Fatalf("seed authoritative reconcile fixture: %v", err)
	}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{socketFlag + "\x00" + socketValue: server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler(root, "alpha"),
	}
	return command, store, server, runner, root
}

func runReconcile(t *testing.T, command *resourceReconcileCommand, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := command.Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func tmuxMutationCallCount(server *fakeTmux) int {
	count := 0
	for _, call := range server.calls {
		if len(call) > 0 && slices.Contains([]string{"set-option", "rename-window"}, call[0]) {
			count++
		}
	}
	return count
}

func TestResourceReconcileDryRunIsDeterministicAndByteStable(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	registryBefore, tmuxBefore := store.snapshot(), server.state()
	first, stderr, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("first dry-run error=%v stderr=%q", err, stderr)
	}
	second, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "--dry-run", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("second dry-run error=%v stderr=%q", err, stderr)
	}
	if first != second {
		t.Fatalf("dry-run output changed across identical plans:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	for _, want := range []string{`"drift": "missing"`, `"surface": "tmux"`, `"after": "project-test-1"`, `"tmuxFlag": "-L"`} {
		if !strings.Contains(first, want) {
			t.Fatalf("dry-run JSON missing %q:\n%s", want, first)
		}
	}
	if got := store.snapshot(); got != registryBefore || store.writes != 0 || store.transactions != 0 {
		t.Fatalf("dry-run mutated Registry: transactions=%d writes=%d\n%s", store.transactions, store.writes, got)
	}
	if got := server.state(); got != tmuxBefore || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("dry-run mutated tmux:\n--- got ---\n%s\n--- want ---\n%s", got, tmuxBefore)
	}
}

func TestResourceReconcileHumanPlanShowsTargetCountsAndWrites(t *testing.T) {
	t.Parallel()

	command, _, _, _, _ := newReconcileFixture(t, "-L", "primary")
	stdout, stderr, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary")
	if err != nil || stderr != "" {
		t.Fatalf("human dry-run error=%v stderr=%q", err, stderr)
	}
	for _, want := range []string{
		"target: tmux -L primary",
		"outcome: planned",
		"counts: changed=",
		"tmux set-option",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human plan missing %q:\n%s", want, stdout)
		}
	}
}

func TestResourceReconcileRegistryItemsClassifyStaleAndOrphan(t *testing.T) {
	t.Parallel()

	before := bindingFixture(t, t.TempDir())
	after := before.Clone()
	after.Windows[0].Metadata.DisplayName = "renamed-runtime"
	after.Windows[1].Status.Conditions = append(after.Windows[1].Status.Conditions, coremetadata.Condition{
		Type: coremetadata.ConditionMissingRuntime, Status: coremetadata.ConditionTrue, Reason: coremetadata.ReasonRuntimeUnbound,
	})
	items := registryReconcileItems(before, after, resourcePlanUIDNormalizer{created: map[string]string{}})
	found := map[resourceDriftKind]bool{}
	for _, item := range items {
		found[item.Drift] = true
	}
	if !found[resourceDriftStale] || !found[resourceDriftOrphan] {
		t.Fatalf("registry drift classifications = %#v, want stale and orphan", items)
	}
}

func TestResourceReconcileRepairsStaleMirrorsForExactBindings(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := bindingFixture(t, root)
	server := bindingFixtureServer()
	session := server.session(driftedSessionName)
	session.opts[tmuxopts.ProjectUIDSession] = registry.Projects[0].Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = "stale-project"
	for index := range session.windows {
		session.windows[index].opts[tmuxopts.WindowUID] = registry.Windows[index].Metadata.UID
		session.windows[index].opts[tmuxopts.WindowName] = "stale-window"
		session.windows[index].opts[tmuxopts.AutomaticRenameWindow] = "on"
		session.windows[index].panes[0].opts[tmuxopts.PaneUID] = registry.Panes[index].Metadata.UID
		session.windows[index].panes[0].opts[tmuxopts.PaneName] = "stale-pane"
	}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: bindingFixtureReconciler(root),
	}
	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("stale mirror preview: %v", err)
	}
	for _, field := range []string{tmuxopts.ProjectNameSession, tmuxopts.WindowName, tmuxopts.PaneName, tmuxopts.AutomaticRenameWindow} {
		if !strings.Contains(preview, `"field": "`+field+`"`) || !strings.Contains(preview, `"drift": "stale"`) {
			t.Fatalf("stale mirror preview missing %s:\n%s", field, preview)
		}
	}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("repair stale mirrors: %v", err)
	}
	if session.opts[tmuxopts.ProjectNameSession] != registry.Projects[0].Metadata.Name {
		t.Fatal("Project name mirror did not converge")
	}
	for index := range session.windows {
		if session.windows[index].opts[tmuxopts.WindowName] != registry.Windows[index].Metadata.Name ||
			session.windows[index].opts[tmuxopts.AutomaticRenameWindow] != "off" ||
			session.windows[index].panes[0].opts[tmuxopts.PaneName] != registry.Panes[index].Metadata.Name {
			t.Fatalf("Window/Pane mirror %d did not converge: %+v %+v", index, session.windows[index].opts, session.windows[index].panes[0].opts)
		}
	}
}

func TestResourceReconcileRepairsRebindPathFromAuthoritativeProjectUID(t *testing.T) {
	t.Parallel()

	oldRoot, newRoot := t.TempDir(), t.TempDir()
	registry := bindingFixture(t, newRoot)
	server := bindingFixtureServer()
	session := server.session(driftedSessionName)
	project := registry.Projects[0]
	session.opts[tmuxopts.ProjectUIDSession] = project.Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = project.Metadata.Name
	session.opts[tmuxopts.ProjectPathSession] = oldRoot
	for index := range session.windows {
		session.windows[index].opts[tmuxopts.WindowUID] = registry.Windows[index].Metadata.UID
		session.windows[index].opts[tmuxopts.WindowName] = registry.Windows[index].Metadata.Name
		session.windows[index].opts[tmuxopts.AutomaticRenameWindow] = "off"
		session.windows[index].panes[0].opts[tmuxopts.PaneUID] = registry.Panes[index].Metadata.UID
		session.windows[index].panes[0].opts[tmuxopts.PaneName] = registry.Panes[index].Metadata.Name
	}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{newRoot: true}, now: resourceFixtureClock}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: bindingFixtureReconciler(newRoot),
	}

	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("rebind drift preview: %v\n%s", err, preview)
	}
	for _, want := range []string{
		`"field": "` + tmuxopts.ProjectPathSession + `"`,
		`"before": "` + oldRoot + `"`,
		`"after": "` + newRoot + `"`,
	} {
		if !strings.Contains(preview, want) {
			t.Fatalf("rebind preview missing %q:\n%s", want, preview)
		}
	}
	if strings.Contains(preview, `"action": "refuse"`) {
		t.Fatalf("valid exact Project UID was refused because its path was stale:\n%s", preview)
	}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("repair rebind drift: %v", err)
	}
	if got := session.opts[tmuxopts.ProjectPathSession]; got != newRoot {
		t.Fatalf("project path mirror = %q, want %q", got, newRoot)
	}
	if session.name != driftedSessionName || session.opts[tmuxopts.ProjectUIDSession] != project.Metadata.UID {
		t.Fatalf("rebind retry changed session identity: name=%q opts=%v", session.name, session.opts)
	}
	server.calls = nil
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("repeat reconcile was not a tmux-write-free no-op: err=%v mutations=%d\n%s", err, tmuxMutationCallCount(server), repeat)
	}
}

func TestResourceReconcileExecuteConvergesOneSocketAndRepeatsNoop(t *testing.T) {
	t.Parallel()

	command, store, primary, runner, _ := newReconcileFixture(t, "-L", "primary")
	secondary := newFakeTmux()
	secondary.addSession("other")
	secondaryBefore := secondary.state()
	runner.servers["-L\x00secondary"] = secondary

	first, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("execute error=%v stderr=%q\n%s", err, stderr, first)
	}
	if !strings.Contains(first, `"outcome": "changed"`) || store.writes != 1 {
		t.Fatalf("execute result/writes mismatch: writes=%d\n%s", store.writes, first)
	}
	session := primary.session("alpha")
	if got := session.opts[tmuxopts.ProjectUIDSession]; got == "" {
		t.Fatal("execute did not mirror Project uid")
	}
	if got := session.windows[0].opts[tmuxopts.WindowUID]; got == "" {
		t.Fatal("execute did not mirror Window uid")
	}
	if got := session.windows[0].panes[0].opts[tmuxopts.PaneUID]; got == "" {
		t.Fatal("execute did not mirror Pane uid")
	}
	if got := secondary.state(); got != secondaryBefore {
		t.Fatalf("unrelated socket changed:\n%s", got)
	}

	primary.calls = nil
	second, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("repeat error=%v stderr=%q\n%s", err, stderr, second)
	}
	if !strings.Contains(second, `"outcome": "no-op"`) || !strings.Contains(second, `"noOp": 1`) {
		t.Fatalf("repeat was not a no-op:\n%s", second)
	}
	if store.writes != 1 || tmuxMutationCallCount(primary) != 0 {
		t.Fatalf("repeat mutated state: Registry writes=%d tmux mutations=%d", store.writes, tmuxMutationCallCount(primary))
	}
}

func TestResourceReconcileBlankD2PaneRemainsUnattributed(t *testing.T) {
	t.Parallel()

	command, store, primary, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	raw := newFakeTmuxPane(primary.mint("%"))
	raw.opts[aiPaneAgentOption] = "antigravity"
	primary.session("alpha").windows[0].panes = append(primary.session("alpha").windows[0].panes, raw)
	primary.calls = nil

	first, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("blank orphan reconcile error=%v stderr=%q\n%s", err, stderr, first)
	}
	if raw.opts[tmuxopts.PaneUID] != "" {
		t.Fatalf("blank D2 Pane %s received Registry identity %q", raw.id, raw.opts[tmuxopts.PaneUID])
	}
	if !strings.Contains(first, `"outcome": "no-op"`) {
		t.Fatalf("blank D2 reconcile report:\n%s", first)
	}

	primary.calls = nil
	repeat, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" || !strings.Contains(repeat, `"outcome": "no-op"`) || tmuxMutationCallCount(primary) != 0 {
		t.Fatalf("blank orphan repeat error=%v stderr=%q mutations=%d\n%s", err, stderr, tmuxMutationCallCount(primary), repeat)
	}
	if store.writes != 1 {
		t.Fatalf("Registry writes = %d, want only the initial live Window handle observation and no D2 import", store.writes)
	}
}

func TestApprovedD3ModeStillPlansNoBlankD2Import(t *testing.T) {
	t.Parallel()

	command, store, primary, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	raw := newFakeTmuxPane(primary.mint("%"))
	raw.opts[aiPaneAgentOption] = "antigravity"
	primary.session("alpha").windows[0].panes = append(primary.session("alpha").windows[0].panes, raw)
	primary.calls = nil

	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatalf("socket target: %v", err)
	}
	planner := resourceReconcilePlanner{
		reader:        explicitTmuxRunner{runner: command.runner, target: target},
		store:         command.resources,
		newReconciler: command.newReconciler,
	}
	planner.approvedOrphanImport = true
	registryBefore, writesBefore := store.snapshot(), store.writes
	plan, err := planner.build(context.Background(), store.registry.Clone())
	if err != nil {
		t.Fatalf("plan blank orphan: %v", err)
	}
	for _, write := range plan.writes {
		if write.target == raw.id && write.field == tmuxopts.PaneUID {
			t.Fatalf("approved D3 mode planned blank D2 import: %+v", write)
		}
	}
	if store.snapshot() != registryBefore || store.writes != writesBefore || tmuxMutationCallCount(primary) != 0 {
		t.Fatalf("D2 planning mutated state: Registry writes=%d want=%d tmux mutations=%d", store.writes, writesBefore, tmuxMutationCallCount(primary))
	}
}

func TestResourceReconcilePreservesRegistryGraphOwnedByAnotherSocket(t *testing.T) {
	t.Parallel()

	command, store, _, runner, _ := newReconcileFixture(t, "-L", "primary")
	otherRoot := t.TempDir()
	store.dirs[otherRoot] = true
	project, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: otherRoot, DefaultShell: "/bin/zsh", OperationID: "op-unrelated-secondary",
	})
	if err != nil {
		t.Fatalf("seed unrelated Registry graph: %v", err)
	}
	window := store.registry.Windows[len(store.registry.Windows)-1]
	pane := store.registry.Panes[len(store.registry.Panes)-1]
	secondary := newFakeTmux()
	secondarySession := secondary.addSession("secondary")
	secondarySession.opts[inttmux.ProjectPathSessionOption] = otherRoot
	secondarySession.opts[tmuxopts.ProjectUIDSession] = project.Project.Metadata.UID
	secondarySession.opts[tmuxopts.ProjectNameSession] = project.Project.Metadata.Name
	secondarySession.windows[0].opts[tmuxopts.WindowUID] = window.Metadata.UID
	secondarySession.windows[0].opts[tmuxopts.WindowName] = window.Metadata.Name
	secondarySession.windows[0].panes[0].opts[tmuxopts.PaneUID] = pane.Metadata.UID
	secondarySession.windows[0].panes[0].opts[tmuxopts.PaneName] = pane.Metadata.Name
	runner.servers["-L\x00secondary"] = secondary

	beforeGraph := resourceRegistryProjectGraph(store.registry, map[string]bool{project.Project.Metadata.UID: true})
	secondaryBefore := secondary.state()
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("primary reconcile: %v\n%s", err, stdout)
	}
	afterGraph := resourceRegistryProjectGraph(store.registry, map[string]bool{project.Project.Metadata.UID: true})
	if !reflect.DeepEqual(beforeGraph, afterGraph) {
		t.Fatalf("unrelated Registry graph changed:\n--- before ---\n%#v\n--- after ---\n%#v", beforeGraph, afterGraph)
	}
	if got := secondary.state(); got != secondaryBefore {
		t.Fatalf("secondary socket changed:\n--- before ---\n%s\n--- after ---\n%s", secondaryBefore, got)
	}
}

func TestResourceReconcileTargetRulesArePreMutationAndExact(t *testing.T) {
	t.Parallel()

	command, store, server, runner, _ := newReconcileFixture(t, "-S", filepath.Join(t.TempDir(), "inside.sock"))
	path := ""
	for key := range runner.servers {
		path = strings.TrimPrefix(key, "-S\x00")
	}
	command.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return path + ",1234,7"
		}
		return ""
	}
	if _, _, err := runReconcile(t, command, "resources", "--dry-run"); err != nil {
		t.Fatalf("inherited target: %v", err)
	}
	for _, call := range runner.calls {
		if call.flag != "-S" || call.value != path {
			t.Fatalf("inherited target escaped exact socket: %+v", call)
		}
	}
	runner.calls = nil
	command.lookupEnv = func(string) string { return "" }
	for _, args := range [][]string{
		{"resources"},
		{"resources", "--socket-path", "relative"},
		{"resources", "--socket", "one", "--socket-path", path},
	} {
		if _, _, err := runReconcile(t, command, args...); !IsUsageError(err) {
			t.Fatalf("args %v error=%v, want usage error", args, err)
		}
	}
	if store.transactions != 0 || store.writes != 0 || tmuxMutationCallCount(server) != 0 || len(runner.calls) != 0 {
		t.Fatalf("invalid target reached mutation/read: transactions=%d writes=%d tmux=%d routed=%d", store.transactions, store.writes, tmuxMutationCallCount(server), len(runner.calls))
	}
}

func TestResourceReconcileRefusesForeignAndAmbiguousBindings(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := bindingFixture(t, root)
	server := bindingFixtureServer()
	session := server.session(driftedSessionName)
	session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
	session.windows[0].opts[tmuxopts.WindowUID] = "win-foreign"
	session.windows[1].opts[tmuxopts.WindowUID] = "win-foreign"
	before := server.state()
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: bindingFixtureReconciler(root),
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil {
		t.Fatal("foreign/ambiguous execute unexpectedly succeeded")
	}
	for _, want := range []string{`"drift": "foreign"`, `"action": "refuse"`, `"outcome": "refused"`, `"retry": "projmux reconcile resources --socket 'primary'"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("negative report missing %q:\n%s", want, stdout)
		}
	}
	if got := session.opts[tmuxopts.ProjectUIDSession]; got != "" {
		t.Fatalf("automatic D3 L8 did not clear foreign Project mirror: %q", got)
	}
	if session.windows[0].opts[tmuxopts.WindowUID] != "win-foreign" || session.windows[1].opts[tmuxopts.WindowUID] != "win-foreign" {
		t.Fatalf("D4 Window claims changed: %s", server.state())
	}
	if got := server.state(); got == before || tmuxMutationCallCount(server) != 1 {
		t.Fatalf("expected only D3 Project L8 mutation:\n--- got ---\n%s\n--- before ---\n%s", got, before)
	}
}

func TestResourceReconcileRefusesDuplicateUIDLessProjectClaims(t *testing.T) {
	t.Parallel()

	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	if _, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: root, DefaultShell: "/bin/zsh", OperationID: "op-duplicate-project-claim",
	}); err != nil {
		t.Fatalf("seed authoritative Project: %v", err)
	}
	second := server.addSession("beta")
	second.opts[inttmux.ProjectPathSessionOption] = root
	registryBefore, tmuxBefore := store.snapshot(), server.state()

	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil {
		t.Fatal("duplicate UID-less Project claims unexpectedly succeeded")
	}
	for _, want := range []string{
		`"failed": 2`,
		`"reason": "multiple live sessions resolve to the same exact Registry Project"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("duplicate Project claim report missing %q:\n%s", want, stdout)
		}
	}
	if store.writes != 0 || store.snapshot() != registryBefore {
		t.Fatalf("duplicate Project claims mutated Registry: writes=%d snapshot=%s", store.writes, store.snapshot())
	}
	if got := server.state(); got != tmuxBefore || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("duplicate Project claims mutated tmux:\n--- got ---\n%s\n--- want ---\n%s", got, tmuxBefore)
	}
}

func TestResourceReconcileForeignProjectRunsAutomaticL8WithoutRegistryLoss(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	session := server.session("alpha")
	session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
	registryBefore := store.snapshot()
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("foreign Project L8 failed: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, `"action": "L8-discard-mirror"`) {
		t.Fatalf("foreign Project L8 missing:\n%s", stdout)
	}
	if store.snapshot() != registryBefore || store.writes != 0 {
		t.Fatalf("automatic L8 changed Registry: writes=%d snapshot=%s", store.writes, store.snapshot())
	}
	if got := session.opts[tmuxopts.ProjectUIDSession]; got != "" {
		t.Fatalf("foreign Project mirror survived L8: %q", got)
	}
}

func TestResourceReconcileRefusesAmbiguousRootlessProjectScope(t *testing.T) {
	t.Parallel()

	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	store := &fakeResourceStore{registry: coremetadata.NewRegistry(), dirs: map[string]bool{firstRoot: true, secondRoot: true}, now: resourceFixtureClock}
	mutator := store.mutator()
	for index, root := range []string{firstRoot, secondRoot} {
		result, err := mutator.RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
			Root: root, DefaultShell: "/bin/zsh", OperationID: fmt.Sprintf("op-ambiguous-%d", index),
		})
		if err != nil {
			t.Fatalf("seed ambiguous Project %d: %v", index, err)
		}
		project, _ := store.registry.Project(result.Project.Metadata.UID)
		project.Status.Session = &coremetadata.SessionProjection{Name: "shared", Live: true}
	}
	server := newFakeTmux()
	server.addSession("shared")
	before := server.state()
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: func(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
			mirror := intmetadata.NewMirror(runner)
			return &registryReconciler{
				discoverRoots: func() ([]string, error) { return nil, nil }, liveSessions: sessions.ExistingSessions,
				observeLegacy: mirror.ObserveLegacySessionTargets, mirror: mirror, shell: "/bin/zsh",
				sessionNameFor: func(string) string { return "shared" },
			}
		},
	}
	registryBefore := store.snapshot()
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(stdout, "multiple Registry Projects claim the live session-name edge") || !strings.Contains(stdout, `"outcome": "refused"`) {
		t.Fatalf("ambiguous rootless scope was not explicitly refused: err=%v\n%s", err, stdout)
	}
	if store.writes != 0 || store.snapshot() != registryBefore || server.state() != before || tmuxMutationCallCount(server) != 0 {
		t.Fatal("ambiguous rootless scope mutated Registry or tmux")
	}
}

func TestResourceReconcileCommitFailureDoesNotPublishAllocatedUIDs(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	registryBefore, tmuxBefore := store.snapshot(), server.state()
	base := command.resources
	command.resources = &resourceStore{
		load:    base.load,
		mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			working := store.registry.Clone()
			if err := fn(&working); err != nil {
				return coremetadata.Registry{}, false, err
			}
			return coremetadata.Registry{}, false, errors.New("injected registry commit failure")
		},
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(stdout, `"outcome": "failed"`) || !strings.Contains(stdout, "registry commit") {
		t.Fatalf("commit failure was not reported: err=%v\n%s", err, stdout)
	}
	if got := store.snapshot(); got != registryBefore {
		t.Fatalf("failed commit changed Registry:\n%s", got)
	}
	if got := server.state(); got != tmuxBefore || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("failed commit published a planned uid:\n--- got ---\n%s\n--- want ---\n%s", got, tmuxBefore)
	}
}

func TestResourceReconcileTmuxFailureLeavesRetryableRegistryAuthority(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	server.fail = []string{"set-option", tmuxopts.WindowUID}
	first, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil {
		t.Fatal("injected tmux failure unexpectedly succeeded")
	}
	if store.writes != 1 || len(store.registry.Projects) != 1 || len(store.registry.Windows) != 1 || len(store.registry.Panes) != 1 {
		t.Fatalf("Registry authority was not committed before tmux failure: writes=%d snapshot=%s", store.writes, store.snapshot())
	}
	for _, want := range []string{`"outcome": "failed"`, `"completedStages"`, `"remainingDrift"`, `"retry": "projmux reconcile resources --socket 'primary'"`} {
		if !strings.Contains(first, want) {
			t.Fatalf("partial report missing %q:\n%s", want, first)
		}
	}

	second, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("exact retry failed: %v\n%s", err, second)
	}
	if !strings.Contains(second, `"outcome": "changed"`) {
		t.Fatalf("retry did not converge:\n%s", second)
	}
	if len(store.registry.Projects) != 1 || len(store.registry.Windows) != 1 || len(store.registry.Panes) != 1 {
		t.Fatalf("retry duplicated Registry identity: %s", store.snapshot())
	}
	third, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(third, `"outcome": "no-op"`) {
		t.Fatalf("post-retry repeat is not no-op: err=%v\n%s", err, third)
	}
}

func TestResourceReconcileHumanFailureListsRemainingDriftItems(t *testing.T) {
	t.Parallel()

	command, _, server, _, _ := newReconcileFixture(t, "-L", "primary")
	server.fail = []string{"set-option", tmuxopts.WindowUID}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary")
	if err == nil {
		t.Fatal("injected human-output failure unexpectedly succeeded")
	}
	for _, want := range []string{"remaining drift:", "tmux:set-option:", "[missing] tmux set-option", "retry: projmux reconcile resources --socket 'primary'"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human partial report missing %q:\n%s", want, stdout)
		}
	}
}

func TestResourceReconcileKeepsReadAndDiagnosticRoutesOutsideMutationMigration(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"doctor"}, {"get", "projects"}, {"describe", "project", "alpha"}, {"reconcile", "resources", "--dry-run"}} {
		if shouldRunLegacyHookMigrations(args) {
			t.Fatalf("read/repair boundary %v triggered unrelated legacy migration", args)
		}
	}
}

func TestResourceRepairMatcherRefusesForeignWithoutChangingLifecycleMatcher(t *testing.T) {
	t.Parallel()

	registry := bindingFixture(t, t.TempDir())
	lifecycle := coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{}).MatchWindow(&registry, registry.Projects[0].Metadata.UID, "win-foreign")
	repair := coremetadata.NewRepairBindingMatcher(coremetadata.RuntimeObservation{}).MatchWindow(&registry, registry.Projects[0].Metadata.UID, "win-foreign")
	if lifecycle.Kind != coremetadata.AdoptionForeign || repair.Kind != coremetadata.AdoptionRefused {
		t.Fatalf("matcher modes lifecycle=%+v repair=%+v", lifecycle, repair)
	}
}
