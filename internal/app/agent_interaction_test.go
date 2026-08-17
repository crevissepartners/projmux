package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type fakeAgentMutationMirror struct {
	target    string
	lookupErr error
	writeErr  error
	calls     []string
}

func TestAgentTopicInteractionAndWorkspaceReadParity(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	store.now = time.Now().UTC()
	agent, _ := store.registry.Agent("agt-alpha-codex")
	agent.Spec.Workspace = coremetadata.AgentWorkspace{CWD: "/srv/alpha", AdditionalWritableRoots: []string{"/srv/beta"}}
	cmd, _, _ := newTestAgentCommand(t, store)
	cmd.now = func() time.Time { return store.now }
	cmd.mirror = &fakeAgentMutationMirror{}
	if _, _, err := runRoute(t, cmd, "topic", "set", "registry topic", "uid:agt-alpha-codex"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runRoute(t, cmd, "status", "set", "in_progress", "uid:agt-alpha-codex"); err != nil {
		t.Fatal(err)
	}
	topic, _, _ := runRoute(t, cmd, "topic", "get", "uid:agt-alpha-codex")
	description, _, err := runRoute(t, newTestDescribeCommand(t, store), "agent", "uid:agt-alpha-codex")
	if err != nil {
		t.Fatal(err)
	}
	structured, _, err := runRoute(t, newTestDescribeCommand(t, store), "agent", "uid:agt-alpha-codex", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if topic != "registry topic\n" {
		t.Fatalf("topic get = %q", topic)
	}
	for _, required := range []string{"Annotations:", "projmux.io/agent-topic=registry topic", "Phase:", "Running", "Interaction:", "in_progress", "WorkspaceCWD:", "/srv/alpha", "AdditionalWritableRoot:", "/srv/beta"} {
		if !strings.Contains(description, required) {
			t.Fatalf("describe missing %q:\n%s", required, description)
		}
	}
	for _, required := range []string{`"projmux.io/agent-topic": "registry topic"`, `"phase": "Running"`, `"kind": "in_progress"`, `"cwd": "/srv/alpha"`, `"additionalWritableRoots"`} {
		if !strings.Contains(structured, required) {
			t.Fatalf("JSON missing %q:\n%s", required, structured)
		}
	}
}

func (f *fakeAgentMutationMirror) FindPaneTargetForUID(_ context.Context, uid string) (string, bool, error) {
	f.calls = append(f.calls, "find "+uid)
	return f.target, f.target != "", f.lookupErr
}

func (f *fakeAgentMutationMirror) WriteTopic(_ context.Context, target, topic string) error {
	f.calls = append(f.calls, "topic "+target+" "+topic)
	return f.writeErr
}

func (f *fakeAgentMutationMirror) WriteInteraction(_ context.Context, target string, kind coremetadata.AgentInteractionKind) error {
	f.calls = append(f.calls, "status "+target+" "+string(kind))
	return f.writeErr
}

func TestAgentTopicRegistryOwnsOnlineAndOfflineValues(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	cmd, ai, _ := newTestAgentCommand(t, store)
	mirror := &fakeAgentMutationMirror{target: "%9"}
	cmd.mirror = mirror

	if _, _, err := runRoute(t, cmd, "topic", "set", "review release", "--agent", "uid:agt-alpha-codex"); err != nil {
		t.Fatalf("set live topic: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if got := agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic]; got != "review release" {
		t.Fatalf("Registry topic = %q", got)
	}
	if got := strings.Join(mirror.calls, "|"); got != "find pan-alpha-codex|topic %9 review release" {
		t.Fatalf("mirror calls = %q", got)
	}
	out, _, err := runRoute(t, cmd, "topic", "get", "uid:agt-alpha-codex")
	if err != nil || out != "review release\n" {
		t.Fatalf("get = %q, %v", out, err)
	}
	if len(ai.calls) != 0 {
		t.Fatalf("canonical topic reached compatibility AI handler: %q", ai.calls)
	}

	mirror.calls = nil
	if _, _, err := runRoute(t, cmd, "topic", "set", "resume me", "uid:agt-beta-codex"); err != nil {
		t.Fatalf("set offline topic: %v", err)
	}
	offline, _ := store.registry.Agent("agt-beta-codex")
	if got := offline.Metadata.Annotations[coremetadata.AnnotationAgentTopic]; got != "resume me" {
		t.Fatalf("offline Registry topic = %q", got)
	}
	if len(mirror.calls) != 0 {
		t.Fatalf("offline topic touched tmux: %q", mirror.calls)
	}

	if _, _, err := runRoute(t, cmd, "topic", "clear", "uid:agt-alpha-codex"); err != nil {
		t.Fatalf("clear live topic: %v", err)
	}
	agent, _ = store.registry.Agent("agt-alpha-codex")
	if _, ok := agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic]; ok {
		t.Fatalf("topic remained after clear: %+v", agent.Metadata.Annotations)
	}
}

func TestAgentStatusIsExactSemanticAuthoritySeparateFromLifecycle(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	cmd, ai, _ := newTestAgentCommand(t, store)
	mirror := &fakeAgentMutationMirror{target: "%9"}
	cmd.mirror = mirror

	if _, _, err := runRoute(t, cmd, "status", "set", "approval_required", "--agent", "uid:agt-alpha-codex"); err != nil {
		t.Fatalf("status set: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("status set changed lifecycle to %q", agent.Status.Phase)
	}
	if got := agent.Status.Interaction; got.Kind != coremetadata.InteractionApprovalRequired || got.Source != string(coremetadata.InteractionSourceManual) || got.ObservedAt.IsZero() {
		t.Fatalf("stored interaction = %+v", got)
	}
	if got := strings.Join(mirror.calls, "|"); got != "find pan-alpha-codex|status %9 approval_required" {
		t.Fatalf("mirror calls = %q", got)
	}
	out, _, err := runRoute(t, cmd, "status", "get", "uid:agt-alpha-codex")
	if err != nil || !strings.Contains(out, "approval_required lifecycle=Running") || !strings.Contains(out, "source=manual") {
		t.Fatalf("status get = %q, %v", out, err)
	}
	if len(ai.calls) != 0 {
		t.Fatalf("canonical status reached compatibility AI handler: %q", ai.calls)
	}

	beforeWrites := store.writes
	if _, _, err := runRoute(t, cmd, "status", "set", "idle", "uid:no-such"); err == nil {
		t.Fatal("missing Agent status set succeeded")
	}
	if store.writes != beforeWrites {
		t.Fatalf("missing Agent fanned out a write: %d -> %d", beforeWrites, store.writes)
	}

	before := store.snapshot()
	if _, _, err := runRoute(t, cmd, "status", "set", "idle", "--source", "secret prompt", "uid:agt-alpha-codex"); err == nil {
		t.Fatal("removed free-form --source flag succeeded")
	}
	if store.snapshot() != before {
		t.Fatal("removed --source flag mutated Registry")
	}

	if _, _, err := runRoute(t, cmd, "status", "set", "idle", "uid:agt-beta-codex"); err == nil {
		t.Fatal("Offline Agent accepted manual semantic history")
	}
}

func TestAgentTopicAndStatusResolveOnlyTheExactActiveManagedPane(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	cmd, _, _ := newTestAgentCommand(t, store)
	cmd.activeTarget = insideTmux("pan-alpha-codex", "win-alpha-main").lookup
	cmd.mirror = &fakeAgentMutationMirror{target: "%46"}
	if _, _, err := runRoute(t, cmd, "topic", "set", "active topic"); err != nil {
		t.Fatalf("active topic: %v", err)
	}
	if _, _, err := runRoute(t, cmd, "status", "set", "input_required"); err != nil {
		t.Fatalf("active status: %v", err)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic] != "active topic" || agent.Status.Interaction.Kind != coremetadata.InteractionInputRequired {
		t.Fatalf("active Agent = %+v", agent)
	}

	before := store.writes
	cmd.activeTarget = insideTmux("pan-alpha-zsh", "win-alpha-main").lookup
	if _, _, err := runRoute(t, cmd, "status", "set", "idle"); err == nil {
		t.Fatal("active shell Pane was synthesized into an Agent")
	}
	if store.writes != before {
		t.Fatalf("active shell Pane changed %d writes to %d", before, store.writes)
	}
}

func TestAgentMutationMirrorFailureKeepsRegistryAndNamesPublicRetry(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		read func(coremetadata.Agent) string
		want string
	}{
		{name: "topic", args: []string{"topic", "set", "committed", "uid:agt-alpha-codex"}, read: func(a coremetadata.Agent) string { return a.Metadata.Annotations[coremetadata.AnnotationAgentTopic] }, want: "committed"},
		{name: "status", args: []string{"status", "set", "response_complete", "uid:agt-alpha-codex"}, read: func(a coremetadata.Agent) string { return string(a.Status.Interaction.Kind) }, want: "response_complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			cmd, _, _ := newTestAgentCommand(t, store)
			cmd.mirror = &fakeAgentMutationMirror{target: "%9", writeErr: errors.New("write refused")}
			_, _, err := runRoute(t, cmd, test.args...)
			if err == nil {
				t.Fatal("mirror failure succeeded")
			}
			for _, fragment := range []string{"committed Registry state", "projmux reconcile resources"} {
				if !strings.Contains(err.Error(), fragment) {
					t.Fatalf("error = %q, want %q", err, fragment)
				}
			}
			agent, _ := store.registry.Agent("agt-alpha-codex")
			if got := test.read(*agent); got != test.want {
				t.Fatalf("committed value = %q, want %q", got, test.want)
			}
		})
	}
}
