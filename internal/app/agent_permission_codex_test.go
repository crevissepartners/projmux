package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const codexPermissionAgentRef = "uid:agt-alpha-codex"

// codexPermissionFixture is the exact native control fixture with an
// approval store, an answering setting, and a scripted broker that records
// every control call.
type codexPermissionFixture struct {
	command   *agentCommand
	approvals *agentapproval.Store
	answering config.AgentApprovalAnswering
	pending   []agentPendingApproval
	calls     []agentControlRequest
	// onReview runs inside the fake review call, before it answers.
	onReview func(agentControlRequest)
	// reviewErr and reviewResponse script the review call's outcome.
	reviewErr      error
	reviewResponse *agentControlResponse
}

func newCodexPermissionFixture(t *testing.T, answering config.AgentApprovalAnswering, pending ...agentPendingApproval) *codexPermissionFixture {
	t.Helper()
	cmd, _, _ := exactControlCLICommand(t)
	f := &codexPermissionFixture{command: cmd, approvals: agentapproval.NewStore(t.TempDir()), answering: answering, pending: pending}
	cmd.approvalStore = func() (*agentapproval.Store, error) { return f.approvals, nil }
	cmd.approvalAnswering = func() config.AgentApprovalAnswering { return f.answering }
	cmd.controlCall = func(_ context.Context, _ string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
		if !endpoint.Same(phase6CLIEndpoint()) || identity != phase6CLIIdentity() || request.Identity != identity || request.Epoch != "epoch-1" {
			t.Fatalf("control binding endpoint=%+v identity=%+v request=%+v", endpoint, identity, request)
		}
		f.calls = append(f.calls, request)
		switch request.Operation {
		case agentControlOpApprovals:
			return agentControlResponse{OK: true, Approvals: f.pending}, nil
		case agentControlOpReview:
			if f.onReview != nil {
				f.onReview(request)
			}
			if f.reviewErr != nil {
				return agentControlResponse{}, f.reviewErr
			}
			if f.reviewResponse != nil {
				return *f.reviewResponse, nil
			}
			return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
		default:
			t.Fatalf("unexpected control operation %q", request.Operation)
			return agentControlResponse{}, nil
		}
	}
	return f
}

func (f *codexPermissionFixture) ops() []string {
	ops := []string{}
	for _, call := range f.calls {
		ops = append(ops, call.Operation)
	}
	return ops
}

func (f *codexPermissionFixture) reviews() []agentControlRequest {
	return slices.DeleteFunc(slices.Clone(f.calls), func(r agentControlRequest) bool { return r.Operation != agentControlOpReview })
}

func codexCommandApproval(id string) agentPendingApproval {
	return agentPendingApproval{
		RequestID: id, Kind: codexappserver.ApprovalCommand, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-" + id,
		Command: "make test", CWD: "/work", Reason: "run the tests",
		Decisions: []codexappserver.ApprovalDecision{codexappserver.DecisionAccept, codexappserver.DecisionDecline, codexappserver.DecisionCancel},
	}
}

// codexCommandApprovalNoDecline is the command approval shape real Codex
// 0.157.1 advertises: accept and cancel, with no decline (its exec-policy
// amendment offer is not a decision projmux sends and is filtered out).
func codexCommandApprovalNoDecline(id string) agentPendingApproval {
	p := codexCommandApproval(id)
	p.Decisions = []codexappserver.ApprovalDecision{codexappserver.DecisionAccept, codexappserver.DecisionCancel}
	return p
}

func codexFileChangeApproval(id string, grantRoot bool) agentPendingApproval {
	p := agentPendingApproval{
		RequestID: id, Kind: codexappserver.ApprovalFileChange, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-" + id, Reason: "edit",
		Decisions: []codexappserver.ApprovalDecision{codexappserver.DecisionAccept, codexappserver.DecisionDecline, codexappserver.DecisionCancel},
	}
	if grantRoot {
		root := "/work"
		p.GrantRoot = &root
		p.Decisions = []codexappserver.ApprovalDecision{codexappserver.DecisionDecline, codexappserver.DecisionCancel}
	}
	return p
}

func codexPermissionsApproval(id string) agentPendingApproval {
	return agentPendingApproval{
		RequestID: id, Kind: codexappserver.ApprovalPermissions, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-" + id,
		RequestCWD: "/work", Permissions: json.RawMessage(`{"network":true}`),
		Decisions: []codexappserver.ApprovalDecision{codexappserver.DecisionGrantTurn},
	}
}

// 1. list shows every pending approval with its kind and full details in
// both projections, works while answering is claude, and marks an id that is
// ambiguous across raw request ids as not answerable. It sends no review.
func TestAgentApprovalCodexListShowsKindDetailsAndAmbiguity(t *testing.T) {
	t.Parallel()

	pending := []agentPendingApproval{codexCommandApproval("7"), codexPermissionsApproval("8"), codexFileChangeApproval("9", false), codexFileChangeApproval("9", true), codexCommandApprovalNoDecline("11"), codexFileChangeApproval("12", true)}
	f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringClaude, pending...)

	stdout, _, err := runRoute(t, f.command, "approval", "list", codexPermissionAgentRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"agent/codex approvals answering claude\n",
		"7\twaiting\ttool command\tanswers allow,deny(decline: the turn continues)\n  input: {",
		"11\twaiting\ttool command\tanswers allow,deny(cancel: the turn stops)\n",
		"12\twaiting\ttool file-change\tanswers deny(decline: the turn continues)\n",
		`"command":"make test"`, `"cwd":"/work"`, `"decisions":["accept","decline","cancel"]`,
		"8\twaiting\ttool permissions\tanswers none; answer with agent approval review\n",
		`"permissions":{"network":true}`, `"requestCwd":"/work"`,
		"9\twaiting\ttool file-change\tambiguous; answer with agent approval review\n",
		codexPermissionListNote,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("list text missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "created ") || strings.Contains(stdout, "deadline ") || strings.Contains(stdout, agentPermissionListNote) {
		t.Fatalf("Codex list text carries Claude-only fields:\n%s", stdout)
	}

	stdout, _, err = runRoute(t, f.command, "approval", "list", codexPermissionAgentRef, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Provider  string `json:"provider"`
		AgentUID  string `json:"agentUID"`
		Answering string `json:"answering"`
		Requests  []map[string]json.RawMessage
	}
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("json = %q: %v", stdout, err)
	}
	if list.Provider != "codex" || list.AgentUID != "agt-alpha-codex" || list.Answering != "claude" || len(list.Requests) != 6 {
		t.Fatalf("list = %+v", list)
	}
	first := list.Requests[0]
	if string(first["id"]) != `"7"` || string(first["state"]) != `"waiting"` || string(first["toolName"]) != `"command"` ||
		string(first["answerable"]) != "true" || string(first["answers"]) != `["allow","deny"]` || string(first["denyDecision"]) != `"decline"` {
		t.Fatalf("first request = %s", stdout)
	}
	if _, ok := first["createdAt"]; ok {
		t.Fatalf("Codex record carries createdAt: %s", stdout)
	}
	if _, ok := first["deadline"]; ok {
		t.Fatalf("Codex record carries deadline: %s", stdout)
	}
	var input map[string]any
	if err := json.Unmarshal(first["toolInput"], &input); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"threadId", "turnId", "itemId", "command", "cwd", "reason", "decisions"} {
		if _, ok := input[key]; !ok {
			t.Fatalf("toolInput lacks %s: %s", key, first["toolInput"])
		}
	}
	if _, ok := input["requestId"]; ok {
		t.Fatalf("toolInput repeats requestId: %s", first["toolInput"])
	}
	for _, index := range []int{1, 2, 3} {
		if string(list.Requests[index]["answerable"]) != "false" {
			t.Fatalf("request %d answerable = %s", index, list.Requests[index]["answerable"])
		}
		if _, ok := list.Requests[index]["denyDecision"]; ok {
			t.Fatalf("request %d carries denyDecision: %s", index, stdout)
		}
	}
	if noDecline := list.Requests[4]; string(noDecline["denyDecision"]) != `"cancel"` || string(noDecline["answers"]) != `["allow","deny"]` {
		t.Fatalf("accept+cancel request = %s", stdout)
	}
	if grantRoot := list.Requests[5]; string(grantRoot["denyDecision"]) != `"decline"` || string(grantRoot["answers"]) != `["deny"]` || string(grantRoot["answerable"]) != "true" {
		t.Fatalf("grant-root request = %s", stdout)
	}
	if !slices.Equal(f.ops(), []string{agentControlOpApprovals, agentControlOpApprovals}) {
		t.Fatalf("operations = %v", f.ops())
	}
	if lines := readPermissionAudit(t, f.approvals); len(lines) != 0 {
		t.Fatalf("list wrote audit lines %v", lines)
	}
}

// 2. With answering projmux, --allow sends exactly one accept, and --deny
// exactly one decline, or cancel when decline is not offered (real Codex
// 0.157.1 command approvals). The answer line names the decision sent.
func TestAgentApprovalCodexAnswerSendsOneAcceptDeclineOrCancel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		pending  agentPendingApproval
		flag     string
		decision string
		line     string
	}{
		{name: "allow", pending: codexCommandApproval("7"), flag: "--allow", decision: "accept", line: "7 allowed for agent/codex (decision accept)\n"},
		{name: "deny declines", pending: codexCommandApproval("7"), flag: "--deny", decision: "decline", line: "7 denied for agent/codex (decision decline; the turn continues)\n"},
		{name: "deny cancels without decline", pending: codexCommandApprovalNoDecline("7"), flag: "--deny", decision: "cancel", line: "7 denied for agent/codex (decision cancel; the turn stops)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, test.pending, codexCommandApproval("8"))
			stdout, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", test.flag)
			if err != nil || stdout != test.line {
				t.Fatalf("answer = %q, %v", stdout, err)
			}
			reviews := f.reviews()
			if len(reviews) != 1 || reviews[0].RequestKey != "7" || reviews[0].Decision != test.decision {
				t.Fatalf("reviews = %+v", reviews)
			}
		})
	}
}

// 3. With answering claude an answer is refused before any binding lookup or
// broker call, and writes no audit line.
func TestAgentApprovalCodexAnswerRefusedWhileAnsweringIsClaude(t *testing.T) {
	t.Parallel()

	f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringClaude, codexCommandApproval("7"))
	binding := f.command.controlBinding.(*staticAgentControlBinding)
	binding.err = errors.New("the binding must not be read")
	_, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--allow")
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "(permission-answering-off)") ||
		!strings.Contains(err.Error(), "nothing was changed") || strings.Contains(err.Error(), "has no captured permission requests") {
		t.Fatalf("answer err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("broker calls = %v", f.ops())
	}
	if _, err := os.Stat(f.approvals.AuditPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audit log exists: %v", err)
	}
}

// 4. An unknown or ambiguous id is not pending, and a decision the request
// does not offer is unavailable; none of them reviews or audits anything.
func TestAgentApprovalCodexAnswerRefusalsSendAndAuditNothing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		id     string
		flag   string
		reason string
	}{
		{name: "unknown", id: "404", flag: "--allow", reason: permissionReasonNotPending},
		{name: "ambiguous", id: "9", flag: "--deny", reason: permissionReasonNotPending},
		{name: "permissions allow", id: "8", flag: "--allow", reason: permissionReasonDecisionUnavailable},
		{name: "permissions deny", id: "8", flag: "--deny", reason: permissionReasonDecisionUnavailable},
		{name: "grant root allow", id: "10", flag: "--allow", reason: permissionReasonDecisionUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux,
				codexCommandApproval("7"), codexPermissionsApproval("8"), codexCommandApproval("9"), codexFileChangeApproval("9", false), codexFileChangeApproval("10", true))
			_, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, test.id, test.flag)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "("+test.reason+")") || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("err = %v, want %s", err, test.reason)
			}
			if test.reason == permissionReasonDecisionUnavailable && !strings.Contains(err.Error(), "agent approval review") {
				t.Fatalf("err = %v, want it to point at review", err)
			}
			if len(f.reviews()) != 0 {
				t.Fatalf("reviews = %+v", f.reviews())
			}
			if _, err := os.Stat(f.approvals.AuditPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("audit log exists: %v", err)
			}
		})
	}
}

// 5. Over every kind and both flags, the values ever sent are accept,
// decline, and cancel, never grant-turn: --allow sends accept, --deny sends
// decline when offered and otherwise cancel, and a request offering neither
// is refused with no review.
func TestAgentApprovalCodexAnswerNeverSendsGrantTurn(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		pending agentPendingApproval
		allow   string // decision --allow sends; empty = unavailable
		deny    string // decision --deny sends; empty = unavailable
	}{
		{name: "command accept decline cancel", pending: codexCommandApproval("1"), allow: "accept", deny: "decline"},
		{name: "command accept cancel (codex 0.157.1)", pending: codexCommandApprovalNoDecline("1"), allow: "accept", deny: "cancel"},
		{name: "file-change", pending: codexFileChangeApproval("1", false), allow: "accept", deny: "decline"},
		{name: "file-change grant root", pending: codexFileChangeApproval("1", true), deny: "decline"},
		{name: "permissions", pending: codexPermissionsApproval("1")},
	} {
		for _, flag := range []string{"--allow", "--deny"} {
			t.Run(test.name+flag, func(t *testing.T) {
				f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, test.pending)
				_, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "1", flag)
				want := test.deny
				if flag == "--allow" {
					want = test.allow
				}
				for _, review := range f.reviews() {
					if !slices.Contains([]string{"accept", "decline", "cancel"}, review.Decision) {
						t.Fatalf("sent decision %q", review.Decision)
					}
				}
				if want == "" {
					if err == nil || !strings.Contains(err.Error(), "("+permissionReasonDecisionUnavailable+")") || len(f.reviews()) != 0 {
						t.Fatalf("err = %v reviews = %+v, want permission-decision-unavailable and no review", err, f.reviews())
					}
					return
				}
				if err != nil || len(f.reviews()) != 1 || f.reviews()[0].Decision != want {
					t.Fatalf("err = %v reviews = %+v, want one %s", err, f.reviews(), want)
				}
			})
		}
	}
}

// 6. The allowed or denied line is on disk before the review call; an audit
// failure sends nothing and names the log; a review failure keeps the line.
func TestAgentApprovalCodexAnswerAuditsBeforeTheReview(t *testing.T) {
	t.Parallel()

	t.Run("line first", func(t *testing.T) {
		f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"))
		var seen []agentapproval.AuditLine
		f.onReview = func(agentControlRequest) { seen = readPermissionAudit(t, f.approvals) }
		if _, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--deny", "--via", "web"); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 {
			t.Fatalf("audit at review time = %+v, want one line", seen)
		}
		line := seen[0]
		if line.Event != agentapproval.AuditDenied || line.RequestID != "7" || line.AgentUID != "agt-alpha-codex" || line.PaneUID != "pan-alpha-codex" ||
			line.ToolName != "command" || line.Input != "make test" || line.Via != agentapproval.ViaWeb || line.Reason != "decision=decline" || line.DecidedAt.IsZero() || line.At.IsZero() {
			t.Fatalf("audit line = %+v", line)
		}
		if lines := readPermissionAudit(t, f.approvals); len(lines) != 1 {
			t.Fatalf("audit after = %+v", lines)
		}
	})

	t.Run("cancel fallback is the decision in the line", func(t *testing.T) {
		f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApprovalNoDecline("7"))
		if _, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--deny"); err != nil {
			t.Fatal(err)
		}
		lines := readPermissionAudit(t, f.approvals)
		if len(lines) != 1 || lines[0].Event != agentapproval.AuditDenied || lines[0].Reason != "decision=cancel" {
			t.Fatalf("audit = %+v", lines)
		}
	})

	t.Run("summary of a non-command kind is bounded key names", func(t *testing.T) {
		f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexFileChangeApproval("7", false))
		if _, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--allow"); err != nil {
			t.Fatal(err)
		}
		lines := readPermissionAudit(t, f.approvals)
		if len(lines) != 1 || lines[0].Event != agentapproval.AuditAllowed || lines[0].ToolName != "file-change" ||
			lines[0].Input != "keys: decisions,itemId,reason,threadId,turnId" || lines[0].Reason != "decision=accept" {
			t.Fatalf("audit = %+v", lines)
		}
	})

	t.Run("audit failure sends nothing", func(t *testing.T) {
		f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"))
		blockPermissionAudit(t, f.approvals)
		stdout, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--allow")
		if stdout != "" || err == nil || publicRouteArgvExitCode(err) == 0 || !errors.Is(err, agentapproval.ErrAudit) ||
			!strings.Contains(err.Error(), f.approvals.AuditPath()) || !strings.Contains(err.Error(), "audit log") || !strings.Contains(err.Error(), "still waiting") {
			t.Fatalf("answer = %q, %v", stdout, err)
		}
		if len(f.reviews()) != 0 {
			t.Fatalf("reviews = %+v", f.reviews())
		}
	})

	for _, test := range []struct {
		name     string
		err      error
		response *agentControlResponse
	}{
		{name: "review transport failure keeps the line", err: errors.New("broker went away")},
		{name: "review refusal keeps the line", response: &agentControlResponse{OK: false, Code: "approval-stale", Message: "request already resolved"}},
		{name: "indeterminate response keeps the line", response: &agentControlResponse{OK: false, Code: "response-indeterminate", Message: "native Codex control refused the exact request"}},
		{name: "timeout keeps the line", response: &agentControlResponse{OK: false, Code: "timeout", Message: "native Codex control timed out"}},
		{name: "oversized response keeps the line", response: &agentControlResponse{OK: false, Code: "response-too-large", Message: "exact Agent control detail is too large to display safely"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"))
			f.reviewErr, f.reviewResponse = test.err, test.response
			stdout, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--allow")
			if stdout != "" || err == nil || publicRouteArgvExitCode(err) == 0 || !strings.Contains(err.Error(), "projmux focus pane uid:pan-alpha-codex") ||
				!strings.Contains(err.Error(), "may or may not have reached Codex") || strings.Contains(err.Error(), "uncommitted") {
				t.Fatalf("answer = %q, %v", stdout, err)
			}
			if len(f.reviews()) != 1 {
				t.Fatalf("reviews = %+v", f.reviews())
			}
			if n := permissionAuditCount(t, f.approvals, agentapproval.AuditAllowed, "7"); n != 1 {
				t.Fatalf("allowed lines = %d, want the write-ahead line to stay", n)
			}
			if n := permissionAuditCount(t, f.approvals, agentapproval.AuditUncommitted, "7"); n != 0 {
				t.Fatalf("uncommitted lines = %d, want none for an outcome that may have reached Codex", n)
			}
		})
	}
}

// 7. A review failure confirmed not to have delivered the decision (a request
// that never left projmux, or a refusal before the server claims the
// approval) follows the answer line with an uncommitted send-failed line for
// the same request id and says the answer did not take effect.
func TestAgentApprovalCodexAnswerCompensatesAConfirmedUndeliveredDecision(t *testing.T) {
	t.Parallel()

	notSent := agentControlNotSent(errors.New("native Codex control endpoint unavailable: dial unix: connect: no such file or directory"))
	for _, test := range []struct {
		name     string
		err      error
		response *agentControlResponse
	}{
		{name: "request not sent", err: notSent},
		{name: "stale epoch", response: &agentControlResponse{Code: "stale-epoch", Message: "exact Agent connection epoch is no longer active"}},
		{name: "stale binding", response: &agentControlResponse{Code: "stale-binding", Message: "exact Agent binding or activation generation changed"}},
		{name: "unavailable", response: &agentControlResponse{Code: "unavailable", Message: "native Codex control connection is unavailable"}},
		{name: "invalid frame", response: &agentControlResponse{Code: "invalid-frame", Message: "exact Agent control request was malformed"}},
		{name: "ambiguous request", response: &agentControlResponse{Code: "ambiguous-request", Message: "pending request identity is missing, resolved, or ambiguous"}},
		{name: "unsafe decision", response: &agentControlResponse{Code: "unsafe-decision", Message: "decision is not a safe one-shot option for this request"}},
	} {
		for _, flag := range []string{"--allow", "--deny"} {
			t.Run(test.name+flag, func(t *testing.T) {
				f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"))
				f.reviewErr, f.reviewResponse = test.err, test.response
				event, reason := agentapproval.AuditAllowed, "decision=accept"
				if flag == "--deny" {
					event, reason = agentapproval.AuditDenied, "decision=decline"
				}
				stdout, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", flag, "--via", "popup")
				if stdout != "" || err == nil || publicRouteArgvExitCode(err) == 0 || !strings.Contains(err.Error(), "projmux focus pane uid:pan-alpha-codex") ||
					!strings.Contains(err.Error(), "the decision did not reach Codex, so the answer did not take effect; its "+event+" audit line is followed by an uncommitted line") {
					t.Fatalf("answer = %q, %v", stdout, err)
				}
				if len(f.reviews()) != 1 {
					t.Fatalf("reviews = %+v", f.reviews())
				}
				lines := readPermissionAudit(t, f.approvals)
				if len(lines) != 2 || lines[0].Event != event || lines[1].Event != agentapproval.AuditUncommitted {
					t.Fatalf("audit = %+v", lines)
				}
				answer, uncommitted := lines[0], lines[1]
				if uncommitted.RequestID != "7" || uncommitted.Reason != agentapproval.UncommittedSendFailed || uncommitted.AgentUID != answer.AgentUID ||
					uncommitted.PaneUID != "pan-alpha-codex" || uncommitted.ToolName != "command" || uncommitted.Input != "make test" ||
					uncommitted.Via != agentapproval.ViaPopup || !uncommitted.DecidedAt.IsZero() || uncommitted.At.IsZero() || answer.Reason != reason {
					t.Fatalf("uncommitted line = %+v after %+v", uncommitted, answer)
				}
			})
		}
	}
}

// 8. When the uncommitted line of a confirmed undelivered decision cannot be
// written, the answer still fails and says the audit log keeps its allowed
// line for an answer that did not take effect, and why.
func TestAgentApprovalCodexAnswerReportsAnUnwrittenUncommittedLine(t *testing.T) {
	t.Parallel()

	f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"))
	f.reviewResponse = &agentControlResponse{Code: "stale-binding", Message: "exact Agent binding or activation generation changed"}
	kept := f.approvals.AuditPath() + ".kept"
	f.onReview = func(agentControlRequest) {
		if err := os.Rename(f.approvals.AuditPath(), kept); err != nil {
			t.Error(err)
		}
		blockPermissionAudit(t, f.approvals)
	}
	stdout, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--allow")
	if stdout != "" || err == nil || publicRouteArgvExitCode(err) == 0 || !errors.Is(err, agentapproval.ErrUncommittedAudit) ||
		!strings.Contains(err.Error(), "the decision did not reach Codex, so the answer did not take effect, but the audit log keeps its allowed line: could not write the uncommitted audit line: "+f.approvals.AuditPath()) {
		t.Fatalf("answer = %q, %v", stdout, err)
	}
	file, readErr := os.ReadFile(kept)
	if readErr != nil || strings.Count(string(file), "\n") != 1 || !strings.Contains(string(file), `"event":"allowed"`) {
		t.Fatalf("kept audit = %q, %v; want the allowed line alone", file, readErr)
	}
	if entries, _ := os.ReadDir(f.approvals.AuditPath()); len(entries) != 0 {
		t.Fatalf("a line landed inside the audit directory: %v", entries)
	}
}

// 9. A successful answer writes no uncommitted line.
func TestAgentApprovalCodexAnswerSuccessWritesNoUncommittedLine(t *testing.T) {
	t.Parallel()

	f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"))
	if _, _, err := runRoute(t, f.command, "approval", "answer", codexPermissionAgentRef, "7", "--allow"); err != nil {
		t.Fatal(err)
	}
	if n := permissionAuditCount(t, f.approvals, agentapproval.AuditUncommitted, "7"); n != 0 {
		t.Fatalf("uncommitted lines = %d", n)
	}
}

// codexReviewNotDelivered confirms the consumer fence refusal, which is
// returned before the transport is called, through callControl's wrapping.
func TestAgentApprovalCodexReviewNotDeliveredClassifiesTheFenceAndTransport(t *testing.T) {
	t.Parallel()

	binding := exactAgentControlBinding{Identity: phase6CLIIdentity(), ProjectUID: "prj", WindowUID: "win"}
	fence := addOpenCodexBindingRecovery(&exactAgentControlBindingError{Reason: "canonical generation consumer fence is stale"}, binding)
	notSent := addOpenCodexBindingRecovery(fmt.Errorf("exact Agent native control unavailable: %w", agentControlNotSent(errors.New("dial failed"))), binding)
	written := addOpenCodexBindingRecovery(fmt.Errorf("exact Agent native control unavailable: %w", errors.New("write native Codex control request: broken pipe")), binding)
	for _, test := range []struct {
		name     string
		err      error
		response agentControlResponse
		want     bool
	}{
		{name: "fence", err: fence, want: true},
		{name: "not sent", err: notSent, want: true},
		{name: "write failed", err: written},
		{name: "ok", response: agentControlResponse{OK: true}},
		{name: "unknown code", response: agentControlResponse{Code: "approval-stale"}},
		{name: "empty code", response: agentControlResponse{}},
	} {
		if got := codexReviewNotDelivered(test.err, test.response); got != test.want {
			t.Errorf("%s: codexReviewNotDelivered = %t, want %t", test.name, got, test.want)
		}
	}
}

// callCodexControl marks a dial failure and a socket path failure as not
// sent and keeps their text.
func TestCallCodexControlMarksADialFailureNotSent(t *testing.T) {
	t.Parallel()

	// A short root keeps the socket path under the platform bound, so the
	// dial itself is what fails: no server listens there.
	shortRoot, err := os.MkdirTemp("/tmp", "pmx-ns-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	_, err = callCodexControl(context.Background(), shortRoot, phase6CLIEndpoint(), phase6CLIIdentity(), agentControlRequest{Operation: agentControlOpApprovals})
	if !errors.Is(err, errAgentControlNotSent) || !strings.HasPrefix(err.Error(), "native Codex control endpoint unavailable: ") {
		t.Fatalf("err = %v, want a not-sent dial failure", err)
	}
	_, err = callCodexControl(context.Background(), "relative", phase6CLIEndpoint(), phase6CLIIdentity(), agentControlRequest{Operation: agentControlOpApprovals})
	if !errors.Is(err, errAgentControlNotSent) || err.Error() != "exact Agent control requires an absolute state directory and durable endpoint/thread identity" {
		t.Fatalf("err = %v, want a not-sent socket path failure", err)
	}
}

// A provider that is neither Claude nor Codex is still refused without any
// broker call or store write.
func TestAgentApprovalCodexOtherProviderStaysUnsupported(t *testing.T) {
	t.Parallel()

	f := newCodexPermissionFixture(t, config.AgentApprovalAnsweringProjmux, codexCommandApproval("7"))
	registry, err := f.command.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := registry.Agent("agt-alpha-codex")
	agent.Spec.Provider = "antigravity"
	f.command.loadRegistry = func() (coremetadata.Registry, error) { return registry, nil }
	for _, args := range [][]string{{"list", codexPermissionAgentRef}, {"answer", codexPermissionAgentRef, "7", "--allow"}} {
		_, _, err := runRoute(t, f.command, append([]string{"approval"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "(permission-provider-unsupported)") || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("%s err = %v", args[0], err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("broker calls = %v", f.ops())
	}
}
