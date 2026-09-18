package diagnostics

import "time"

// Result lines this tmux adapter owed a client and could not show.
//
// An interactive route finishes its intent without error -- it commits a Pane, a
// Window, a rename, a deletion, or it establishes that the operator asked for
// nothing -- and then puts one bounded line on the exact client that asked. When
// that line cannot be shown, because the client detached or the server refused
// the message, there is nothing left to undo and the route reports success, so
// the operator sees nothing at all. This family is the only place that loss stays
// observable: one record per surface, with no client, target, message, or error
// string in it.
//
// The predicate is deliberately "a line this adapter owed the client after an
// intent that did not fail", not "a committed mutation". The two differ in one
// place -- a blank rename response commits nothing and still owes the operator
// the line that says so -- and widening the predicate is what keeps this family
// from recording something untrue about it.

// SurfaceSite is the closed, content-free identity of one such result line. It
// names the adapter seam that owed the line, never what the line said.
type SurfaceSite string

const (
	// SurfaceSiteSplitFocus is a committed split whose new Pane could not be
	// focused, so the reason line replaced the silent success.
	SurfaceSiteSplitFocus SurfaceSite = "split.focus"
	// SurfaceSiteSplitNotice is a committed split whose start notice -- the
	// requested Pane directory was not used -- could not be shown.
	SurfaceSiteSplitNotice SurfaceSite = "split.start-notice"
	// SurfaceSiteSplitReplace is a committed replacing split whose result line
	// could not be shown.
	SurfaceSiteSplitReplace SurfaceSite = "split.replace"
	// SurfaceSitePaneMenuSplit is a committed pane-menu split.
	SurfaceSitePaneMenuSplit SurfaceSite = "pane-menu.split"
	// SurfaceSitePaneMenuKill is a committed pane-menu kill, whose summary is
	// the only durable description of the Pane that is now gone.
	SurfaceSitePaneMenuKill SurfaceSite = "pane-menu.kill"
	// SurfaceSiteWindowIntent is a Window intent -- create, rename, delete --
	// reporting through the shared Window result line, including the blank
	// rename response whose line says that nothing was requested.
	SurfaceSiteWindowIntent SurfaceSite = "window-intent"
)

// surfaceUnshownEvent is the single event name this family writes.
const surfaceUnshownEvent = "runtime.surface.unshown"

var surfaceSites = [...]SurfaceSite{
	SurfaceSiteSplitFocus, SurfaceSiteSplitNotice, SurfaceSiteSplitReplace,
	SurfaceSitePaneMenuSplit, SurfaceSitePaneMenuKill, SurfaceSiteWindowIntent,
}

// SurfaceSites returns the closed site inventory in stable order. The returned
// slice is a copy.
func SurfaceSites() []SurfaceSite {
	out := make([]SurfaceSite, 0, len(surfaceSites))
	out = append(out, surfaceSites[:]...)
	return out
}

func validSurfaceSite(site SurfaceSite) bool {
	for _, candidate := range surfaceSites {
		if site == candidate {
			return true
		}
	}
	return false
}

// RecordUnshownResult appends one record for a result line this adapter owed a
// client and could not show. It deliberately does not claim the top-level
// outcome: the command succeeded, and suppressing its own outcome record would
// hide that success.
func (r *AIRecorder) RecordUnshownResult(site SurfaceSite, started time.Time) {
	if r == nil {
		return
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	appendSurfaceUnshown(r.writer, r.runID, r.version, r.muxBackend, site, started, now)
}

// RecordUnshownResult is the lifecycle recorder's half of the same family. The
// two interactive adapter types carry different recorders, and this contract is
// one invariant, so both spell it the same way.
func (r *LifecycleRecorder) RecordUnshownResult(site SurfaceSite, started time.Time) {
	if r == nil {
		return
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	event, ok := surfaceUnshownEventFor(r.runID, r.version, r.muxBackend, site, started, now)
	if !ok {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.append(event)
}

func appendSurfaceUnshown(writer EventWriter, runID, version, muxBackend string, site SurfaceSite, started, now time.Time) {
	event, ok := surfaceUnshownEventFor(runID, version, muxBackend, site, started, now)
	if !ok || writer == nil {
		return
	}
	_ = writer.Append(event)
}

func surfaceUnshownEventFor(runID, version, muxBackend string, site SurfaceSite, started, now time.Time) (Event, bool) {
	if !validSurfaceSite(site) {
		return Event{}, false
	}
	return Event{
		At:         now.UTC().Format(time.RFC3339Nano),
		Level:      "error",
		Component:  "runtime",
		Event:      surfaceUnshownEvent,
		Result:     "error",
		Kind:       "runtime",
		DurationMS: max(now.Sub(started).Milliseconds(), 0),
		RunID:      runID,
		Version:    version,
		MuxBackend: muxBackend,
		Source:     string(site),
	}, true
}

func surfaceSiteSet() map[string]struct{} {
	out := make(map[string]struct{}, len(surfaceSites))
	for _, site := range surfaceSites {
		out[string(site)] = struct{}{}
	}
	return out
}
