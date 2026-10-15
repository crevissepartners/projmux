package agentsessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

func TestReadAdmissionAndProviderRefusal(t *testing.T) {
	root := t.TempDir()
	registry := metadata.NewRegistry()
	registry.Agents = []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent-shell", Name: "shell"}, Spec: metadata.AgentSpec{Provider: "shell"}}}
	loads, states := 0, 0
	service := Service{LoadRegistry: func() (metadata.Registry, error) { loads++; return registry, nil }, StateDir: func() (string, error) { states++; return filepath.Join(root, "absent"), nil }}
	ref, err := selector.ParseRef(metadata.KindAgent, "uid:agent-shell")
	if err != nil {
		t.Fatal(err)
	}
	query := selector.Query{Agents: []selector.Ref{ref}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.ListAgent(ctx, query); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = service.ListProject(ctx, "uid:proj-absent"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if loads != 0 || states != 0 {
		t.Fatal("canceled admission read dependencies")
	}
	_, err = service.ListAgent(context.Background(), query)
	var refused *Failure
	if !errors.As(err, &refused) || refused.Code != ReasonProviderUnsupported || !metadata.IsUsageError(err) {
		t.Fatalf("provider refusal: %v", err)
	}
	if loads != 1 || states != 0 {
		t.Fatal("provider refusal opened history")
	}
	registry.Agents = append(registry.Agents, metadata.Agent{Metadata: metadata.ObjectMeta{UID: "agent-second", Name: "shell"}, Spec: metadata.AgentSpec{Provider: "codex"}})
	named, _ := selector.ParseRef(metadata.KindAgent, "shell")
	_, err = service.ListAgent(context.Background(), selector.Query{Agents: []selector.Ref{named}})
	var failed *resources.Failure
	if !errors.As(err, &failed) || failed.Code != "selector-ambiguous" {
		t.Fatalf("ambiguous selection: %v", err)
	}
	if states != 0 {
		t.Fatal("ambiguous selection opened history")
	}
	if _, err = os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
		t.Fatalf("created missing state: %v", err)
	}
}

func TestMissingHistoryEmptyAndProjectUIDBoundary(t *testing.T) {
	root := t.TempDir()
	registry := metadata.NewRegistry()
	uid, err := metadata.NewUID(metadata.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	agent := metadata.Agent{Metadata: metadata.ObjectMeta{UID: "agent-empty", Name: "empty"}, Spec: metadata.AgentSpec{Provider: "codex"}}
	registry.Agents = []metadata.Agent{agent}
	service := Service{LoadRegistry: func() (metadata.Registry, error) { return registry, nil }, StateDir: func() (string, error) { return filepath.Join(root, "absent"), nil }}
	ref, _ := selector.ParseRef(metadata.KindAgent, "uid:agent-empty")
	listed, err := service.ListAgent(context.Background(), selector.Query{Agents: []selector.Ref{ref}})
	if err != nil || listed.Sessions == nil || len(listed.Sessions) != 0 {
		t.Fatalf("empty Agent: %+v %v", listed, err)
	}
	project, err := service.ListProject(context.Background(), "uid:"+uid)
	if err != nil || project.ProjectUID != uid || project.ProjectName != "" || project.Sessions == nil || len(project.Sessions) != 0 {
		t.Fatalf("empty removed Project: %+v %v", project, err)
	}
	for _, raw := range []string{"missing", "uid:proj-malformed", "uid:agent-empty", "uid:"} {
		if _, err = service.ListProject(context.Background(), raw); err == nil {
			t.Fatalf("accepted removed Project ref %q", raw)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
		t.Fatalf("created missing state: %v", err)
	}
}
