package agentquestion

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const cliQuestions = `[{"question":"Which?","header":"Build","options":[{"label":"make (Recommended)"},{"label":"task"}],"multiSelect":true}]`

func TestCLIAsyncCompatibilityCapacityAndOwner(t *testing.T) {
	now := time.Now().UTC()
	legacy := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
	old, e := legacy.Create(Record{ID: "question-0000000000000001", AgentUID: "agent-a", Questions: json.RawMessage(cliQuestions), Deadline: now.Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(legacy.Path())
	if e != nil {
		t.Fatal(e)
	}
	native := legacy.AsyncStore()
	if _, e = native.Create(Record{ID: "question-0000000000000002", AgentUID: "agent-a", Provider: "codex", Delivery: "async", Questions: json.RawMessage(`[{"id":"q","question":"Choose?","options":[]}]`), Deadline: now.Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	nativeBefore, e := os.ReadFile(native.Path())
	if e != nil {
		t.Fatal(e)
	}
	st := legacy.CLIAsyncStore()
	for range maxRecords {
		if _, e = st.Create("agent-a", json.RawMessage(cliQuestions), time.Hour); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = st.Create("agent-b", json.RawMessage(cliQuestions), time.Hour); !errors.Is(e, ErrCapacity) {
		t.Fatalf("capacity: %v", e)
	}
	after, _ := os.ReadFile(legacy.Path())
	nativeAfter, _ := os.ReadFile(native.Path())
	if string(nativeBefore) != string(nativeAfter) {
		t.Fatal("native async bytes changed")
	}
	if _, e = decodeState(nativeBefore); e != nil {
		t.Fatal("old native reader rejected untouched bytes", e)
	}
	if string(before) != string(after) {
		t.Fatal("legacy bytes changed")
	}
	// The pre-feature strict reader still accepts the entire untouched file.
	d, e := decodeState(before)
	if e != nil || len(d.Records) != 1 || d.Records[0].ID != old.ID {
		t.Fatalf("old reader: %+v %v", d, e)
	}
	if _, e = legacy.AsyncStore().List(""); e != nil {
		t.Fatal(e)
	}
	release, owned, e := st.LockOwner()
	if e != nil || !owned {
		t.Fatalf("owner %v %v", owned, e)
	}
	if _, owned, e = legacy.CLIAsyncStore().LockOwner(); e != nil || owned {
		t.Fatalf("duplicate owner %v %v", owned, e)
	}
	release()
	release, owned, e = st.LockOwner()
	if e != nil || !owned {
		t.Fatal("owner recovery failed")
	}
	defer release()
	if filepath.Dir(st.Path()) != filepath.Join(filepath.Dir(legacy.Path()), ".cli-async") {
		t.Fatal(st.Path())
	}
}
func TestCLIAsyncAtomicAnswerDeclineExpiryAndRecovery(t *testing.T) {
	now := time.Now().UTC()
	base := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
	st := base.CLIAsyncStore()
	r, e := st.Create("agent-a", json.RawMessage(cliQuestions), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wins := make(chan CLIAsyncRecord, 2)
	for _, reason := range []string{"", "declined"} {
		wg.Go(func() {
			v, e := st.Resolve(r.ID, "agent-a", map[string]string{"Which?": "make (Recommended)"}, reason)
			if e == nil {
				wins <- v
			} else if !errors.Is(e, ErrNotPending) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("winners %d", len(wins))
	}
	r, _, e = base.CLIAsyncStore().Get(r.ID)
	if e != nil || len(r.Parts) != 1 || !r.Pending() {
		t.Fatalf("durable outbox %+v %v", r, e)
	}
	now = now.Add(48 * time.Hour)
	// Pending outboxes cannot be evicted by retention, even after their deadline.
	if _, e = st.Create("agent-b", json.RawMessage(cliQuestions), time.Hour); e != nil {
		t.Fatal(e)
	}
	if _, found, _ := st.Get(r.ID); !found {
		t.Fatal("pending outbox evicted")
	}
	exp, e := st.Create("agent-a", json.RawMessage(cliQuestions), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Minute)
	exp, e = st.Resolve(exp.ID, "agent-a", nil, "expired")
	if e != nil || exp.State != StateExpired || exp.ResultReason != "expired" {
		t.Fatalf("expiry %+v %v", exp, e)
	}
	bodies, _ := exp.Messages()
	if !strings.Contains(bodies[0], `"status":"no-answer"`) {
		t.Fatal(bodies)
	}
}
func TestCLIAsyncUTF8PartsLosslessAndBudget(t *testing.T) {
	raw := json.RawMessage(`[{"question":"` + strings.Repeat("한글<&", 600) + `","options":[{"label":"yes"}]}]`)
	st := NewStore(t.TempDir()).CLIAsyncStore()
	r, e := st.Create("agent-a", raw, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	qs, _ := ParseQuestions(raw)
	r, e = st.Resolve(r.ID, "agent-a", map[string]string{qs[0].Question: strings.Repeat("답변", 500)}, "")
	if e != nil {
		t.Fatal(e)
	}
	bodies, e := r.Messages()
	if e != nil || len(bodies) < 2 {
		t.Fatalf("parts %d %v", len(bodies), e)
	}
	var joined strings.Builder
	for i, b := range bodies {
		if len(b) > 2048 || !utf8.ValidString(b) {
			t.Fatalf("part %d bytes %d", i, len(b))
		}
		var p struct {
			Part, Total int
			Data        string
			QuestionID  string `json:"questionId"`
		}
		if e = json.Unmarshal([]byte(b), &p); e != nil || p.Part != i+1 || p.Total != len(bodies) || p.QuestionID != r.ID {
			t.Fatalf("part %+v %v", p, e)
		}
		joined.WriteString(p.Data)
	}
	var result struct {
		Questions  json.RawMessage
		Answers    map[string]string
		QuestionID string `json:"questionId"`
	}
	if e = json.Unmarshal([]byte(joined.String()), &result); e != nil {
		t.Fatal(e)
	}
	if result.QuestionID != r.ID || result.Answers[qs[0].Question] != r.Answers[qs[0].Question] {
		t.Fatal("answer or id lost")
	}
	got, _ := ParseQuestions(result.Questions)
	if got[0].Question != qs[0].Question {
		t.Fatal("question lost")
	}
}

func TestCLIAsyncOwnerIdleExitAndRestart(t *testing.T) {
	st := NewStore(t.TempDir()).CLIAsyncStore()
	release, owned, e := st.LockOwner()
	if e != nil || !owned {
		t.Fatal(owned, e)
	}
	r, e := st.Create("agent-a", json.RawMessage(cliQuestions), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if idle, e := st.ReleaseIfIdle(release); e != nil || idle {
		t.Fatal("owner abandoned waiting question", idle, e)
	}
	r, e = st.Resolve(r.ID, r.AgentUID, nil, "declined")
	if e != nil {
		t.Fatal(e)
	}
	if idle, e := st.ReleaseIfIdle(release); e != nil || idle {
		t.Fatal("owner abandoned outbox", idle, e)
	}
	if e = st.MutateResult(r.ID, func(v *CLIAsyncRecord) error {
		v.Outcome = "delivered"
		for i := range v.Parts {
			v.Parts[i].State = "delivered"
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if idle, e := st.ReleaseIfIdle(release); e != nil || !idle {
		t.Fatal("settled owner did not exit", idle, e)
	}
	release, owned, e = st.LockOwner()
	if e != nil || !owned {
		t.Fatal("restart failed", owned, e)
	}
	release()
}

func TestCLIAsyncDeletedTargetClosesWaitingCard(t *testing.T) {
	st := NewStore(t.TempDir()).CLIAsyncStore()
	r, e := st.Create("agent-deleted", json.RawMessage(cliQuestions), 24*time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if e = st.TargetDeleted(r.ID); e != nil {
		t.Fatal(e)
	}
	got, found, e := st.Get(r.ID)
	if e != nil || !found || got.State != StateClosed || got.Outcome != "target-deleted" || got.Pending() || len(got.Parts) != 0 {
		t.Fatalf("deleted target %+v %v", got, e)
	}
	if _, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Which?": "make (Recommended)"}, ""); !errors.Is(e, ErrNotPending) {
		t.Fatal("deleted target answered", e)
	}
}

func TestCLIAsyncAcceptedResultCanAlwaysStoreReceipts(t *testing.T) {
	st := NewStore(t.TempDir()).CLIAsyncStore()
	r, e := st.Create("agent-a", json.RawMessage(cliQuestions), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(st.Path())
	r, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Which?": strings.Repeat("x", 1<<20)}, "")
	if errors.Is(e, ErrCapacity) {
		after, _ := os.ReadFile(st.Path())
		if string(before) != string(after) {
			t.Fatal("capacity refusal changed waiting record")
		}
		return
	}
	if e != nil {
		t.Fatal(e)
	}
	e = st.MutateResult(r.ID, func(v *CLIAsyncRecord) error {
		for i := range v.Parts {
			// A normal peer receipt; no question/answer payload is duplicated here.
			receipt := map[string]any{"version": 2, "messageRef": v.Parts[i].MessageRef, "conversationRef": "conversation-result", "source": map[string]string{"agentUID": "agent-a", "paneUID": "pane-a", "activationGeneration": "generation-first", "provider": "claude", "incarnation": "route-first"}, "target": map[string]string{"agentUID": "agent-a", "paneUID": "pane-a", "activationGeneration": "generation-first", "provider": "claude", "incarnation": "route-first"}, "delivery": map[string]any{"state": "delivered", "reason": "provider-peer-context-accepted", "acceptedAt": v.CreatedAt, "terminalAt": v.UpdatedAt}, "deadline": v.ResultDeadline}
			raw, _ := json.Marshal(receipt)
			v.Parts[i].Receipt = raw
			v.Parts[i].State = "delivered"
		}
		v.Outcome = "delivered"
		return nil
	})
	if e != nil {
		t.Fatalf("an accepted result has no room to persist its receipts: %v", e)
	}
}

func TestCLIAsyncReservationsCoverAcceptedReceiptGrowth(t *testing.T) {
	st := NewStore(t.TempDir()).CLIAsyncStore()
	var accepted []CLIAsyncRecord
	for range maxRecords {
		r, e := st.Create("agent-a", json.RawMessage(cliQuestions), time.Hour)
		if errors.Is(e, ErrCapacity) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		r, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Which?": strings.Repeat("x", 32<<10)}, "")
		if errors.Is(e, ErrCapacity) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		accepted = append(accepted, r)
	}
	if len(accepted) == 0 {
		t.Fatal("no moderate result admitted")
	}
	for _, r := range accepted {
		if e := st.MutateResult(r.ID, func(v *CLIAsyncRecord) error {
			for i := range v.Parts {
				summary := map[string]string{"messageRef": v.Parts[i].MessageRef, "agentUID": v.AgentUID, "state": "delivered", "reason": ""}
				prefix, _ := json.Marshal(summary)
				summary["reason"] = strings.Repeat("x", MaxCLIReceiptBytes-len(prefix))
				raw, _ := json.Marshal(summary)
				if len(raw) != MaxCLIReceiptBytes {
					t.Fatal("receipt fixture size", len(raw))
				}
				v.Parts[i].Receipt = raw
				v.Parts[i].State = "delivered"
			}
			v.Outcome = "delivered"
			return nil
		}); e != nil {
			t.Fatal("accepted receipt growth exceeded reserved budget", e)
		}
	}
	raw, e := os.ReadFile(st.Path())
	if e != nil || len(raw) > maxStoreBytes {
		t.Fatal("namespace byte budget", len(raw), e)
	}
	for _, r := range accepted {
		got, found, e := st.Get(r.ID)
		if e != nil || !found || got.Outcome != "delivered" {
			t.Fatal("pending outbox lost", found, e)
		}
	}
}
