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

// aiIngestDeliveryResults are the outcomes that mean a reflection write was
// attempted.
//
// The quiet lane is deliberately not among them. A quiet event reached its Pane
// and deliberately wrote nothing, so it can neither succeed nor fail at
// delivery, and it is by far the most common outcome: on a live machine it was
// 989 of 1028 attributed records. Counting it as delivered turned one failure
// out of three real write attempts into one out of twenty-nine, which reads as
// a healthy path with an incident rather than a path that fails a third of the
// time. Diluting a rate by widening its denominator is a shape this diagnosis
// has met before.
var aiIngestDeliveryResults = []string{"state", "notify"}

// aiIngestDeliverySource is one provider hook source's delivery outcome.
type aiIngestDeliverySource struct {
	Source string `json:"source"`
	// Delivered counts events that attempted a write and changed their Pane.
	Delivered int `json:"delivered"`
	// Failed counts events that attempted a write and could not.
	Failed int `json:"failed"`
	// Quiet counts events that reached their Pane and attempted no write. They
	// are reported beside the two counts above and never inside them.
	Quiet int `json:"quiet"`
	// Opaque is the subset of Failed whose reason answers nothing.
	Opaque int `json:"opaque"`
	// PathBearing is the subset whose explanatory detail carries a filesystem
	// path. It is counted and reported, and it deliberately does not move the
	// verdict: a reason can name its cause precisely and still carry a path,
	// and those are two different properties about two different halves of the
	// string. Folding them into one number is the mistake that made a
	// contractual refusal read as an attribution failure.
	PathBearing int                         `json:"path_bearing"`
	Reasons     []aiIngestAttributionReason `json:"reasons,omitempty"`
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
	quiet, pathBearing := map[string]int{}, map[string]int{}
	reasons := map[string]map[string]int{}
	for _, entry := range entries {
		source := strings.TrimSpace(entry.Source)
		if !isAIIngestHookSource(source) || strings.TrimSpace(entry.Pane) == "" {
			continue
		}
		// The path check covers every attributed record, not only the failures.
		// A path can ride a quiet event's reason as easily as a failure's, and
		// on this machine that is where one live inflow sits.
		if aiIngestDetailCarriesAPath(entry.Reason) {
			pathBearing[source]++
		}
		result := strings.TrimSpace(entry.Result)
		if !slices.Contains(aiIngestDeliveryResults, result) && result != "error" {
			quiet[source]++
			continue
		}
		health.Records++
		if result != "error" {
			delivered[source]++
			continue
		}
		failed[source]++
		reason := aiIngestReasonToken(entry.Reason)
		if aiIngestReasonIsOpaque(reason) {
			opaque[source]++
			// An opaque reason is not recorded verbatim. It is the shape that
			// carries a raw process detail onto a diagnostics surface, and the
			// count is the whole answer an operator needs.
			reason = aiIngestOpaqueDeliveryReason
		}
		if reasons[source] == nil {
			reasons[source] = map[string]int{}
		}
		reasons[source][reason]++
	}
	for _, source := range aiIngestAttributionSources {
		if delivered[source] == 0 && failed[source] == 0 && quiet[source] == 0 {
			continue
		}
		health.Sources = append(health.Sources, aiIngestDeliverySource{
			Source:      source,
			Delivered:   delivered[source],
			Failed:      failed[source],
			Quiet:       quiet[source],
			Opaque:      opaque[source],
			PathBearing: pathBearing[source],
			Reasons:     aiIngestDeliveryReasons(reasons[source]),
		})
	}
	return health
}

// aiIngestOpaqueDeliveryReason is what an opaque failure is counted as. The
// original string is never rendered: it is the one that carries a path or a raw
// process detail, which is exactly what a diagnostics surface must not repeat.
const aiIngestOpaqueDeliveryReason = "opaque delivery failure"

// aiIngestReasonToken takes the naming half of a delivery failure reason.
//
// A reflection refusal is written as a bounded token, optionally followed by
// ": " and an explanation. The token is the part that has to answer the
// question; the explanation exists to help an operator and may say anything a
// tmux message says. Judging opacity on the whole string would call a reason
// that names its cause precisely opaque the moment the explanation behind it
// mentioned a socket, which is the opposite of what this verdict measures.
func aiIngestReasonToken(reason string) string {
	token, _, found := strings.Cut(strings.TrimSpace(reason), ": ")
	if !found {
		return strings.TrimSpace(reason)
	}
	return strings.TrimSpace(token)
}

// aiIngestReasonIsOpaque reports whether a delivery failure token answers
// nothing.
//
// An empty token is the plainest case. A raw process-exit or signal string is
// the shape observed in the field. A path where a token belongs is a third:
// a bounded vocabulary token never contains one, so a path in that position
// means no token was written at all.
func aiIngestReasonIsOpaque(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return true
	}
	if strings.Contains(token, "/") {
		return true
	}
	for _, shape := range aiIngestOpaqueReasonShapes {
		if shape.MatchString(token) {
			return true
		}
	}
	return false
}

// aiIngestDetailCarriesAPath reports whether the explanation behind a bounded
// token names a filesystem path.
//
// It is a separate observation from opacity and never moves the verdict. This
// track's change boundary keeps paths out of the records, so the count belongs
// on the surface where an operator and an owner can see it; deciding whether a
// given path may be there is not a reading this diagnosis gets to make.
func aiIngestDetailCarriesAPath(reason string) bool {
	_, detail, found := strings.Cut(strings.TrimSpace(reason), ": ")
	return found && strings.Contains(detail, "/")
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
