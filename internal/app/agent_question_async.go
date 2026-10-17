package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
)

func (c *agentCommand) askQuestion(req agentQuestionRequest, a metadata.Agent, out io.Writer) error {
	st, e := c.openQuestionStore()
	if e != nil {
		return e
	}
	cli := st.CLIAsyncStore()
	r, e := cli.Create(a.Metadata.UID, json.RawMessage(req.questions), req.expiresIn)
	if e != nil {
		return MapMetadataError(e)
	}
	start := c.questionAsyncStart
	if start == nil {
		start = launchQuestionOutbox
	}
	if e = start(); e != nil {
		return errors.Join(fmt.Errorf("question %s: expiry owner start failed: %w", r.ID, e), cli.RemoveUnstarted(r.ID))
	}
	if req.json {
		return json.NewEncoder(out).Encode(struct {
			ID        string              `json:"id"`
			AgentUID  string              `json:"agentUID"`
			Delivery  string              `json:"delivery"`
			State     agentquestion.State `json:"state"`
			CreatedAt time.Time           `json:"createdAt"`
			Deadline  time.Time           `json:"deadline"`
		}{r.ID, r.AgentUID, r.Delivery, r.State, r.CreatedAt, r.Deadline})
	}
	_, e = fmt.Fprintln(out, r.ID)
	return e
}

// launchQuestionOutbox waits only for owner admission, never for a question or
// provider. The child has no inherited provider input and a single owner lock.
func launchQuestionOutbox() error {
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	r, w, e := os.Pipe()
	if e != nil {
		return e
	}
	defer r.Close()
	cmd := tmuxexec.Command(binary, "internal", "agent-question-outbox") // #nosec G204 -- this executable and fixed private route.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{w}
	cmd.Env = questionOutboxEnvironment(os.Environ())
	if e = cmd.Start(); e != nil {
		_ = w.Close()
		return e
	}
	_ = w.Close()
	done := make(chan error, 1)
	go func() {
		var b [1]byte
		_, e := io.ReadFull(r, b[:])
		if e == nil && b[0] != '1' {
			e = errors.New("question owner refused startup")
		}
		done <- e
	}()
	select {
	case e = <-done:
	case <-time.After(5 * time.Second):
		e = errors.New("question owner startup timed out")
	}
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return e
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
func runQuestionOutbox(args []string) error {
	if len(args) != 0 {
		return usageError("invalid question outbox arguments")
	}
	ready := os.NewFile(3, "question-owner-ready")
	if ready == nil {
		return errors.New("question owner requires readiness descriptor")
	}
	defer ready.Close()
	st, e := defaultAgentQuestionStore()
	if e != nil {
		return e
	}
	cli := st.CLIAsyncStore()
	release, owned, e := cli.LockOwner()
	if e != nil {
		return e
	}
	if !owned {
		_, e = ready.Write([]byte("1"))
		return e
	}
	var once sync.Once
	unlock := func() { once.Do(release) }
	defer unlock()
	if _, e = ready.Write([]byte("1")); e != nil {
		return e
	}
	_ = ready.Close()
	c := newAgentCommand()
	for {
		if e = c.dispatchQuestionOutbox(context.Background(), cli); e != nil {
			if errors.Is(e, agentquestion.ErrMalformedStore) {
				return e
			}
			// Registry replacement, provider loss, and a temporarily busy disk
			// must not silently remove the independent expiry owner.
			time.Sleep(time.Second)
			continue
		}
		idle, e := cli.ReleaseIfIdle(unlock)
		if e != nil {
			if errors.Is(e, agentquestion.ErrMalformedStore) {
				return e
			}
			time.Sleep(time.Second)
			continue
		}
		if idle {
			return nil
		}
		time.Sleep(time.Second)
	}
}

// dispatchQuestionOutbox uses current UID routes. Accepted refs are observed,
// never submitted again. A crash during an unreceipted write becomes unknown.
func (c *agentCommand) dispatchQuestionOutbox(ctx context.Context, st *agentquestion.CLIAsyncStore) error {
	rs, e := st.List("")
	if e != nil {
		return e
	}
	var reg metadata.Registry
	registryLoaded := false
	for _, r := range rs {
		if !r.Pending() {
			continue
		}
		if !registryLoaded {
			reg, e = c.loadRegistry()
			if e != nil {
				return e
			}
			registryLoaded = true
		}
		a, found := reg.Agent(r.AgentUID)
		if !found {
			if e = st.TargetDeleted(r.ID); e != nil {
				return e
			}
			continue
		}
		if r.Outcome == "" {
			if c.clock().Before(r.Deadline) {
				continue
			}
			r, e = st.Resolve(r.ID, r.AgentUID, nil, "expired")
			if errors.Is(e, agentquestion.ErrNotPending) {
				continue
			}
			if e != nil {
				return e
			}
		}
		terminal := ""
		if !c.clock().Before(r.ResultDeadline) {
			terminal = "result-expired"
		}
		if terminal != "" {
			if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error { v.Outcome = terminal; return nil }); e != nil {
				return e
			}
			continue
		}
		bodies, e := r.Messages()
		if e != nil {
			return e
		}
		for n, part := range r.Parts {
			if part.State == "delivered" || part.State == "unknown" || part.State == "failed" || c.clock().Before(part.NextAttempt) {
				continue
			}
			existing, exists, readErr := c.messageStore.Get(part.MessageRef)
			if readErr != nil {
				return readErr
			}
			var receipt agentmessages.Receipt
			if exists {
				env := existing.Envelope
				resultCreated := r.ResultDeadline.Add(-24 * time.Hour)
				if env.Source.AgentUID != r.AgentUID || env.Target.AgentUID != r.AgentUID || env.Payload != bodies[n] || env.Operator() || env.Authority != message.PeerAuthority() || env.ReplyTo != "" || env.AcceptedAt.Before(resultCreated) {
					raw := questionResultReceipt(agentmessages.ReceiptFor(existing), "reference-conflict")
					if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
						v.Parts[n].State = "unknown"
						v.Parts[n].Receipt = raw
						v.Outcome = "unknown"
						return nil
					}); e != nil {
						return e
					}
					continue
				}
				existing, _, readErr = c.messageStore.Status(part.MessageRef, c.clock())
				if readErr != nil {
					return readErr
				}
				receipt = agentmessages.ReceiptFor(existing)
				if part.State == "sending" && !receipt.Delivery.State.Terminal() {
					// The prior owner died before saving its dispatch result.
					// Broker acceptance alone cannot disprove a provider write.
					raw := questionResultReceipt(receipt, "")
					if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
						v.Parts[n].State = "unknown"
						v.Parts[n].Receipt = raw
						v.Outcome = "unknown"
						return nil
					}); e != nil {
						return e
					}
					continue
				}
			} else if part.State == "sending" || part.State == "accepted" {
				if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
					v.Parts[n].State = "unknown"
					v.Outcome = "unknown"
					return nil
				}); e != nil {
					return e
				}
				continue
			} else {
				if a.Status.Phase != metadata.PhaseRunning {
					continue
				}
				// Route admission failures occur before claiming a provider write and can
				// safely wait for resume/relaunch of this same UID.
				if _, e = c.resolveMessageRoute(reg, *a); e != nil {
					continue
				}
				if _, e = c.resolveMessageTargetRoute(reg, *a); e != nil {
					continue
				}
				if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error { v.Parts[n].State = "sending"; return nil }); e != nil {
					return e
				}
				result, _ := c.peerMessageService().Send(ctx, agentmessages.SendRequest{Source: "uid:" + r.AgentUID, Target: "uid:" + r.AgentUID, MessageRef: part.MessageRef, Text: bodies[n], TTL: r.ResultDeadline.Sub(c.clock())})
				receipt = result.Receipt
				if receipt.MessageRef == "" {
					// Non-reply Service.Send returns an empty receipt only
					// before provider dispatch. The completed call can retry;
					// a crash with an outstanding sending claim stays unknown.
					if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
						v.Parts[n].State = "pending"
						v.Parts[n].NextAttempt = c.clock().Add(5 * time.Second)
						return nil
					}); e != nil {
						return e
					}
					continue
				}
			}
			state := "accepted"
			if receipt.Delivery.OutcomeUnknown {
				state = "unknown"
			} else if receipt.Delivery.State == message.StateDelivered {
				state = "delivered"
			} else if receipt.Delivery.State.Terminal() {
				state = "failed"
			}
			raw := questionResultReceipt(receipt, "")
			if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
				v.Parts[n].State = state
				if state == "failed" {
					// OutcomeUnknown was checked first: this receipt proves
					// no provider write. Commit the next stable ref before a
					// later dispatch to the Agent's then-current route.
					v.Parts[n].State = "pending"
					v.Parts[n].Attempt++
					v.Parts[n].MessageRef = v.PartRef(n, v.Parts[n].Attempt)
					delay := time.Duration(min(v.Parts[n].Attempt, 6)) * 10 * time.Second
					v.Parts[n].NextAttempt = c.clock().Add(delay)
				}
				if state == "unknown" {
					v.Outcome = "unknown"
				}
				v.Parts[n].Receipt = raw
				return nil
			}); e != nil {
				return e
			}
		}
		if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
			pending := false
			outcome := "delivered"
			for _, p := range v.Parts {
				if p.State == "unknown" {
					outcome = "unknown"
				} else if p.State == "failed" && outcome != "unknown" {
					outcome = "failed"
				}
				if p.State == "pending" || p.State == "sending" || p.State == "accepted" {
					pending = true
				}
			}
			if !pending {
				v.Outcome = outcome
			}
			return nil
		}); e != nil {
			return e
		}
	}
	return nil
}

// Mutation startup recovers an owner lost to machine/process termination.
// Read-only routes remain free of process and store effects.
// Relaunch previews/no-ops must stay read-only. Successful process relaunch
// recovers at its owned-wait boundary; the generic startup routes below have
// no successful preview/no-op form.
func questionOutboxRecoveryRequested(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return args[0] == "agent" && (args[1] == "resume" || args[1] == "question" && len(args) > 2 && (args[2] == "answer" || args[2] == "decline")) || args[0] == "create" && args[1] == "agent" || args[0] == "config" && args[1] == "apply"
}
func recoverQuestionOutbox(args []string) {
	if questionOutboxRecoveryRequested(args) {
		newAgentCommand().recoverQuestionOwner()
	}
}

func (c *agentCommand) recoverQuestionOwner() {
	st, e := c.openQuestionStore()
	if e != nil {
		return
	}
	rs, e := st.CLIAsyncStore().List("")
	if e != nil {
		return
	}
	for _, r := range rs {
		if r.Pending() {
			start := c.questionAsyncStart
			if start == nil {
				start = launchQuestionOutbox
			}
			_ = start()
			return
		}
	}
}

func questionOutboxEnvironment(env []string) []string {
	out := []string{}
	for _, v := range claudeHelperEnvironment(env) {
		k, _, _ := strings.Cut(v, "=")
		if k == "TMUX" || k == "TMUX_PANE" || strings.HasPrefix(k, "PROJMUX_") || strings.HasPrefix(k, "__PROJMUX_") {
			continue
		}
		out = append(out, v)
	}
	return out
}

// questionResultReceipt retains the bounded delivery evidence needed by this
// outbox. The full, canonical route receipt remains addressable by messageRef in
// the broker. Only an oversized operational reason is shortened, with a marker.
func questionResultReceipt(r agentmessages.Receipt, reason string) json.RawMessage {
	if reason == "" {
		reason = r.Delivery.Reason
	}
	summary := struct {
		MessageRef      string        `json:"messageRef"`
		AgentUID        string        `json:"agentUID"`
		State           message.State `json:"state"`
		Reason          string        `json:"reason,omitempty"`
		OutcomeUnknown  bool          `json:"outcomeUnknown,omitempty"`
		ReasonTruncated bool          `json:"reasonTruncated,omitempty"`
		AcceptedAt      time.Time     `json:"acceptedAt"`
		Deadline        time.Time     `json:"deadline"`
	}{MessageRef: r.MessageRef, AgentUID: r.Target.AgentUID, State: r.Delivery.State, Reason: reason, OutcomeUnknown: r.Delivery.OutcomeUnknown, AcceptedAt: r.Delivery.AcceptedAt, Deadline: r.Deadline}
	for {
		b, _ := json.Marshal(summary)
		if len(b) <= agentquestion.MaxCLIReceiptBytes {
			return b
		}
		_, n := utf8.DecodeLastRuneInString(summary.Reason)
		if n == 0 {
			return nil
		}
		summary.Reason = summary.Reason[:len(summary.Reason)-n]
		summary.ReasonTruncated = true
	}
}
