package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCodexAsyncQuestionCLIAndSDKNativeSourceReadOnly(t *testing.T) {
	for i := 0; i < len(nativeAppAsyncFrames); i += 2 {
		f := newAsyncProcessFixture(t, nativeAppAsyncFrames[i:i+2], true)
		b := f.endpoint.binding
		state := filepath.Dir(filepath.Dir(f.path))
		f.control.questions = agentquestion.NewStore(state)
		// Frozen T1 host behavior: listings are read-only and direct async
		// submission is refused. The upgraded client must not infer capability.
		oldHost := processQuestionCallback(func(ctx context.Context, r processQuestionRequest) ([]ExactProcessQuestion, error) {
			if r.QuestionID != "" {
				return nil, processhost.ErrStale
			}
			views, err := f.control.exactQuestions(ctx, r)
			for i := range views {
				if views[i].Delivery == "async" {
					views[i].CanAnswer = false
				}
			}
			return views, err
		})
		processQuestionCallbacks.Store(b, oldHost)
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
		reg, err := f.store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		agent, _ := reg.Agent(b.Agent)
		command := &agentCommand{loadRegistry: f.store.LoadDegradedReadOnly, messagePaths: agentMessagePaths{registryPath: f.path}, questionStore: func() (*agentquestion.Store, error) { return f.control.questions, nil }}
		var out bytes.Buffer
		if err = command.listQuestions(agentQuestionRequest{spelling: "agent question list", json: true}, *agent, &out); err != nil {
			t.Fatal(err)
		}
		var listed agentQuestionList
		if err = json.Unmarshal(out.Bytes(), &listed); err != nil || len(listed.Questions) != 1 || len(listed.ExactQuestions) != 1 {
			t.Fatal(out.String(), err)
		}
		q := listed.Questions[0]
		if q.State != agentquestion.StateWaiting || q.Delivery != "async" || q.CanAnswer || len(q.Prompts[0].Options) != 2 || !q.Prompts[0].IsOther {
			t.Fatal(q)
		}
		client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: filepath.Join(f.root, "config"), HomeDir: f.root})
		if err != nil {
			t.Fatal(err)
		}
		sdk, err := client.ListQuestions(context.Background(), "uid:"+b.Agent)
		if err != nil || questionJSON(t, listed) != questionJSON(t, sdk) {
			t.Fatal("SDK parity", err)
		}
		beforeWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
		beforeStore, _ := os.ReadFile(f.control.questions.AsyncStore().Path())
		err = command.answerQuestion(agentQuestionRequest{spelling: "agent question answer", questionID: q.ID, texts: []string{"1=explicit answer"}}, *agent, nil, &out)
		if err == nil {
			t.Fatal("CLI async answer accepted")
		}
		_, err = client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: q.ID, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("explicit answer")}}})
		if err == nil {
			t.Fatal("SDK async answer accepted")
		}
		if _, err = oldHost(context.Background(), processQuestionRequest{Binding: b, QuestionID: q.ID, Selections: map[int]agentquestion.Selection{0: {Text: "answer", HasText: true}}}); !errors.Is(err, processhost.ErrStale) {
			t.Fatal("direct callback", err)
		}
		afterWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
		afterStore, _ := os.ReadFile(f.control.questions.AsyncStore().Path())
		if !bytes.Equal(beforeWire, afterWire) || !bytes.Equal(beforeStore, afterStore) {
			t.Fatal("answer refusal had effects")
		}
		if err = f.endpoint.handle.Turn(context.Background(), f.endpoint.authority(), "next", "fixture"); err != nil {
			last, _ := f.endpoint.handle.Observe(b)
			t.Fatalf("next turn: %v state=%s failure=%s", err, last.State, last.Failure)
		}
		snap = f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" && s.Sequence > snap.Sequence })
		events, _, eventsErr := f.endpoint.handle.Events(b, 0)
		gap := false
		for _, event := range events {
			if event.Kind == "stream-gap" {
				gap = true
			}
		}
		if eventsErr != nil || !gap {
			t.Fatal("fixture did not exercise host ring gap", eventsErr)
		}
		if err = f.control.syncControls(context.Background()); err != nil {
			t.Fatal(err)
		}
		same, err := client.ListQuestions(context.Background(), "uid:"+b.Agent)
		if err != nil || len(same.Questions) != 1 || same.Questions[0].ID != q.ID || !same.Questions[0].Deadline.Equal(q.Deadline) || same.Questions[0].State != core.QuestionWaiting {
			t.Fatal("next turn lost async", err)
		}
		if err = f.endpoint.handle.Stop(b); err != nil {
			t.Fatal(err)
		}
		_ = f.control.syncControls(context.Background())
		closed, _, err := f.control.questions.AsyncStore().Get(q.ID)
		if err != nil || closed.State != agentquestion.StateClosed {
			t.Fatal("exit did not fence", err)
		}
	}
}

func TestCodexAsyncCaptureGetPruneRaceKeepsProviderReady(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(map[bool]string{true: "terminal-prune", false: "active-loss"}[terminal], func(t *testing.T) {
			f := newAsyncProcessFixture(t, nativeAppAsyncFrames[:2], true)
			b := f.endpoint.binding
			f.turn(t, "first", "fixture")
			snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.AsyncQuestions) == 1 && s.Turn == "" })
			clock := snap.AsyncQuestions[0].CreatedAt
			base := agentquestion.NewStore(filepath.Dir(filepath.Dir(f.path))).WithClock(func() time.Time { return clock })
			f.control.questions = base
			result := processAgentCreateResult{Binding: b, Provider: aiModeCodex, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: f.path}
			if err := result.recordProcessSnapshot(snap); err != nil {
				t.Fatal(err)
			}
			if err := f.control.captureAsyncSnapshot(snap); err != nil {
				t.Fatal(err)
			}
			id := codexAsyncQuestionID(b, snap.AsyncQuestions[0].Request)
			sidecar := base.AsyncStore()
			if terminal {
				if _, err := sidecar.Close(id, agentquestion.CloseReason("process-ended")); err != nil {
					t.Fatal(err)
				}
				clock = clock.Add(26 * time.Hour)
			}
			// Get invokes its clock after reading the file. A separate normal writer
			// prunes the loaded terminal record before capture's second Get.
			gets := 0
			other := sidecar.WithClock(func() time.Time { return clock })
			f.control.questions = base.WithClock(func() time.Time {
				gets++
				if gets == 1 {
					if terminal {
						_, err := other.Create(agentquestion.Record{ID: "question-ffffffffffffffff", Provider: "codex", Delivery: "async", AgentUID: "other", Questions: snap.AsyncQuestions[0].Request.Input, CreatedAt: clock, Deadline: clock.Add(time.Hour)})
						if err != nil {
							t.Fatal(err)
						}
					} else if err := os.WriteFile(sidecar.Path(), []byte(`{"version":1,"records":[]}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return clock
			})
			current, err := f.endpoint.handle.Observe(b)
			if err != nil {
				t.Fatal(err)
			}
			beforeWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			synchronize := processSnapshotSynchronizer(f.control.captureAsyncSnapshot, f.control.captureAsyncSnapshot)
			err = synchronize(current)
			if !terminal {
				if !errors.Is(err, agentquestion.ErrNotFound) {
					t.Fatal("active second-Get loss was ignored", err)
				}
				return
			}
			if err != nil {
				t.Fatal("normal prune became owner synchronization failure", err)
			}
			if _, tracked := f.control.records[id]; tracked {
				t.Fatal("terminal record not retired")
			}
			after, err := f.endpoint.handle.Observe(b)
			if err != nil || after.State != "ready" || after.Exit != nil || !after.AsyncQuestions[0].Closed || !after.AsyncQuestions[0].Deadline.Equal(current.AsyncQuestions[0].Deadline) {
				t.Fatal("provider/deadline was changed", after, err)
			}
			views, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b})
			if err != nil || len(views) != 0 {
				t.Fatal("list poisoned after normal prune", views, err)
			}
			afterWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
			if !bytes.Equal(beforeWire, afterWire) {
				t.Fatal("prune wrote provider input")
			}
			persisted, found, err := sidecar.Get(id)
			if err != nil || found {
				t.Fatal("pruned history recreated", persisted, err)
			}
			beforeStore, _ := os.ReadFile(sidecar.Path())
			if err = synchronize(after); err != nil {
				t.Fatal("terminal replay poisoned synchronization", err)
			}
			afterStore, _ := os.ReadFile(sidecar.Path())
			if !bytes.Equal(beforeStore, afterStore) {
				t.Fatal("terminal replay rewrote history")
			}
			if err = f.endpoint.handle.Turn(context.Background(), f.endpoint.authority(), "next", "fixture"); err != nil {
				t.Fatal("provider stopped after prune", err)
			}
			next := f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" && s.Sequence > after.Sequence })
			if err = synchronize(next); err != nil {
				t.Fatal("next turn poisoned by tombstone", err)
			}
			live, err := f.endpoint.handle.Observe(b)
			if err != nil || live.State != "ready" || live.Exit != nil {
				t.Fatal("provider not alive", live, err)
			}
		})
	}
}
