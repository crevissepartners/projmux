package app

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Attribution and delivery are two layers, and this file owns the second.
//
// A hook that found its Pane has not yet done anything to it. The write that
// follows can fail on its own, and when it does the failure is the operator's
// only evidence that the event went nowhere. Reporting attribution alone would
// say the hook succeeded when the Pane never moved.

// aiIngestOpaqueReasonShapes are the raw failure strings that carry no answer.
//
// `exit status 1` names a process that ended and nothing else: not which write
// failed, not why, not what an operator could do about it. A signal string is
// the same fact from the kernel's side. Neither is a reason; both are the
// absence of one, wearing an error's clothes.
var aiIngestOpaqueReasonShapes = []*regexp.Regexp{
	regexp.MustCompile(`^exit status \d+$`),
	regexp.MustCompile(`^signal: `),
}

// aiIngestDeliverySource is one provider hook source's delivery outcome.
type aiIngestDeliverySource struct {
	Source string `json:"source"`
	// Delivered counts events that reached their Pane and changed it.
	Delivered int `json:"delivered"`
	// Failed counts events that reached their Pane and could not change it.
	Failed int `json:"failed"`
	// Opaque is the subset of Failed whose reason answers nothing.
	Opaque  int                         `json:"opaque"`
	Reasons []aiIngestAttributionReason `json:"reasons,omitempty"`
}

// aiIngestDeliveryHealth is the content-free projection of whether attributed
// hook events actually land on the Pane that owns them.
type aiIngestDeliveryHealth struct {
	Observed bool                     `json:"observed"`
	Records  int                      `json:"records"`
	Sources  []aiIngestDeliverySource `json:"sources,omitempty"`
}

// Failed is how many attributed events could not change their Pane.
func (h aiIngestDeliveryHealth) Failed() int {
	total := 0
	for _, source := range h.Sources {
		total += source.Failed
	}
	return total
}

// Opaque is how many of those failures answer nothing.
func (h aiIngestDeliveryHealth) Opaque() int {
	total := 0
	for _, source := range h.Sources {
		total += source.Opaque
	}
	return total
}

// projectAIIngestDeliveryHealth classifies one window of records.
//
// Only records that already carry a Pane are counted. A hook that never found
// its Pane failed at attribution, and folding it in here would report one
// defect as two.
func projectAIIngestDeliveryHealth(entries []aiIngestLogEntry) aiIngestDeliveryHealth {
	health := aiIngestDeliveryHealth{Observed: true}
	delivered, failed, opaque := map[string]int{}, map[string]int{}, map[string]int{}
	reasons := map[string]map[string]int{}
	for _, entry := range entries {
		source := strings.TrimSpace(entry.Source)
		if !isAIIngestHookSource(source) || strings.TrimSpace(entry.Pane) == "" {
			continue
		}
		health.Records++
		if strings.TrimSpace(entry.Result) != "error" {
			delivered[source]++
			continue
		}
		failed[source]++
		reason := strings.TrimSpace(entry.Reason)
		if aiIngestReasonIsOpaque(reason) {
			opaque[source]++
			// An opaque reason is not recorded verbatim. It is the shape that
			// carries a path or a raw process detail onto a diagnostics
			// surface, and the count is the whole answer an operator needs.
			reason = aiIngestOpaqueDeliveryReason
		}
		if reasons[source] == nil {
			reasons[source] = map[string]int{}
		}
		reasons[source][reason]++
	}
	for _, source := range aiIngestAttributionSources {
		if delivered[source] == 0 && failed[source] == 0 {
			continue
		}
		health.Sources = append(health.Sources, aiIngestDeliverySource{
			Source:    source,
			Delivered: delivered[source],
			Failed:    failed[source],
			Opaque:    opaque[source],
			Reasons:   aiIngestDeliveryReasons(reasons[source]),
		})
	}
	return health
}

// aiIngestOpaqueDeliveryReason is what an opaque failure is counted as. The
// original string is never rendered: it is the one that carries a path or a raw
// process detail, which is exactly what a diagnostics surface must not repeat.
const aiIngestOpaqueDeliveryReason = "opaque delivery failure"

// aiIngestReasonIsOpaque reports whether a delivery failure reason answers
// nothing.
//
// An empty reason is the plainest case. A raw process-exit or signal string is
// the shape observed in the field. A reason carrying a path is opaque for a
// second reason as well: this track's change boundary keeps paths out of the
// records entirely, so one appearing here is a leak as much as a non-answer.
func aiIngestReasonIsOpaque(reason string) bool {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return true
	}
	if strings.Contains(reason, "/") {
		return true
	}
	for _, shape := range aiIngestOpaqueReasonShapes {
		if shape.MatchString(reason) {
			return true
		}
	}
	return false
}

func isAIIngestHookSource(source string) bool {
	return slices.Contains(aiIngestAttributionSources, source)
}

func aiIngestDeliveryReasons(counts map[string]int) []aiIngestAttributionReason {
	if len(counts) == 0 {
		return nil
	}
	ordered := make([]aiIngestAttributionReason, 0, len(counts))
	for reason, count := range counts {
		ordered = append(ordered, aiIngestAttributionReason{Reason: reason, Count: count})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Reason < ordered[j].Reason })
	return ordered
}
