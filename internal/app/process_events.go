package app

import (
	"encoding/json"
	"math"
	"slices"
	"sync/atomic"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
)

// processHostReads are the reads every provider owner serves. They are
// reported in observation apart from the foreground actions, so install census
// and foreground admission are unchanged.
var processHostReads = []string{runtimeservice.ProcessHostEventsRead}

// processHostReadsWithheld makes an owner answer as one built before Reads,
// so tests can prove a reader refuses such an owner. It is atomic because the
// owner answers on its own goroutine.
var processHostReadsWithheld atomic.Bool

// advertisedProcessHostReads are the Reads an observation reports.
func advertisedProcessHostReads() []string {
	if processHostReadsWithheld.Load() {
		return nil
	}
	return slices.Clone(processHostReads)
}

// processEventsFrameSlack covers the Events and Omitted keys and brackets the
// per-event estimate leaves out.
const processEventsFrameSlack = 64

// fitProcessHostEvents answers one events read inside one owner frame. It keeps
// the head of the batch, which follows After, and leaves the tail for the next
// read (More, Last). Only an event that cannot fit even as the first of a
// batch loses its provider payload (Raw and Request), and its sequence is
// listed in Omitted; its Exit evidence and identity are kept. Gap reports only
// the host's own stream-gap. ok is false when the answer cannot be framed at
// all.
func fitProcessHostEvents(events []processhost.Event, view processHostObservation, sequence, after uint64) (runtimeservice.ProcessHostEvents, bool) {
	batch := runtimeservice.ProcessHostEvents{Observation: runtimeservice.ProcessHostObservation(view), Sequence: sequence, Last: after}
	bound := batch
	bound.Gap, bound.More, bound.Sequence, bound.Last = true, true, math.MaxUint64, math.MaxUint64
	base, err := json.Marshal(processForegroundResult{Accepted: true, Events: &bound})
	if err != nil {
		return runtimeservice.ProcessHostEvents{}, false
	}
	size := len(base) + 1 + processEventsFrameSlack
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return runtimeservice.ProcessHostEvents{}, false
		}
		cost := len(encoded) + 1
		if size+cost > localipc.MaxFrameBytes {
			if len(batch.Events) > 0 {
				batch.More = true
				break
			}
			event.Raw, event.Request = nil, nil
			if encoded, err = json.Marshal(event); err != nil {
				return runtimeservice.ProcessHostEvents{}, false
			}
			// The omitted sequence costs at most its digits and a comma.
			cost = len(encoded) + 1 + 21
			if size+cost > localipc.MaxFrameBytes {
				return runtimeservice.ProcessHostEvents{}, false
			}
			batch.Omitted = append(batch.Omitted, event.Sequence)
		}
		size += cost
		batch.Events = append(batch.Events, event)
		batch.Last = event.Sequence
		batch.Gap = batch.Gap || event.Kind == "stream-gap"
	}
	if _, err := localipc.MarshalJSON(processForegroundResult{Accepted: true, Events: &batch}); err != nil {
		return runtimeservice.ProcessHostEvents{}, false
	}
	return batch, true
}
