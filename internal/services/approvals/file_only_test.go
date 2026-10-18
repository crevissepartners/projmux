package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
)

func TestFileOnlyClassificationAndSourceErrorOrder(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "antigravity", "future", ""} {
		t.Run(provider, func(t *testing.T) {
			st := agentapproval.NewStore(t.TempDir())
			opens := 0
			a := m.Agent{Metadata: m.ObjectMeta{UID: "agent", Name: "fixture"}, Spec: m.AgentSpec{Provider: provider}}
			s := Service{OpenStore: func() (*agentapproval.Store, error) { opens++; return st, nil }, Answering: func() config.AgentApprovalAnswering { return config.AgentApprovalAnsweringProjmux }}
			list, e := s.ListFileOnly(context.Background(), a)
			mode := "single-read"
			if provider == "claude" {
				mode = "stored"
			}
			if e != nil || list.ReadMode != mode || list.Provider != m.NormalizeProvider(provider) || list.AgentUID != "agent" || list.Requests == nil || opens != 1 {
				t.Fatal(list, e, opens)
			}
			injected := errors.New("open source failure")
			s.OpenStore = func() (*agentapproval.Store, error) { return nil, injected }
			if _, e := s.ListFileOnly(context.Background(), a); !errors.Is(e, injected) {
				t.Fatal("classification hid open error", e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s.OpenStore = func() (*agentapproval.Store, error) { t.Fatal("canceled open"); return nil, nil }
			if _, e := s.ListFileOnly(ctx, a); !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		})
	}
}
func TestFileOnlyStoredProjectionAndUnchangedBytes(t *testing.T) {
	root := t.TempDir()
	st := agentapproval.NewStore(root)
	now := time.Now().UTC()
	for i, state := range []agentapproval.State{agentapproval.StateWaiting, agentapproval.StateAllowed, agentapproval.StateExpired} {
		created, deadline := now, now.Add(time.Hour)
		if i == 2 {
			created, deadline = now.Add(-2*time.Hour), now.Add(-time.Hour)
		}
		_, e := st.Create(agentapproval.Record{ID: []string{"permission-0000000000000001", "permission-0000000000000002", "permission-0000000000000003"}[i], AgentUID: "agent", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"display fixture"}`), State: state, CreatedAt: created, Deadline: deadline})
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e := st.Answer("permission-0000000000000002", "agent", false, agentapproval.ViaCLI); e != nil {
		t.Fatal(e)
	}
	// A later clock proves effective expiration without persisting the projection.
	a := m.Agent{Metadata: m.ObjectMeta{UID: "agent"}, Spec: m.AgentSpec{Provider: "claude"}}
	s := Service{OpenStore: func() (*agentapproval.Store, error) { return st, nil }, Answering: func() config.AgentApprovalAnswering { return config.AgentApprovalAnsweringProjmux }}
	before, e := os.ReadFile(st.Path())
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(st.Path())
	if e != nil {
		t.Fatal(e)
	}
	list, e := s.ListFileOnly(context.Background(), a)
	if e != nil || len(list.Requests) != 1 || list.Answering != "projmux" || string(list.Requests[0].ToolInput) != `{"command":"display fixture"}` {
		t.Fatal(list, e)
	}
	expired := st.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
	s.OpenStore = func() (*agentapproval.Store, error) { return expired, nil }
	list, e = s.ListFileOnly(context.Background(), a)
	if e != nil || len(list.Requests) != 0 || list.ReadMode != "stored" {
		t.Fatal(list, e)
	}
	after, e := os.ReadFile(st.Path())
	if e != nil {
		t.Fatal(e)
	}
	afterInfo, e := os.Stat(st.Path())
	if e != nil {
		t.Fatal(e)
	}
	if string(before) != string(after) || info.Mode() != afterInfo.Mode() || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("file-only modified source")
	}
	if e := os.WriteFile(st.Path(), []byte("malformed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ListFileOnly(context.Background(), a); e == nil {
		t.Fatal("Claude source failure hidden")
	}
	a.Spec.Provider = "codex"
	if list, e := s.ListFileOnly(context.Background(), a); e != nil || list.ReadMode != "single-read" {
		t.Fatal("non-Claude unnecessarily read Claude store", list, e)
	}
}
