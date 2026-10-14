package agentquestion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAsyncStoreRejectsAnswerAndDirectWithoutWrites(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore(t.TempDir()).AsyncStore().WithClock(func() time.Time { return now })
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

// Frozen baseline 9f91c35a Record tags/types: Delivery did not exist. This
// reader preserves the old strict JSON/version/trailing-data admission without
// depending on an installed binary or the current Record's fields.
type baselineQuestionRecord struct {
	ID          string            `json:"id"`
	Provider    string            `json:"provider,omitempty"`
	AgentUID    string            `json:"agentUID"`
	PaneUID     string            `json:"paneUID,omitempty"`
	SessionID   string            `json:"sessionID,omitempty"`
	Generation  string            `json:"generation,omitempty"`
	RuntimeID   string            `json:"runtimeID,omitempty"`
	RequestID   string            `json:"requestID,omitempty"`
	ToolUseID   string            `json:"toolUseID,omitempty"`
	Questions   json.RawMessage   `json:"questions"`
	CreatedAt   time.Time         `json:"createdAt"`
	Deadline    time.Time         `json:"deadline"`
	State       State             `json:"state"`
	Disposition string            `json:"disposition,omitempty"`
	Answers     map[string]string `json:"answers,omitempty"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

func readBaselineQuestionFile(data []byte) error {
	if len(data) == 0 || len(data) > 2<<20 {
		return ErrMalformedStore
	}
	var state struct {
		Version int                      `json:"version"`
		Records []baselineQuestionRecord `json:"records"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.Version != 1 || len(state.Records) > 64 {
		return ErrMalformedStore
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrMalformedStore
	}
	seen := map[string]bool{}
	for _, r := range state.Records {
		// The wire has already passed the frozen old field admission. Convert only
		// for the unchanged state/question validity checks shared by both versions.
		raw, _ := json.Marshal(r)
		var record Record
		if json.Unmarshal(raw, &record) != nil || !validRecord(record) || seen[r.ID] {
			return ErrMalformedStore
		}
		seen[r.ID] = true
	}
	return nil
}
func asyncNamespaceRecord(id string, now time.Time, async bool) Record {
	r := Record{ID: id, Provider: "codex", AgentUID: "agent", Questions: json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[]}]`), CreatedAt: now, Deadline: now.Add(time.Hour)}
	if async {
		r.Delivery = "async"
	}
	return r
}
func TestAsyncNamespaceLegacyReaderAndTypeIsolation(t *testing.T) {
	now := time.Now().UTC()
	legacy := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
	async := legacy.AsyncStore()
	if async.Path() != filepath.Join(filepath.Dir(legacy.Path()), ".async", "questions.json") || async.AsyncStore().Path() != async.Path() || legacy.asyncNamespace {
		t.Fatal("namespace path or mutation")
	}
	if (*Store)(nil).AsyncStore() != nil || NewStoreAt("").AsyncStore().Path() != "" {
		t.Fatal("invalid path gained authority")
	}
	r, err := legacy.Create(asyncNamespaceRecord("question-0000000000000001", now, false))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacy.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err = readBaselineQuestionFile(before); err != nil {
		t.Fatal("old reader cannot read legacy", err)
	}
	ar, err := async.Create(asyncNamespaceRecord("question-0000000000000002", now, true))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(legacy.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("async changed legacy")
	}
	data, _ := os.ReadFile(async.Path())
	if !errors.Is(readBaselineQuestionFile(data), ErrMalformedStore) {
		t.Fatal("old reader accepted delivery field")
	}
	if _, err = legacy.Answer(r.ID, r.AgentUID, map[string]string{"q1": `["text"]`}); err != nil {
		t.Fatal("legacy answer changed", err)
	}
	for _, pair := range []struct {
		store  *Store
		record Record
	}{{legacy, ar}, {async, r}} {
		before, _ := os.ReadFile(pair.store.Path())
		if _, err = pair.store.Create(pair.record); !errors.Is(err, ErrInvalidRecord) {
			t.Fatal("cross namespace create", err)
		}
		after, _ := os.ReadFile(pair.store.Path())
		if !bytes.Equal(before, after) {
			t.Fatal("rejected create wrote")
		}
		contaminated, _ := json.Marshal(diskState{Version: 1, Records: []Record{pair.record}})
		if err = os.WriteFile(pair.store.Path(), contaminated, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = pair.store.List("agent"); !errors.Is(err, ErrMalformedStore) {
			t.Fatal("cross namespace read", err)
		}
	}
}
func TestAsyncNamespaceLockTempAndCallbackIsolation(t *testing.T) {
	now := time.Now().UTC()
	legacy := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
	callbacks := 0
	legacy.lockWaitFor = func(d time.Duration) time.Duration { callbacks++; return d }
	async := legacy.AsyncStore()
	for i, pair := range []struct {
		held, writer *Store
		delivery     bool
	}{{legacy, async, true}, {async, legacy, false}} {
		err := pair.held.withLock(lockWait, func() error {
			temp := filepath.Join(filepath.Dir(pair.held.Path()), tempPrefix+"live")
			if err := os.WriteFile(temp, []byte("live"), 0600); err != nil {
				return err
			}
			defer os.Remove(temp)
			_, err := pair.writer.Create(asyncNamespaceRecord(fmt.Sprintf("question-%016x", i+1), now, pair.delivery))
			if err != nil {
				return err
			}
			data, err := os.ReadFile(temp)
			if err != nil || string(data) != "live" {
				t.Fatal("other namespace removed active temp", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal("lock namespaces interfered", err)
		}
	}
	if callbacks != 4 {
		t.Fatal("lock callbacks not copied", callbacks)
	}
	for _, s := range []*Store{legacy, async} {
		records, err := s.List("agent")
		if err != nil || len(records) != 1 || !records[0].CreatedAt.Equal(now) {
			t.Fatal("clock lost", err)
		}
		temps, err := filepath.Glob(filepath.Join(filepath.Dir(s.Path()), tempPrefix+"*"))
		if err != nil || len(temps) != 0 {
			t.Fatal("orphan temp", temps, err)
		}
		// flock files are canonical coordination files, not stale owner claims.
		if err = s.withLock(lockWait, func() error { return nil }); err != nil {
			t.Fatal("lock not released", err)
		}
	}
}
func TestAsyncNamespaceBudgetsAndTerminalRetention(t *testing.T) {
	for _, delivery := range []bool{false, true} {
		t.Run(fmt.Sprint(delivery), func(t *testing.T) {
			now := time.Now().UTC()
			s := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
			if delivery {
				s = s.AsyncStore()
			}
			for i := range 64 {
				if _, err := s.Create(asyncNamespaceRecord(fmt.Sprintf("question-%016x", i), now, delivery)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(s.Path())
			if _, err := s.Create(asyncNamespaceRecord("question-ffffffffffffffff", now, delivery)); !errors.Is(err, ErrCapacity) {
				t.Fatal("count budget", err)
			}
			after, _ := os.ReadFile(s.Path())
			if !bytes.Equal(before, after) {
				t.Fatal("capacity refusal wrote")
			}
			if _, err := s.Close("question-0000000000000000", CloseReason("process-ended")); err != nil {
				t.Fatal(err)
			}

			if _, err := s.Create(asyncNamespaceRecord("question-ffffffffffffffff", now, delivery)); err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.Get("question-0000000000000000"); err != nil || found {
				t.Fatal("capacity did not prune terminal", err)
			}
			// Capacity may evict the oldest terminal, while an independent store below
			// checks the 24h retention without capacity pressure.
			fresh := NewStore(t.TempDir()).WithClock(func() time.Time { return now })
			if delivery {
				fresh = fresh.AsyncStore()
			}
			r, err := fresh.Create(asyncNamespaceRecord("question-0000000000000001", now, delivery))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = fresh.Close(r.ID, CloseReason("process-ended")); err != nil {
				t.Fatal(err)
			}
			now = now.Add(23 * time.Hour)
			if _, err = fresh.Create(asyncNamespaceRecord("question-0000000000000002", now, delivery)); err != nil {
				t.Fatal(err)
			}
			if _, found, err := fresh.Get(r.ID); err != nil || !found {
				t.Fatal("terminal removed before retention", err)
			}
			now = now.Add(2 * time.Hour)
			if _, err = fresh.Create(asyncNamespaceRecord("question-0000000000000003", now, delivery)); err != nil {
				t.Fatal(err)
			}
			if _, found, err := fresh.Get(r.ID); err != nil || found {
				t.Fatal("terminal survived retention prune", err)
			}
			before, _ = os.ReadFile(fresh.Path())
			huge := asyncNamespaceRecord("question-0000000000000004", now, delivery)
			huge.AgentUID = strings.Repeat("a", 2<<20)
			if _, err = fresh.Create(huge); !errors.Is(err, ErrCapacity) {
				t.Fatal("byte budget", err)
			}
			after, _ = os.ReadFile(fresh.Path())
			if !bytes.Equal(before, after) {
				t.Fatal("byte refusal wrote")
			}
		})
	}
}
