package agentquestion

import (
	"encoding/json"
	"errors"
	"fmt"
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
	if e != nil || len(bodies) != 1 || len(bodies[0]) > 2048 || !utf8.ValidString(bodies[0]) {
		t.Fatalf("compact result count=%d err=%v", len(bodies), e)
	}
	var result map[string]json.RawMessage
	if e = json.Unmarshal([]byte(bodies[0]), &result); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"questions", "answers", "part", "total", "data"} {
		if _, exists := result[field]; exists {
			t.Fatalf("oversized result includes %s", field)
		}
	}
	if string(result["questionId"]) != `"`+r.ID+`"` || string(result["status"]) != `"answered"` || string(result["answersFrom"]) != `"agent question list"` {
		t.Fatalf("missing lookup marker or identity: %s", bodies[0])
	}
	listed, e := st.List(r.AgentUID)
	if e != nil || len(listed) != 1 || listed[0].Answers[qs[0].Question] != strings.Repeat("답변", 500) {
		t.Fatalf("list lost original answer: %v", e)
	}
	listedQuestions, e := ParseQuestions(listed[0].Questions)
	if e != nil || len(listedQuestions) != 1 || listedQuestions[0].Question != qs[0].Question || listedQuestions[0].Options[0].Label != "yes" {
		t.Fatal("list lost original question", e)
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

// CLI questions have no native masked-answer surface. The shared parser also
// carries Codex metadata, which must not silently enable secret persistence.
func TestCLIAsyncSecretMetadataFailsClosed(t *testing.T) {
	secret := json.RawMessage(`[{"question":"Token?","options":[{"label":"other"}],"isSecret":true}]`)
	t.Run("existing-result", func(t *testing.T) {
		r := CLIAsyncRecord{Record: Record{ID: "question-0000000000000001", Questions: secret, Answers: map[string]string{"Token?": "private-fixture-value"}}}
		if _, err := r.Messages(); !errors.Is(err, ErrInvalidQuestions) {
			t.Fatalf("secret peer payload admitted: %v", err)
		}
	})
	t.Run("admission", func(t *testing.T) {
		st := NewStore(t.TempDir()).CLIAsyncStore()
		if _, err := st.Create("agent-a", secret, time.Hour); !errors.Is(err, ErrInvalidQuestions) {
			t.Fatalf("secret CLI question admitted: %v", err)
		}
		if _, err := os.Stat(st.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused secret admission wrote a store: %v", err)
		}
	})
	t.Run("existing-waiting", func(t *testing.T) {
		st := NewStore(t.TempDir()).CLIAsyncStore()
		r, err := st.Create("agent-a", json.RawMessage(`[{"question":"Token?","options":[{"label":"other"}]}]`), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err = st.update(func(d *cliAsyncDisk, _ time.Time) error { d.Records[0].Questions = secret; return nil }); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(st.Path())
		if _, err = st.Resolve(r.ID, r.AgentUID, map[string]string{"Token?": "private-fixture-value"}, ""); !errors.Is(err, ErrInvalidQuestions) {
			t.Fatalf("secret answer persisted: %v", err)
		}
		after, _ := os.ReadFile(st.Path())
		if string(before) != string(after) {
			t.Fatal("refused secret answer changed durable record")
		}
		if _, err = st.Resolve(r.ID, r.AgentUID, nil, "declined"); !errors.Is(err, ErrCLISecretNativeOnly) {
			t.Fatal("secret decline admitted", err)
		}
		if got, _, _ := st.Get(r.ID); got.Pending() {
			t.Fatal("unsupported secret record owns delivery work")
		}
	})
}

func TestCLIAsyncCompactResultQuestionOrderAndBudget(t *testing.T) {
	for _, reason := range []string{"", "declined", "expired"} {
		t.Run(reason, func(t *testing.T) {
			q1 := strings.Repeat("한글<&", 600)
			raw, _ := json.Marshal([]Question{{Question: q1, Options: []Option{{Label: "yes"}}}, {Question: "Second?", Options: []Option{{Label: "no"}}}})
			st := NewStore(t.TempDir()).CLIAsyncStore()
			r, err := st.Create("agent-a", raw, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			answers := map[string]string{q1: "yes", "Second?": "no"}
			if reason != "" {
				answers = nil
			}
			r, err = st.Resolve(r.ID, r.AgentUID, answers, reason)
			if err != nil {
				t.Fatal(err)
			}
			bodies, err := r.Messages()
			if err != nil || len(bodies) != 1 || len(r.Parts) != 1 || len(bodies[0]) > 2048 {
				t.Fatalf("result %v %v", bodies, err)
			}
			var result struct {
				QuestionID string `json:"questionId"`
				Status     string
				Answers    map[string]string
				Reason     string
			}
			if err = json.Unmarshal([]byte(bodies[0]), &result); err != nil {
				t.Fatal(err)
			}
			if result.QuestionID != r.ID || result.Reason != reason {
				t.Fatalf("identity/reason: %+v", result)
			}
			if reason == "" && (result.Status != "answered" || len(result.Answers) != 2 || result.Answers["1"] != "yes" || result.Answers["2"] != "no") {
				t.Fatalf("ordered answers: %+v", result)
			}
			if reason != "" && (result.Status != "no-answer" || len(result.Answers) != 0) {
				t.Fatalf("non-answer: %+v", result)
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal([]byte(bodies[0]), &fields); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"questions", "part", "total", "data", "answersFrom"} {
				if _, exists := fields[field]; exists {
					t.Fatalf("unexpected %s", field)
				}
			}
			reloaded, found, err := st.Get(r.ID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			again, err := reloaded.Messages()
			if err != nil || len(again) != 1 || again[0] != bodies[0] {
				t.Fatal("reload changed compact body", again, err)
			}
		})
	}
}

// Frozen with the pre-compact writer, including single-body and multipart
// outboxes, retry refs, receipts and an uncertain write. Reading must not migrate
// either the stored record or the bytes compared against the message store.
func TestCLIAsyncLegacyPartsReadAndRetryUnchanged(t *testing.T) {
	raw, err := os.ReadFile("testdata/cli_async_legacy_results.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Record CLIAsyncRecord
		Bodies []string
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Record.ID, func(t *testing.T) {
			now := fixture.Record.CreatedAt
			st := NewStore(t.TempDir()).WithClock(func() time.Time { return now }).CLIAsyncStore()
			if err := st.update(func(d *cliAsyncDisk, _ time.Time) error { d.Records = []CLIAsyncRecord{fixture.Record}; return nil }); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(st.Path())
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := st.Get(fixture.Record.ID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			bodies, err := got.Messages()
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := json.Marshal(got)
			want, _ := json.Marshal(fixture.Record)
			if string(actual) != string(want) {
				t.Fatal("reading changed legacy delivery evidence")
			}
			if len(bodies) != len(fixture.Bodies) {
				t.Fatalf("legacy part count %d want %d", len(bodies), len(fixture.Bodies))
			}
			for i, body := range bodies {
				if body != fixture.Bodies[i] {
					t.Fatalf("legacy body %d changed", i)
				}
			}
			after, err := os.ReadFile(st.Path())
			if err != nil || string(before) != string(after) {
				t.Fatal("read rewrote disk", err)
			}
			// A proved non-write can advance another part's attempt; the unknown
			// first part and all other evidence must remain exactly as recorded.
			index := len(got.Parts) - 1
			if len(got.Parts) == 1 {
				index = -1
			}
			if err = st.MutateResult(got.ID, func(r *CLIAsyncRecord) error {
				if index >= 0 {
					r.Parts[index].Attempt++
					r.Parts[index].MessageRef = r.PartRef(index, r.Parts[index].Attempt)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			retried, found, err := st.Get(got.ID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			retryBodies, err := retried.Messages()
			if err != nil {
				t.Fatal(err)
			}
			for i, body := range retryBodies {
				if body != fixture.Bodies[i] {
					t.Fatalf("retry changed body %d", i)
				}
			}
			if index >= 0 {
				got.Parts[index].Attempt++
				got.Parts[index].MessageRef = "message-" + got.ID + "-" + fmt.Sprint(index+1) + "-retry-3"
			}
			actual, _ = json.Marshal(retried)
			want, _ = json.Marshal(got)
			if string(actual) != string(want) {
				t.Fatal("retry changed other delivery evidence")
			}
		})
	}
}

func TestCLIAsyncCompactEncodedByteBoundary(t *testing.T) {
	r := CLIAsyncRecord{Record: Record{ID: "question-0000000000000001", Questions: json.RawMessage(cliQuestions), Answers: map[string]string{"Which?": "x"}}}
	bodies, err := r.Messages()
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(bodies[0]) - 1
	for _, size := range []int{2048, 2049} {
		r.Answers["Which?"] = strings.Repeat("x", size-overhead)
		bodies, err = r.Messages()
		if err != nil || len(bodies) != 1 {
			t.Fatal(len(bodies), err)
		}
		if size == 2048 {
			if len(bodies[0]) != 2048 || !strings.Contains(bodies[0], `"answers":{"1":"`+r.Answers["Which?"]+`"}`) {
				t.Fatal("exact budget answer was omitted")
			}
		} else if len(bodies[0]) > 2048 || strings.Contains(bodies[0], `"answers":`) || !strings.Contains(bodies[0], `"answersFrom":"agent question list"`) {
			t.Fatal("over-budget answer was not replaced entirely")
		}
	}
	// JSON escaping, rather than source string length, determines the budget.
	r.Answers["Which?"] = strings.Repeat("<&", 200)
	bodies, err = r.Messages()
	if err != nil || len(bodies) != 1 || strings.Contains(bodies[0], `"answers":`) || !strings.Contains(bodies[0], `"answersFrom":"agent question list"`) {
		t.Fatal("escaped answer escaped byte budget", err)
	}
}
