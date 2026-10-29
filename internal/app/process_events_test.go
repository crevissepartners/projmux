package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
)

type processEventsTestInput struct {
	RegistryPath string
	Binding      processhost.Binding
	After        uint64
}

type processEventsTestOutput struct {
	Batch       runtimeservice.ProcessHostEvents
	Unsupported bool
	Stale       bool
	Error       string
}

// A second actual process runs the shared events read client.
func TestProcessEventsClientHelper(t *testing.T) {
	if os.Getenv("PMX_TEST_PROCESS_EVENTS") != "1" {
		return
	}
	var input processEventsTestInput
	if json.NewDecoder(os.Stdin).Decode(&input) != nil {
		os.Exit(2)
	}
	reg, err := intmetadata.NewStore(input.RegistryPath).LoadDegradedReadOnly()
	if err != nil {
		os.Exit(3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
	defer cancel()
	r := runtimeservice.ProcessObserver{Ctx: ctx, Registry: reg, RegistryPath: input.RegistryPath, Binding: input.Binding}
	var output processEventsTestOutput
	output.Batch, err = r.ReadProcessHostEvents(input.After)
	if err != nil {
		output.Error = err.Error()
		output.Unsupported = errors.Is(err, runtimeservice.ErrProcessHostUnsupportedRead)
		output.Stale = errors.Is(err, processhost.ErrStale)
	}
	_ = json.NewEncoder(os.Stdout).Encode(output)
	os.Exit(0)
}

func readProcessEventsFromPeer(t *testing.T, binary, path string, binding processhost.Binding, after uint64) processEventsTestOutput {
	t.Helper()
	raw, _ := json.Marshal(processEventsTestInput{RegistryPath: path, Binding: binding, After: after})
	cmd := exec.Command(binary, "-test.run=^TestProcessEventsClientHelper$")
	cmd.Env = append(os.Environ(), "PMX_TEST_PROCESS_EVENTS=1")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	var got processEventsTestOutput
	if err != nil || json.Unmarshal(out, &got) != nil {
		t.Fatalf("actual peer events read: %s %v", out, err)
	}
	return got
}

func TestClaudeProcessHostEventsOwnerRead(t *testing.T) {
	testProcessHostEventsOwnerRead(t, aiModeClaude)
}

func TestCodexProcessHostEventsOwnerRead(t *testing.T) {
	testProcessHostEventsOwnerRead(t, aiModeCodex)
}

// The detached owner answers an exact events read with a batch and the
// observation of the same-lock snapshot; reads never stop the owner or write
// the Registry, and a stale binding is refused.
func testProcessHostEventsOwnerRead(t *testing.T, provider string) {
	var binding processhost.Binding
	var path, binary string
	var store *intmetadata.Store
	var snap processhost.Snapshot
	var events func(processhost.Binding, uint64) ([]processhost.Event, processhost.Snapshot, error)
	if provider == aiModeClaude {
		f := newProcessClaudeFixture(t, nil)
		f.turn(t, "events-pending", "question")
		snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) > 0 })
		binding, path, binary, store = f.binding, f.path, f.binary, f.store
		events = f.handle.Events
	} else {
		f := newProcessCodexFixture(t, nil)
		f.turn(t, "events-pending", "controls")
		snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
		binding, path, binary, store = f.endpoint.binding, f.path, f.binary, f.store
		events = f.endpoint.handle.Events
	}
	reg := installProcessObservationSchema(t, store, binding, snap)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, current, err := events(binding, 0)
	if err != nil || len(want) == 0 {
		t.Fatalf("fixture events: %d %v", len(want), err)
	}

	got := readProcessEventsFromPeer(t, binary, path, binding, 0)
	batch := got.Batch
	if got.Error != "" || batch.Gap || batch.More || len(batch.Omitted) != 0 || len(batch.Events) != len(want) || batch.Last != want[len(want)-1].Sequence {
		t.Fatalf("events read: %+v", got)
	}
	for i, event := range batch.Events {
		if event.Sequence != want[i].Sequence || event.Kind != want[i].Kind || event.Binding != binding || !bytes.Equal(event.Raw, want[i].Raw) {
			t.Fatalf("event %d: %+v want %+v", i, event, want[i])
		}
	}
	view := batch.Observation
	if view.Binding != binding || view.Provider != provider || view.State != "ready" || view.Protocol != processHostProtocol ||
		view.Session != current.Session || view.Connection != current.Connection || view.TurnID != current.Turn || view.Sequence != batch.Last ||
		view.Pending != len(current.Pending) || strings.Join(view.Reads, ",") != runtimeservice.ProcessHostEventsRead {
		t.Fatalf("same-lock observation: %+v snapshot %+v", view, current)
	}
	if strings.Contains(strings.Join(view.Actions, ","), runtimeservice.ProcessHostEventsRead) {
		t.Fatalf("events read entered foreground actions: %v", view.Actions)
	}

	// Continuing from Last loses and repeats nothing.
	next := readProcessEventsFromPeer(t, binary, path, binding, batch.Last)
	if next.Error != "" || len(next.Batch.Events) != 0 || next.Batch.Last != batch.Last || next.Batch.More || next.Batch.Gap {
		t.Fatalf("continued read: %+v", next)
	}

	stale := binding
	stale.Generation = "old"
	if refused := readProcessEventsFromPeer(t, binary, path, stale, 0); !refused.Stale || len(refused.Batch.Events) != 0 {
		t.Fatalf("stale binding read: %+v", refused)
	}
	cancelled := runtimeservice.ProcessObserver{Ctx: canceledContext(), Registry: reg, RegistryPath: path, Binding: binding}
	if _, err := cancelled.ReadProcessHostEvents(0); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("cancelled read: %v", err)
	}

	// An owner that advertises no events read is refused before the request.
	processHostReadsWithheld.Store(true)
	t.Cleanup(func() { processHostReadsWithheld.Store(false) })
	old := readProcessEventsFromPeer(t, binary, path, binding, 0)
	processHostReadsWithheld.Store(false)
	if !old.Unsupported || old.Stale || len(old.Batch.Events) != 0 {
		t.Fatalf("old owner read: %+v", old)
	}

	if observed, err := rereadProcessObserver(reg, path, binding); err != nil || observed.State != "ready" || observed.Exit != nil {
		t.Fatalf("events read changed the owner: %+v %v", observed, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("events read wrote Registry")
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// The host's own dropped range reaches the reader as Gap with the stream-gap
// event; the read continues after it.
func TestCodexProcessHostEventsHostGap(t *testing.T) {
	f := newProcessCodexFixtureWithEvents(t, nil, 2)
	f.turn(t, "events-gap", "controls")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	binding := f.endpoint.binding
	installProcessObservationSchema(t, f.store, binding, snap)
	direct, _, err := f.endpoint.handle.Events(binding, 0)
	if err != nil || len(direct) == 0 || direct[0].Kind != "stream-gap" {
		t.Fatalf("fixture did not exercise host ring gap: %+v %v", direct, err)
	}
	got := readProcessEventsFromPeer(t, f.binary, f.path, binding, 0)
	if got.Error != "" || !got.Batch.Gap || got.Batch.More || len(got.Batch.Events) == 0 || got.Batch.Events[0].Kind != "stream-gap" || got.Batch.Events[0].Sequence != direct[0].Sequence {
		t.Fatalf("host gap read: %+v", got)
	}
	next := readProcessEventsFromPeer(t, f.binary, f.path, binding, got.Batch.Last)
	if next.Error != "" || next.Batch.Gap || len(next.Batch.Events) != 0 {
		t.Fatalf("read after gap: %+v", next)
	}
}

func processEventsTestView() processHostObservation {
	binding := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "gen", Operation: "op"}
	return processHostObservation{Binding: binding, Provider: aiModeCodex, State: "ready", Protocol: processHostProtocol, Reads: processHostReads}
}

func processEventsFixture(binding processhost.Binding, first uint64, sizes ...int) []processhost.Event {
	var events []processhost.Event
	for i, size := range sizes {
		raw, _ := json.Marshal(strings.Repeat("x", size))
		events = append(events, processhost.Event{Binding: binding, Session: "s", Connection: "c", Turn: "t", Sequence: first + uint64(i), Kind: "provider", Raw: raw, Request: &processhost.Request{ID: "r", Input: raw}})
	}
	return events
}

// The owner keeps the head of a batch that does not fit one frame: the tail is
// read next from Last without loss, and it is not a Gap.
func TestFitProcessHostEventsKeepsHeadAndContinues(t *testing.T) {
	view := processEventsTestView()
	all := processEventsFixture(view.Binding, 11, 10<<10, 10<<10, 10<<10, 10<<10, 10<<10, 10<<10, 10<<10, 10<<10)
	var read []uint64
	after := uint64(10)
	for rounds := 0; ; rounds++ {
		if rounds > len(all) {
			t.Fatal("read did not finish")
		}
		var rest []processhost.Event
		for _, event := range all {
			if event.Sequence > after {
				rest = append(rest, event)
			}
		}
		batch, ok := fitProcessHostEvents(rest, view, after)
		if !ok || batch.Gap || len(batch.Omitted) != 0 {
			t.Fatalf("fit: %+v %v", batch, ok)
		}
		if _, err := localipc.MarshalJSON(processForegroundResult{Accepted: true, Events: &batch}); err != nil {
			t.Fatal(err)
		}
		if rounds == 0 && !batch.More {
			t.Fatal("fixture fits one frame")
		}
		for _, event := range batch.Events {
			if event.Sequence != after+1 || !bytes.Equal(event.Raw, all[event.Sequence-11].Raw) {
				t.Fatalf("head not kept: %d after %d", event.Sequence, after)
			}
			read = append(read, event.Sequence)
			after = event.Sequence
		}
		if batch.Last != after {
			t.Fatalf("Last %d after %d", batch.Last, after)
		}
		if !batch.More {
			break
		}
	}
	if len(read) != len(all) {
		t.Fatalf("read %v", read)
	}
}

// An event larger than a frame keeps its identity and Exit evidence, is named
// in Omitted, and the events after it are read next.
func TestFitProcessHostEventsOmitsOversizePayload(t *testing.T) {
	view := processEventsTestView()
	all := processEventsFixture(view.Binding, 1, localipc.MaxFrameBytes+1, 16, 16)
	all[0].Exit = &processhost.Exit{Code: 3}
	batch, ok := fitProcessHostEvents(all, view, 0)
	if !ok || batch.Gap || batch.More || len(batch.Events) != 3 || batch.Last != 3 || len(batch.Omitted) != 1 || batch.Omitted[0] != 1 {
		t.Fatalf("oversize fit: %+v %v", batch, ok)
	}
	first := batch.Events[0]
	if first.Raw != nil || first.Request != nil || first.Exit == nil || first.Exit.Code != 3 || first.Binding != view.Binding || first.Session != "s" || first.Connection != "c" || first.Turn != "t" || first.Kind != "provider" {
		t.Fatalf("oversize event: %+v", first)
	}
	if !bytes.Equal(batch.Events[1].Raw, all[1].Raw) || batch.Events[1].Request == nil {
		t.Fatal("following event lost its payload")
	}
	// A following oversize event is left for the next read, where it is the
	// head and is omitted in turn; Omitted stays ascending without repeats.
	all = processEventsFixture(view.Binding, 1, localipc.MaxFrameBytes+1, localipc.MaxFrameBytes+1, 16)
	batch, ok = fitProcessHostEvents(all, view, 0)
	if !ok || !batch.More || batch.Last != 1 || len(batch.Events) != 1 || len(batch.Omitted) != 1 || batch.Omitted[0] != 1 {
		t.Fatalf("second oversize fit: %+v %v", batch, ok)
	}
	batch, ok = fitProcessHostEvents(all[1:], view, 1)
	if !ok || batch.More || batch.Last != 3 || len(batch.Omitted) != 1 || batch.Omitted[0] != 2 || batch.Events[1].Raw == nil {
		t.Fatalf("continued oversize fit: %+v %v", batch, ok)
	}
}

// Only the host's own stream-gap sets Gap.
func TestFitProcessHostEventsHostGap(t *testing.T) {
	view := processEventsTestView()
	events := append([]processhost.Event{{Binding: view.Binding, Sequence: 5, Kind: "stream-gap"}}, processEventsFixture(view.Binding, 6, 16)...)
	batch, ok := fitProcessHostEvents(events, view, 0)
	if !ok || !batch.Gap || batch.More || batch.Last != 6 || len(batch.Events) != 2 {
		t.Fatalf("host gap fit: %+v %v", batch, ok)
	}
}
