package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMutationLockRecorderAggregationAndRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	policy := testMutationPolicy(t, "fixture.alpha", "fixture.beta", "fixture.idle")
	store := NewStoreWithMutationLockPolicy(filepath.Join(t.TempDir(), "operations.jsonl"), policy)
	r := NewMutationLockRecorder(store, policy, "run-test", "0.16.1", func() time.Time { return now })
	r.Record("fixture.alpha", 250*time.Millisecond, time.Second) // threshold is exclusive
	r.Record("fixture.alpha", 300*time.Millisecond, 2*time.Second)
	r.Record("fixture.alpha", 400*time.Millisecond, 3*time.Second) // rate limited
	r.Record("fixture.beta", 500*time.Millisecond, time.Second)    // independent site
	now = now.Add(time.Minute - time.Nanosecond)
	r.Record("fixture.alpha", time.Second, time.Second)
	now = now.Add(time.Nanosecond)
	r.Record("fixture.alpha", 600*time.Millisecond, time.Second)
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("slow events = %d, want 3", len(events))
	}
	now = now.Add(9*time.Minute - time.Nanosecond)
	r.Record("fixture.alpha", 0, 0)
	events, _ = store.Read()
	if len(events) != 3 {
		t.Fatal("summary before interval")
	}
	now = now.Add(time.Nanosecond)
	r.Record("fixture.alpha", 0, 0)
	events, err = store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d, want 5", len(events))
	}
	if events[3].Operation != "fixture.alpha" || events[4].Operation != "fixture.beta" {
		t.Fatalf("summary sites = %+v", events[3:])
	}
	if events[3].Message != "count=7 wait_total_ms=2550 wait_max_ms=1000 held_total_ms=8000 held_max_ms=3000" {
		t.Fatal(events[3].Message)
	}
	now = now.Add(10 * time.Minute)
	r.Record("fixture.alpha", 0, 0)
	events, _ = store.Read()
	if len(events) != 6 || events[5].Message != "count=1 wait_total_ms=0 wait_max_ms=0 held_total_ms=0 held_max_ms=0" {
		t.Fatalf("reset summary = %+v", events)
	}
}

type mutationLockFailWriter struct{ calls int }

func (w *mutationLockFailWriter) Append(Event) error { w.calls++; return errors.New("unavailable") }
func TestMutationLockRecorderFailureAndClosedContent(t *testing.T) {
	now := time.Now()
	policy := testMutationPolicy(t, "fixture.alpha")
	w := &mutationLockFailWriter{}
	r := NewMutationLockRecorder(w, policy, "run", "0.16.1", func() time.Time { return now })
	r.Record("/private/path", time.Second, time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	if w.calls != 1 {
		t.Fatalf("failed append retries = %d", w.calls)
	}
	e := Event{Level: "info", Component: "mutation", Event: mutationLockSummaryEvent, Result: "success", Operation: "fixture.alpha", Code: "mutation.lock.summary", Message: "count=1 wait_total_ms=0 wait_max_ms=0 held_total_ms=0 held_max_ms=0"}
	if err := validateMutationLockEvent(e, policy); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Event){func(e *Event) { e.Operation = "secret" }, func(e *Event) { e.Message += " /secret" }, func(e *Event) { e.AgentUID = "secret" }, func(e *Event) { e.WaitMS = new(int64) }, func(e *Event) { e.Command = "agent" }} {
		bad := e
		change(&bad)
		if validateEventShape(bad, policy) == nil {
			t.Fatalf("accepted content: %+v", bad)
		}
	}
	other := e
	other.Component = "cli"
	other.Event = "command.outcome"
	if validateEventShape(other, policy) == nil {
		t.Fatal("accepted mutation fields on unrelated event")
	}
	for _, site := range policy.sites {
		if strings.Contains(site, "/") {
			t.Fatal(site)
		}
	}
}

func TestMutationLockRateLimitSurvivesSummary(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	policy := testMutationPolicy(t, "fixture.alpha", "fixture.beta", "fixture.idle")
	store := NewStoreWithMutationLockPolicy(filepath.Join(t.TempDir(), "operations.jsonl"), policy)
	r := NewMutationLockRecorder(store, policy, "run-test", "0.16.1", func() time.Time { return now })
	now = now.Add(10*time.Minute - time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	now = now.Add(time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	now = now.Add(time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Event != mutationLockWaitEvent || events[1].Event != mutationLockSummaryEvent {
		t.Fatalf("summary reset rate limit: %+v", events)
	}
}

func testMutationPolicy(t *testing.T, sites ...string) *MutationLockPolicy {
	t.Helper()
	p, err := NewMutationLockPolicy(sites)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMutationLockPolicyCopiesAndValidatesInventory(t *testing.T) {
	sites := []string{"fixture.alpha"}
	p, err := NewMutationLockPolicy(sites)
	if err != nil {
		t.Fatal(err)
	}
	sites[0] = "fixture.changed"
	if !p.permits("fixture.alpha") || p.permits(sites[0]) {
		t.Fatal("caller changed policy")
	}
	for _, sites := range [][]string{{""}, {"fixture.alpha", "fixture.alpha"}, {"/private/path"}, {"secret value"}, {"identity:123"}, {"UPPER"}, {"a..b"}, {"a."}, {strings.Repeat("a", 97)}} {
		if policy, err := NewMutationLockPolicy(sites); err == nil || policy != nil {
			t.Fatalf("accepted inventory %q", sites)
		}
	}
	for _, p := range []*MutationLockPolicy{nil, testMutationPolicy(t)} {
		w := &mutationLockFailWriter{}
		NewMutationLockRecorder(w, p, "run", "test", nil).Record("fixture.alpha", time.Second, 0)
		if w.calls != 0 {
			t.Fatal("empty policy recorded")
		}
	}
}

func TestMutationLockRecorderConcurrentClampAndNil(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	p := testMutationPolicy(t, "fixture.alpha", "fixture.idle")
	w := &mutationLockCaptureWriter{}
	r := NewMutationLockRecorder(w, p, "run", "test", func() time.Time { return now })
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { r.Record("fixture.alpha", -time.Second, -time.Second) })
	}
	wg.Wait()
	now = now.Add(10 * time.Minute)
	r.Record("fixture.alpha", -1, -1)
	if len(w.events) != 1 || w.events[0].Message != "count=101 wait_total_ms=0 wait_max_ms=0 held_total_ms=0 held_max_ms=0" {
		t.Fatalf("concurrent summary = %+v", w.events)
	}
	var absent *MutationLockRecorder
	absent.Record("fixture.alpha", time.Second, time.Second)
	NewMutationLockRecorder(nil, p, "run", "test", nil).Record("fixture.alpha", time.Second, 0)
}

type mutationLockCaptureWriter struct{ events []Event }

func (w *mutationLockCaptureWriter) Append(e Event) error { w.events = append(w.events, e); return nil }

func TestMutationLockStorePolicyRoundTripAndReaderRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	p := testMutationPolicy(t, "fixture.alpha")
	store := NewStoreWithMutationLockPolicy(path, p)
	w := &mutationLockCaptureWriter{}
	NewMutationLockRecorder(w, p, "run", "test", nil).Record("fixture.alpha", time.Second, time.Second)
	event := w.events[0]
	if err := NewStore(path).Append(event); err == nil {
		t.Fatal("default writer accepted family")
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejection created directory")
	}
	if err := store.Append(event); err != nil {
		t.Fatal(err)
	}
	for _, reader := range []*Store{store, NewStoreWithMutationLockPolicy(path, testMutationPolicy(t, "fixture.alpha"))} {
		got, err := reader.Read()
		if err != nil || !reflect.DeepEqual(got, []Event{event}) {
			t.Fatalf("Read: %+v %v", got, err)
		}
		gotRO, err := reader.ReadOnly()
		if err != nil || !reflect.DeepEqual(gotRO.Events, []Event{event}) || gotRO.Malformed != 0 {
			t.Fatalf("ReadOnly: %+v %v", gotRO, err)
		}
	}
	for _, reader := range []*Store{NewStore(path), NewStoreWithMutationLockPolicy(path, testMutationPolicy(t, "fixture.beta"))} {
		if err := reader.Append(event); err == nil {
			t.Fatal("wrong policy writer accepted")
		}
		got, err := reader.Read()
		if err != nil || len(got) != 0 {
			t.Fatalf("wrong policy Read: %+v %v", got, err)
		}
		gotRO, err := reader.ReadOnly()
		if err != nil || len(gotRO.Events) != 0 || gotRO.Malformed != 1 {
			t.Fatalf("wrong policy ReadOnly: %+v %v", gotRO, err)
		}
	}
}

func TestMutationLockClosedShapeRejectsEveryUnrelatedField(t *testing.T) {
	p := testMutationPolicy(t, "fixture.alpha")
	w := &mutationLockCaptureWriter{}
	NewMutationLockRecorder(w, p, "run", "test", nil).Record("fixture.alpha", time.Second, 0)
	e := w.events[0]
	allowed := stringSet("At", "Level", "Component", "Event", "Result", "RunID", "Version", "MuxBackend", "Operation", "Code", "Message")
	typ := reflect.TypeFor[Event]()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if _, ok := allowed[name]; ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			bad := e
			f := reflect.ValueOf(&bad).Elem().Field(i)
			switch f.Kind() {
			case reflect.String:
				f.SetString("private-identity")
			case reflect.Int, reflect.Int64:
				f.SetInt(1)
			case reflect.Pointer:
				f.Set(reflect.New(f.Type().Elem()))
			default:
				t.Fatalf("uncovered type %s", f.Type())
			}
			if _, err := sanitizeEvent(bad, "", p); err == nil {
				t.Fatal("accepted unrelated field")
			}
		})
	}
	for _, change := range []func(*Event){
		func(e *Event) { e.Operation = "fixture.unknown" },
		func(e *Event) { e.Operation = "/private/path" },
		func(e *Event) { e.Message += " secret=private" },
		func(e *Event) { e.Message = "wait_ms=" + strings.Repeat("1", maxMessageRunes) + " held_ms=0" },
		func(e *Event) { e.Code = "mutation.lock.summary" },
		func(e *Event) { e.Level = "error" },
		func(e *Event) { e.Result = "error" },
		func(e *Event) { e.Event = "command.outcome" },
		func(e *Event) { e.Component = "cli" },
	} {
		bad := e
		change(&bad)
		if _, err := sanitizeEvent(bad, "", p); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := sanitizeEvent(e, ""); err == nil {
		t.Fatal("default sanitizer accepted family")
	}
}

func TestMutationLockDecoderRejectsUnknownContent(t *testing.T) {
	p := testMutationPolicy(t, "fixture.alpha")
	w := &mutationLockCaptureWriter{}
	NewMutationLockRecorder(w, p, "run", "test", nil).Record("fixture.alpha", time.Second, 0)
	data, err := json.Marshal(w.events[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"secret", "path", "identity", "metadata"} {
		line := string(data[:len(data)-1]) + `,"` + field + `":"private"}` + "\n"
		result := decodeCompleteRecords(strings.NewReader(line), "", p)
		if len(result.Events) != 0 || result.Malformed != 1 {
			t.Fatalf("accepted %s: %+v", field, result)
		}
	}
}

func TestMutationLockAppendFailureKeepsIntervalAndRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	p := testMutationPolicy(t, "fixture.alpha")
	w := &mutationLockFailWriter{}
	r := NewMutationLockRecorder(w, p, "run", "test", func() time.Time { return now })
	now = now.Add(10*time.Minute - time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	now = now.Add(time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	now = now.Add(time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	if w.calls != 2 {
		t.Fatalf("append failure retried: %d", w.calls)
	}
	now = now.Add(time.Minute - time.Second)
	r.Record("fixture.alpha", time.Second, time.Second)
	if w.calls != 3 {
		t.Fatalf("slow interval not retained: %d", w.calls)
	}
}

func TestMutationLockMessageLimitAcrossStorePaths(t *testing.T) {
	p := testMutationPolicy(t, "fixture.alpha")
	w := &mutationLockCaptureWriter{}
	NewMutationLockRecorder(w, p, "run", "test", nil).Record("fixture.alpha", time.Second, 0)
	for _, shape := range []struct{ event, code, prefix, suffix string }{
		{mutationLockWaitEvent, "mutation.lock.wait.slow", "wait_ms=", " held_ms=0"},
		{mutationLockSummaryEvent, "mutation.lock.summary", "count=1 wait_total_ms=", " wait_max_ms=0 held_total_ms=0 held_max_ms=0"},
	} {
		t.Run(shape.event, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "logs", LogFileName)
			store := NewStoreWithMutationLockPolicy(path, p)
			boundary := w.events[0]
			boundary.Event, boundary.Code = shape.event, shape.code
			boundary.Message = shape.prefix + strings.Repeat("1", maxMessageRunes-len(shape.prefix)-len(shape.suffix)) + shape.suffix
			oversized := boundary
			oversized.Message = shape.prefix + "1" + strings.TrimPrefix(boundary.Message, shape.prefix)
			if err := store.Append(oversized); err == nil {
				t.Fatal("Append accepted oversized message")
			}
			if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected append created directory")
			}
			if err := store.Append(boundary); err != nil {
				t.Fatal(err)
			}
			got, err := store.Read()
			if err != nil || !reflect.DeepEqual(got, []Event{boundary}) {
				t.Fatalf("boundary Read: %+v %v", got, err)
			}
			gotRO, err := store.ReadOnly()
			if err != nil || !reflect.DeepEqual(gotRO.Events, []Event{boundary}) || gotRO.Malformed != 0 {
				t.Fatalf("boundary ReadOnly: %+v %v", gotRO, err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Append(oversized); err == nil {
				t.Fatal("existing Store accepted oversized message")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected append changed journal")
			}
			raw, err := json.Marshal(oversized)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(append(before, raw...), '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err = store.Read()
			if err != nil || !reflect.DeepEqual(got, []Event{boundary}) {
				t.Fatalf("oversized Read: %+v %v", got, err)
			}
			gotRO, err = store.ReadOnly()
			if err != nil || !reflect.DeepEqual(gotRO.Events, []Event{boundary}) || gotRO.Malformed != 1 {
				t.Fatalf("oversized ReadOnly: %+v %v", gotRO, err)
			}
		})
	}
}
