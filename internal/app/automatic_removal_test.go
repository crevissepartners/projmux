package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"io"
	"os"

	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"strings"
	"testing"
)

func TestAutomaticProcessRollbackProtectionAndRecords(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		for _, protected := range []bool{false, true} {
			t.Run(strings.Join([]string{boolName(virtual), boolName(protected)}, "/"), func(t *testing.T) {
				fixture := newFakeResourceStore(t)
				store := fixture.store()
				path := bindDeletionRecords(t, store)
				mutator := fixture.mutator()
				options := coremetadata.CreateAgentOptions{Provider: "codex", Workspace: coremetadata.AgentWorkspace{CWD: "/srv/beta"}}
				var agent coremetadata.Agent
				var pane coremetadata.Pane
				windowUID := "win-beta-main"
				var err error
				if virtual {
					var window coremetadata.Window
					window, agent, pane, err = mutator.CreateProcessWindow(&fixture.registry, "prj-beta", coremetadata.BootstrapWindow{Name: "rollback"}, options, coremetadata.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "operation"})
					windowUID = window.Metadata.UID
				} else {
					agent, err = mutator.CreateAgent(&fixture.registry, windowUID, options)
					if err == nil {
						pane, err = mutator.AttachAgentPane(&fixture.registry, agent.Metadata.UID, coremetadata.BootstrapPane{}, "operation")
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				binding := processhost.Binding{Host: "host", Project: "prj-beta", Window: windowUID, Agent: agent.Metadata.UID, Pane: pane.Metadata.UID, Generation: "generation", Operation: "operation"}
				if !virtual {
					if err = mutator.ReserveProcessBinding(&fixture.registry, metadataProcessBinding(binding)); err != nil {
						t.Fatal(err)
					}
				}
				if protected {
					current, _ := fixture.registry.Agent(agent.Metadata.UID)
					current.Metadata.Labels = map[string]string{coremetadata.LabelProtected: "true"}
				}
				result := processAgentCreateResult{Binding: binding, createdWindow: virtual}
				command := &createCommand{store: store}
				err = command.rollbackProcessAgent(&result)
				_, present := fixture.registry.Agent(agent.Metadata.UID)
				if protected {
					if err == nil || !strings.Contains(err.Error(), "agent-protected") || !present {
						t.Fatalf("protected residual: %v present=%t", err, present)
					}
					retainedAgent, _ := fixture.registry.Agent(agent.Metadata.UID)
					if retainedAgent.Status.Phase != coremetadata.PhaseOffline || retainedAgent.Status.LastTermination != nil {
						t.Fatalf("unstarted retained state: %+v", retainedAgent)
					}
					if len(readDeletionRecords(t, path)) != 0 {
						t.Fatal("protected rollback logged removal")
					}
				} else {
					if err != nil || present {
						t.Fatalf("rollback: %v present=%t", err, present)
					}
					if record := onlyDeletionRecord(t, path); record.Operation != "process-create-rollback" || record.Affected[0].UID != agent.Metadata.UID {
						t.Fatalf("record=%+v", record)
					}
				}
			})
		}
	}
}
func boolName(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestAutomaticPromotionRollbackProtectionAndRecords(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(boolName(protected), func(t *testing.T) {
			fixture := newFakeResourceStore(t)
			store := fixture.store()
			path := bindDeletionRecords(t, store)
			before := fixture.registry.Clone()
			linkage, err := fixture.mutator().LinkAgentPane(&fixture.registry, "win-beta-main", "pan-beta-zsh", coremetadata.LegacyPane{Provider: "claude", LaunchAuthorship: "1", CWD: "/srv/beta"}, coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{}), "promotion")
			if err != nil || !linkage.Linked() {
				t.Fatalf("promotion: %+v %v", linkage, err)
			}
			receipt := promotionRegistryReceipt{before: before, committed: fixture.registry.Clone(), promotions: []resourceAuthorshipPromotion{{PaneUID: "pan-beta-zsh", WindowUID: "win-beta-main", AgentUID: linkage.AgentUID}}}
			if protected {
				agent, _ := fixture.registry.Agent(linkage.AgentUID)
				agent.Metadata.Labels = map[string]string{coremetadata.LabelProtected: "true"}
			}
			kernel := &resourceControllerKernel{store: store}
			err = kernel.rollbackPromotionRegistry(receipt)
			agent, present := fixture.registry.Agent(linkage.AgentUID)
			if protected {
				if err == nil || !strings.Contains(err.Error(), "remaining Offline") || !present || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
					t.Fatalf("residual=%+v %v", agent, err)
				}
				if len(readDeletionRecords(t, path)) != 0 {
					t.Fatal("retained promotion logged removal")
				}
			} else {
				if err != nil || present {
					t.Fatalf("rollback: present=%t %v", present, err)
				}
				if record := onlyDeletionRecord(t, path); record.Operation != "promotion-rollback" || record.Affected[0].UID != linkage.AgentUID {
					t.Fatalf("record=%+v", record)
				}
			}
			if err = fixture.registry.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAutomaticWindowCascadeProtectionAndRecords(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(boolName(protected), func(t *testing.T) {
			fixture, inventory, event := prepareLastBetaProjectCascade(t)
			store := fixture.store()
			path := bindDeletionRecords(t, store)
			if protected {
				agent, _ := fixture.registry.Agent("agt-beta-codex")
				agent.Metadata.Labels = map[string]string{coremetadata.LabelProtected: "true"}
			}
			if _, err := reconcileLifecycle(context.Background(), event, inventory, store); err != nil {
				t.Fatal(err)
			}
			inventory.windows["win-beta-main"] = false
			inventory.windowSessions["$1"] = 0
			unlinked := event
			unlinked.teardownKind = coremetadata.TeardownEventWindowUnlinked
			unlinked.runtimePaneID = ""
			unlinked.runtimeSessionID = "$1"
			unlinked.runtimeWindowID = "@4"
			if _, err := reconcileLifecycle(context.Background(), unlinked, inventory, store); err != nil {
				t.Fatal(err)
			}
			agent, present := fixture.registry.Agent("agt-beta-codex")
			_, windowPresent := fixture.registry.Window("win-beta-main")
			if protected {
				if !present || !windowPresent || agent.Status.Phase != coremetadata.PhaseOffline {
					t.Fatalf("protected cascade: %+v window=%t", agent, windowPresent)
				}
				if _, ok := fixture.registry.Pane(event.receipts[0].PaneUID); ok {
					t.Fatal("ended Pane survived")
				}
				if len(readDeletionRecords(t, path)) != 0 {
					t.Fatal("protected cascade logged removal")
				}
			} else {
				if present || windowPresent {
					t.Fatal("unprotected cascade retained Agent/Window")
				}
				if record := onlyDeletionRecord(t, path); record.Operation != "lifecycle-window-cascade" || record.Affected[0].UID != "agt-beta-codex" {
					t.Fatalf("record=%+v", record)
				}
			}
			rows := len(readDeletionRecords(t, path))
			if _, err := reconcileLifecycle(context.Background(), unlinked, inventory, store); err != nil {
				t.Fatal(err)
			}
			if len(readDeletionRecords(t, path)) != rows {
				t.Fatal("repeat added deletion record")
			}
		})
	}
}

func TestAutomaticProjectFreshProtectionAndRecords(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(boolName(protected), func(t *testing.T) {
			fixture := freshStartFixtureStore(t)
			store := fixture.store()
			path := bindDeletionRecords(t, store)
			if protected {
				agent, _ := fixture.registry.Agent("agt-beta-codex")
				agent.Metadata.Labels = map[string]string{coremetadata.LabelProtected: "true"}
			}
			starter := &registryProjectFreshStarter{resources: store, runner: &projectionMissingSessionRunner{}}
			plan, err := starter.PlanProjectFreshStart("/srv/beta")
			if err != nil {
				t.Fatal(err)
			}
			before := fixture.snapshot()
			_, err = starter.PruneProjectFreshStart(context.Background(), "/srv/beta", plan)
			if protected {
				if err == nil || !strings.Contains(err.Error(), "agent-protected") || fixture.snapshot() != before {
					t.Fatalf("Fresh refusal: %v", err)
				}
				if len(readDeletionRecords(t, path)) != 0 {
					t.Fatal("refused Fresh logged removal")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if record := onlyDeletionRecord(t, path); record.Operation != "project-fresh" || record.Affected[0].UID != "agt-beta-codex" {
					t.Fatalf("record=%+v", record)
				}
			}
		})
	}
}

// Degraded recovery cannot demand label removal that the real mutation gate
// refuses. This pins the recovery escape path against the ordinary mutation gate.
func TestAutomaticDegradedRecoveryCannotRemoveProtectionLabel(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root+"/config")
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	store := intmetadata.NewDefaultStore(paths)
	fixture := newFakeResourceStore(t)
	agent, _ := fixture.registry.Agent("agt-beta-codex")
	agent.Metadata.Labels = map[string]string{coremetadata.LabelProtected: "true"}
	if _, err = store.Update(func(current *coremetadata.Registry) error { *current = fixture.registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	damaged := fixture.registry.Clone()
	damaged.NameReservations = nil
	data, err := json.Marshal(damaged)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.Path(), data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newLabelCommand()
	err = cmd.Run([]string{"agent", "uid:agt-beta-codex", coremetadata.LabelProtected + "-"}, io.Discard, io.Discard)
	if !errors.Is(err, intmetadata.ErrRegistryDegraded) {
		t.Fatalf("real label-removal gate: %v", err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, data) {
		t.Fatal("degraded label removal wrote Registry")
	}
}
