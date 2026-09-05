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
}

// aiIngestAttributionReason is one closed attribution failure and its count.
type aiIngestAttributionReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// aiIngestAttributionSource is one provider hook source's attribution outcome
// over the window.
type aiIngestAttributionSource struct {
	Source       string                      `json:"source"`
	Attributed   int                         `json:"attributed"`
	Unattributed int                         `json:"unattributed"`
	Reasons      []aiIngestAttributionReason `json:"reasons,omitempty"`
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

// Unattributed is how many hook events over the window never reached a Pane.
func (h aiIngestAttributionHealth) Unattributed() int {
	total := 0
	for _, source := range h.Sources {
		total += source.Unattributed
	}
	return total
}

// Attributed is how many hook events over the window reached one.
func (h aiIngestAttributionHealth) Attributed() int {
	total := 0
	for _, source := range h.Sources {
		total += source.Attributed
	}
	return total
}

// readAIIngestAttributionHealth reads the tail of ai-ingest.log and projects
// attribution health from it.
//
// It opens the log read-only and creates nothing. An absent log is reported as
// unobserved rather than as an empty healthy result: a machine that has never
// run a hook and one whose log this reader cannot open give the same counts,
// and only the first is good news.
func readAIIngestAttributionHealth(path string) aiIngestAttributionHealth {
	entries, ok := readAIIngestLogTail(path, aiIngestAttributionWindow, aiIngestAttributionRecords)
	if !ok {
		return aiIngestAttributionHealth{}
	}
	return projectAIIngestAttributionHealth(entries)
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
	unattributed := map[string]int{}
	reasons := map[string]map[string]int{}
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
		reason := strings.TrimSpace(entry.Reason)
		if !slices.Contains(aiPaneMatchReasons, reason) {
			continue
		}
		health.Records++
		unattributed[source]++
		if reasons[source] == nil {
			reasons[source] = map[string]int{}
		}
		reasons[source][reason]++
	}
	for _, source := range aiIngestAttributionSources {
		if attributed[source] == 0 && unattributed[source] == 0 {
			continue
		}
		health.Sources = append(health.Sources, aiIngestAttributionSource{
			Source:       source,
			Attributed:   attributed[source],
			Unattributed: unattributed[source],
			Reasons:      aiIngestAttributionReasons(reasons[source]),
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
