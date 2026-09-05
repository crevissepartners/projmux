package app

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
)

// aiIngestAttributionWindow bounds how much of ai-ingest.log one diagnostics
// read looks at.
//
// The log is trimmed to a size bound rather than to a record count, so a window
// expressed in bytes is the one that behaves the same before and after a trim.
// It is a tail read: attribution health is a statement about what hooks are
// doing now, and a window that reached back to the file's first line would keep
// reporting an install-day failure long after it was fixed.
const aiIngestAttributionWindow = 256 << 10

// aiIngestAttributionRecords bounds how many parsed records the window yields,
// so a log of very short lines cannot turn this into an unbounded parse.
const aiIngestAttributionRecords = 4096

// The window must stay inside what a trim keeps.
//
// `trimAIIngestLogFile` discards the front of the log once it passes
// aiIngestLogMaxSize, keeping the last aiIngestLogRetain bytes. As long as the
// window is no larger than what is retained, every record the window covers is
// a record that survived the trim, and a shrinking log costs history the window
// never claimed to hold. Lower aiIngestLogRetain below aiIngestAttributionWindow
// and that stops being true silently: the window would span a discarded prefix,
// the counts would fall, and the diagnosis would read the deletion as an
// improvement -- a check failing to check its own premise, which is the same
// shape as every defect this file exists to detect.
//
// The subtraction is unsigned, so violating the invariant is a build failure
// rather than a diagnosis that quietly starts lying. Both constant names appear
// here on purpose: whoever next edits aiIngestLogRetain finds this by grep.
const _ = uint(aiIngestLogRetain - aiIngestAttributionWindow)

// aiIngestAttributionSources are the provider hook sources whose attribution
// this projection answers for. tmux-bell is deliberately absent: it is not a
// provider hook and carries its own pane through a different route, so folding
// it in would move the number this section exists to show.
var aiIngestAttributionSources = []string{"codex-hook", "claude-hook", "antigravity-hook"}

// aiPaneMatchReasons is the closed set of answers a failed attribution gives.
// A record whose reason is outside this set is not an attribution outcome, and
// counting it as one would put payload and transport failures into the number
// that measures whether hooks find their Pane.
var aiPaneMatchReasons = []string{
	aiPaneMatchReasonNoMatch,
	aiPaneMatchReasonNoInventory,
	aiPaneMatchReasonRegistryUnavailable,
	aiPaneMatchReasonExplicitUnknown,
	aiPaneMatchReasonExplicitNoRuntime,
	aiPaneMatchReasonExplicitStale,
	aiPaneMatchReasonConversationUnknown,
	aiPaneMatchReasonConversationShared,
	aiPaneMatchReasonExplicitForeign,
	aiPaneMatchReasonExplicitForeignOnly,
}

// aiPaneMatchRefusals are the answers that name something the attribution
// contract never promised, rather than a promise it failed to keep.
//
// The contract's scope excludes a Pane that is already gone, and an event whose
// conversation no Pane owns is exactly that case: a hook still firing from a
// thread whose Agent and Pane were retired. Refusing it is the mechanism
// working, and the specific token exists so the refusal can be told apart from
// a failure.
//
// Keeping them out of the failure count is not a courtesy. A gate that reports
// contractual refusals as breakage cries wolf, and an ignored signal is exactly
// how the defects this whole track chased survived a neighbouring track's eight
// phases. The tokens that stay failures are the ones where the mechanism itself
// could not answer -- an unreadable inventory or Registry -- plus the ladder
// exhausting with readable data, which is the shape a re-broken hook identity
// would take.
var aiPaneMatchRefusals = []string{
	aiPaneMatchReasonExplicitUnknown,
	aiPaneMatchReasonExplicitNoRuntime,
	aiPaneMatchReasonExplicitStale,
	aiPaneMatchReasonConversationUnknown,
}

// The two foreign-identity answers stay failures rather than refusals.
//
// A refusal names a Pane that no longer exists. These name a Pane that exists
// and is not this hook's, after which the event went looking and still found
// nobody. The conversation behind it may well have a live Pane, so the event
// was owed one; that is a failure to attribute, not a promise the contract
// never made.

// aiIngestAttributionReason is one closed attribution failure and its count.
type aiIngestAttributionReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// aiIngestAttributionSource is one provider hook source's attribution outcome
// over the window.
type aiIngestAttributionSource struct {
	Source     string `json:"source"`
	Attributed int    `json:"attributed"`
	// Unattributed counts events the mechanism owed a Pane and did not deliver
	// one for. This is the number the contract requires to stay at zero.
	Unattributed int `json:"unattributed"`
	// Refused counts events the contract never promised to attribute, because
	// the Pane they name is gone. It is reported beside the failure count and
	// never inside it.
	Refused int                         `json:"refused"`
	Reasons []aiIngestAttributionReason `json:"reasons,omitempty"`
	// RefusalReasons breaks Refused down, so a reader can see that a large
	// number is a retired conversation rather than a hidden fault.
	RefusalReasons []aiIngestAttributionReason `json:"refusal_reasons,omitempty"`
}

// aiIngestAttributionHealth is the content-free projection of whether provider
// hook events are reaching the Pane that owns them.
//
// Every field is a count or a closed token. No pane content, payload, path, or
// provider text crosses into it.
type aiIngestAttributionHealth struct {
	// Observed reports whether a log was readable at all. False is not
	// "healthy": it is "this question was not answered", and the difference is
	// the one an operator has to see.
	Observed bool                        `json:"observed"`
	Records  int                         `json:"records"`
	Sources  []aiIngestAttributionSource `json:"sources,omitempty"`
}

// Unattributed is how many hook events over the window were owed a Pane and
// did not get one.
func (h aiIngestAttributionHealth) Unattributed() int {
	total := 0
	for _, source := range h.Sources {
		total += source.Unattributed
	}
	return total
}

// aiIngestWindowSpan reports the first and last record timestamps in a window.
//
// Every hook-layer count in this diagnosis is cumulative over the whole window,
// and the window spans whatever happened during it -- including a deployment.
// A repair therefore cannot move these numbers until the records that predate
// it age out, and a reader who does not know the span reads the delay as the
// repair having failed, then reads the eventual drop as time having healed it.
//
// Naming the span is the cheap half of the fix. Splitting the counts at a
// deployment boundary would be the expensive half, and it needs something this
// reader does not have: the hook layer is a short-lived process re-resolved on
// every firing, so the binary vintage of a long-running process says nothing
// about which image wrote a given record.
func aiIngestWindowSpan(entries []aiIngestLogEntry) (string, string) {
	first, last := "", ""
	for _, entry := range entries {
		at := strings.TrimSpace(entry.At)
		if at == "" {
			continue
		}
		if first == "" || at < first {
			first = at
		}
		if last == "" || at > last {
			last = at
		}
	}
	return first, last
}

// readAIIngestLogTail reads the last window of a JSON-lines log.
//
// The first line of the window is dropped whenever the window did not start at
// the file's beginning, because a byte-aligned tail almost always lands inside
// a record and a half-parsed line is not a record this projection may count.
func readAIIngestLogTail(path string, window int64, limit int) ([]aiIngestLogEntry, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	// #nosec G304 -- path is the caller's resolved private state file.
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	offset := info.Size() - window
	partial := offset > 0
	if partial {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil, false
		}
	}
	reader := bufio.NewReader(io.LimitReader(file, window))
	if partial {
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, true
		}
	}
	entries := make([]aiIngestLogEntry, 0, 64)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<10)
	for scanner.Scan() {
		if len(entries) >= limit {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry aiIngestLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, true
}

// projectAIIngestAttributionHealth classifies one window of records.
//
// A record with a Pane is an attribution that succeeded. A record with no Pane
// and one of the closed match failures is an attribution that did not. Every
// other record — a payload error, a quiet event, a lifecycle transition — is
// neither, and is left out rather than folded into a bucket, which is the same
// discipline the match vocabulary itself was written under.
func projectAIIngestAttributionHealth(entries []aiIngestLogEntry) aiIngestAttributionHealth {
	health := aiIngestAttributionHealth{Observed: true}
	attributed := map[string]int{}
	unattributed, refused := map[string]int{}, map[string]int{}
	reasons, refusalReasons := map[string]map[string]int{}, map[string]map[string]int{}
	tally := func(into map[string]map[string]int, source, reason string) {
		if into[source] == nil {
			into[source] = map[string]int{}
		}
		into[source][reason]++
	}
	for _, entry := range entries {
		source := strings.TrimSpace(entry.Source)
		if !slices.Contains(aiIngestAttributionSources, source) {
			continue
		}
		if strings.TrimSpace(entry.Pane) != "" {
			health.Records++
			attributed[source]++
			continue
		}
		// The conversion is explicit because this record's reason column becomes a
		// named vocabulary type once the closed-vocabulary work lands, and an
		// explicit conversion compiles against both spellings. It is not
		// redundant; removing it breaks the build a version from now.
		reason := strings.TrimSpace(string(entry.Reason))
		if !slices.Contains(aiPaneMatchReasons, reason) {
			continue
		}
		health.Records++
		if slices.Contains(aiPaneMatchRefusals, reason) {
			refused[source]++
			tally(refusalReasons, source, reason)
			continue
		}
		unattributed[source]++
		tally(reasons, source, reason)
	}
	for _, source := range aiIngestAttributionSources {
		if attributed[source] == 0 && unattributed[source] == 0 && refused[source] == 0 {
			continue
		}
		health.Sources = append(health.Sources, aiIngestAttributionSource{
			Source:         source,
			Attributed:     attributed[source],
			Unattributed:   unattributed[source],
			Refused:        refused[source],
			Reasons:        aiIngestAttributionReasons(reasons[source]),
			RefusalReasons: aiIngestAttributionReasons(refusalReasons[source]),
		})
	}
	return health
}

// aiIngestAttributionReasons orders one source's failures by token so two reads
// of the same window render identically.
func aiIngestAttributionReasons(counts map[string]int) []aiIngestAttributionReason {
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
