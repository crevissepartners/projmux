package diagnostics

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type topologyFailWriter struct{ calls int }

func (w *topologyFailWriter) Append(Event) error {
	w.calls++
	return errors.New("private token prompt conversation")
}

func TestTopologyRecorderCommittedCountsAndOnce(t *testing.T) {
	for _, n := range []int{0, 1, 9} {
		store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
		owner := NewLifecycleRecorder(store, "topology-run", "1.0.0", "tmux")
		recorder := owner.Topology()
		counts := TopologyCounts{Resumed: n, Skipped: n}
		if n > 0 {
			counts.Reasons = map[TopologyAgentReason]int{TopologyAgentSessionRefMissing: n}
		}
		recorder.Record(time.Now(), LifecycleSuccess, counts)
		recorder.Record(time.Now(), LifecycleError, TopologyCounts{})
		events, err := store.Read()
		want := 1
		if n > 0 {
			want++
		}
		if err != nil || len(events) != want || !owner.RecordedOutcome() {
			t.Fatalf("events=%+v err=%v", events, err)
		}
		if *events[0].ResumedCount != n || *events[0].SkippedCount != n {
			t.Fatalf("outcome=%+v", events[0])
		}
		if n > 0 && (events[1].RunID != events[0].RunID || *events[1].ItemCount != n) {
			t.Fatalf("reason=%+v", events[1])
		}
	}
}

func TestTopologyRecorderFailureAndInvalidCounts(t *testing.T) {
	for _, counts := range []TopologyCounts{
		{Resumed: -1}, {Skipped: -1}, {Skipped: 1},
		{Skipped: 1, Reasons: map[TopologyAgentReason]int{"private-prompt": 1}},
		{Skipped: 1, Reasons: map[TopologyAgentReason]int{TopologyAgentSessionRefMissing: -1}},
		{Reasons: map[TopologyAgentReason]int{TopologyAgentSessionRefMissing: 0}},
		{Skipped: 1, Reasons: map[TopologyAgentReason]int{TopologyAgentSessionRefMissing: 2}},
	} {
		writer := &topologyFailWriter{}
		owner := NewLifecycleRecorder(writer, "run", "1.0.0", "tmux")
		owner.Topology().Record(time.Now(), LifecycleSuccess, counts)
		if writer.calls != 0 || owner.RecordedOutcome() {
			t.Fatalf("accepted invalid counts %+v", counts)
		}
	}
	writer := &topologyFailWriter{}
	owner := NewLifecycleRecorder(writer, "run", "1.0.0", "tmux")
	recorder := owner.Topology()
	recorder.Record(time.Now(), LifecycleError, TopologyCounts{Resumed: 1})
	if writer.calls != 0 {
		t.Fatal("error accepted committed resume")
	}
	recorder.Record(time.Now(), LifecycleError, TopologyCounts{})
	recorder.Record(time.Now(), LifecycleError, TopologyCounts{})
	if writer.calls != 1 || !owner.RecordedOutcome() {
		t.Fatal("writer failure lost ownership or duplicated outcome")
	}
}

func TestTopologyEventClosedShapes(t *testing.T) {
	base := fixtureEvent("run")
	base.Command, base.Subcommand = "", ""
	base.Event, base.Component = "topology.outcome", "topology"
	base.ResumedCount, base.SkippedCount = intPointer(1), intPointer(10)
	if _, err := sanitizeEvent(base, ""); err != nil {
		t.Fatal(err)
	}
	for _, code := range topologyAgentReasons {
		event := base
		event.Event, event.Code = "topology.agent.skipped", string(code)
		event.ResumedCount, event.SkippedCount = nil, nil
		event.ItemCount = intPointer(1)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"unknown code":     func(e *Event) { e.Code = "topology.agent.private-name" },
		"outcome code":     func(e *Event) { e.Code = string(TopologyAgentSessionRefMissing) },
		"negative":         func(e *Event) { e.ResumedCount = intPointer(-1) },
		"missing count":    func(e *Event) { e.SkippedCount = nil },
		"wrong item count": func(e *Event) { e.ItemCount = intPointer(2) },
		"snapshot count":   func(e *Event) { e.WindowCount = intPointer(2) },
		"raw message":      func(e *Event) { e.Message = "prompt token UID argv name" },
		"provider":         func(e *Event) { e.Provider = "codex" },
		"source":           func(e *Event) { e.Source = "manual" },
		"operation":        func(e *Event) { e.Operation = string(OperationSessionCreate) },
		"error count":      func(e *Event) { e.Level, e.Result, e.Kind = "error", "error", "runtime" },
		"started":          func(e *Event) { e.Result = "started" },
		"reason totals": func(e *Event) {
			e.Event, e.Code, e.ItemCount = "topology.agent.skipped", string(TopologyAgentSessionRefMissing), intPointer(1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			event := base
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("accepted %+v", event)
			}
		})
	}
	for _, family := range []string{"command.outcome", "lifecycle.outcome", "session-state.outcome", "resource.sampler.outcome"} {
		event := base
		event.Event = family
		if _, err := sanitizeEvent(event, ""); err == nil {
			t.Fatalf("topology counts accepted by %s", family)
		}
	}
	data, _ := json.Marshal(base)
	for _, prohibited := range []string{"message", "name", "uid", "argv", "prompt", "auth", "metadata", "conversation"} {
		if strings.Contains(string(data), prohibited) {
			t.Fatalf("private field %s in %s", prohibited, data)
		}
	}
}
