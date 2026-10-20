package projmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestAgentPersonaPublicConsumer(t *testing.T) {
	client, e := projmux.NewClient(projmux.Options{HomeDir: t.TempDir(), ConfigDir: t.TempDir(), StateDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	var result core.AgentPersonaResult
	result, e = client.AttachAgentPersona(context.Background(), core.AttachAgentPersonaRequest{Ref: "uid:missing", Persona: "reviewer", DryRun: true})
	var refusal *projmux.Refusal
	if !errors.As(e, &refusal) || refusal.Code != projmux.CodeResourceNotFound || result.AgentUID != "" {
		t.Fatalf("%+v %v", result, e)
	}
	_, e = client.DetachAgentPersona(context.Background(), core.DetachAgentPersonaRequest{})
	if !errors.As(e, &refusal) || refusal.Code != projmux.CodeSelectorInvalid {
		t.Fatal(e)
	}
	// Recovery and partial-result fields can be handled without internal types.
	result = core.AgentPersonaResult{Action: "attach", AgentUID: "agent", Phase: core.PhaseOffline, Interaction: core.InteractionIdle, CurrentPersonaDigest: "old", NewPersonaDigest: "new", Notices: []string{"did not resume"}, Recovery: "projmux agent resume uid:agent"}
	if result.Recovery == "" || len(result.Notices) != 1 {
		t.Fatal(result)
	}
}

func TestAgentPersonaPublicDryRunAndPartialPlan(t *testing.T) {
	root, config, state := t.TempDir(), t.TempDir(), t.TempDir()
	project := core.Project{APIVersion: "projmux.io/v1alpha1", Kind: core.KindProject, Metadata: core.ObjectMeta{UID: "project", Name: "sample"}, Spec: core.ProjectSpec{Root: root, PrimaryWindowRef: "window"}}
	window := core.Window{APIVersion: "projmux.io/v1alpha1", Kind: core.KindWindow, Metadata: core.ObjectMeta{UID: "window", Name: "work", OwnerRef: &core.OwnerRef{Kind: core.KindProject, UID: "project"}}, Spec: core.WindowSpec{AnchorPaneRef: "anchor", DefaultShellPaneRef: "anchor"}}
	pane := core.Pane{APIVersion: "projmux.io/v1alpha1", Kind: core.KindPane, Metadata: core.ObjectMeta{UID: "anchor", Name: "shell", OwnerRef: &core.OwnerRef{Kind: core.KindWindow, UID: "window"}}, Spec: core.PaneSpec{Role: core.PaneRoleShell, CWD: root, Runtime: core.PaneRuntimeSpec{Kind: core.RuntimeTmux}}}
	agent := core.Agent{APIVersion: "projmux.io/v1alpha1", Kind: core.KindAgent, Metadata: core.ObjectMeta{UID: "agent", Name: "worker", OwnerRef: &core.OwnerRef{Kind: core.KindWindow, UID: "window"}}, Spec: core.AgentSpec{Provider: "claude", Workspace: core.AgentWorkspace{CWD: root}}, Status: core.AgentStatus{Phase: core.PhaseOffline, SessionRef: &core.AgentSessionRef{Provider: "claude", Claude: &core.ClaudeSessionRef{SessionID: "11111111-2222-4333-8444-555555555555"}}}}
	registry, e := json.Marshal(map[string]any{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": []core.Project{project}, "windows": []core.Window{window}, "panes": []core.Pane{pane}, "agents": []core.Agent{agent}})
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(state, "metadata", "registry.json")
	if e = os.MkdirAll(filepath.Dir(file), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file, registry, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(config, "personas"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(config, "personas", "reviewer.md"), []byte("Review carefully.\n"), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: config, StateDir: state, SocketPath: filepath.Join(root, "unused.sock")})
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.AttachAgentPersona(context.Background(), core.AttachAgentPersonaRequest{Ref: "uid:agent", Persona: "reviewer", DryRun: true})
	if e != nil || out.Outcome != "would-resume" || out.AgentUID != "agent" || out.NewPersonaDigest == "" {
		t.Fatalf("%+v %v", out, e)
	}
	out, e = c.DetachAgentPersona(context.Background(), core.DetachAgentPersonaRequest{Ref: "uid:agent"})
	var refusal *projmux.Refusal
	if !errors.As(e, &refusal) || refusal.Code != projmux.CodeExecutableInvalid || out.Action != "detach" || out.AgentUID != "agent" || out.Phase != core.PhaseOffline {
		t.Fatalf("partial plan %+v %v", out, e)
	}
	after, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	if string(after) != string(registry) {
		t.Fatal("public dry-run/admission failure wrote state")
	}
}
