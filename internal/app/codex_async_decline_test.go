package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestAsyncDeclineCLIAndSDKSubmittedReadbackOnce(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			f, client, id := newAsyncAnswerFixture(t, active)
			b := f.endpoint.binding
			store, _, projection := newProcessAttentionFixture(t, f.root, b, "codex")
			f.control.attention = projection
			if err := projection.sync(f.endpoint.handle, b); err != nil {
				t.Fatal(err)
			}
			list, err := client.ListQuestions(context.Background(), "uid:"+b.Agent)
			if err != nil || len(list.Questions) != 1 || !list.Questions[0].CanDecline || !list.ExactQuestions[0].CanDecline {
				t.Fatal(list, err)
			}
			deadline := list.Questions[0].Deadline
			if active {
				r, err := client.DeclineQuestion(context.Background(), "uid:"+b.Agent, id)
				if err != nil || r.QuestionID != id || r.Delivery != "async" || r.Outcome != "submitted" {
					t.Fatal(r, err)
				}
			} else {
				reg, err := f.store.LoadDegradedReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				agent, _ := reg.Agent(b.Agent)
				command := &agentCommand{loadRegistry: f.store.LoadDegradedReadOnly, messagePaths: agentMessagePaths{registryPath: f.path}, questionStore: func() (*agentquestion.Store, error) { return f.control.questions, nil }}
				var out, stderr bytes.Buffer
				if err := command.runQuestion([]string{"decline", "uid:" + agent.Metadata.UID, id}, &out, &stderr); err != nil || !strings.Contains(out.String(), id+" decline submitted") || strings.Contains(out.String(), "answered") {
					t.Fatal(out.String(), err)
				}
			}
			list, err = client.ListQuestions(context.Background(), "uid:"+b.Agent)
			if err != nil || len(list.Questions) != 1 || list.Questions[0].State != core.QuestionClosed || list.Questions[0].Disposition != "async-declined" || list.Questions[0].CanAnswer || list.Questions[0].CanDecline || len(list.Questions[0].Answers) != 0 || !list.Questions[0].Deadline.Equal(deadline) {
				t.Fatal(list, err)
			}
			r, err := client.DeclineQuestion(context.Background(), "uid:"+b.Agent, id)
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionNotPending || r.Outcome != "submitted" {
				t.Fatal(r, err)
			}
			answer, err := client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("must not send")}}})
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionNotPending || answer.Outcome == "submitted" {
				t.Fatal(answer, err)
			}
			wire, err := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for line := range bytes.SplitSeq(wire, []byte("\n")) {
				var m struct {
					Method string
					Params struct{ Input []struct{ Text string } }
				}
				if json.Unmarshal(line, &m) != nil {
					continue
				}
				for _, input := range m.Params.Input {
					if !strings.Contains(input.Text, "Operator non-answer notice") {
						continue
					}
					count++
					want := "turn/start"
					if active {
						want = "turn/steer"
					}
					if m.Method != want || !strings.Contains(input.Text, "call_42babc3b4f8d4f14806598aaf0439781") || !strings.Contains(input.Text, "CORE576 native probe: which color?") || !strings.Contains(input.Text, "explicitly declines to answer every question") || strings.Contains(input.Text, `"answers"`) {
						t.Fatal(m)
					}
				}
			}
			if count != 1 {
				t.Fatal("notice count", count, string(wire))
			}
			attention, err := store.read()
			if err != nil || len(attention[b.Pane].Pending) != 0 {
				t.Fatal(attention, err)
			}
			// Test the pending set itself with independent permission, another question,
			// and a completed response; a reply badge alone is not retirement evidence.
			snap, err := f.endpoint.handle.Observe(b)
			if err != nil {
				t.Fatal(err)
			}
			row := attention[b.Pane]
			other := snap.AsyncQuestions[0]
			other.Request.ID = "other-item"
			other.Closed = false
			other.Deadline = time.Now().Add(time.Hour)
			snap.AsyncQuestions = append(snap.AsyncQuestions, other)
			snap.Pending = []processhost.Request{{ID: "approval", Kind: "permission"}}
			if _, err = row.applyEvent(processhost.Event{Kind: "turn-result"}); err != nil {
				t.Fatal(err)
			}
			if err = row.reconcilePending(snap, projection.push); err != nil || len(row.Pending) != 2 {
				t.Fatal(row, err)
			}
			if row.Badge != aibadge.ResponseComplete {
				t.Fatal("response completion lost", row)
			}
			snap.AsyncQuestions[1].Closed = true
			if err = row.reconcilePending(snap, projection.push); err != nil || len(row.Pending) != 1 || row.badge() != aibadge.ApprovalRequired {
				t.Fatal(row, err)
			}
			snap.Pending = nil
			if err = row.reconcilePending(snap, projection.push); err != nil || len(row.Pending) != 0 || row.badge() != aibadge.ResponseComplete {
				t.Fatal(row, err)
			}
		})
	}
	t.Run("whole-multiple-prompt-set", func(t *testing.T) {
		frames := append([]string(nil), nativeAppAsyncFrames[:2]...)
		for i := range frames {
			var frame map[string]any
			if err := json.Unmarshal([]byte(frames[i]), &frame); err != nil {
				t.Fatal(err)
			}
			item := frame["params"].(map[string]any)["item"].(map[string]any)
			item["questions"] = []any{map[string]any{"title": "First original prompt", "options": []string{"cobalt", "amber"}}, map[string]any{"title": "Second original prompt", "options": []string{}}}
			raw, _ := json.Marshal(frame)
			frames[i] = string(raw)
		}
		f := newAsyncProcessFixture(t, frames, true)
		b := f.endpoint.binding
		f.control.questions = agentquestion.NewStore(filepath.Dir(filepath.Dir(f.path)))
		processQuestionCallbacks.Store(b, processQuestionCallback(f.control.exactQuestions))
		t.Cleanup(func() { processQuestionCallbacks.Delete(b) })
		f.turn(t, "first", "fixture")
		snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.AsyncQuestions) == 1 && s.Turn == "" })
		result := processAgentCreateResult{Binding: b, Provider: aiModeCodex, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: f.path}
		if err := result.recordProcessSnapshot(snap); err != nil {
			t.Fatal(err)
		}
		if err := f.control.syncControls(context.Background()); err != nil {
			t.Fatal(err)
		}
		id := codexAsyncQuestionID(b, snap.AsyncQuestions[0].Request)
		receipt, err := (ProcessQuestionDispatcher{RegistryPath: f.path}).DeclineExactProcessQuestionReceipt(context.Background(), b.Agent, id)
		if err != nil || receipt.Outcome != "submitted" {
			t.Fatal(receipt, err)
		}
		wire, readErr := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Count(wire, []byte("Operator non-answer notice")) != 1 || !bytes.Contains(wire, []byte("First original prompt")) || !bytes.Contains(wire, []byte("Second original prompt")) || bytes.Contains(wire, []byte(`\"answers\"`)) {
			t.Fatal(string(wire))
		}
	})

}

func TestAsyncDeclineConcurrentAnswerAndDeclineWinner(t *testing.T) {
	for _, mode := range []string{"race", "declines", "answer-first", "decline-first"} {
		t.Run(mode, func(t *testing.T) {
			f, client, id := newAsyncAnswerFixture(t, true)
			uid := "uid:" + f.endpoint.binding.Agent
			answer := func() error {
				_, err := client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: uid, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("explicit")}}})
				return err
			}
			decline := func() error { _, err := client.DeclineQuestion(context.Background(), uid, id); return err }
			if mode == "answer-first" || mode == "decline-first" {
				first, second := answer, decline
				if mode == "decline-first" {
					first, second = decline, answer
				}
				if err := first(); err != nil {
					t.Fatal(err)
				}
				var refusal *projmux.Refusal
				if err := second(); !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionNotPending {
					t.Fatal(err)
				}
			} else {
				start := make(chan struct{})
				wins := make(chan bool, 8)
				var wg sync.WaitGroup
				for i := range 8 {
					wg.Go(func() {
						<-start
						fn := decline
						if mode == "race" && i%2 == 0 {
							fn = answer
						}
						wins <- fn() == nil
					})
				}
				close(start)
				wg.Wait()
				close(wins)
				count := 0
				for win := range wins {
					if win {
						count++
					}
				}
				if count != 1 {
					t.Fatal("winners", count)
				}
			}
			wire, readErr := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if bytes.Count(wire, []byte("Operator non-answer notice"))+bytes.Count(wire, []byte("Operator answers")) != 1 {
				t.Fatal(string(wire))
			}
			stored, found, err := f.control.questions.AsyncStore().Get(id)
			if err != nil || !found || stored.State != agentquestion.StateClosed || (stored.Disposition != "async-declined" && stored.Disposition != "async-submitted") {
				t.Fatal(stored, err)
			}
			if mode == "answer-first" {
				r, err := client.DeclineQuestion(context.Background(), uid, id)
				if err == nil || r.Outcome == "submitted" {
					t.Fatal("answer became notice", r, err)
				}
			}
		})
	}
}

func TestAsyncDeclineUnknownNeverResends(t *testing.T) {
	for _, behavior := range []string{"refused", "lost", "prune"} {
		t.Run(behavior, func(t *testing.T) {
			f, client, id := newAsyncAnswerFixture(t, true, behavior)
			uid := "uid:" + f.endpoint.binding.Agent
			r, err := client.DeclineQuestion(context.Background(), uid, id)
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionDeliveryUnknown || r.Outcome != "unknown" || r.QuestionID != id || r.Delivery != "async" {
				t.Fatal(r, err)
			}
			before, readErr := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			_, _ = client.DeclineQuestion(context.Background(), uid, id)
			_, _ = client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: uid, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("retry")}}})
			after, readErr := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("unknown resent")
			}
		})
	}
}

func TestAsyncDeclineUnsupportedAndSecretEffects(t *testing.T) {
	// Malformed requests never reach a callback, even when it exists.
	b := processhost.Binding{Agent: "fixture"}
	calls := 0
	processQuestionCallbacks.Store(b, processQuestionCallback(func(context.Context, processQuestionRequest) ([]ExactProcessQuestion, error) {
		calls++
		return nil, nil
	}))
	defer processQuestionCallbacks.Delete(b)
	for _, r := range []processQuestionRequest{{Binding: b, Action: "other"}, {Binding: b, Action: "other", QuestionID: "id"}, {Binding: b, Action: "decline"}, {Binding: b, Action: "decline", QuestionID: "id", Selections: map[int]agentquestion.Selection{0: {Text: "bad", HasText: true}}}} {
		result := applyExactProcessQuestions(context.Background(), coremetadata.ProcessIdentity{}, r, nil)
		if result.Accepted || calls != 0 {
			t.Fatal(result, calls)
		}
	}
	{
		f := newProcessCodexFixture(t, nil)
		f.turn(t, "first", "controls")
		snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
		result := processAgentCreateResult{Binding: f.endpoint.binding, Provider: aiModeCodex, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: f.path}
		if err := result.recordProcessSnapshot(snap); err != nil {
			t.Fatal(err)
		}
		views, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: f.endpoint.binding})
		if err != nil || len(views) != 1 {
			t.Fatal(views, err)
		}
		processQuestionCallbacks.Store(f.endpoint.binding, processQuestionCallback(f.control.exactQuestions))
		t.Cleanup(func() { processQuestionCallbacks.Delete(f.endpoint.binding) })
		before, readErr := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		receipt, ipcErr := (ProcessQuestionDispatcher{RegistryPath: f.path}).DeclineExactProcessQuestionReceipt(context.Background(), f.endpoint.binding.Agent, views[0].QuestionID)
		var ipcTyped *processquestions.DeliveryError
		if !errors.As(ipcErr, &ipcTyped) || receipt.Code != "question-decline-unsupported" || receipt.Outcome != "" {
			t.Fatal(receipt, ipcErr)
		}
		_, err = f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: f.endpoint.binding, QuestionID: views[0].QuestionID, Action: "decline"})
		var typed *asyncAnswerError
		if !errors.As(err, &typed) || typed.receipt.Code != "question-decline-unsupported" || typed.cause == nil {
			t.Fatal(err)
		}
		after, readErr := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("blocking decline wrote")
		}
	}

	{
		f := newProcessClaudeFixture(t, nil)
		f.turn(t, "first", "question")
		snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
		result := processAgentCreateResult{Binding: f.binding, Provider: aiModeClaude, Handle: f.handle, registryPath: f.path}
		if err := result.recordProcessSnapshot(snap); err != nil {
			t.Fatal(err)
		}
		views, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: f.binding})
		if err != nil || len(views) != 1 {
			t.Fatal(views, err)
		}
		processQuestionCallbacks.Store(f.binding, processQuestionCallback(f.control.exactQuestions))
		t.Cleanup(func() { processQuestionCallbacks.Delete(f.binding) })
		before, err := json.Marshal(snap.Pending)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := (ProcessQuestionDispatcher{RegistryPath: f.path}).DeclineExactProcessQuestionReceipt(context.Background(), f.binding.Agent, views[0].QuestionID)
		var typed *processquestions.DeliveryError
		if !errors.As(err, &typed) || receipt.Code != "question-decline-unsupported" || receipt.Outcome != "" {
			t.Fatal(receipt, err)
		}
		post, err := f.handle.Observe(f.binding)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(post.Pending)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("Claude decline consumed pending request", err)
		}
	}
	{
		// Secret shape is admitted only to the display/store fixture. Native async
		// source currently has no secret flag; future secret metadata stays fenced.
		st := agentquestion.NewStore(t.TempDir())
		now := time.Now().UTC()
		b := processhost.Binding{Agent: "agent", Pane: "pane", Generation: "generation"}
		raw := json.RawMessage(`[{"id":"secret","question":"Native only","isSecret":true,"isOther":true,"options":[]}]`)
		request := processhost.Request{ID: "call_secret", Kind: "async-question", Session: "thread", Connection: "connection", Turn: "original", Input: raw}
		snap := processhost.Snapshot{Binding: b, State: "ready", Session: "thread", Connection: "connection", AsyncQuestions: []processhost.AsyncQuestion{{Request: request, CreatedAt: now}}}
		id := codexAsyncQuestionID(b, request)
		if _, err := st.AsyncStore().Create(agentquestion.Record{ID: id, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, SessionID: request.Session, RequestID: request.ID, ToolUseID: request.Turn, Provider: "codex", Delivery: "async", Questions: raw, CreatedAt: now, Deadline: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		before, readErr := os.ReadFile(st.AsyncStore().Path())
		if readErr != nil {
			t.Fatal(readErr)
		}
		view, err := asyncQuestionView(id, request, st, &snap)
		if err != nil || view.CanDecline || !view.CanAnswer {
			t.Fatal(view, err)
		}
		control := &codexProcessControl{questions: st, records: map[string]processhost.Request{id: request}}
		_, err = control.answerAsyncQuestion(context.Background(), processQuestionRequest{Binding: b, QuestionID: id, Action: "decline"}, snap)
		var typed *asyncAnswerError
		if !errors.As(err, &typed) || typed.receipt.Code != "question-secret-native-only" || typed.receipt.Outcome != "" {
			t.Fatal(err)
		}
		after, readErr := os.ReadFile(st.AsyncStore().Path())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("secret decline claimed")
		}
	}
	// Parsing decline cannot accept an answer selection or an implicit Agent.
	for _, args := range [][]string{{"decline"}, {"decline", "agent"}, {"decline", "agent", "question", "--text", "1=no"}, {"decline", "agent", "question", "--option", "1=x"}, {"decline", "agent", "question", "--index", "1=1"}} {
		if _, err := parseAgentQuestionArgs(args, new(bytes.Buffer)); err == nil {
			t.Fatal(args)
		}
	}
	var _ processquestions.DeclineReceiptDispatcher = ProcessQuestionDispatcher{}
}
