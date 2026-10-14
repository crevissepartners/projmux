package agentquestion

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestAsyncStoreRejectsAnswerAndDirectWithoutWrites(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
	r, err := store.Create(Record{ID: "question-0000000000000001", Provider: "codex", Delivery: "async", AgentUID: "agent", Questions: json.RawMessage(`[{"id":"q1","question":"Pick","isOther":true,"options":[{"label":"cobalt"}]}]`), CreatedAt: now, Deadline: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.Path())
	if _, err = store.Answer(r.ID, r.AgentUID, map[string]string{"q1": `["cobalt"]`}); !errors.Is(err, ErrNotPending) {
		t.Fatal(err)
	}
	if _, err = store.BeginDirect(r); !errors.Is(err, ErrNotPending) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(store.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("read-only answer changed store")
	}
	for _, state := range []State{StateExpired, StateClosed} {
		terminal := r
		terminal.State = state
		want := ErrExpired
		if state == StateClosed {
			want = ErrClosed
		}
		if !errors.Is(refusalFor(terminal), want) {
			t.Fatal(state)
		}
	}
	now = now.Add(time.Minute)
	if _, err = store.Answer(r.ID, r.AgentUID, map[string]string{"q1": `["cobalt"]`}); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err = store.BeginDirect(r); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestAsyncStoreDiscriminatorAndLegacyCompatibility(t *testing.T) {
	now := time.Now().UTC()
	r := Record{ID: "question-0000000000000001", Provider: "codex", Delivery: "async", AgentUID: "agent", Questions: json.RawMessage(`[{"id":"q1","question":"Text","isOther":true,"options":[]}]`), CreatedAt: now, Deadline: now.Add(time.Minute), UpdatedAt: now, State: StateWaiting}
	if !validRecord(r) {
		t.Fatal("valid async")
	}
	for _, change := range []func(*Record){func(r *Record) { r.Delivery = "unknown" }, func(r *Record) { r.Provider = "" }, func(r *Record) { r.State = StateAnswered; r.Answers = map[string]string{"q1": "answer"} }} {
		bad := r
		change(&bad)
		if validRecord(bad) {
			t.Fatal("invalid async admitted")
		}
	}
	r.Delivery = ""
	if !validRecord(r) || refusalFor(r) != nil {
		t.Fatal("blocking changed")
	}
	legacy, _ := json.Marshal(diskState{Version: storeVersion, Records: []Record{r}})
	if _, err := decodeState(legacy); err != nil {
		t.Fatal(err)
	}
	r.Delivery = "async"
	encoded, _ := json.Marshal(diskState{Version: storeVersion, Records: []Record{r}})
	if _, err := decodeState(encoded); err != nil {
		t.Fatal(err)
	}
}
