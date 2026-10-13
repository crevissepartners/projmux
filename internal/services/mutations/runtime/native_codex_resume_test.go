package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func resumeRouteFixture() (NativeCodexRoute, *metadata.AgentSessionRef) {
	route := NativeCodexRoute{Endpoint: metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "codex-0.160.1"}, State: metadata.CodexGenerationCurrent, TUIExecutable: "/fixture/codex", Default: true}
	ref := &metadata.AgentSessionRef{Provider: "codex", Codex: &metadata.CodexSessionRef{ThreadID: "thread", Endpoint: &route.Endpoint, Lifecycle: &metadata.CodexGenerationLifecycleRef{State: metadata.CodexGenerationCurrent}}}
	return route, ref
}
func TestNativeResumeUnknownAndDifferentThreadNeverRetry(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		failure  error
	}{{"unknown", "", errors.New("unknown RPC outcome")}, {"different", "another-thread", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			route, ref := resumeRouteFixture()
			reg := metadata.Registry{Agents: []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent"}, Status: metadata.AgentStatus{SessionRef: ref}}}}
			before := reg.Agents[0].Clone()
			calls := 0
			n := NativeCodexResumeExecution{Spelling: "agent resume", AgentUID: "agent", ConversationID: "thread", Route: route, Resume: func(context.Context, NativeCodexRoute, metadata.AgentWorkspace, string, codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error) {
				calls++
				return codexappserver.ThreadBinding{ThreadID: tc.id}, tc.failure
			}, Plan: func(NativeCodexRoute, metadata.AgentWorkspace, string, string, string) (string, []string, error) {
				t.Fatal("failed resume planned another lane")
				return "", nil, nil
			}}
			_, _, err := n.PrepareLaunch(t.Context(), &reg, metadata.Mutator{}, metadata.Pane{}, SuperviseSpec{}, metadata.AgentWorkspace{}, "", nil)
			if err == nil || calls != 1 || n.ThreadID != "" || !reg.Agents[0].Status.SessionRef.SameConversation(before.Status.SessionRef) {
				t.Fatalf("resume replayed/changed identity: calls=%d err=%v", calls, err)
			}
			if tc.failure != nil && !errors.Is(err, tc.failure) {
				t.Fatal("lost uncertain cause", err)
			}
		})
	}
}
func TestNativeResumeRejectsStoredLifecycleBeforeProbe(t *testing.T) {
	route, ref := resumeRouteFixture()
	ref.Codex.Lifecycle.State = metadata.CodexGenerationRetired
	_, err := ResolveNativeCodexResumeRoute(t.Context(), ref, "uid:agent", func(context.Context, metadata.CodexEndpointRef) (NativeCodexRoute, error) {
		t.Fatal("retired lifecycle probed provider")
		return route, nil
	})
	if NativeThreadReason(err) != NativeCodexReasonGenerationUnavailable {
		t.Fatal(err)
	}
}
func TestNativeResumeRetiredDomainAndSocketBarrier(t *testing.T) {
	current, _ := resumeRouteFixture()
	old := current.Endpoint
	old.EndpointGenerationID = "codex-0.159.0"
	probes := 0
	_, err := ResolveNativeCodexEndpoint(t.Context(), old, "/fixture/state", func(context.Context) (NativeCodexRoute, error) { return current, nil }, func(_ context.Context, socket string) bool {
		probes++
		if !strings.HasPrefix(socket, "/fixture/state/g/") {
			t.Fatal(socket)
		}
		return true
	})
	if NativeThreadReason(err) != NativeCodexReasonRetiredRunning || probes != 1 {
		t.Fatal(err, probes)
	}
	old.StateDomainID = "foreign"
	_, err = ResolveNativeCodexEndpoint(t.Context(), old, "/fixture/state", func(context.Context) (NativeCodexRoute, error) { return current, nil }, func(context.Context, string) bool { t.Fatal("foreign domain dialed retired socket"); return false })
	if NativeThreadReason(err) != NativeCodexReasonRetiredStateDomain {
		t.Fatal(err)
	}
	bound := BindCodexResumeAgentRef(err, "uid:agent-exact")
	if !strings.Contains(bound.Error(), "uid:agent-exact") {
		t.Fatal(bound)
	}
}
