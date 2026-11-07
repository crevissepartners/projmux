package projmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// approvalShapeControl counts native control reads so a test can tell whether a
// call reached the Codex control path.
type approvalShapeControl struct{ lists atomic.Int32 }

func (c *approvalShapeControl) ListApprovals(context.Context, string) ([]core.PendingApproval, error) {
	c.lists.Add(1)
	return nil, nil
}
func (c *approvalShapeControl) ReviewApproval(context.Context, string, string, core.OfferedDecision) error {
	panic("shape test submitted a decision")
}

// approvalShapeFixture returns a Client with one process Agent and one tmux
// Agent of provider, and the saved answering policy.
func approvalShapeFixture(t *testing.T, provider string, policy config.AgentApprovalAnswering) (c *Client, process, tmux string) {
	t.Helper()
	c, _, process, pane := sdkStopFixture(t)
	if _, err := store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *m.Registry) error {
		a, _ := reg.Agent(process)
		p, _ := reg.Pane(pane)
		a.Spec.Provider = provider
		p.Status.ProcessSession.Provider = provider
		if provider == "claude" {
			p.Status.ProcessSession.SessionID = "conversation"
			p.Status.ProcessSession.ThreadID = ""
		}
		created, err := store.DefaultMutator().CreateAgent(reg, a.Metadata.OwnerUID(), m.CreateAgentOptions{Name: "shape-tmux", Provider: provider})
		tmux = created.Metadata.UID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.paths.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAgentApprovalAnsweringFile(c.paths.AgentApprovalAnsweringFile(), policy); err != nil {
		t.Fatal(err)
	}
	return c, process, tmux
}

func approvalShapeCode(t *testing.T, err error) Code {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want a *Refusal, got %v", err)
	}
	return refusal.Code
}

// The public SDK refuses a Claude request id that projmux could not have minted
// with permission-not-found before any store, audit, policy or control effect.
func TestAnswerApprovalRefusesMalformedClaudeIDBeforeAnyEffect(t *testing.T) {
	malformed := []string{"", "   ", "permission-", "permission-000000000000001", "permission-00000000000000001", "permission-000000000000000A", "permission-000000000000000g", "7", "../x", "Permission-0000000000000001", "xpermission-0000000000000001", "permission-0000000000000001x"}
	for _, kind := range []string{"tmux", "process"} {
		for _, policy := range []config.AgentApprovalAnswering{config.AgentApprovalAnsweringClaude, config.AgentApprovalAnsweringProjmux} {
			for i, id := range malformed {
				t.Run(fmt.Sprintf("%s/%s/%d", kind, policy, i), func(t *testing.T) {
					c, process, tmux := approvalShapeFixture(t, "claude", policy)
					trap := &approvalShapeControl{}
					c.approvalControl = trap
					uid := process
					if kind == "tmux" {
						uid = tmux
					}
					before := sdkReadSnapshot(t, c.paths.StateDir)
					receipt, err := c.AnswerApproval(context.Background(), AnswerApprovalRequest{Agent: "uid:" + uid, RequestID: id, Allow: true})
					if got := approvalShapeCode(t, err); got != "permission-not-found" {
						t.Fatalf("code %q, want permission-not-found: %v", got, err)
					}
					if !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "does not exist") {
						t.Fatalf("refusal text differs from the store's not-found: %v", err)
					}
					if receipt != (core.ApprovalReceipt{}) || trap.lists.Load() != 0 {
						t.Fatalf("effects: receipt %+v control reads %d", receipt, trap.lists.Load())
					}
					if after := sdkReadSnapshot(t, c.paths.StateDir); !reflect.DeepEqual(before, after) {
						t.Fatalf("state changed\nbefore %v\nafter  %v", before, after)
					}
					approvals := agentapproval.NewStore(c.paths.StateDir)
					for _, path := range []string{approvals.Path(), approvals.Path() + ".flock", approvals.AuditPath()} {
						if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("%s exists: %v", path, err)
						}
					}
				})
			}
		}
	}
}

// A well-formed id that names nothing keeps the store's behavior: the answering
// policy still decides for tmux Agents, and the store writes one refused line.
func TestAnswerApprovalWellFormedMissingClaudeIDKeepsStoreBehavior(t *testing.T) {
	const id = "permission-0000000000000001"
	for _, test := range []struct {
		kind   string
		policy config.AgentApprovalAnswering
		code   Code
		audit  bool
	}{
		{"tmux", config.AgentApprovalAnsweringClaude, CodePermissionAnsweringOff, false},
		{"tmux", config.AgentApprovalAnsweringProjmux, "permission-not-found", true},
		{"process", config.AgentApprovalAnsweringClaude, "permission-not-found", true},
		{"process", config.AgentApprovalAnsweringProjmux, "permission-not-found", true},
	} {
		t.Run(test.kind+"/"+string(test.policy), func(t *testing.T) {
			c, process, tmux := approvalShapeFixture(t, "claude", test.policy)
			uid := process
			if test.kind == "tmux" {
				uid = tmux
			}
			_, err := c.AnswerApproval(context.Background(), AnswerApprovalRequest{Agent: "uid:" + uid, RequestID: " " + id + " ", Allow: true})
			if got := approvalShapeCode(t, err); got != test.code {
				t.Fatalf("code %q, want %q: %v", got, test.code, err)
			}
			raw, readErr := os.ReadFile(agentapproval.NewStore(c.paths.StateDir).AuditPath())
			if !test.audit {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("audit written under answering off: %v", readErr)
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if readErr != nil || len(lines) != 1 || !strings.Contains(lines[0], "refused") || !strings.Contains(lines[0], "not-found") {
				t.Fatalf("want one refused not-found audit line, got %q (%v)", raw, readErr)
			}
		})
	}
}

// Codex Agents are not checked for a Claude id shape: an id such as 7 still
// reaches the store (process) or native control (tmux) as before.
func TestAnswerApprovalCodexIDsKeepTheirPath(t *testing.T) {
	c, process, tmux := approvalShapeFixture(t, "codex", config.AgentApprovalAnsweringProjmux)
	trap := &approvalShapeControl{}
	c.approvalControl = trap
	_, err := c.AnswerApproval(context.Background(), AnswerApprovalRequest{Agent: "uid:" + process, RequestID: "7", Allow: true})
	if got := approvalShapeCode(t, err); got != "permission-not-found" {
		t.Fatalf("process Codex code %q: %v", got, err)
	}
	if raw, readErr := os.ReadFile(agentapproval.NewStore(c.paths.StateDir).AuditPath()); readErr != nil || !strings.Contains(string(raw), "not-found") {
		t.Fatalf("process Codex id did not reach the store: %q %v", raw, readErr)
	}
	if trap.lists.Load() != 0 {
		t.Fatal("process Codex touched native control")
	}
	_, err = c.AnswerApproval(context.Background(), AnswerApprovalRequest{Agent: "uid:" + tmux, RequestID: "7", Allow: true})
	if err == nil || trap.lists.Load() == 0 {
		t.Fatalf("tmux Codex id did not reach native control: reads %d err %v", trap.lists.Load(), err)
	}
}
