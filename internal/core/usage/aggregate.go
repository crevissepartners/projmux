package usage

import (
	"sort"
	"time"
)

// Aggregate computes Snapshots for a single model across the canonical
// windows. The caller supplies the persisted bucket slice (from Store.Append
// or Store.Load), the current wall clock, and the resolved Limits row for
// the model. The returned snapshots are ordered as AllWindows().
//
// Rollover policy:
//
//   - Window5h: rolling. ResetsAt is the timestamp at which the EARLIEST
//     event currently inside the window will fall out, i.e. earliest + 5h.
//     If the window has no events, ResetsAt is now + 5h. Documented per
//     spec.
//   - WindowWeekly: calendar-aligned. ResetsAt is the next Monday 00:00 in
//     the local timezone. This matches the reset cadence Anthropic and
//     OpenAI publicly document at the time of writing.
func Aggregate(model string, buckets []Bucket, limits ModelLimits, now time.Time) []Snapshot {
	out := make([]Snapshot, 0, len(AllWindows()))
	for _, w := range AllWindows() {
		out = append(out, snapshotFor(model, w, buckets, limits, now))
	}
	return out
}

// snapshotFor returns the snapshot for one (model, window) pair.
func snapshotFor(model string, w Window, buckets []Bucket, limits ModelLimits, now time.Time) Snapshot {
	cutoff := windowStart(w, now)
	tokens, earliest := sumWithin(buckets, cutoff, now)
	limit := limits.For(w)
	pct := 0.0
	if limit > 0 {
		pct = float64(tokens) / float64(limit) * 100.0
	}
	return Snapshot{
		Model:     model,
		Window:    w,
		Tokens:    tokens,
		Limit:     limit,
		Pct:       pct,
		ResetsAt:  NextResetAt(w, earliest, now),
		UpdatedAt: now,
	}
}

// windowStart returns the inclusive lower bound for events that should be
// counted in this window relative to now.
func windowStart(w Window, now time.Time) time.Time {
	switch w {
	case Window5h:
		return now.Add(-w.Duration())
	case WindowWeekly:
		// For aggregation purposes we still compute a 7-day rolling sum.
		// The Monday-rollover policy only affects ResetsAt below; consumed
		// tokens accrue across the rolling 168-hour window so callers can
		// see usage trends near the boundary instead of a cliff.
		return now.Add(-w.Duration())
	default:
		return now
	}
}

// sumWithin returns (tokens, earliest) for the buckets between [cutoff, now].
// earliest is the zero time when the window has no events.
func sumWithin(buckets []Bucket, cutoff, now time.Time) (int64, time.Time) {
	cutoff = cutoff.UTC()
	end := now.UTC()
	var tokens int64
	var earliest time.Time
	for _, b := range buckets {
		m := b.Minute.UTC()
		if m.Before(cutoff) {
			continue
		}
		if m.After(end) {
			continue
		}
		tokens += b.Tokens
		if earliest.IsZero() || m.Before(earliest) {
			earliest = m
		}
	}
	return tokens, earliest
}

// NextResetAt computes the next rollover for the window. earliest is the
// earliest in-window event minute (zero if none). Documented in Aggregate.
func NextResetAt(w Window, earliest, now time.Time) time.Time {
	switch w {
	case Window5h:
		if earliest.IsZero() {
			return now.Add(w.Duration())
		}
		return earliest.Add(w.Duration())
	case WindowWeekly:
		return nextMondayMidnight(now)
	default:
		return time.Time{}
	}
}

// nextMondayMidnight returns the next Monday 00:00 in now's local timezone.
// If now is exactly Monday 00:00, the result is one week later.
func nextMondayMidnight(now time.Time) time.Time {
	loc := now.Location()
	if loc == nil {
		loc = time.UTC
	}
	year, month, day := now.Date()
	startOfToday := time.Date(year, month, day, 0, 0, 0, 0, loc)
	weekday := int(startOfToday.Weekday()) // Sunday = 0 ... Saturday = 6.
	// Days until next Monday. weekday=Mon(1) -> 7 days (next Monday, not now).
	daysUntil := (8 - weekday) % 7
	if daysUntil == 0 {
		daysUntil = 7
	}
	return startOfToday.AddDate(0, 0, daysUntil)
}

// SortedSnapshots returns snapshots ordered by (model, window). Convenience
// for CLI rendering that wants stable output.
func SortedSnapshots(snaps []Snapshot) []Snapshot {
	out := make([]Snapshot, len(snaps))
	copy(out, snaps)
	windowOrder := map[Window]int{Window5h: 0, WindowWeekly: 1}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return windowOrder[out[i].Window] < windowOrder[out[j].Window]
	})
	return out
}
