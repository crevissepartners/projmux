package sessionhistory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The attribution.
//
// A history row written before the affiliation keys existed carries no
// projectUID, and neither does a conversation only the Registry's current ref
// names. ListProject attributes both through the current Registry, so they
// lose their Project the moment their Agent is deleted. Attribute persists
// what the current Registry proves about them now: one row per conversation
// whose Agent's whole Agent -> Window -> Project chain resolves, carrying that
// affiliation with `affiliationBasis: "registry"`.
//
// It reads nothing but the history file and the Registry it is given: not
// deletion records, not Registry backups, not transcripts. An Agent missing
// from the Registry is never restored; its rows stay as they are.

// AttributeOptions are the inputs of one run.
type AttributeOptions struct {
	// StateDir is the projmux state directory holding the history file.
	StateDir string
	// Registry is the current Registry, read only.
	Registry *coremetadata.Registry
	// DryRun computes the same report and writes nothing.
	DryRun bool
}

// AttributeReport is the outcome of one run. The JSON keys are a stable
// contract: `agent sessions attribute -o json` prints them.
//
// Every scanned conversation lands in exactly one of attributed,
// alreadyAttributed, and unresolved.
type AttributeReport struct {
	DryRun      bool   `json:"dryRun"`
	HistoryPath string `json:"historyPath"`
	// Scanned counts the conversations, identified by (agentUID, provider,
	// sessionId): the file's claude and codex rows, then the Registry's
	// current refs the file does not name.
	Scanned int `json:"scanned"`
	// Attributed conversations yielded one new row (appended, or would be
	// under DryRun).
	Attributed int `json:"attributed"`
	// AlreadyAttributed conversations have a file row with a projectUID.
	AlreadyAttributed int `json:"alreadyAttributed"`
	// Unresolved conversations' Agent is not in the Registry, or its
	// Agent -> Window -> Project chain does not resolve.
	Unresolved int `json:"unresolved"`
	// CorruptLines is ReadResult.Corrupt of the file that was read.
	CorruptLines int `json:"corruptLines"`
	// Rows are the attributed rows, in scan order.
	Rows []Record `json:"rows"`
}

// Attribute appends, for every conversation of the history file of StateDir
// and every current ref of Registry that carries no projectUID yet, one row
// with the affiliation the Registry gives its Agent now. The row's other
// values are Merge's over the conversation's file rows and its current ref;
// its source is the highest-ranked source of its file rows, or `observed` for
// a conversation only the Registry names (`current` is never written).
//
// Like Backfill, it holds the history file's exclusive lock across the read,
// the check, and the one append, so concurrent runs never attribute the same
// conversation twice and a second run appends nothing. The lock is released
// before the fsync, which Attribute still waits for. Nothing to append
// creates nothing.
func Attribute(opts AttributeOptions) (AttributeReport, error) {
	report := AttributeReport{DryRun: opts.DryRun, Rows: []Record{}}
	if strings.TrimSpace(opts.StateDir) == "" {
		return report, errors.New("agent session history: no state directory")
	}
	report.HistoryPath = Path(opts.StateDir)
	current := currentRecords(opts.Registry)

	if opts.DryRun {
		read, err := Read(opts.StateDir, "")
		if err != nil {
			return report, err
		}
		classifyAttribution(&report, read, current, opts.Registry)
		return report, nil
	}
	// Without a history file, only the current refs could be attributed; when
	// none is, nothing is created.
	if _, err := os.Stat(report.HistoryPath); errors.Is(err, os.ErrNotExist) {
		classifyAttribution(&report, ReadResult{}, current, opts.Registry)
		if len(report.Rows) == 0 {
			return report, nil
		}
		report = AttributeReport{DryRun: opts.DryRun, HistoryPath: report.HistoryPath, Rows: []Record{}}
	}
	file, err := openLocked(opts.StateDir, os.O_RDWR, backfillLockWait)
	if err != nil {
		return report, err
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return report, fmt.Errorf("agent session history: rewind: %w", err)
	}
	read, err := readRecords(file, "")
	if err != nil {
		return report, err
	}
	classifyAttribution(&report, read, current, opts.Registry)
	if len(report.Rows) == 0 {
		return report, nil
	}
	var framed []byte
	for _, row := range report.Rows {
		line, err := frame(row)
		if err != nil {
			return report, err
		}
		framed = append(framed, line...)
	}
	if _, err := file.Write(framed); err != nil {
		return report, fmt.Errorf("agent session history: append: %w", err)
	}
	if err := unlockAndSync(file); err != nil {
		return report, err
	}
	return report, nil
}

// currentRecords is the current ref of every Registry Agent with a claude or
// codex provider, in Registry order, as ListProject reads them.
func currentRecords(reg *coremetadata.Registry) []Record {
	if reg == nil {
		return nil
	}
	var rows []Record
	for _, agent := range reg.Agents {
		switch coremetadata.NormalizeProvider(agent.Spec.Provider) {
		case ProviderClaude, ProviderCodex:
		default:
			continue
		}
		if row, ok := RecordFor(agent.Metadata.UID, agent.Status.SessionRef, SourceCurrent); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// classifyAttribution applies the precedence to every conversation of read
// and current: one with a file row that has a projectUID is already
// attributed, one whose Agent's chain does not resolve in reg is unresolved,
// and the rest yield one row each.
func classifyAttribution(report *AttributeReport, read ReadResult, current []Record, reg *coremetadata.Registry) {
	type key struct{ agentUID, provider, sessionID string }
	type conversation struct {
		key      key
		rows     []Record
		current  *Record
		recorded bool
	}
	var conversations []*conversation
	index := map[key]*conversation{}
	for _, record := range read.Records {
		if record.Provider != ProviderClaude && record.Provider != ProviderCodex {
			continue
		}
		k := key{record.AgentUID, record.Provider, record.SessionID}
		c, ok := index[k]
		if !ok {
			c = &conversation{key: k}
			index[k] = c
			conversations = append(conversations, c)
		}
		c.rows = append(c.rows, record)
		if record.ProjectUID != "" {
			c.recorded = true
		}
	}
	for _, record := range current {
		k := key{record.AgentUID, record.Provider, record.SessionID}
		c, ok := index[k]
		if !ok {
			c = &conversation{key: k}
			index[k] = c
			conversations = append(conversations, c)
		}
		row := record
		c.current = &row
	}

	report.CorruptLines = read.Corrupt
	resolved := map[string]Affiliation{}
	for _, c := range conversations {
		report.Scanned++
		if c.recorded {
			report.AlreadyAttributed++
			continue
		}
		affiliation, cached := resolved[c.key.agentUID]
		if !cached {
			affiliation = ResolveAffiliation(reg, c.key.agentUID)
			resolved[c.key.agentUID] = affiliation
		}
		if !affiliation.complete() {
			report.Unresolved++
			continue
		}
		row := Merge(c.rows, c.current)[0]
		row.Source = SourceObserved
		if len(c.rows) > 0 {
			row.Source = c.rows[0].Source
			for _, record := range c.rows[1:] {
				if sourceRank(record.Source) > sourceRank(row.Source) {
					row.Source = record.Source
				}
			}
		}
		if row.Source == SourceCurrent {
			row.Source = SourceObserved
		}
		row.ProjectUID, row.WindowUID, row.AgentName = affiliation.ProjectUID, affiliation.WindowUID, affiliation.AgentName
		row.AffiliationBasis = BasisRegistry
		report.Attributed++
		report.Rows = append(report.Rows, row.normalized())
	}
}
