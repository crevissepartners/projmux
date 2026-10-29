package runtime

import (
	"errors"
	"slices"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ProcessHostProtocol is control protocol v1. An observation without Protocol
// comes from an owner built before the protocol had a version.
const ProcessHostProtocol = 1

// ProcessHostEventsRead is the read an owner advertises in its observation's
// Reads when it serves ProcessEventsRequest. Reads are separate from the
// foreground Actions, so install census and foreground admission never see
// them.
const ProcessHostEventsRead = "events"

// ErrProcessHostUnsupportedRead refuses a read the exact owner does not
// advertise. The request is never sent to such an owner.
var ErrProcessHostUnsupportedRead = errors.New("process-host-unsupported-read")

// ProcessEventsRequest asks the exact owner for the events after a sequence
// cursor.
type ProcessEventsRequest struct {
	Binding processhost.Binding
	After   uint64
}

// ProcessHostEvents is one bounded event batch and the owner's observation of
// the snapshot taken under the same lock; Sequence is that snapshot's last
// event sequence.
//
// Events are ascending and all follow After. Gap is set only when the host
// itself dropped events after After; its stream-gap event stays in Events.
// When the answer frame cannot carry the whole batch, the tail is left for the
// next read: More is set and Last is the last returned sequence, which is the
// next read's After. Omitted lists, ascending, the returned events whose
// provider payload (Raw and Request) did not fit in one frame; their Exit and
// identity fields are kept.
type ProcessHostEvents struct {
	Events      []processhost.Event `json:",omitempty"`
	Observation ProcessHostObservation
	Sequence    uint64
	Gap, More   bool
	Last        uint64
	Omitted     []uint64 `json:",omitempty"`
}

// ProcessHostReads reports whether an observation proves the owner serves
// read. A protocol other than v1, a missing Reads, or an empty member proves
// nothing.
func ProcessHostReads(view ProcessHostObservation, read string) bool {
	return view.Protocol == ProcessHostProtocol && !slices.Contains(view.Reads, "") && slices.Contains(view.Reads, read)
}

// ReadProcessHostEvents reads one bounded event batch from the exact live
// owner of r.Binding. It first observes the owner and refuses with
// ErrProcessHostUnsupportedRead, sending nothing more, when the owner does not
// advertise the events read. Any binding, socket, or observation mismatch is
// processhost.ErrStale and never follows another generation. The read neither
// stops the owner nor records a Wait.
func (r ProcessObserver) ReadProcessHostEvents(after uint64) (ProcessHostEvents, error) {
	binding := r.Binding
	if r.Ctx.Err() != nil {
		return ProcessHostEvents{}, processhost.ErrStale
	}
	// Only the Registry's current activation is read; another generation's
	// socket is never contacted.
	activation, provider, ok := r.Registry.CurrentProcessActivation(MetadataProcessBinding(binding))
	if !ok {
		return ProcessHostEvents{}, processhost.ErrStale
	}
	view, err := r.ObserveHostView(binding, activation, provider)
	if err != nil {
		return ProcessHostEvents{}, err
	}
	if !ProcessHostReads(view, ProcessHostEventsRead) {
		return ProcessHostEvents{}, ErrProcessHostUnsupportedRead
	}
	socket := ProcessHostSocket(provider, r.RegistryPath, binding.Pane, binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return ProcessHostEvents{}, err
	}
	var result processEventsResult
	err = localipc.ReadOwnedJSON(r.Ctx, socket, identity, activation.HostProcess, processEventsWireRequest(provider, ProcessEventsRequest{Binding: binding, After: after}), &result)
	if errors.Is(err, localipc.ErrSocketReplaced) {
		err = processhost.ErrStale
	}
	if err != nil {
		return ProcessHostEvents{}, err
	}
	current, err := localipc.InspectOwnedSocket(socket)
	if err != nil || current != identity || !result.Accepted || result.Events == nil || !ProcessObservationMatches(r.Registry, binding, result.Events.Observation) || !validProcessHostEvents(binding, after, *result.Events) {
		return ProcessHostEvents{}, processhost.ErrStale
	}
	return *result.Events, nil
}

// validProcessHostEvents checks the batch shape the owner promises, so a
// reader never resumes from a cursor the batch did not prove.
func validProcessHostEvents(binding processhost.Binding, after uint64, batch ProcessHostEvents) bool {
	last := after
	gap := false
	for _, event := range batch.Events {
		if event.Binding != binding || event.Sequence <= last {
			return false
		}
		last = event.Sequence
		gap = gap || event.Kind == "stream-gap"
	}
	if batch.Last != last || batch.Gap != gap || batch.Sequence < last || (batch.More && len(batch.Events) == 0) {
		return false
	}
	for i, sequence := range batch.Omitted {
		if (i > 0 && sequence <= batch.Omitted[i-1]) || !slices.ContainsFunc(batch.Events, func(e processhost.Event) bool { return e.Sequence == sequence }) {
			return false
		}
	}
	return true
}

// processEventsResult is the part of the owner's answer an events read reads.
type processEventsResult struct {
	Accepted bool
	Events   *ProcessHostEvents `json:",omitempty"`
}

func processEventsWireRequest(provider string, request ProcessEventsRequest) any {
	if provider == codexProvider {
		return codexObservationRequest{Events: &request}
	}
	return claudeObservationRequest{Events: &request}
}
