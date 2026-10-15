package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func newAsyncAnswerFixture(t *testing.T, active bool, behavior ...string) (*processCodexFixture, *projmux.Client, string) {
	t.Helper()
	f := newAsyncProcessFixture(t, nativeAppAsyncFrames[:2], !active, behavior...)
	b := f.endpoint.binding
	state := filepath.Dir(filepath.Dir(f.path))
	f.control.questions = agentquestion.NewStore(state)
	f.control.questionWindow = 7 * 24 * time.Hour
	processQuestionCallbacks.Store(b, processQuestionCallback(f.control.exactQuestions))
	t.Cleanup(func() { processQuestionCallbacks.Delete(b) })
	f.turn(t, "first", "fixture")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.AsyncQuestions) == 1 && (active || s.Turn == "") })
	result := processAgentCreateResult{Binding: b, Provider: aiModeCodex, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: f.path}
	if err := result.recordProcessSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if err := f.control.syncControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: filepath.Join(f.root, "config"), HomeDir: f.root})
	if err != nil {
		t.Fatal(err)
	}
	return f, client, codexAsyncQuestionID(b, snap.AsyncQuestions[0].Request)
}

func TestAsyncAnswerCLIAndSDKSubmittedReadbackOnce(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(fmt.Sprintf("active-%t", active), func(t *testing.T) {
			f, client, id := newAsyncAnswerFixture(t, active)
			b := f.endpoint.binding
			store, _, projection := newProcessAttentionFixture(t, f.root, b, "codex")
			f.control.attention = projection
			if err := projection.sync(f.endpoint.handle, b); err != nil {
				t.Fatal(err)
			}
			list, err := client.ListQuestions(context.Background(), "uid:"+b.Agent)
			if err != nil || len(list.Questions) != 1 || !list.Questions[0].CanAnswer {
				t.Fatal(list, err)
			}
			deadline := list.Questions[0].Deadline
			text := "  explicit é answer  "
			if active {
				result, err := client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Option: ptr("cobalt")}}})
				if err != nil || result.QuestionID != id || result.Delivery != "async" || result.Outcome != "submitted" {
					t.Fatal(result, err)
				}
			} else {
				reg, err := f.store.LoadDegradedReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				agent, _ := reg.Agent(b.Agent)
				command := &agentCommand{loadRegistry: f.store.LoadDegradedReadOnly, messagePaths: agentMessagePaths{registryPath: f.path}, questionStore: func() (*agentquestion.Store, error) { return f.control.questions, nil }}
				var out bytes.Buffer
				if err = command.answerQuestion(agentQuestionRequest{spelling: "agent question answer", questionID: id, texts: []string{"1=" + text}}, *agent, nil, &out); err != nil || !strings.Contains(out.String(), id+" submitted") || strings.Contains(out.String(), "answered") {
					t.Fatal(out.String(), err)
				}
			}
			list, err = client.ListQuestions(context.Background(), "uid:"+b.Agent)
			if err != nil || len(list.Questions) != 1 || list.Questions[0].ID != id || list.Questions[0].State != core.QuestionClosed || list.Questions[0].Disposition != "async-submitted" || list.Questions[0].CanAnswer || !list.Questions[0].Deadline.Equal(deadline) || len(list.Questions[0].Answers) != 0 {
				t.Fatal(list, err)
			}
			duplicate, err := client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: &text}}})
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionNotPending || duplicate.Outcome != "submitted" {
				t.Fatal(duplicate, err)
			}
			wire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			method := "turn/start"
			if active {
				method = "turn/steer"
			}
			submitted := 0
			for line := range bytes.SplitSeq(wire, []byte("\n")) {
				var message struct {
					Method string `json:"method"`
					Params struct {
						Input []struct {
							Text string `json:"text"`
						} `json:"input"`
					} `json:"params"`
				}
				if json.Unmarshal(line, &message) != nil || message.Method != method {
					continue
				}
				for _, input := range message.Params.Input {
					if strings.Contains(input.Text, "Operator answers") {
						submitted++
						if !strings.Contains(input.Text, "call_42babc3b4f8d4f14806598aaf0439781") || !strings.Contains(input.Text, "CORE576 native probe: which color?") {
							t.Fatal(input.Text)
						}
						want := "cobalt"
						if !active {
							want = text
						}
						if !strings.Contains(input.Text, want) {
							t.Fatal("answer changed", input.Text)
						}
					}
				}
			}
			if submitted != 1 {
				t.Fatal("wire count", submitted, string(wire))
			}
			attention, err := store.read()
			if err != nil || len(attention[b.Pane].Pending) != 0 {
				t.Fatal("attention not retired", attention, err)
			}
		})
	}
}

func TestAsyncAnswerConcurrentSubmissionAndValidationEffects(t *testing.T) {
	f, client, id := newAsyncAnswerFixture(t, true)
	b := f.endpoint.binding
	beforeWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	beforeStore, _ := os.ReadFile(f.control.questions.AsyncStore().Path())
	for _, request := range []projmux.AnswerQuestionRequest{
		{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr(strings.Repeat("é", 2049))}}},
		{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Option: ptr("missing")}}},
	} {
		if _, err := client.AnswerQuestion(context.Background(), request); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	afterWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	afterStore, _ := os.ReadFile(f.control.questions.AsyncStore().Path())
	if !bytes.Equal(beforeWire, afterWire) || !bytes.Equal(beforeStore, afterStore) {
		t.Fatal("validation changed state")
	}
	snap, _ := f.endpoint.handle.Observe(b)
	if snap.AsyncQuestions[0].Closed {
		t.Fatal("validation locked form")
	}
	var wg sync.WaitGroup
	success := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			r, err := client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("explicit")}}})
			success <- err == nil && r.Outcome == "submitted"
		})
	}
	wg.Wait()
	close(success)
	count := 0
	for ok := range success {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatal("accepted", count)
	}
}

func TestAsyncAnswerUnknownPreservesResultAndNeverResends(t *testing.T) {
	for _, behavior := range []string{"refused", "lost"} {
		t.Run(behavior, func(t *testing.T) {
			f, client, id := newAsyncAnswerFixture(t, true, behavior)
			b := f.endpoint.binding
			result, err := client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("explicit")}}})
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionDeliveryUnknown || result.QuestionID != id || result.Delivery != "async" || result.Outcome != "unknown" || strings.Contains(err.Error(), "nothing was changed") {
				t.Fatal(result, err)
			}
			stored, found, err := f.control.questions.AsyncStore().Get(id)
			if err != nil || !found || stored.Disposition != "async-unknown" {
				t.Fatal(stored, err)
			}
			before, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			_, _ = client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("retry")}}})
			after, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			if !bytes.Equal(before, after) {
				t.Fatal("unknown resent")
			}
		})
	}
}

func TestAsyncClaimCrashPruneControllerReplacementCannotReopen(t *testing.T) {
	for _, settlement := range []string{"unknown", "declined"} {
		for _, capacity := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-capacity-%t", settlement, capacity), func(t *testing.T) {
				f, _, id := newAsyncAnswerFixture(t, false)
				b := f.endpoint.binding
				st := f.control.questions.AsyncStore()
				r, _, err := st.Get(id)
				if err != nil {
					t.Fatal(err)
				}
				// Keep the frozen claim boundary, with either a lost controller before
				// native input or an already settled decline, then exercise old pruning.
				if err = f.endpoint.handle.RetainAsyncQuestion(b, f.control.records[id], r.Deadline, true); err != nil {
					t.Fatal(err)
				}
				claim, claimErr := st.BeginAsync(r)
				if claimErr != nil {
					t.Fatal(claimErr)
				}
				if settlement == "declined" {
					if err = st.FinishAsync(claim, "declined"); err != nil {
						t.Fatal(err)
					}
				}
				raw, err := os.ReadFile(st.Path())
				if err != nil {
					t.Fatal(err)
				}
				var disk struct {
					Version int                    `json:"version"`
					Records []agentquestion.Record `json:"records"`
				}
				if err = json.Unmarshal(raw, &disk); err != nil {
					t.Fatal(err)
				}
				// Frozen T1 pruning: terminal rows older than 24h are discarded, and
				// capacity removes the oldest terminal other than direct-answer-pending.
				clock := r.UpdatedAt.Add(25 * time.Hour)
				if capacity {
					clock = r.UpdatedAt
					for i := range 64 {
						copy := r
						copy.ID = fmt.Sprintf("question-%016x", i+1)
						copy.State = agentquestion.StateWaiting
						copy.Disposition = ""
						copy.UpdatedAt = clock
						copy.Deadline = clock.Add(time.Hour)
						disk.Records = append(disk.Records, copy)
					}
				}
				kept := disk.Records[:0]
				for _, row := range disk.Records {
					if row.State.Terminal() && row.UpdatedAt.Before(clock.Add(-24*time.Hour)) {
						continue
					}
					kept = append(kept, row)
				}
				disk.Records = kept
				for len(disk.Records) > 64 {
					oldest := -1
					for i, row := range disk.Records {
						if row.State.Terminal() && row.Disposition != "direct-answer-pending" && (oldest < 0 || row.UpdatedAt.Before(disk.Records[oldest].UpdatedAt)) {
							oldest = i
						}
					}
					if oldest < 0 {
						t.Fatal("T1 capacity")
					}
					disk.Records = append(disk.Records[:oldest], disk.Records[oldest+1:]...)
				}
				raw, _ = json.Marshal(disk)
				if err = os.WriteFile(st.Path(), raw, 0600); err != nil {
					t.Fatal(err)
				}
				replacement := &codexProcessControl{endpoint: f.endpoint, questions: f.control.questions, questionWindow: f.control.questionWindow, now: f.control.now}
				snap, _ := f.endpoint.handle.Observe(b)
				if err = replacement.captureAsyncSnapshot(snap); err != nil {
					t.Fatal(err)
				}
				views, err := exactQuestionViews(aiModeCodex, replacement.records, nil, replacement.questions, snap)
				if err != nil || len(views) != 0 {
					t.Fatal("pruned claim reopened", views, err)
				}
				if err = f.endpoint.handle.Turn(context.Background(), f.endpoint.authority(), "replay", "fixture"); err != nil {
					t.Fatal(err)
				}
				replay := f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" && s.Sequence > snap.Sequence })
				if !replay.AsyncQuestions[0].Closed {
					t.Fatal("replay reopened host tombstone")
				}
				if err = replacement.captureAsyncSnapshot(replay); err != nil {
					t.Fatal(err)
				}
				before, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
				_, err = replacement.answerAsyncQuestion(context.Background(), processQuestionRequest{Binding: b, QuestionID: id, Selections: map[int]agentquestion.Selection{0: {Text: "retry", HasText: true}}}, snap)
				if err == nil {
					t.Fatal("reopened")
				}
				_, declineErr := replacement.exactQuestions(context.Background(), processQuestionRequest{Binding: b, QuestionID: id, Action: "decline"})
				if declineErr == nil {
					t.Fatal("pruned question declined again")
				}
				after, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
				if !bytes.Equal(before, after) {
					t.Fatal("wire after crash")
				}
			})
		}
	}
}

func TestAsyncAnswerFenceThenStorageOrBeforeWriteFailure(t *testing.T) {
	for _, action := range []string{"", "decline"} {
		for _, storageFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("action-%s-storage-%t", action, storageFailure), func(t *testing.T) {
				f, _, id := newAsyncAnswerFixture(t, true)
				b := f.endpoint.binding
				st := f.control.questions.AsyncStore()
				original, err := os.ReadFile(st.Path())
				if err != nil {
					t.Fatal(err)
				}
				lock, err := os.OpenFile(st.Path()+".flock", os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				before, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					request := processQuestionRequest{Binding: b, QuestionID: id, Action: action}
					if action == "" {
						request.Selections = map[int]agentquestion.Selection{0: {Text: "explicit", HasText: true}}
					}
					_, err := f.control.exactQuestions(ctx, request)
					result <- err
				}()
				f.wait(t, func(s processhost.Snapshot) bool { return s.AsyncQuestions[0].Closed })
				if storageFailure {
					if err = os.WriteFile(st.Path(), []byte("malformed fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					cancel()
				}
				if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
					t.Fatal(err)
				}
				err = <-result
				var receipt *asyncAnswerError
				if !errors.As(err, &receipt) || receipt.receipt.Outcome != "not-submitted" || receipt.receipt.Code == "question-delivery-unknown" {
					t.Fatal(receipt, err)
				}
				after, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
				if !bytes.Equal(before, after) {
					t.Fatal("before-write failure wrote native input")
				}
				if storageFailure {
					if !errors.Is(err, agentquestion.ErrMalformedStore) {
						t.Fatal("storage failure hidden", err)
					}
					if _, readErr := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b}); !errors.Is(readErr, agentquestion.ErrMalformedStore) {
						t.Fatal("read error hidden", readErr)
					}
					if err = os.WriteFile(st.Path(), original, 0600); err != nil {
						t.Fatal(err)
					}
					// Reconciliation closes the original waiting record as not submitted;
					// it must not mistake this form fence for a provider process exit.
					if _, err = f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b}); err != nil {
						t.Fatal(err)
					}
				}
				stored, found, err := st.Get(id)
				if err != nil || !found || stored.State != agentquestion.StateClosed || stored.Disposition != "async-not-submitted" {
					t.Fatal(stored, err)
				}
			})
		}
	}
}

func TestAsyncAnswerMultiplePromptsStayInOneOperatorInput(t *testing.T) {
	frames := append([]string(nil), nativeAppAsyncFrames[:2]...)
	for i := range frames {
		var frame map[string]any
		if err := json.Unmarshal([]byte(frames[i]), &frame); err != nil {
			t.Fatal(err)
		}
		item := frame["params"].(map[string]any)["item"].(map[string]any)
		item["questions"] = []any{map[string]any{"title": "Pick color", "options": []string{"cobalt", "amber"}}, map[string]any{"title": "Explain choice", "options": []string{}}}
		raw, _ := json.Marshal(frame)
		frames[i] = string(raw)
	}
	f := newAsyncProcessFixture(t, frames, true)
	b := f.endpoint.binding
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
	before, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	_, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b, QuestionID: id, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"cobalt"}}}})
	if !errors.Is(err, agentquestion.ErrInvalidAnswer) {
		t.Fatal("partial accepted", err)
	}
	after, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatal("partial native write")
	}
	_, err = f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b, QuestionID: id, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"cobalt"}}, 1: {Text: "  rationale é  ", HasText: true}}})
	var receipt *asyncAnswerError
	if !errors.As(err, &receipt) || receipt.cause != nil || receipt.receipt.Outcome != "submitted" {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	answers := 0
	for line := range bytes.SplitSeq(after, []byte("\n")) {
		var m struct {
			Method string `json:"method"`
			Params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &m) != nil || m.Method != "turn/start" {
			continue
		}
		for _, input := range m.Params.Input {
			if strings.Contains(input.Text, "Operator answers") {
				answers++
				if !strings.Contains(input.Text, "Pick color") || !strings.Contains(input.Text, "Explain choice") || !strings.Contains(input.Text, "  rationale é  ") {
					t.Fatal(input.Text)
				}
			}
		}
	}
	if answers != 1 {
		t.Fatal("multi input count", answers)
	}
}

func TestAsyncAnswerAcceptedButLostSettlementStaysUnknown(t *testing.T) {
	f, client, id := newAsyncAnswerFixture(t, true, "prune")
	b := f.endpoint.binding
	result, err := client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("explicit")}}})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionDeliveryUnknown || result.QuestionID != id || result.Delivery != "async" || result.Outcome != "unknown" {
		t.Fatal(result, err)
	}
	snap, err := f.endpoint.handle.Observe(b)
	if err != nil || snap.State != "ready" || !snap.AsyncQuestions[0].Closed {
		t.Fatal("settle failure reopened or stopped provider", snap, err)
	}
	list, err := client.ListQuestions(context.Background(), "uid:"+b.Agent)
	if err != nil || len(list.Questions) != 0 {
		t.Fatal("pruned claim recreated", list, err)
	}
	before, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	_, err = client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("retry")}}})
	if err == nil {
		t.Fatal("lost settlement resubmitted")
	}
	after, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatal("lost settlement wrote twice")
	}
}

func TestAsyncAnswerAppDispatcherOptionalReceipt(t *testing.T) {
	f, _, id := newAsyncAnswerFixture(t, true)
	d := ProcessQuestionDispatcher{RegistryPath: f.path}
	if !d.SupportsAsyncQuestionAnswers() {
		t.Fatal("optional receipt capability absent")
	}
	receipt, err := d.AnswerExactProcessQuestionReceipt(context.Background(), f.endpoint.binding.Agent, id, map[int]agentquestion.Selection{0: {Text: "explicit", HasText: true}})
	if err != nil || receipt.QuestionID != id || receipt.Delivery != "async" || receipt.Outcome != "submitted" {
		t.Fatal(receipt, err)
	}
	before, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if err = d.AnswerExactProcessQuestion(context.Background(), f.endpoint.binding.Agent, id, map[int]agentquestion.Selection{0: {Text: "retry", HasText: true}}); err == nil {
		t.Fatal("legacy entrypoint replayed settled async answer")
	}
	after, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatal("legacy adapter replayed input")
	}
}
