package approvals

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
)

func TestListStoreReportsAgentProvider(t *testing.T) {
	for _, provider := range []string{"claude", "codex", " Codex "} {
		t.Run(provider, func(t *testing.T) {
			store := agentapproval.NewStore(t.TempDir())
			agent := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent", Name: "fixture"}, Spec: coremetadata.AgentSpec{Provider: provider}}
			service := Service{
				OpenStore: func() (*agentapproval.Store, error) { return store, nil },
				Answering: func() config.AgentApprovalAnswering { return config.AgentApprovalAnsweringProjmux },
			}
			assertList := func(wantRequests int) {
				t.Helper()
				list, err := service.ListStore(context.Background(), agent)
				if err != nil {
					t.Fatal(err)
				}
				if want := coremetadata.NormalizeProvider(provider); list.Provider != want {
					t.Errorf("provider = %q, want %q", list.Provider, want)
				}
				if list.AgentUID != agent.Metadata.UID || list.AgentName != agent.Metadata.Name || list.Answering != "projmux" || list.Requests == nil || len(list.Requests) != wantRequests {
					t.Fatalf("list = %+v, want %d requests and unchanged identity/answering", list, wantRequests)
				}
				if wantRequests == 1 && (list.Requests[0].ID != "permission-0000000000000001" || list.Requests[0].ToolName != "Bash" || string(list.Requests[0].ToolInput) != `{"command":"display fixture"}`) {
					t.Fatalf("request projection changed: %+v", list.Requests[0])
				}
			}
			assertList(0)
			now := time.Now().UTC()
			if _, err := store.Create(agentapproval.Record{ID: "permission-0000000000000001", AgentUID: agent.Metadata.UID, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"display fixture"}`), State: agentapproval.StateWaiting, CreatedAt: now, Deadline: now.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			assertList(1)
		})
	}
}
