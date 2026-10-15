package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Native 0.162.1 raw frames from the active-turn and after-turn probes.
var nativeAppAsyncFrames = []string{
	`{"method":"item/started","params":{"item":{"type":"agentMessage","id":"call_42babc3b4f8d4f14806598aaf0439781","text":"CORE576 native probe: which color?\n- cobalt\n- amber","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 native probe: which color?","options":["cobalt","amber"]}]},"threadId":"01a13b1c-72ab-7a13-a794-a815f52c29d2","turnId":"01a13b1c-72d8-7b72-ad20-5f18583ed08c","startedAtMs":1791993085978},"emittedAtMs":1791993085978}`,
	`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"call_42babc3b4f8d4f14806598aaf0439781","text":"CORE576 native probe: which color?\n- cobalt\n- amber","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 native probe: which color?","options":["cobalt","amber"]}]},"threadId":"01a13b1c-72ab-7a13-a794-a815f52c29d2","turnId":"01a13b1c-72d8-7b72-ad20-5f18583ed08c","completedAtMs":1791993085978},"emittedAtMs":1791993085978}`,
	`{"method":"item/started","params":{"item":{"type":"agentMessage","id":"call_98dc8036f5814ba4a7b8fc6770b58047","text":"CORE576 after-turn probe: which shape?\n- circle\n- square","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 after-turn probe: which shape?","options":["circle","square"]}]},"threadId":"01a13b1e-7bdc-7e32-b9b1-e532526a6d92","turnId":"01a13b1e-7c09-7d82-ac84-4248b585ddaf","startedAtMs":1791993219280},"emittedAtMs":1791993219280}`,
	`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"call_98dc8036f5814ba4a7b8fc6770b58047","text":"CORE576 after-turn probe: which shape?\n- circle\n- square","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 after-turn probe: which shape?","options":["circle","square"]}]},"threadId":"01a13b1e-7bdc-7e32-b9b1-e532526a6d92","turnId":"01a13b1e-7c09-7d82-ac84-4248b585ddaf","completedAtMs":1791993219280},"emittedAtMs":1791993219280}`,
}

func TestCodexAsyncStableExactKeyAndDeadline(t *testing.T) {
	b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
	now := time.Now().UTC()
	clock := now
	store := agentquestion.NewStore(filepath.Join(t.TempDir(), "store")).AsyncStore().WithClock(func() time.Time { return clock })
	q := processhost.AsyncQuestion{CreatedAt: now, Request: processhost.Request{ID: "call_item", Session: "thread", Connection: "connection", Turn: "turn", Kind: "async-question", Input: json.RawMessage(`[{"id":"q1","question":"Text only","options":[],"isOther":true}]`)}}
	snap := processhost.Snapshot{Binding: b, State: "ready", Session: "thread", Connection: "connection", Turn: "turn", AsyncQuestions: []processhost.AsyncQuestion{q}}
	var records map[string]processhost.Request
	if err := captureExactQuestions(aiModeCodex, b, snap, &records, new(map[string]bool), nil, store, time.Minute, func() time.Time { return clock }); err != nil {
		t.Fatal(err)
	}
	id := codexAsyncQuestionID(b, q.Request)
	original, found, err := store.Get(id)
	if err != nil || !found {
		t.Fatal(err)
	}
	snap.Turn = "next"
	clock = now.Add(30 * time.Second)
	if err = captureAsyncQuestions(b, snap, &records, store, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	same, _, _ := store.Get(id)
	if !same.Deadline.Equal(original.Deadline) || same.State != agentquestion.StateWaiting {
		t.Fatal("deadline renewed or turn ended", same)
	}
	views, err := exactQuestionViews(aiModeCodex, records, nil, store)
	if err != nil || len(views) != 1 || views[0].Delivery != "async" || views[0].CanAnswer || !views[0].Prompts[0].IsOther || len(views[0].Prompts[0].Options) != 0 {
		t.Fatal(views, err)
	}
	if _, _, _, err = exactQuestionRequest(aiModeCodex, processQuestionRequest{QuestionID: id, Selections: map[int]agentquestion.Selection{0: {Text: "answer", HasText: true}}}, records, nil, store); err == nil {
		t.Fatal("async gained direct answer admission")
	}
	for _, change := range []func(*processhost.Binding, *processhost.Request){
		func(b *processhost.Binding, r *processhost.Request) { b.Project = "other" }, func(b *processhost.Binding, r *processhost.Request) { b.Window = "other" }, func(b *processhost.Binding, r *processhost.Request) { b.Agent = "other" }, func(b *processhost.Binding, r *processhost.Request) { b.Pane = "other" }, func(b *processhost.Binding, r *processhost.Request) { b.Generation = "other" }, func(b *processhost.Binding, r *processhost.Request) { r.Session = "other" }, func(b *processhost.Binding, r *processhost.Request) { r.Connection = "other" }, func(b *processhost.Binding, r *processhost.Request) { r.Turn = "other" }, func(b *processhost.Binding, r *processhost.Request) { r.ID = "other" },
	} {
		other, request := b, q.Request
		change(&other, &request)
		if codexAsyncQuestionID(other, request) == id {
			t.Fatal("identity alias")
		}
	}
	clock = original.Deadline
	if err = captureAsyncQuestions(b, snap, &records, store, time.Minute); err != nil {
		t.Fatal(err)
	}
	expired, _, _ := store.Get(id)
	if expired.State != agentquestion.StateExpired {
		t.Fatal("expiry")
	}
	clock = now.Add(30 * time.Second)
	if _, err = store.Close(id, agentquestion.CloseReason("process-ended")); err != nil {
		t.Fatal(err)
	}
	if err = captureAsyncQuestions(b, snap, &records, store, time.Minute); err != nil {
		t.Fatal(err)
	}
	closed, _, _ := store.Get(id)
	if closed.State != agentquestion.StateClosed {
		t.Fatal("terminal replay reopened")
	}
	wrong := snap
	wrong.Connection = "wrong"
	if err = captureAsyncQuestions(b, wrong, &records, store, time.Minute); err == nil {
		t.Fatal("wrong connection")
	}
	replacement := b
	replacement.Generation = "new"
	snap.Binding = replacement
	snap.AsyncQuestions = nil
	if err = captureAsyncQuestions(replacement, snap, &records, store, time.Minute); err != nil {
		t.Fatal(err)
	}
}

// The protocol fixture emits the original native frames without changing their
// item/message shape. Only the handshake is synthetic; no real provider runs.
func newAsyncProcessFixture(t *testing.T, frames []string, complete bool, behaviors ...string) *processCodexFixture {
	t.Helper()
	framesJSON, _ := json.Marshal(frames)
	script := `import json,sys,os,time
frames=[json.loads(x) for x in json.loads(FRAMES)]
thread=frames[0]['params']['threadId']; turn=frames[0]['params']['turnId']
count=0
for line in sys.stdin:
 with open(os.path.join(os.getcwd(),'wire.jsonl'),'a') as log: log.write(line)
 m=json.loads(line); method=m.get('method','')
 def emit(x): print(json.dumps(x),flush=True)
 def reply(x): emit({'id':m['id'],'result':x})
 if method=='initialize': reply({'userAgent':'projmux/0.162.1'})
 elif method=='thread/start': reply({'thread':{'id':thread},'model':'stub-model','reasoningEffort':'low','sandbox':{'type':'readOnly'},'approvalPolicy':'on-request'})
 elif method=='turn/steer':
  if BEHAVIOR=='lost': sys.exit(0)
  elif BEHAVIOR=='refused': emit({'id':m['id'],'error':{'code':-32000,'message':'fixture refusal'}})
  elif BEHAVIOR=='prune':
   os.unlink(os.path.join(os.getcwd(),'state','projmux','agent-questions','.async','questions.json'))
   reply({})
  else: reply({})
 elif method=='turn/start':
  count+=1
  if count>1 and BEHAVIOR=='lost': sys.exit(0)
  if count>1 and BEHAVIOR=='refused':
   emit({'id':m['id'],'error':{'code':-32000,'message':'fixture refusal'}}); continue
  current=turn if count==1 else 'next-turn'
  reply({'turn':{'id':current}})
  if count==1:
   for f in frames: emit(f)
  else:
   for i in range(12):
    emit({'method':'item/agentMessage/delta','params':{'threadId':thread,'turnId':current,'delta':'fixture'}}); time.sleep(0.005)
   for f in frames: emit(f)
  if COMPLETE or count>1: emit({'method':'turn/completed','params':{'threadId':thread,'turn':{'id':current,'status':'completed'}}})
`
	// JSON is embedded as a Python string literal, with no shell interpolation.
	quoted, _ := json.Marshal(string(framesJSON))
	script = strings.ReplaceAll(script, "FRAMES", string(quoted))
	flag := "False"
	if complete {
		flag = "True"
	}
	script = strings.ReplaceAll(script, "COMPLETE", flag)
	behavior := ""
	if len(behaviors) > 0 {
		behavior = behaviors[0]
	}
	behaviorJSON, _ := json.Marshal(behavior)
	script = strings.ReplaceAll(script, "BEHAVIOR", string(behaviorJSON))
	return newProcessCodexFixtureWithEvents(t, func(root, binary string, env []string) processhost.Command {
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", script}, Dir: root, Env: env}
	}, 8)
}

func TestCodexAsyncProjectionCapacityFailureAndGenerationFence(t *testing.T) {
	b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
	store := agentquestion.NewStore(t.TempDir()).AsyncStore()
	now := time.Now().UTC()
	input := json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[]}]`)
	for i := range 64 {
		id := fmt.Sprintf("question-%016x", i)
		if _, err := store.Create(agentquestion.Record{ID: id, Provider: "codex", Delivery: "async", AgentUID: "other", Questions: input, CreatedAt: now, Deadline: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	snap := processhost.Snapshot{Binding: b, State: "ready", Session: "thread", Connection: "connection", AsyncQuestions: []processhost.AsyncQuestion{{CreatedAt: now, Request: processhost.Request{ID: "call_item", Kind: "async-question", Session: "thread", Connection: "connection", Turn: "turn", Input: input}}}}
	var records map[string]processhost.Request
	if err := captureAsyncQuestions(b, snap, &records, store, time.Minute); !errors.Is(err, agentquestion.ErrCapacity) || len(records) != 0 {
		t.Fatal("failed capture stamped success", err)
	}
	own, err := store.List(b.Agent)
	if err != nil || len(own) != 0 {
		t.Fatal("failed capture persisted", err)
	}
	// Store read errors are surfaced and cannot produce success metadata.
	bad := agentquestion.NewStoreAt(filepath.Join(t.TempDir(), "malformed.json")).AsyncStore()
	if err = os.MkdirAll(filepath.Dir(bad.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(bad.Path(), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = captureAsyncQuestions(b, snap, &records, bad, time.Minute); !errors.Is(err, agentquestion.ErrMalformedStore) {
		t.Fatal(err)
	}
	fresh := agentquestion.NewStore(t.TempDir()).AsyncStore()
	if err = captureAsyncQuestions(b, snap, &records, fresh, time.Minute); err != nil {
		t.Fatal(err)
	}
	id := codexAsyncQuestionID(b, snap.AsyncQuestions[0].Request)
	replacement := b
	replacement.Generation = "new-generation"
	// The old controller fences its own records when Registry routing is lost.
	snap.State = "unknown"
	snap.AsyncQuestions = nil
	if err = captureAsyncQuestions(b, snap, &records, fresh, time.Minute); err != nil {
		t.Fatal(err)
	}
	closed, _, err := fresh.Get(id)
	if err != nil || closed.State != agentquestion.StateClosed || len(records) != 0 {
		t.Fatal("replacement retained old question", err)
	}
	snap.Binding, snap.State = replacement, "ready"
	if err = captureAsyncQuestions(replacement, snap, &records, fresh, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAsyncRetiringControllerPreservesOtherEndpoint(t *testing.T) {
	for _, difference := range []string{"generation", "pane", "session"} {
		t.Run(difference, func(t *testing.T) {
			b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "old-generation", Operation: "operation"}
			now := time.Now().UTC()
			store := agentquestion.NewStore(t.TempDir()).AsyncStore().WithClock(func() time.Time { return now })
			q := processhost.AsyncQuestion{CreatedAt: now, Request: processhost.Request{ID: "call_old", Session: "thread", Connection: "connection", Turn: "turn", Kind: "async-question", Input: json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[]}]`)}}
			snap := processhost.Snapshot{Binding: b, State: "ready", Session: q.Request.Session, Connection: q.Request.Connection, AsyncQuestions: []processhost.AsyncQuestion{q}}
			var records map[string]processhost.Request
			if err := captureAsyncQuestions(b, snap, &records, store, time.Minute); err != nil {
				t.Fatal(err)
			}
			otherBinding, otherRequest := b, q.Request
			otherRequest.ID = "call_new"
			switch difference {
			case "generation":
				otherBinding.Generation = "new-generation"
			case "pane":
				otherBinding.Pane = "other-pane"
			case "session":
				otherRequest.Session = "other-thread"
			}
			otherID := codexAsyncQuestionID(otherBinding, otherRequest)
			other, err := store.Create(agentquestion.Record{ID: otherID, Provider: aiModeCodex, Delivery: "async", AgentUID: b.Agent, PaneUID: otherBinding.Pane, Generation: otherBinding.Generation, SessionID: otherRequest.Session, RequestID: otherRequest.ID, ToolUseID: otherRequest.Turn, Questions: otherRequest.Input, CreatedAt: now, Deadline: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(other)
			snap.State = "unknown"
			if err = captureAsyncQuestions(b, snap, &records, store, time.Minute); err != nil {
				t.Fatal(err)
			}
			old, _, err := store.Get(codexAsyncQuestionID(b, q.Request))
			if err != nil || old.State != agentquestion.StateClosed || len(records) != 0 {
				t.Fatal("old endpoint was not fenced", old, err)
			}
			after, found, err := store.Get(otherID)
			afterJSON, _ := json.Marshal(after)
			if err != nil || !found || after.State != agentquestion.StateWaiting || string(before) != string(afterJSON) {
				t.Fatal("retiring controller changed another endpoint", after, err)
			}
		})
	}
}

func TestCodexAsyncAbsentDoesNotReadBlockingStoreClock(t *testing.T) {
	b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
	calls := 0
	store := agentquestion.NewStore(t.TempDir()).AsyncStore().WithClock(func() time.Time { calls++; return time.Now() })
	var records map[string]processhost.Request
	snap := processhost.Snapshot{Binding: b, State: "ready", Session: "thread", Connection: "connection"}
	if err := captureAsyncQuestions(b, snap, &records, store, time.Minute); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("async absence consumed blocking answer clock", calls)
	}
	records["question-native"] = processhost.Request{Kind: "question"}
	if err := captureAsyncQuestions(b, snap, &records, store, time.Minute); err != nil || calls != 0 {
		t.Fatal("blocking metadata consumed async clock", calls, err)
	}
}

func TestCodexAsyncTerminalPruneKeepsBlockingAndNewQuestions(t *testing.T) {
	for _, mode := range []string{"capacity", "24h"} {
		t.Run(mode, func(t *testing.T) {
			b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
			now := time.Now().UTC()
			clock := now
			store := agentquestion.NewStore(t.TempDir()).WithClock(func() time.Time { return clock })
			sidecar := store.AsyncStore()
			input := json.RawMessage(`[{"id":"q1","question":"Async","isOther":true,"options":[]}]`)
			old := processhost.AsyncQuestion{CreatedAt: now, Request: processhost.Request{ID: "call_old", Session: "thread", Connection: "connection", Turn: "old-turn", Kind: "async-question", Tool: "agentMessage", Input: input}}
			snap := processhost.Snapshot{Binding: b, State: "ready", Session: "thread", Connection: "connection", AsyncQuestions: []processhost.AsyncQuestion{old}}
			var records map[string]processhost.Request
			var native map[string]bool
			if err := captureAsyncQuestions(b, snap, &records, store, time.Hour); err != nil {
				t.Fatal(err)
			}
			id := codexAsyncQuestionID(b, old.Request)
			deadline := snap.AsyncQuestions[0].Deadline
			clock = now.Add(time.Hour)
			if _, err := sidecar.Close(id, agentquestion.CloseReason("process-ended")); err != nil {
				t.Fatal(err)
			}
			terminal, found, err := sidecar.Get(id)
			if err != nil || !found || terminal.State != agentquestion.StateExpired {
				t.Fatal("expiry not established", terminal, err)
			}
			snap.AsyncQuestions[0].Closed = true // the host's retained terminal observation
			addOther := func(n int) {
				_, err := sidecar.Create(agentquestion.Record{ID: fmt.Sprintf("question-%016x", n), Provider: "codex", Delivery: "async", AgentUID: "other", Questions: input, CreatedAt: clock, Deadline: clock.Add(48 * time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "capacity" {
				for i := range 63 {
					addOther(i)
				}
				addOther(63) // normal writer pressure evicts the old expired history
				if _, err = sidecar.Close("question-0000000000000000", agentquestion.CloseReason("process-ended")); err != nil {
					t.Fatal(err)
				}
			} else {
				clock = now.Add(26 * time.Hour)
				addOther(0)
			}
			if _, found, err = sidecar.Get(id); err != nil || found {
				t.Fatal("normal prune did not remove old history", err)
			}
			next := old
			next.Request.ID = "call_new"
			next.Request.Turn = "new-turn"
			next.CreatedAt = clock
			snap.Turn = "new-turn"
			snap.AsyncQuestions = append(snap.AsyncQuestions, next)
			snap.Pending = []processhost.Request{{ID: "blocking", Connection: "connection", Session: "thread", Turn: "new-turn", Kind: "question", Input: json.RawMessage(`{"method":"item/tool/requestUserInput","params":{"questions":[{"id":"held","question":"Blocking","isOther":true,"options":[]}]}}`)}}
			resolver := func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux }
			synchronize := processSnapshotSynchronizer(func(s processhost.Snapshot) error {
				return captureExactQuestions(aiModeCodex, b, s, &records, &native, resolver, store, time.Hour, func() time.Time { return clock })
			}, func(s processhost.Snapshot) error {
				return captureExactQuestions(aiModeCodex, b, s, &records, &native, resolver, store, time.Hour, func() time.Time { return clock })
			})
			if err = synchronize(snap); err != nil {
				t.Fatal("normal prune escalated through owner synchronization", err)
			}
			views, err := exactQuestionViews(aiModeCodex, records, native, store, snap)
			if err != nil || len(views) != 2 {
				t.Fatal("blocking/new async unavailable", views, err)
			}
			if _, tracked := records[id]; tracked {
				t.Fatal("pruned tombstone still tracked")
			}
			if !snap.AsyncQuestions[0].Deadline.Equal(deadline) || snap.AsyncQuestions[0].Request.Tool != old.Request.Tool || string(snap.AsyncQuestions[0].Request.Input) != string(old.Request.Input) {
				t.Fatal("proof renewed deadline or changed native input")
			}
			asyncSeen, blockingSeen := false, false
			for _, v := range views {
				if v.QuestionID == id {
					t.Fatal("pruned view reopened")
				}
				if v.Delivery == "async" {
					asyncSeen = v.CanAnswer && v.State == agentquestion.StateWaiting
				}
				if v.Delivery == "held-question" {
					blockingSeen = v.CanAnswer && v.State == agentquestion.StateWaiting
				}
			}
			if !asyncSeen || !blockingSeen {
				t.Fatal("read/answer contracts changed", views)
			}
			before, _ := os.ReadFile(sidecar.Path())
			beforeLegacy, _ := os.ReadFile(store.Path())
			if err = captureExactQuestions(aiModeCodex, b, snap, &records, &native, resolver, store, 2*time.Hour, func() time.Time { return clock }); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(sidecar.Path())
			afterLegacy, _ := os.ReadFile(store.Path())
			if string(before) != string(after) || string(beforeLegacy) != string(afterLegacy) {
				t.Fatal("terminal replay recreated history or rewrote deadlines")
			}
			if _, found, err = sidecar.Get(id); err != nil || found {
				t.Fatal("terminal replay reopened", err)
			}
		})
	}
}

func TestCodexAsyncViewPruneRaceRequiresTerminalProof(t *testing.T) {
	b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
	now := time.Now().UTC()
	clock := now
	store := agentquestion.NewStore(t.TempDir()).WithClock(func() time.Time { return clock })
	sidecar := store.AsyncStore()
	q := processhost.AsyncQuestion{CreatedAt: now, Request: processhost.Request{ID: "call_item", Session: "thread", Connection: "connection", Turn: "turn", Kind: "async-question", Tool: "agentMessage", Input: json.RawMessage(`[{"id":"q1","question":"Async","isOther":true,"options":[]}]`)}}
	snap := processhost.Snapshot{Binding: b, State: "ready", Session: "thread", Connection: "connection", AsyncQuestions: []processhost.AsyncQuestion{q}}
	var records map[string]processhost.Request
	if err := captureAsyncQuestions(b, snap, &records, store, time.Hour); err != nil {
		t.Fatal(err)
	}
	id := codexAsyncQuestionID(b, q.Request)
	if _, err := sidecar.Close(id, agentquestion.CloseReason("process-ended")); err != nil {
		t.Fatal(err)
	}
	if err := captureAsyncQuestions(b, snap, &records, store, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !snap.AsyncQuestions[0].Closed || snap.AsyncQuestions[0].Deadline.IsZero() {
		t.Fatal("caller clone lost observed terminal proof")
	}
	clock = now.Add(26 * time.Hour)
	if _, err := sidecar.Create(agentquestion.Record{ID: "question-ffffffffffffffff", Provider: "codex", Delivery: "async", AgentUID: "other", Questions: q.Request.Input, CreatedAt: clock, Deadline: clock.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// Pruning happens after capture, immediately before the view's Get.
	if _, err := exactQuestionViews(aiModeCodex, records, nil, store); !errors.Is(err, agentquestion.ErrNotFound) {
		t.Fatal("proof-free missing was ignored", err)
	}
	wrong := snap
	wrong.Binding.Generation = "wrong"
	if _, err := exactQuestionViews(aiModeCodex, records, nil, store, wrong); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("wrong proof accepted", err)
	}
	before, _ := os.ReadFile(sidecar.Path())
	views, err := exactQuestionViews(aiModeCodex, records, nil, store, snap)
	if err != nil || len(views) != 0 || len(records) != 0 {
		t.Fatal("terminal view race not retired", views, err)
	}
	after, _ := os.ReadFile(sidecar.Path())
	if string(before) != string(after) {
		t.Fatal("view recreated pruned record")
	}
	// An active known loss remains an error in both capture and view.
	records[id] = q.Request
	snap.AsyncQuestions[0].Closed = false
	snap.AsyncQuestions[0].Deadline = now.Add(time.Hour)
	if err = captureAsyncQuestions(b, snap, &records, store, time.Hour); !errors.Is(err, agentquestion.ErrNotFound) {
		t.Fatal("active missing ignored", err)
	}
	if _, err = exactQuestionViews(aiModeCodex, records, nil, store, snap); !errors.Is(err, agentquestion.ErrNotFound) {
		t.Fatal("active view missing ignored", err)
	}
	snap.AsyncQuestions[0].Closed = true
	if err = os.WriteFile(sidecar.Path(), []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = captureAsyncQuestions(b, snap, &records, store, time.Hour); !errors.Is(err, agentquestion.ErrMalformedStore) {
		t.Fatal("terminal proof hid malformed store", err)
	}
	if _, err = exactQuestionViews(aiModeCodex, records, nil, store, snap); !errors.Is(err, agentquestion.ErrMalformedStore) {
		t.Fatal("terminal view hid malformed store", err)
	}
}
