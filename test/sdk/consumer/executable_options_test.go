package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalExecutableOptionsCodes(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	registryPath := filepath.Join(state, "metadata", "registry.json")
	if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
		t.Fatal(err)
	}
	meta := func(uid, name string, kind core.Kind, owner string) core.ObjectMeta {
		m := core.ObjectMeta{UID: uid, Name: name}
		if owner != "" {
			m.OwnerRef = &core.OwnerRef{Kind: kind, UID: owner}
		}
		return m
	}
	project := core.Project{APIVersion: "projmux.io/v1alpha1", Kind: core.KindProject, Metadata: meta("project", "project", "", ""), Spec: core.ProjectSpec{Root: root, PrimaryWindowRef: "window"}}
	window := core.Window{APIVersion: "projmux.io/v1alpha1", Kind: core.KindWindow, Metadata: meta("window", "window", core.KindProject, "project"), Spec: core.WindowSpec{AnchorPaneRef: "shell"}}
	shell := core.Pane{APIVersion: "projmux.io/v1alpha1", Kind: core.KindPane, Metadata: meta("shell", "shell", core.KindWindow, "window"), Spec: core.PaneSpec{Role: core.PaneRoleShell, CWD: root}}
	agent := core.Agent{APIVersion: "projmux.io/v1alpha1", Kind: core.KindAgent, Metadata: meta("agent", "worker", core.KindWindow, "window"), Spec: core.AgentSpec{Provider: "claude", Workspace: core.AgentWorkspace{CWD: root}}, Status: core.AgentStatus{Phase: core.PhaseOffline, SessionRef: &core.AgentSessionRef{Provider: "claude", ObservedAt: time.Now(), Claude: &core.ClaudeSessionRef{SessionID: "11111111-2222-4333-8444-555555555555"}}}}
	baseline, err := json.Marshal(map[string]any{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": []core.Project{project}, "windows": []core.Window{window}, "panes": []core.Pane{shell}, "agents": []core.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, baseline, 0600); err != nil {
		t.Fatal(err)
	}
	nonexec := filepath.Join(root, "not-executable")
	if err := os.WriteFile(nonexec, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{"", "relative", filepath.Join(root, "missing"), root, nonexec} {
		t.Run(binary, func(t *testing.T) {
			recorderCalls := 0
			c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: state, SocketPath: filepath.Join(root, "absent.sock"), ProjmuxExecutable: binary, CreateRecorder: func(projmux.CreateOutcome) error { recorderCalls++; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range []string{"resume", "relaunch", "create"} {
				var err error
				want := projmux.CodeExecutableInvalid
				switch op {
				case "resume":
					result, e := c.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:agent"})
					err = e
					if result.PaneID != "" {
						t.Fatal("resume created a pane")
					}
				case "relaunch":
					result, e := c.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:agent", Model: "sonnet"})
					err = e
					if result.Stop.Committed || result.Resume.PaneID != "" {
						t.Fatal("relaunch had effects")
					}
				case "create":
					want = projmux.CodeCreateExecutableInvalid
					result, e := c.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "uid:project", Window: "uid:window", Provider: "claude"})
					err = e
					if result.Agent.Metadata.UID != "" {
						t.Fatal("create created an agent")
					}
				}
				var refusal *projmux.Refusal
				if !errors.As(err, &refusal) || refusal.Code != want {
					t.Fatalf("%s: want %s, got %v", op, want, err)
				}
				if binary == filepath.Join(root, "missing") && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s lost stat cause: %v", op, err)
				}
				after, err := os.ReadFile(registryPath)
				if err != nil || string(after) != string(baseline) {
					t.Fatalf("%s changed registry: %v", op, err)
				}
			}
			if recorderCalls != 0 {
				t.Fatalf("admitted %d transactions", recorderCalls)
			}
		})
	}
	if projmux.CodeCreateExecutableInvalid != "create-executable-invalid" || projmux.CodeExecutableInvalid != "executable-invalid" {
		t.Fatal("public code values changed")
	}
}
