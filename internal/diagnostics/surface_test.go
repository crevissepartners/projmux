package diagnostics

import (
	"testing"
	"time"
)

// surface_test.go closes the unshown-result family: every site is spellable by
// both recorders, the record it writes passes the journal schema, and nothing
// outside the closed inventory can be written at all.

func TestUnshownResultRecordsEverySiteFromBothRecorders(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC)
	started := now.Add(-40 * time.Millisecond)

	for _, site := range SurfaceSites() {
		for _, recorder := range []struct {
			name   string
			record func(*LifecycleRecorder, SurfaceSite)
		}{
			{"lifecycle", func(r *LifecycleRecorder, site SurfaceSite) { r.RecordUnshownResult(site, started) }},
			{"ai", func(r *LifecycleRecorder, site SurfaceSite) {
				ai := r.AI()
				ai.now = func() time.Time { return now }
				ai.RecordUnshownResult(site, started)
			}},
		} {
			t.Run(string(site)+"/"+recorder.name, func(t *testing.T) {
				writer := &recordingEventWriter{}
				lifecycle := NewLifecycleRecorder(writer, "surface-safe-run", "0.15.3", "tmux")
				lifecycle.now = func() time.Time { return now }

				recorder.record(lifecycle, site)

				events := writer.snapshot()
				if len(events) != 1 {
					t.Fatalf("events = %#v, want exactly one", events)
				}
				event := events[0]
				if event.Event != surfaceUnshownEvent || event.Component != "runtime" || event.Source != string(site) {
					t.Fatalf("event = %#v", event)
				}
				if event.Level != "error" || event.Result != "error" || event.Kind != "runtime" {
					t.Fatalf("event severity = %q/%q/%q, want error/error/runtime", event.Level, event.Result, event.Kind)
				}
				if event.DurationMS != 40 {
					t.Fatalf("duration = %dms, want 40", event.DurationMS)
				}
				// The record must never claim the command's own outcome: the
				// mutation succeeded, and suppressing that success is the one
				// thing this family may not do.
				if lifecycle.RecordedOutcome() {
					t.Fatal("RecordedOutcome() = true, want the top-level outcome left to the command")
				}
				if event.Message != "" || event.Operation != "" || event.Code != "" || event.Command != "" || event.Subcommand != "" {
					t.Fatalf("event carries prose or a command class: %#v", event)
				}
				if _, err := sanitizeEvent(event, "/private/home"); err != nil {
					t.Fatalf("sanitizeEvent() error = %v", err)
				}
			})
		}
	}
}

func TestUnshownResultRefusesASiteOutsideTheClosedInventory(t *testing.T) {
	writer := &recordingEventWriter{}
	lifecycle := NewLifecycleRecorder(writer, "surface-safe-run", "0.15.3", "tmux")

	lifecycle.RecordUnshownResult(SurfaceSite("split.whatever-the-caller-felt-like"), time.Now())
	lifecycle.AI().RecordUnshownResult(SurfaceSite(""), time.Now())

	if events := writer.snapshot(); len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func TestUnshownResultToleratesNilRecorders(t *testing.T) {
	var lifecycle *LifecycleRecorder
	var ai *AIRecorder
	lifecycle.RecordUnshownResult(SurfaceSiteSplitFocus, time.Now())
	ai.RecordUnshownResult(SurfaceSiteSplitFocus, time.Now())
	// A command literal that never wired a recorder still reports success.
	if lifecycle.RecordedOutcome() {
		t.Fatal("a nil recorder claimed an outcome")
	}
}

func TestSurfaceSitesAreDistinctAndAllowlisted(t *testing.T) {
	sites := SurfaceSites()
	if len(sites) == 0 {
		t.Fatal("the surface site inventory is empty")
	}
	seen := make(map[SurfaceSite]bool, len(sites))
	for _, site := range sites {
		if seen[site] {
			t.Fatalf("duplicate surface site %q", site)
		}
		seen[site] = true
		if !validSurfaceSite(site) {
			t.Fatalf("site %q is not accepted by validSurfaceSite", site)
		}
		if _, ok := allowedSurfaceSites[string(site)]; !ok {
			t.Fatalf("site %q is missing from the journal allowlist", site)
		}
	}
	if want := len(sites) + len(recordedOnlySurfaceSites); len(allowedSurfaceSites) != want {
		t.Fatalf("allowlist holds %d sites, want the %d live sites plus %d recorded-only ones",
			len(allowedSurfaceSites), len(sites), len(recordedOnlySurfaceSites))
	}
}

// TestRecordedOnlySurfaceSitesStayReadableButAreNeverWritten keeps a retired
// seam's records in the journal a reader can count, and keeps anything live
// from writing a new one.
func TestRecordedOnlySurfaceSitesStayReadableButAreNeverWritten(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC)
	for _, site := range recordedOnlySurfaceSites {
		t.Run(site, func(t *testing.T) {
			if validSurfaceSite(SurfaceSite(site)) {
				t.Fatalf("recorded-only site %q is still in the live inventory", site)
			}
			historical := Event{
				At: now.Format(time.RFC3339Nano), Level: "error", Component: "runtime", Event: surfaceUnshownEvent,
				Result: "error", Kind: "runtime", RunID: "surface-safe-run", Version: "0.15.3", MuxBackend: "tmux",
				Source: site,
			}
			if _, err := sanitizeEvent(historical, "/private/home"); err != nil {
				t.Fatalf("historical %q record rejected: %v", site, err)
			}
			writer := &recordingEventWriter{}
			lifecycle := NewLifecycleRecorder(writer, "surface-safe-run", "0.15.3", "tmux")
			lifecycle.RecordUnshownResult(SurfaceSite(site), now)
			lifecycle.AI().RecordUnshownResult(SurfaceSite(site), now)
			if events := writer.snapshot(); len(events) != 0 {
				t.Fatalf("a retired site was written: %#v", events)
			}
		})
	}
}
