package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
)

func TestAgentApprovalListReportsTargetProvider(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, runtime := range []coremetadata.RuntimeKind{coremetadata.RuntimeTmux, coremetadata.RuntimeProcess} {
			t.Run(provider+"/"+string(runtime), func(t *testing.T) {
				cmd, resources, _ := exactControlCLICommand(t)
				agent, _ := resources.registry.Agent("agt-alpha-codex")
				pane, _ := resources.registry.Pane(agent.Status.PaneRef)
				agent.Spec.Provider = provider
				pane.Spec.Runtime.Kind = runtime
				store := agentapproval.NewStore(t.TempDir())
				cmd.approvalStore = func() (*agentapproval.Store, error) { return store, nil }
				cmd.approvalAnswering = func() config.AgentApprovalAnswering { return config.AgentApprovalAnsweringProjmux }
				calls := 0
				cmd.controlCall = func(_ context.Context, _ string, _ coremetadata.CodexEndpointRef, _ codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
					calls++
					if request.Operation != agentControlOpApprovals {
						t.Fatalf("unexpected control operation %q", request.Operation)
					}
					return agentControlResponse{OK: true, Approvals: []agentPendingApproval{}}, nil
				}
				stdout, _, err := runRoute(t, cmd, "approval", "list", codexPermissionAgentRef, "-o", "json")
				if err != nil {
					t.Fatal(err)
				}
				var list agentPermissionList
				if err := json.Unmarshal([]byte(stdout), &list); err != nil {
					t.Fatal(err)
				}
				if list.Provider != agent.Spec.Provider {
					t.Errorf("provider = %q, want target Agent provider %q", list.Provider, agent.Spec.Provider)
				}
				if list.AgentUID != agent.Metadata.UID || list.AgentName != agent.Metadata.Name || list.Answering != "projmux" || list.Requests == nil || len(list.Requests) != 0 {
					t.Fatalf("list = %+v, want unchanged identity/answering and empty requests", list)
				}
				wantCalls := 0
				if provider == aiModeCodex && runtime == coremetadata.RuntimeTmux {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("control calls = %d, want %d", calls, wantCalls)
				}
			})
		}
	}
}
