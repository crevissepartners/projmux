package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type sdkApprovalControl struct {
	f *codexPermissionFixture
	t *testing.T
}

func (s sdkApprovalControl) ListApprovals(ctx context.Context, uid string) ([]core.PendingApproval, error) {
	reg, _ := s.f.command.loadRegistry()
	a, _ := reg.Agent(uid)
	b, err := s.f.command.bindAgentControl("agent approval review", reg, *a)
	if err != nil {
		return nil, err
	}
	response, err := s.f.command.callControl(b, agentControlRequest{Operation: agentControlOpApprovals})
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(response.Approvals)
	var out []core.PendingApproval
	err = json.Unmarshal(data, &out)
	return out, err
}
func (s sdkApprovalControl) ReviewApproval(ctx context.Context, uid, id string, d core.OfferedDecision) error {
	reg, _ := s.f.command.loadRegistry()
	a, _ := reg.Agent(uid)
	b, err := s.f.command.bindAgentControl("agent approval review", reg, *a)
	if err != nil {
		return err
	}
	response, err := s.f.command.callControl(b, agentControlRequest{Operation: agentControlOpReview, RequestKey: id, Decision: string(d)})
	if err != nil {
		return err
	}
	return response.Error()
}
func approvalSDKClient(t *testing.T, reg metadata.Registry, state string, policy config.AgentApprovalAnswering, control projmux.ApprovalControl) *projmux.Client {
	t.Helper()
	dir := t.TempDir()
	paths := config.Paths{ConfigDir: dir, StateDir: state}
	if err := os.MkdirAll(filepath.Dir(store.PathFor(state)), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(store.PathFor(state), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgentApprovalAnsweringFile(), []byte(string(policy)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir, StateDir: state, ApprovalControl: control})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func codexApprovalSDK(t *testing.T, f *codexPermissionFixture) *projmux.Client {
	t.Helper()
	reg, _ := f.command.loadRegistry()
	state := t.TempDir()
	f.approvals = agentapproval.NewStore(state).WithClock(func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) })
	return approvalSDKClient(t, reg, state, f.answering, sdkApprovalControl{f, t})
}
func TestApprovalSDKCodexListParity(t *testing.T) {
	f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringClaude, codexCommandApproval("7"), codexCommandApprovalNoDecline("8"), codexPermissionsApproval("9"), codexFileChangeApproval("10", true), codexCommandApproval("11"), codexCommandApproval("11"))
	client := codexApprovalSDK(t, f)
	out, _, err := runRoute(t, f.command, "approval", "list", codexPermissionAgentRef, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := client.ListApprovals(context.Background(), codexPermissionAgentRef)
	if err != nil {
		t.Fatal(err)
	}
	if questionJSON(t, v) != out {
		t.Fatalf("projection differs\n%s\n%s", questionJSON(t, v), out)
	}
	if got := v.Requests[0].OfferedDecisions(); !reflect.DeepEqual(got, []core.OfferedDecision{"accept", "decline", "cancel"}) {
		t.Fatal("missing offered decisions", got)
	}
	// Projection bytes and nested provider decisions belong to the caller.
	v.Requests[0].ToolInput[0] = 'X'
	again, err := client.ListApprovals(context.Background(), codexPermissionAgentRef)
	if err != nil || questionJSON(t, again) != out {
		t.Fatal("result aliases provider input", err)
	}
}
func TestApprovalSDKCodexAnswerParityAndAuditOrder(t *testing.T) {
	for _, test := range []struct {
		name    string
		allow   bool
		pending agentPendingApproval
	}{{"allow", true, codexCommandApproval("7")}, {"deny", false, codexCommandApproval("7")}, {"cancel", false, codexCommandApprovalNoDecline("7")}} {
		t.Run(test.name, func(t *testing.T) {
			cli := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, test.pending)
			sdk := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, test.pending)
			client := codexApprovalSDK(t, sdk)
			_ = codexApprovalSDK(t, cli)
			for _, f := range []*codexPermissionFixture{cli, sdk} {
				f.onReview = func(r agentControlRequest) {
					raw, err := os.ReadFile(f.approvals.AuditPath())
					if err != nil || !strings.Contains(string(raw), "decision="+r.Decision) {
						t.Fatal("review preceded audit", err, string(raw))
					}
				}
			}
			flag := "--deny"
			if test.allow {
				flag = "--allow"
			}
			out, _, err := runRoute(t, cli.command, "approval", "answer", codexPermissionAgentRef, "7", flag, "--via", "dashboard")
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := client.AnswerApproval(context.Background(), projmux.AnswerApprovalRequest{Agent: codexPermissionAgentRef, RequestID: "7", Allow: test.allow, Via: "dashboard"})
			if err != nil {
				t.Fatal(err)
			}
			a, _ := os.ReadFile(cli.approvals.AuditPath())
			b, _ := os.ReadFile(sdk.approvals.AuditPath())
			var al, bl agentapproval.AuditLine
			if json.Unmarshal(a, &al) != nil || json.Unmarshal(b, &bl) != nil || al.At.IsZero() || bl.At.IsZero() || al.DecidedAt != al.At || bl.DecidedAt != bl.At {
				t.Fatal("invalid audit timestamp")
			}
			al.At, bl.At, al.DecidedAt, bl.DecidedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
			if !reflect.DeepEqual(al, bl) {
				t.Fatalf("audit differs %s %s", a, b)
			}
			if !reflect.DeepEqual(cli.reviews(), sdk.reviews()) || !strings.Contains(out, "decision "+string(receipt.Decision)) {
				t.Fatalf("control mismatch %v %v %s", cli.reviews(), sdk.reviews(), out)
			}
		})
	}
}
func TestApprovalSDKReviewParityAndOfferedOnly(t *testing.T) {
	for _, policy := range []config.AgentApprovalAnswering{config.AgentApprovalAnsweringClaude, config.AgentApprovalAnsweringProjmux} {
		f := newCodexPermissionFixture(t, policy, codexPermissionsApproval("7"))
		client := codexApprovalSDK(t, f)
		f.command.controlPicker = &scriptedAgentControlPicker{answers: []intpicker.Result{{Value: "decision:grant-turn"}}}
		out, _, err := runRoute(t, f.command, "approval", "review", codexPermissionAgentRef, "--request", "7")
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.ReviewApproval(context.Background(), projmux.ReviewApprovalRequest{Agent: codexPermissionAgentRef, RequestID: "7", Decision: "grant-turn"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.reviews()[0], f.reviews()[1]) || out != `approval resolved request="7" decision="grant-turn"`+"\n" {
			t.Fatal("review parity", out, f.reviews())
		}
		if _, err := os.Stat(f.approvals.AuditPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("review added audit", err)
		}
		_, err = client.ReviewApproval(context.Background(), projmux.ReviewApprovalRequest{Agent: codexPermissionAgentRef, RequestID: "7", Decision: "accept"})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodePermissionDecisionUnavailable || len(f.reviews()) != 2 {
			t.Fatal("unoffered decision sent", err)
		}
	}
}
func TestApprovalSDKRefusalParity(t *testing.T) {
	for _, test := range []struct {
		name, id, via string
		policy        config.AgentApprovalAnswering
		pending       agentPendingApproval
		code          projmux.Code
	}{
		{"off", "7", "cli", config.AgentApprovalAnsweringClaude, codexCommandApproval("7"), projmux.CodePermissionAnsweringOff},
		{"missing", "8", "cli", config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"), projmux.CodePermissionNotPending},
		{"ambiguous", "7", "cli", config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"), projmux.CodePermissionNotPending},
		{"no-decision", "7", "cli", config.AgentApprovalAnsweringProjmux, codexPermissionsApproval("7"), projmux.CodePermissionDecisionUnavailable},
		{"via", "7", "bad/name", config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"), projmux.CodeOperatorClientInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCodexPermissionFixture(t, test.policy, test.pending)
			if test.name == "ambiguous" {
				f.pending = append(f.pending, test.pending)
			}
			client := codexApprovalSDK(t, f)
			_, _, cliErr := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, test.id, "--allow", "--via", test.via)
			_, err := client.AnswerApproval(context.Background(), projmux.AnswerApprovalRequest{Agent: codexPermissionAgentRef, RequestID: test.id, Allow: true, Via: test.via})
			var refusal *projmux.Refusal
			if cliErr == nil || !errors.As(err, &refusal) || refusal.Code != test.code || err.Error() != cliErr.Error() || publicRouteArgvExitCode(cliErr) != 2 || len(f.reviews()) != 0 {
				t.Fatalf("refusal mismatch cli=%v sdk=%v code=%+v", cliErr, err, refusal)
			}
		})
	}
}
func TestApprovalSDKStoreParityAndIsolation(t *testing.T) {
	for _, allow := range []bool{true, false} {
		reg, _ := sdkResourceFixture(t)
		a := &reg.Agents[0]
		a.Spec.Provider = "claude"
		a.Status.PaneRef = "pan-alpha-zsh"
		a.Status.Phase = metadata.PhaseOffline
		state := t.TempDir()
		client := approvalSDKClient(t, reg, state, config.AgentApprovalAnsweringProjmux, nil)
		st := agentapproval.NewStore(state)
		record, err := st.Create(agentapproval.Record{ID: "permission-0000000000000001", AgentUID: a.Metadata.UID, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"echo <ok>"}`), Deadline: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }, approvalStore: func() (*agentapproval.Store, error) { return st, nil }, approvalAnswering: func() config.AgentApprovalAnswering { return config.AgentApprovalAnsweringProjmux }, now: time.Now}
		var out, stderr bytes.Buffer
		if err := cmd.runPermissionApproval([]string{"list", "reader", "-o", "json"}, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		listed, err := client.ListApprovals(context.Background(), "reader")
		if err != nil || out.String() != questionJSON(t, listed) {
			t.Fatal("stored list mismatch", err, out.String(), questionJSON(t, listed))
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := client.AnswerApproval(ctx, projmux.AnswerApprovalRequest{Agent: "reader", RequestID: record.ID, Allow: allow}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		current, _, _ := st.Get(record.ID)
		if current.State != agentapproval.StateWaiting {
			t.Fatal("cancel wrote")
		}
		other := approvalSDKClient(t, reg, t.TempDir(), config.AgentApprovalAnsweringProjmux, nil)
		empty, err := other.ListApprovals(context.Background(), "reader")
		if err != nil || len(empty.Requests) != 0 {
			t.Fatal("client root leak", err)
		}
		result, err := client.AnswerApproval(context.Background(), projmux.AnswerApprovalRequest{Agent: "reader", RequestID: record.ID, Allow: allow})
		if err != nil {
			t.Fatal(err)
		}
		current, _, _ = st.Get(record.ID)
		if result.State != string(current.State) || current.Via != "cli" {
			t.Fatal("stored answer mismatch", result, current)
		}
		_, err = client.AnswerApproval(context.Background(), projmux.AnswerApprovalRequest{Agent: "reader", RequestID: record.ID, Allow: allow})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodePermissionNotPending {
			t.Fatal("second answer", err)
		}
	}
}

func TestApprovalSDKNativeBindingUsesSameFrameAndFence(t *testing.T) {
	cmd, resources, _ := exactControlCLICommand(t)
	reg := resources.registry
	agent, _ := reg.Agent("agt-alpha-codex")
	paths, err := cmd.controlPaths()
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	transport := resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: filepath.Join(t.TempDir(), "exact.sock")}
	runner := approvalTestRunner(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != transport.Value {
			t.Fatal("unrouted read", name, args)
		}
		if args[2] == "list-panes" {
			return []byte("pan-alpha-codex\x1f%7\n"), nil
		}
		if args[2] == "display-message" {
			if !strings.Contains(args[len(args)-1], "@projmux_ai_thread_id") {
				t.Fatal("wrong thread option", args)
			}
			return []byte("%7\x1fpan-alpha-codex\x1fthread-1\x1fprovider-control-plane\x1fepoch-1\x1f\n"), nil
		}
		t.Fatal("unexpected read", args)
		return nil, nil
	})
	client := agentcontrol.Client{StateDir: paths.StateDir, Transport: transport, Runner: runner, LoadRegistry: func() (metadata.Registry, error) { return reg, nil }}
	sdk, err := client.Bind(context.Background(), reg, *agent)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := cmd.bindAgentControl("agent approval review", reg, *agent)
	if err != nil || !reflect.DeepEqual(sdk, serviceApprovalBinding(cli)) || len(calls) != 2 {
		t.Fatal("binding differs", sdk, cli, err)
	}
	changed := reg.Clone()
	a, _ := changed.Agent(agent.Metadata.UID)
	a.Status.SessionRef.Codex = nil
	if err := agentcontrol.ValidateConsumer(changed, sdk); err == nil {
		t.Fatal("stale consumer admitted")
	}
}

type approvalTestRunner func(context.Context, string, ...string) ([]byte, error)

func (r approvalTestRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r(ctx, name, args...)
}

func TestApprovalSDKStoreRefusalParity(t *testing.T) {
	for _, test := range []struct {
		name string
		code projmux.Code
	}{{"missing", projmux.CodePermissionNotFound}, {"answered", projmux.CodePermissionNotPending}, {"closed", projmux.CodePermissionNotPending}, {"expired", projmux.CodePermissionExpired}, {"off", projmux.CodePermissionAnsweringOff}, {"provider", projmux.CodePermissionProviderUnsupported}} {
		t.Run(test.name, func(t *testing.T) {
			reg, _ := sdkResourceFixture(t)
			agent := &reg.Agents[0]
			agent.Spec.Provider = "claude"
			if test.name == "provider" {
				agent.Spec.Provider = "gemini"
			}
			agent.Status.PaneRef = "pan-alpha-zsh"
			agent.Status.Phase = metadata.PhaseOffline
			policy := config.AgentApprovalAnsweringProjmux
			if test.name == "off" {
				policy = config.AgentApprovalAnsweringClaude
			}
			state := t.TempDir()
			client := approvalSDKClient(t, reg, state, policy, nil)
			st := agentapproval.NewStore(state)
			id := "permission-0000000000000001"
			if test.name != "missing" {
				now := time.Now()
				created, deadline := now, now.Add(time.Hour)
				if test.name == "expired" {
					created, deadline = now.Add(-time.Hour), now.Add(-time.Minute)
				}
				_, err := st.Create(agentapproval.Record{ID: id, AgentUID: agent.Metadata.UID, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"true"}`), CreatedAt: created, Deadline: deadline})
				if err != nil {
					t.Fatal(err)
				}
				if test.name == "answered" {
					if _, err := st.Answer(id, agent.Metadata.UID, true, "cli"); err != nil {
						t.Fatal(err)
					}
				}
				if test.name == "closed" {
					if _, err := st.Close(id, "fixture"); err != nil {
						t.Fatal(err)
					}
				}
			}
			cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }, approvalStore: func() (*agentapproval.Store, error) { return st, nil }, approvalAnswering: func() config.AgentApprovalAnswering { return policy }, now: time.Now}
			var out, stderr bytes.Buffer
			cliErr := cmd.runPermissionApproval([]string{"answer", "reader", id, "--allow"}, &out, &stderr)
			_, err := client.AnswerApproval(context.Background(), projmux.AnswerApprovalRequest{Agent: "reader", RequestID: id, Allow: true})
			var refusal *projmux.Refusal
			if cliErr == nil || !errors.As(err, &refusal) || refusal.Code != test.code || cliErr.Error() != err.Error() || publicRouteArgvExitCode(cliErr) != 2 {
				t.Fatalf("store refusal differs cli=%v sdk=%v refusal=%+v", cliErr, err, refusal)
			}
		})
	}
}

func TestApprovalSDKReviewMissingAndAmbiguous(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringClaude, codexCommandApproval("7"))
		id := "8"
		if duplicate {
			f.pending = append(f.pending, codexCommandApproval("7"))
			id = "7"
		}
		client := codexApprovalSDK(t, f)
		_, _, cliErr := runRoute(t, f.command, "approval", "review", codexPermissionAgentRef, "--request", id)
		_, err := client.ReviewApproval(context.Background(), projmux.ReviewApprovalRequest{Agent: codexPermissionAgentRef, RequestID: id, Decision: "accept"})
		var refusal *projmux.Refusal
		if cliErr == nil || !errors.As(err, &refusal) || refusal.Code != projmux.CodePermissionNotPending || cliErr.Error() != err.Error() || len(f.reviews()) != 0 {
			t.Fatalf("review refusal differs cli=%v sdk=%v", cliErr, err)
		}
	}
}

func TestApprovalSDKReviewUnsupportedProvider(t *testing.T) {
	for _, provider := range []string{"claude", "gemini"} {
		f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringClaude)
		reg, _ := f.command.loadRegistry()
		a, _ := reg.Agent("agt-alpha-codex")
		a.Spec.Provider = provider
		f.command.loadRegistry = func() (metadata.Registry, error) { return reg, nil }
		client := codexApprovalSDK(t, f)
		_, _, cliErr := runRoute(t, f.command, "approval", "review", codexPermissionAgentRef, "--request", "7")
		_, err := client.ReviewApproval(context.Background(), projmux.ReviewApprovalRequest{Agent: codexPermissionAgentRef, RequestID: "7", Decision: "accept"})
		var refusal *projmux.Refusal
		if cliErr == nil || !errors.As(err, &refusal) || refusal.Code != projmux.CodePermissionProviderUnsupported || cliErr.Error() != err.Error() || len(f.calls) != 0 {
			t.Fatalf("review provider differs cli=%v sdk=%v", cliErr, err)
		}
	}
}
