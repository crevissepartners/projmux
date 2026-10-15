package agentquestion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAsyncAnswerExplicitBoundsAndSecret(t *testing.T) {
	q := []Question{{ID: "q1", Question: "Text", IsOther: true}}
	for _, tt := range []struct{ name, text, code string }{
		{"exact", strings.Repeat("é", 2048), ""},
		{"over", strings.Repeat("é", 2048) + "x", "question-answer-too-large"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, err := ValidateAsyncSelections(q, map[int]Selection{0: {HasText: true, Text: tt.text}})
			if code != tt.code || (err != nil) != (tt.code != "") {
				t.Fatal(code, err)
			}
		})
	}
	if code, _ := ValidateAsyncSelections(q, map[int]Selection{}); code != "question-invalid-answer" {
		t.Fatal(code)
	}
	q = append(q, Question{ID: "secret", Question: "Secret", IsSecret: true})
	if code, _ := ValidateAsyncSelections(q, map[int]Selection{0: {HasText: true, Text: "answer"}}); code != "question-secret-native-only" {
		t.Fatal(code)
	}
	q = []Question{{ID: "q1", Question: "Q1", IsOther: true}, {ID: "q2", Question: "Q2", IsOther: true}}
	if code, _ := ValidateAsyncSelections(q, map[int]Selection{0: {HasText: true, Text: strings.Repeat("\x01", 4096)}, 1: {HasText: true, Text: "ok"}}); code != "question-answer-too-large" {
		t.Fatal("JSON escapes not counted", code)
	}
}

func TestAsyncAnswerAtomicClaimAndStrictT1State(t *testing.T) {
	now := time.Now().UTC()
	legacy := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
	store := legacy.AsyncStore()
	r, err := store.Create(asyncNamespaceRecord("question-0000000000000001", now, true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Create(asyncNamespaceRecord("question-0000000000000002", now, false))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(legacy.Path())
	var wg sync.WaitGroup
	receipts := make(chan Record, 16)
	for range 16 {
		wg.Go(func() {
			receipt, err := store.BeginAsync(r)
			if err == nil {
				receipts <- receipt
			} else if !errors.Is(err, ErrNotPending) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	close(receipts)
	if len(receipts) != 1 {
		t.Fatal("claims", len(receipts))
	}
	receipt := <-receipts
	raw, _ := os.ReadFile(store.Path())
	// T1 permits only the original closed state, a valid token disposition,
	// no unknown fields and no persisted answer body.
	state, err := decodeState(raw)
	if err != nil || len(state.Records) != 1 {
		t.Fatal(err)
	}
	got := state.Records[0]
	if got.State != StateClosed || got.Disposition != "async-unknown" || len(got.Answers) != 0 {
		t.Fatal(got)
	}
	var shape map[string]any
	if json.Unmarshal(raw, &shape) != nil {
		t.Fatal("JSON")
	}
	for _, outcome := range []string{"submitted", "unknown", "not-submitted"} {
		t.Run(outcome, func(t *testing.T) {
			isolated := NewStore(t.TempDir()).AsyncStore().WithClock(func() time.Time { return now })
			original, err := isolated.Create(r)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := isolated.BeginAsync(original)
			if err != nil {
				t.Fatal(err)
			}
			if err = isolated.FinishAsync(claim, outcome); err != nil {
				t.Fatal(err)
			}
			final, found, err := isolated.Get(r.ID)
			if err != nil || !found || final.Disposition != "async-"+outcome || final.State != StateClosed || len(final.Answers) != 0 {
				t.Fatal(final, err)
			}
			if _, err = isolated.BeginAsync(original); !errors.Is(err, ErrNotPending) {
				t.Fatal("reopened", err)
			}
		})
	}
	if err = store.FinishAsync(receipt, "submitted"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(legacy.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("legacy mutated")
	}
}

func TestAsyncClaimPruningNeverPretendsSettlement(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	store := NewStore(t.TempDir()).AsyncStore().WithClock(func() time.Time { return clock })
	r, err := store.Create(asyncNamespaceRecord("question-0000000000000001", now, true))
	if err != nil {
		t.Fatal(err)
	}
	r.Deadline = now.Add(7 * 24 * time.Hour)
	// Use a fresh store to capture the long deadline from creation.
	store = NewStore(t.TempDir()).AsyncStore().WithClock(func() time.Time { return clock })
	r, err = store.Create(r)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.BeginAsync(r)
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(25 * time.Hour)
	_, err = store.Create(asyncNamespaceRecord("question-0000000000000002", clock, true))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishAsync(claim, "submitted"); !errors.Is(err, ErrNotFound) {
		t.Fatal("lost receipt became success", err)
	}
	// Capacity pruning has the same bounded-history contract.
	clock = now
	store = NewStore(t.TempDir()).AsyncStore().WithClock(func() time.Time { return clock })
	r, err = store.Create(r)
	if err != nil {
		t.Fatal(err)
	}
	claim, err = store.BeginAsync(r)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 65; i++ {
		_, err = store.Create(asyncNamespaceRecord(fmt.Sprintf("question-%016x", i), now, true))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = store.FinishAsync(claim, "submitted"); !errors.Is(err, ErrNotFound) {
		t.Fatal("capacity receipt loss", err)
	}
}
