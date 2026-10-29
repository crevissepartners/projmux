package runtime

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Only a v1 owner that lists the events read, with no empty member, proves it
// serves the read; anything else is refused before a request is sent.
func TestProcessHostReadsProveOnlyAdvertisedRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		view ProcessHostObservation
		want bool
	}{
		{"advertised", ProcessHostObservation{Protocol: ProcessHostProtocol, Reads: []string{ProcessHostEventsRead}}, true},
		{"protocol 0", ProcessHostObservation{Reads: []string{ProcessHostEventsRead}}, false},
		{"other protocol", ProcessHostObservation{Protocol: ProcessHostProtocol + 1, Reads: []string{ProcessHostEventsRead}}, false},
		{"no reads", ProcessHostObservation{Protocol: ProcessHostProtocol}, false},
		{"other read", ProcessHostObservation{Protocol: ProcessHostProtocol, Reads: []string{"other"}}, false},
		{"malformed", ProcessHostObservation{Protocol: ProcessHostProtocol, Reads: []string{ProcessHostEventsRead, ""}}, false},
		{"action only", ProcessHostObservation{Protocol: ProcessHostProtocol, Actions: []string{ProcessHostEventsRead}}, false},
	} {
		if got := ProcessHostReads(tc.view, ProcessHostEventsRead); got != tc.want {
			t.Fatalf("%s: %v", tc.name, got)
		}
	}
}

// A reader resumes only from a cursor the batch proves.
func TestValidProcessHostEventsShape(t *testing.T) {
	b := processhost.Binding{Host: "h", Project: "p", Window: "w", Agent: "a", Pane: "pane", Generation: "g", Operation: "o"}
	other := b
	other.Generation = "next"
	ev := func(seq uint64, kind string) processhost.Event {
		return processhost.Event{Binding: b, Sequence: seq, Kind: kind}
	}
	for _, tc := range []struct {
		name  string
		batch ProcessHostEvents
		want  bool
	}{
		{"empty", ProcessHostEvents{Last: 4}, true},
		{"head", ProcessHostEvents{Events: []processhost.Event{ev(5, "x"), ev(6, "x")}, Last: 6, More: true}, true},
		{"gap", ProcessHostEvents{Events: []processhost.Event{ev(9, "stream-gap"), ev(10, "x")}, Last: 10, Gap: true}, true},
		{"omitted", ProcessHostEvents{Events: []processhost.Event{ev(5, "x"), ev(6, "x")}, Last: 6, Omitted: []uint64{5, 6}}, true},
		{"empty more", ProcessHostEvents{Last: 4, More: true}, false},
		{"wrong last", ProcessHostEvents{Events: []processhost.Event{ev(5, "x")}, Last: 4}, false},
		{"not after", ProcessHostEvents{Events: []processhost.Event{ev(4, "x")}, Last: 4}, false},
		{"descending", ProcessHostEvents{Events: []processhost.Event{ev(6, "x"), ev(5, "x")}, Last: 5}, false},
		{"other generation", ProcessHostEvents{Events: []processhost.Event{{Binding: other, Sequence: 5}}, Last: 5}, false},
		{"gap without event", ProcessHostEvents{Events: []processhost.Event{ev(5, "x")}, Last: 5, Gap: true}, false},
		{"gap event hidden", ProcessHostEvents{Events: []processhost.Event{ev(5, "stream-gap")}, Last: 5}, false},
		{"omitted repeat", ProcessHostEvents{Events: []processhost.Event{ev(5, "x")}, Last: 5, Omitted: []uint64{5, 5}}, false},
		{"omitted foreign", ProcessHostEvents{Events: []processhost.Event{ev(5, "x")}, Last: 5, Omitted: []uint64{7}}, false},
	} {
		if got := validProcessHostEvents(b, 4, tc.batch); got != tc.want {
			t.Fatalf("%s: %v", tc.name, got)
		}
	}
}
