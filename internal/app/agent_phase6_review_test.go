package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestResourceAgentBindingNeverStartsLegacyTitleWatcher(t *testing.T) {
	t.Parallel()
	cmd := testAICommand(t.TempDir())
	var calls []string
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	cmd.BindManagedAgentPane("%7", aiModeCodex, "/work", "codex:work")
	cmd.BindResumedAgentPane("%8", aiModeCodex, "/work", "codex:work", "thread-1")
	for _, call := range calls {
		if strings.Contains(call, "run-shell") || strings.Contains(call, "watch-title") {
			t.Fatalf("resource binding started legacy watcher: %q", call)
		}
	}
}

func TestExplicitWatcherRefusesResourceAgentBeforePaneContentRead(t *testing.T) {
	t.Parallel()
	h := newSessionRefHarness(t, aiModeCodex)
	before := h.agent(t)
	if err := h.cmd.runWatchTitle([]string{"%7"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, call := range h.tmuxCalls {
		if strings.Contains(call, "capture-pane") || strings.Contains(call, "pane_title") {
			t.Fatalf("resource watcher read pane content: %q", call)
		}
	}
	if after := h.agent(t); !reflect.DeepEqual(after, before) || h.updates != 0 {
		t.Fatalf("watcher mutated Agent: before=%+v after=%+v updates=%d", before, after, h.updates)
	}
}

type activationAuthorityRunner struct {
	paneUID string
	calls   [][]string
}

func (r *activationAuthorityRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte(r.paneUID + "\n"), nil
}

func TestActivationAcknowledgementReadsOnlyCommittedProviderHookAuthority(t *testing.T) {
	t.Parallel()
	h := newSessionRefHarness(t, aiModeCodex)
	agent, _ := h.registry.Agent(h.agentUID)
	agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
	runner := &activationAuthorityRunner{paneUID: h.paneUID}

	acknowledged, source, err := h.cmd.AwaitAgentActivation(context.Background(), runner, "%7", 0)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged || source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("pending authority acknowledged=%v source=%q", acknowledged, source)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		for _, forbidden := range []string{aiPaneStateOption, aiPaneBadgeKindOption, "pane_title", "capture-pane"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("activation observer read inferred presentation: %q", joined)
			}
		}
	}

	h.ingest(t, []string{"codex-hook"},
		`{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","session_id":"codex-session-1","cwd":"/src/app"}`)
	acknowledged, source, err = h.cmd.AwaitAgentActivation(context.Background(), runner, "%7", 0)
	if err != nil || !acknowledged || source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("committed provider acknowledgement=%v source=%q err=%v", acknowledged, source, err)
	}
}

func TestManagedCompatibilityProjectionFailureKeepsCommittedAuthority(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		args   []string
		assert func(*testing.T, coremetadata.Agent)
	}{
		{name: "topic", args: []string{"topic", "set", "--pane", "%7", "committed"}, assert: func(t *testing.T, a coremetadata.Agent) {
			if a.Metadata.Annotations[coremetadata.AnnotationAgentTopic] != "committed" {
				t.Fatalf("topic = %q", a.Metadata.Annotations[coremetadata.AnnotationAgentTopic])
			}
		}},
		{name: "status", args: []string{"status", "set", "thinking", "%7"}, assert: func(t *testing.T, a coremetadata.Agent) {
			if a.Status.Interaction.Kind != coremetadata.InteractionInProgress {
				t.Fatalf("interaction = %+v", a.Status.Interaction)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newSessionRefHarness(t, aiModeCodex)
			h.cmd.runCommand = func(_ context.Context, _ string, args ...string) error {
				if slicesContain(args, "set-option") {
					return errors.New("projection refused")
				}
				return nil
			}
			var stdout, stderr bytes.Buffer
			err := h.cmd.Run(test.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "committed Registry state") || !strings.Contains(err.Error(), "projmux reconcile resources") {
				t.Fatalf("error = %v", err)
			}
			test.assert(t, h.agent(t))
		})
	}
}

func TestManagedProviderHookProjectionFailureKeepsOneCommittedObservation(t *testing.T) {
	t.Parallel()
	h := newSessionRefHarness(t, aiModeCodex)
	h.cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		h.tmuxCalls = append(h.tmuxCalls, name+" "+strings.Join(args, " "))
		if slicesContain(args, aiPaneStateOption) {
			return errors.New("semantic projection refused")
		}
		return nil
	}
	h.cmd.stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","session_id":"codex-session-1","cwd":"/src/app"}`)
	var stdout, stderr bytes.Buffer
	err := h.cmd.runIngest([]string{"codex-hook"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "committed Registry state") || !strings.Contains(err.Error(), "projmux reconcile resources") {
		t.Fatalf("error = %v", err)
	}
	if h.updates != 1 {
		t.Fatalf("provider observation used %d Registry transactions, want 1", h.updates)
	}
	agent := h.agent(t)
	if agent.Status.Interaction.Kind != coremetadata.InteractionInProgress || agent.Status.Interaction.Source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("interaction = %+v", agent.Status.Interaction)
	}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.ThreadID != "codex-thread-1" {
		t.Fatalf("session ref = %+v", agent.Status.SessionRef)
	}
}

func slicesContain(values []string, want string) bool {
	return slices.Contains(values, want)
}

func TestCanonicalMirrorUsesCommittedAgentBinding(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	cmd, _, _ := newTestAgentCommand(t, store)
	mirror := &fakeAgentMutationMirror{target: "%9"}
	cmd.mirror = mirror
	original := cmd.store.update
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		return original(func(reg *coremetadata.Registry) error {
			if _, err := store.mutator().ReleaseAgentPane(reg, "agt-alpha-codex", coremetadata.AgentExitNormal, "test transition"); err != nil {
				return err
			}
			return fn(reg)
		})
	}
	if _, _, err := runRoute(t, cmd, "topic", "set", "offline commit", "uid:agt-alpha-codex"); err != nil {
		t.Fatal(err)
	}
	if len(mirror.calls) != 0 {
		t.Fatalf("pre-transaction pane snapshot was mirrored: %q", mirror.calls)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic] != "offline commit" {
		t.Fatalf("committed Agent = %+v", agent)
	}
}

func TestCanonicalStatusRefusesAgentThatWentOfflineInsideTransaction(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	cmd, _, _ := newTestAgentCommand(t, store)
	cmd.mirror = &fakeAgentMutationMirror{target: "%9"}
	original := cmd.store.update
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		return original(func(reg *coremetadata.Registry) error {
			if _, err := store.mutator().ReleaseAgentPane(reg, "agt-alpha-codex", coremetadata.AgentExitNormal, "test transition"); err != nil {
				return err
			}
			return fn(reg)
		})
	}
	before := store.snapshot()
	if _, _, err := runRoute(t, cmd, "status", "set", "idle", "uid:agt-alpha-codex"); err == nil {
		t.Fatal("offline transition raced to success")
	}
	if store.snapshot() != before || store.writes != 0 {
		t.Fatalf("failed status mutation committed: writes=%d", store.writes)
	}
}

func TestMissingDefaultWorkspaceFailsBeforeCreateMutation(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	project, _ := store.registry.Project("prj-alpha")
	project.Spec.Root = filepath.Join(t.TempDir(), "missing")
	tmux := newFakeTmux()
	cmd, launcher := newTestAgentCreateCommand(t, store, tmux)
	cmd.resolveWorkspace = resolveAgentWorkspace
	before := store.snapshot()
	_, _, err := runRoute(t, cmd, "agent", "--provider", "codex", "--project", "alpha")
	if err == nil {
		t.Fatal("missing default root succeeded")
	}
	if store.writes != 0 || store.snapshot() != before || tmuxMutationCallCount(tmux) != 0 || len(launcher.plans) != 0 {
		t.Fatalf("missing default mutated writes=%d tmux=%d plans=%d", store.writes, tmuxMutationCallCount(tmux), len(launcher.plans))
	}
}

func TestResumeWorkspaceRevalidationFailsBeforeRuntimeMutation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		workspace func(*testing.T, string) coremetadata.AgentWorkspace
	}{
		{name: "missing", workspace: func(_ *testing.T, root string) coremetadata.AgentWorkspace {
			return coremetadata.AgentWorkspace{CWD: filepath.Join(root, "missing")}
		}},
		{name: "unauthorized", workspace: func(t *testing.T, _ string) coremetadata.AgentWorkspace {
			return coremetadata.AgentWorkspace{CWD: t.TempDir()}
		}},
		{name: "stale symlink escape", workspace: func(t *testing.T, root string) coremetadata.AgentWorkspace {
			outside := t.TempDir()
			link := filepath.Join(root, "workspace")
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			return coremetadata.AgentWorkspace{CWD: link}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			root := t.TempDir()
			project, _ := store.registry.Project("prj-beta")
			project.Spec.Root = root
			agent, _ := store.registry.Agent("agt-beta-codex")
			agent.Spec.Workspace = test.workspace(t, root)
			setFixtureSessionRef(t, store, agent.Metadata.UID, resumeFixtureRef(resourceFixtureClock))
			tmux := newFakeTmux()
			cmd, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
			cmd.resolveWorkspace = resolveAgentWorkspaceFor
			cmd.rebind.resolveWorkspace = resolveAgentWorkspaceFor
			before := store.snapshot()
			if _, _, err := runRoute(t, cmd, "resume", "uid:"+agent.Metadata.UID); err == nil {
				t.Fatal("invalid persisted workspace resumed")
			}
			if store.snapshot() != before || store.writes != 0 || len(splitWindowCalls(tmux)) != 0 || len(launcher.plans) != 0 {
				t.Fatalf("resume failure mutated writes=%d splits=%d plans=%d", store.writes, len(splitWindowCalls(tmux)), len(launcher.plans))
			}
		})
	}
}

func TestLegacyZeroWorkspaceProjectsAndPersistsOwnerProjectRootOnResume(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	root := t.TempDir()
	project, _ := store.registry.Project("prj-beta")
	project.Spec.Root = root
	agentBefore, _ := store.registry.Agent("agt-beta-codex")
	agentBefore.Spec.Workspace = coremetadata.AgentWorkspace{}
	setFixtureSessionRef(t, store, agentBefore.Metadata.UID, resumeFixtureRef(resourceFixtureClock))
	projected, _, ok := resourceFor(store.registry, coremetadata.KindAgent, agentBefore.Metadata.UID)
	if !ok || projected.(coremetadata.Agent).Spec.Workspace.CWD != root {
		t.Fatalf("legacy read workspace = %#v", projected)
	}
	listed, _, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "beta", "-o", "json")
	if err != nil || !strings.Contains(listed, `"cwd": "`+root+`"`) {
		t.Fatalf("legacy get JSON = %q err=%v", listed, err)
	}
	described, _, err := runRoute(t, newTestDescribeCommand(t, store), "agent", "uid:"+agentBefore.Metadata.UID)
	if err != nil || !strings.Contains(described, "WorkspaceCWD:") || !strings.Contains(described, root) {
		t.Fatalf("legacy describe = %q err=%v", described, err)
	}
	tmux := newFakeTmux()
	cmd, _, _, _ := newTestAgentResumeCommand(t, store, tmux)
	cmd.resolveWorkspace = resolveAgentWorkspaceFor
	cmd.rebind.resolveWorkspace = resolveAgentWorkspaceFor
	if _, _, err := runRoute(t, cmd, "resume", "uid:"+agentBefore.Metadata.UID); err != nil {
		t.Fatal(err)
	}
	after, _ := store.registry.Agent(agentBefore.Metadata.UID)
	if after.Spec.Workspace.CWD != root {
		t.Fatalf("persisted workspace = %+v", after.Spec.Workspace)
	}
	pane, ok := store.registry.Pane(after.Status.PaneRef)
	if !ok || pane.Spec.CWD != root {
		t.Fatalf("resumed Pane workspace = %+v", pane)
	}
}

type agentMirrorRoutingRunner struct{ calls [][]string }

func (r *agentMirrorRoutingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if slicesContain(args, "list-panes") {
		return []byte("pan-alpha-codex\\037%7\n"), nil
	}
	return nil, nil
}

func TestAgentMirrorRoutesEveryLookupAndWriteThroughInheritedAbsoluteSocket(t *testing.T) {
	t.Parallel()
	const socket = "/tmp/projmux-phase6-routing.sock"
	runner := &agentMirrorRoutingRunner{}
	mirror := inheritedAgentMutationMirror(func(key string) string {
		if key == "TMUX" {
			return socket + ",123,0"
		}
		return ""
	}, runner)
	if mirror == nil {
		t.Fatal("absolute inherited socket did not enable Agent mirror")
	}
	store := newFakeResourceStore(t)
	cmd, _, _ := newTestAgentCommand(t, store)
	cmd.mirror = mirror
	if _, _, err := runRoute(t, cmd, "topic", "set", "routed", "uid:agt-alpha-codex"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runRoute(t, cmd, "status", "set", "input_required", "uid:agt-alpha-codex"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) < 2 {
		t.Fatalf("calls = %v", runner.calls)
	}
	for _, call := range runner.calls {
		if len(call) < 3 || call[0] != "tmux" || call[1] != "-S" || call[2] != socket {
			t.Fatalf("call escaped exact -S routing: %v", call)
		}
	}
}

func TestAgentMirrorOutsideAbsoluteInheritedSocketIsRegistryOnly(t *testing.T) {
	t.Parallel()
	for _, inherited := range []string{"", "relative.sock,123,0", "malformed"} {
		runner := &agentMirrorRoutingRunner{}
		mirror := inheritedAgentMutationMirror(func(key string) string {
			if key == "TMUX" {
				return inherited
			}
			return ""
		}, runner)
		if mirror != nil {
			t.Fatalf("TMUX=%q enabled Agent mirror", inherited)
		}
		store := newFakeResourceStore(t)
		cmd, _, _ := newTestAgentCommand(t, store)
		cmd.mirror = mirror
		if _, _, err := runRoute(t, cmd, "topic", "set", "registry-only", "uid:agt-alpha-codex"); err != nil {
			t.Fatal(err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("TMUX=%q touched default/foreign server: %v", inherited, runner.calls)
		}
	}
}

func TestUnknownClearsEverySemanticLiveOptionWhileIdleSetsOnlyState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		kind         coremetadata.AgentInteractionKind
		wantStateSet bool
	}{
		{name: "unknown", kind: coremetadata.InteractionUnknown},
		{name: "idle", kind: coremetadata.InteractionIdle, wantStateSet: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &agentMirrorRoutingRunner{}
			mirror := &tmuxAgentMutationMirror{runner: runner}
			if err := mirror.WriteInteraction(context.Background(), "%7", test.kind); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 3 {
				t.Fatalf("calls = %v", runner.calls)
			}
			for _, call := range runner.calls {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, aiPaneStateOption) && test.wantStateSet {
					if !strings.Contains(joined, " idle") || strings.Contains(joined, " -u ") {
						t.Fatalf("idle state write = %q", joined)
					}
					continue
				}
				if !strings.Contains(joined, " -u ") {
					t.Fatalf("semantic option was not cleared: %q", joined)
				}
			}
		})
	}
}
