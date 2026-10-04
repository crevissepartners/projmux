package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestCodexProcessV5OwnershipAndSessionFences(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	e := f.endpoint
	p, _ := reg.Pane(e.binding.Pane)
	a, _ := reg.Agent(e.binding.Agent)
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Status.Activation.Kind != coremetadata.RuntimeProcess || p.Status.Activation.Process == nil || p.Status.Activation.Codex != nil || p.Status.Activation.RuntimeID != "" || p.Status.ProcessSession.ThreadID != e.evidence.ThreadID || p.Status.ProcessSession.ConnectionID != e.binding.Operation || p.Status.ProcessSession.SessionID != "" {
		t.Fatal("incomplete v5 ownership", p.Status)
	}
	if a.Status.Activation.State == coremetadata.ActivationAcknowledged {
		t.Fatal("thread init acknowledged an absent prompt")
	}
	for _, n := range f.wire(t) {
		if string(n["method"]) == `"turn/start"` {
			t.Fatal("startup fabricated user turn")
		}
	}
	for name, change := range map[string]func(*coremetadata.Pane){
		"thread":      func(p *coremetadata.Pane) { p.Status.ProcessSession.ThreadID = "foreign-thread" },
		"connection":  func(p *coremetadata.Pane) { p.Status.ProcessSession.ConnectionID = "foreign-connection" },
		"child birth": func(p *coremetadata.Pane) { p.Status.Activation.Process.Child.Start = "reused" },
		"host birth":  func(p *coremetadata.Pane) { p.Status.Activation.Process.HostProcess.Start = "reused" },
		"host instance": func(p *coremetadata.Pane) {
			p.Status.Activation.Process.Binding.HostInstanceID = "foreign"
			p.Status.ProcessSession.Binding.HostInstanceID = "foreign"
		},
		"reservation only": func(p *coremetadata.Pane) { p.Status.Activation = coremetadata.PaneActivation{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := reg.Clone()
			p, _ := bad.Pane(e.binding.Pane)
			change(p)
			if _, _, err := f.store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = bad; return nil }); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.route(context.Background()); !errors.Is(err, processhost.ErrStale) {
				t.Fatal("foreign durable evidence granted authority", err)
			}
			after, _ := os.ReadFile(f.path)
			if !bytes.Equal(before, after) {
				t.Fatal("route probe wrote Registry")
			}
			if _, _, err := f.store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg.Clone(); return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexProcessQuestionPolicyParity(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, central := range []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux} {
			t.Run(string(central)+map[bool]string{false: "-off", true: "-on"}[channel], func(t *testing.T) {
				f := newProcessCodexFixture(t, nil)
				_, _, err := f.store.UpdateConvergent(func(r *coremetadata.Registry) error {
					a, _ := r.Agent(f.endpoint.binding.Agent)
					delete(a.Metadata.Annotations, coremetadata.AnnotationAgentQuestionChannel)
					if channel {
						a.Metadata.Annotations[coremetadata.AnnotationAgentQuestionChannel] = coremetadata.QuestionChannelOn
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				f.control.questionAnswering = func() config.AgentQuestionAnswering { return central }
				f.turn(t, "policy", "controls")
				f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
				if err := f.control.sync(context.Background()); err != nil {
					t.Fatal(err)
				}
				questions, err := f.control.questions.List(f.endpoint.binding.Agent)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if channel || central == config.AgentQuestionAnsweringProjmux {
					want = 1
				}
				if len(questions) != want {
					t.Fatalf("questions=%d want=%d", len(questions), want)
				}
				if want == 0 {
					// Enabling the channel later must capture the still-blocking request.
					_, _, err = f.store.UpdateConvergent(func(r *coremetadata.Registry) error {
						a, _ := r.Agent(f.endpoint.binding.Agent)
						if a.Metadata.Annotations == nil {
							a.Metadata.Annotations = map[string]string{}
						}
						a.Metadata.Annotations[coremetadata.AnnotationAgentQuestionChannel] = coremetadata.QuestionChannelOn
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				f.answerControls(t)
			})
		}
	}
}

func TestCodexProcessUnknownServerRequestIsObservable(t *testing.T) {
	f := newProcessCodexFixture(t, func(root, binary string, env []string) processhost.Command {
		script := strings.Replace(processCodexProviderFixture, "  if prompt=='controls'", "  if prompt=='unknown':emit({'id':99,'method':'unsupported/request','params':{'threadId':'process-thread','turnId':current}})\n  elif prompt=='controls'", 1)
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", script}, Dir: root, Env: env}
	})
	if err := f.endpoint.handle.Turn(context.Background(), f.endpoint.authority(), "unknown", "unknown"); err != nil && !strings.Contains(err.Error(), "unsupported Codex server request") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := f.endpoint.handle.Wait(ctx, f.endpoint.binding)
	if err != nil || snap.Exit == nil || !strings.Contains(snap.Failure, "unsupported Codex server request") {
		t.Fatal("silent unsupported request", snap, err)
	}
	events, _, err := f.endpoint.handle.Events(f.endpoint.binding, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || event.Kind == "protocol-error"
	}
	if !found {
		t.Fatal("missing protocol diagnostic event")
	}
}

// Every subprocess uses the copied product binary and this fixture's HOME.
// The provider is a protocol stub; no broker, live registry or API is used.
func TestCodexProcessV5ActualCLIIsolated(t *testing.T) {
	product := os.Getenv("PMX_TEST_CLI")
	if product == "" {
		t.Skip("set PMX_TEST_CLI to an isolated copied product binary")
	}
	f := newProcessCodexFixture(t, nil)
	e := f.endpoint
	paths := config.DefaultPaths(filepath.Join(f.root, "config"), filepath.Join(f.root, "state"))
	f.control.questions = agentquestion.NewStore(paths.StateDir)
	f.control.approvals = agentapproval.NewStore(paths.StateDir)
	f.control.questionAnswering = func() config.AgentQuestionAnswering {
		v, _ := config.LoadAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile())
		return v
	}
	attention := newProcessAttentionStore(paths.StateDir)
	if err := attention.activate(e.binding, "codex", ""); err != nil {
		t.Fatal(err)
	}
	f.control.attention = &processAttentionProjection{store: attention, queue: notify.NewDefaultStore(paths)}
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key == "HOME" || key == "CODEX_HOME" || key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "PMX_INTERNAL_") {
			continue
		}
		env = append(env, v)
	}
	// Attention combines process rows with tmux rows. The isolated tmux read
	// fixture supplies an empty peer set without contacting an ambient server.
	tmuxDir := filepath.Join(f.root, "tmux-read")
	if err := os.Mkdir(tmuxDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmuxDir, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env = append(env, "PATH="+tmuxDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "HOME="+f.root, "CODEX_HOME="+filepath.Join(f.root, ".codex"), "XDG_CONFIG_HOME="+filepath.Join(f.root, "config"), "XDG_STATE_HOME="+filepath.Join(f.root, "state"), "XDG_CACHE_HOME="+filepath.Join(f.root, "cache"))
	cli := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, product, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		t.Logf("CLI %v: %s", args, out)
		return out
	}
	ref := "uid:" + e.binding.Agent
	scope := []string{"-p", "uid:" + e.binding.Project, "-w", "uid:" + e.binding.Window}
	describe := cli(append([]string{"describe", "pane", "--pane", "uid:" + e.binding.Pane, "-o", "json"}, scope...)...)
	if !bytes.Contains(describe, []byte(`"threadID": "process-thread"`)) {
		t.Fatal("actual CLI lost v5 thread", string(describe))
	}
	f.turn(t, "cli-controls", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	beforeAnswer := cli("attention", "list", "--json")
	if !bytes.Contains(beforeAnswer, []byte(e.binding.Pane)) || !bytes.Contains(beforeAnswer, []byte(`"ai_state": "waiting"`)) {
		t.Fatal("blocking attention absent", string(beforeAnswer))
	}
	cli(append([]string{"agent", "question", "list", ref, "-o", "json"}, scope...)...)
	qs, err := f.control.questions.List(e.binding.Agent)
	if err != nil || len(qs) != 1 {
		t.Fatal(qs, err)
	}
	cli(append([]string{"agent", "question", "answer", ref, qs[0].ID, "--option", "1=blue"}, scope...)...)
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	record := attentionRecord(t, attention, e.binding.Pane)
	if record.badge() != aibadge.ApprovalRequired {
		t.Fatal("answer cleared sibling approval badge", record)
	}
	list := cli("attention", "list", "--json")
	if !bytes.Contains(list, []byte(e.binding.Pane)) || !bytes.Contains(list, []byte(`"ai_state": "waiting"`)) {
		t.Fatal("approval attention absent", string(list))
	}
	notices := cli("get", "notifications", "--live", "--json")
	if !bytes.Contains(notices, []byte("Input required")) || !bytes.Contains(notices, []byte("Approval required")) {
		t.Fatal("blocking notifications absent", string(notices))
	}
	// Approval CLI routing is activated by the next public-consumer slice.
	approvals, err := f.control.approvals.List(e.binding.Agent)
	if err != nil || len(approvals) != 1 {
		t.Fatal(approvals, err)
	}
	if _, err = f.control.approvals.Answer(approvals[0].ID, e.binding.Agent, false, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	// Channel off with central answering enabled uses the same CLI answer store.
	cli(append([]string{"agent", "question", "disable", ref}, scope...)...)
	cli("config", "agent-questions", "--answering", "projmux")
	f.turn(t, "central-controls", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	qs, err = f.control.questions.List(e.binding.Agent)
	if err != nil {
		t.Fatal(err)
	}
	var waiting string
	for _, q := range qs {
		if q.State == agentquestion.StateWaiting {
			waiting = q.ID
		}
	}
	if waiting == "" {
		t.Fatal("central answering did not capture process question")
	}
	cli(append([]string{"agent", "question", "answer", ref, waiting, "--option", "1=red"}, scope...)...)
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The response write finishes before the provider records its wire log.
	f.wait(t, func(processhost.Snapshot) bool {
		for _, n := range f.wire(t) {
			if len(n["result"]) > 0 && bytes.Contains(n["result"], []byte("red")) {
				return true
			}
		}
		return false
	})
}

func TestCodexProcessForegroundAndCoordinationWireRemainDistinct(t *testing.T) {
	left := newProcessCodexFixture(t, nil)
	right := newProcessCodexFixture(t, nil)
	e := left.endpoint
	request := processForegroundRequest{Authority: e.authority(), Action: "turn", Operation: "operator-input", Prompt: "plain operator input"}
	if err := applyCodexForeground(context.Background(), e.handle, request); err != nil {
		t.Fatal(err)
	}
	left.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{e.binding.Agent: e, right.endpoint.binding.Agent: right.endpoint}}
	right.endpoint.messages.Store(m)
	now := time.Now().UTC()
	receipt, err := m.send(context.Background(), e.binding.Agent, right.endpoint.binding.Agent, "distinct-message", "distinct-conversation", "bounded coordination", now, now.Add(time.Minute))
	if err != nil || receipt.Delivery.State != coremessage.StateDelivered {
		t.Fatal(receipt, err)
	}
	right.answerControls(t)
	text := func(f *processCodexFixture) string {
		t.Helper()
		for _, n := range f.wire(t) {
			if string(n["method"]) == `"turn/start"` {
				var params struct {
					Input []struct {
						Text string `json:"text"`
					} `json:"input"`
				}
				if err := json.Unmarshal(n["params"], &params); err != nil || len(params.Input) != 1 {
					t.Fatal(err)
				}
				return params.Input[0].Text
			}
		}
		t.Fatal("no provider turn")
		return ""
	}
	if text(left) != request.Prompt {
		t.Fatal("operator turn gained an envelope")
	}
	var envelope coremessage.Envelope
	if err := json.Unmarshal([]byte(text(right)), &envelope); err != nil || envelope.Authority != coremessage.PeerAuthority() || envelope.Payload != "bounded coordination" || envelope.MessageRef != "distinct-message" {
		t.Fatal("coordination envelope lost", envelope, err)
	}
}
