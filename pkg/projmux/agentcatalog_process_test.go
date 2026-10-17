package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestCapabilitiesSDKProcessWaitUsesRegistryEvidence(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			for _, current := range []bool{true, false} {
				state := t.TempDir()
				storage := store.NewStore(store.PathFor(state))
				var uid string
				_, err := storage.Update(func(registry *m.Registry) error {
					mutator := store.DefaultMutator()
					created, err := mutator.RegisterProject(registry, m.RegisterProjectOptions{
						Root: t.TempDir(), Name: "catalog-process", OperationID: "register-fixture",
					})
					if err != nil {
						return err
					}
					_, agent, pane, err := mutator.CreateProcessWindow(registry, created.Project.Metadata.UID, m.BootstrapWindow{
						Name: "catalog-window",
					}, m.CreateAgentOptions{Name: "catalog-agent", Provider: provider, OperationID: "create-fixture"}, m.ProcessBinding{
						HostInstanceID: "fixture-host", Generation: "fixture-generation", OperationID: "fixture-operation",
					})
					if err != nil {
						return err
					}
					uid = agent.Metadata.UID
					if !current {
						return nil
					}
					return mutator.RecordProcessChild(registry, m.ProcessActivation{
						Binding:     pane.Status.ProcessSession.Binding,
						HostProcess: m.ProcessIdentity{PID: 11, OwnerUID: 1000, Start: "fixture-host-birth"},
						Child:       m.ProcessIdentity{PID: 12, OwnerUID: 1000, Start: "fixture-child-birth"},
					})
				})
				if err != nil {
					t.Fatal(err)
				}
				client, err := NewClient(Options{HomeDir: state, ConfigDir: filepath.Join(state, "config"), StateDir: state})
				if err != nil {
					t.Fatal(err)
				}
				path := store.PathFor(state)
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				request := CapabilitiesRequest{Agent: "uid:" + uid}
				projection, err := client.Capabilities(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				if projection.Runtime == nil || projection.Runtime.Ready != current || projection.Runtime.LiveVerified || projection.Runtime.Evidence != "registry" || projection.Runtime.PaneRuntimeID != "" {
					t.Fatalf("SDK runtime: %+v", projection.Runtime)
				}
				found := false
				for _, entry := range projection.Capabilities {
					if entry.Action == "wait.idle" {
						found = true
						if entry.Available == nil || *entry.Available != current || entry.Evidence != "registry" {
							t.Fatalf("SDK wait projection: %+v", entry)
						}
					}
				}
				if !found {
					t.Fatal("SDK wait capability missing")
				}
				raw, err := json.Marshal(projection)
				if err != nil || strings.Contains(string(raw), `"paneRuntimeID"`) {
					t.Fatalf("SDK invented tmux ID: %s %v", raw, err)
				}
				projection.Runtime.Ready = !current
				for i := range projection.Capabilities {
					if projection.Capabilities[i].Available != nil {
						*projection.Capabilities[i].Available = !current
					}
				}
				fresh, err := client.Capabilities(context.Background(), request)
				if err != nil || fresh.Runtime == nil || fresh.Runtime.Ready != current {
					t.Fatalf("SDK result aliases runtime: %+v %v", fresh, err)
				}
				for _, entry := range fresh.Capabilities {
					if entry.Action == "wait.idle" && (entry.Available == nil || *entry.Available != current) {
						t.Fatalf("SDK result aliases wait availability: %+v", entry)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := client.Capabilities(ctx, request); !errors.Is(err, context.Canceled) {
					t.Fatalf("SDK cancellation: %v", err)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(before) {
					t.Fatalf("SDK capability read changed Registry: %v", err)
				}
			}
		})
	}
}
