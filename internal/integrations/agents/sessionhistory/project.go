package sessionhistory

import (
	"slices"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Basis says how a project session's Agent was attributed to the Project.
type Basis string

const (
	// BasisRecorded: a history row of the Agent carries the Project in its own
	// projectUID, the affiliation its writer resolved at write time.
	BasisRecorded Basis = "recorded"
	// BasisRegistry: the Agent's row carries no projectUID, and the Agent is
	// in the current Registry with its whole Agent -> Window -> Project chain;
	// or the row carries the affiliation `agent sessions attribute` persisted
	// from the Registry of its run (affiliationBasis `registry`), which holds
	// after the Agent is deleted.
	BasisRegistry Basis = "registry"
)

// ProjectResult is the session list of one Project. The JSON keys are a
// stable contract: `agent sessions project -o json` prints them.
type ProjectResult struct {
	ProjectUID string           `json:"projectUID"`
	Sessions   []ProjectSession `json:"sessions"`
	// Unattributed counts the sessions, over the whole history, none of whose
	// rows could be attributed to any Project.
	Unattributed int `json:"unattributed"`
	// Ambiguous counts the sessions, over the whole history, whose rows are
	// attributed to two or more Projects. They are listed under none.
	Ambiguous int `json:"ambiguous"`
	// CorruptLines is ReadResult.Corrupt of the file that was read.
	CorruptLines int `json:"corruptLines"`
}

// ProjectSession is one conversation, identified by (provider, sessionId),
// attributed to the Project.
type ProjectSession struct {
	Provider       string                `json:"provider"`
	SessionID      string                `json:"sessionId"`
	TranscriptPath string                `json:"transcriptPath"`
	ObservedAt     time.Time             `json:"observedAt"`
	LastRecordAt   *time.Time            `json:"lastRecordAt,omitempty"`
	Source         Source                `json:"source"`
	Agents         []ProjectSessionAgent `json:"agents"`
}

// ProjectSessionAgent is one Agent a project session's attributed rows name.
type ProjectSessionAgent struct {
	AgentUID  string `json:"agentUID"`
	AgentName string `json:"agentName"`
	WindowUID string `json:"windowUID"`
	// InRegistry reports whether the Agent exists in the current Registry. A
	// deleted Agent stays listed through the affiliation its rows recorded.
	InRegistry bool  `json:"inRegistry"`
	Basis      Basis `json:"basis"`
}

// ListProject is the one read of a Project's sessions.
//
// Input: every claude or codex row of the history file of stateDir, plus one
// `current` row for each Agent of reg with a supported provider. Each row is
// attributed on its own: a row with a projectUID is attributed with its own
// projectUID, windowUID, and agentName, never re-resolved, as `registry` when
// its affiliationBasis is `registry` (Attribute wrote it) and `recorded`
// otherwise; a row without one whose Agent is in reg with a complete chain is
// `registry` with reg's values; any other row is unattributed. Nothing else
// -- not deletion records, not a transcript's folder or cwd, not time
// proximity -- attributes a row.
//
// Rows are grouped into sessions by (provider, sessionId). A session with no
// attributed row counts in Unattributed; one whose attributed rows name two
// or more Projects counts in Ambiguous; one naming exactly projectUID is
// listed. A listed session's observedAt, transcriptPath, lastRecordAt, and
// source are merged over its attributed rows with Merge's rules, and it lists
// one Agent per distinct agentUID of those rows, in first-appearance order
// (history file order, then the current rows in Registry order). An Agent
// with both bases is `recorded`, with the latest recorded row's name and
// Window. Sessions are ordered by observedAt; equal instants keep their
// first-appearance order.
func ListProject(stateDir string, reg *coremetadata.Registry, projectUID string) (ProjectResult, error) {
	result := ProjectResult{ProjectUID: projectUID, Sessions: []ProjectSession{}}
	read, err := Read(stateDir, "")
	if err != nil {
		return result, err
	}
	result.CorruptLines = read.Corrupt

	rows := make([]Record, 0, len(read.Records))
	for _, record := range read.Records {
		if record.Provider == ProviderClaude || record.Provider == ProviderCodex {
			rows = append(rows, record)
		}
	}
	if reg != nil {
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
	}

	type sessionKey struct{ provider, sessionID string }
	type attributed struct {
		record      Record
		affiliation Affiliation
		basis       Basis
	}
	type session struct {
		key      sessionKey
		rows     []attributed
		projects []string
	}
	var sessions []*session
	index := map[sessionKey]*session{}
	current := map[string]Affiliation{}
	for _, row := range rows {
		k := sessionKey{row.Provider, row.SessionID}
		s, ok := index[k]
		if !ok {
			s = &session{key: k}
			index[k] = s
			sessions = append(sessions, s)
		}
		entry := attributed{record: row}
		if row.ProjectUID != "" {
			entry.basis = BasisRecorded
			if row.AffiliationBasis == BasisRegistry {
				entry.basis = BasisRegistry
			}
			entry.affiliation = Affiliation{ProjectUID: row.ProjectUID, WindowUID: row.WindowUID, AgentName: row.AgentName}
		} else {
			affiliation, cached := current[row.AgentUID]
			if !cached {
				affiliation = ResolveAffiliation(reg, row.AgentUID)
				current[row.AgentUID] = affiliation
			}
			if !affiliation.complete() {
				continue
			}
			entry.basis, entry.affiliation = BasisRegistry, affiliation
		}
		s.rows = append(s.rows, entry)
		if !slices.Contains(s.projects, entry.affiliation.ProjectUID) {
			s.projects = append(s.projects, entry.affiliation.ProjectUID)
		}
	}

	for _, s := range sessions {
		switch {
		case len(s.projects) == 0:
			result.Unattributed++
			continue
		case len(s.projects) > 1:
			result.Ambiguous++
			continue
		case s.projects[0] != projectUID:
			continue
		}
		listed := ProjectSession{Provider: s.key.provider, SessionID: s.key.sessionID, Agents: []ProjectSessionAgent{}}
		agentAt := map[string]int{}
		recordedAt := map[string]time.Time{}
		for i, entry := range s.rows {
			record := entry.record
			// Merge's rules, over this session's attributed rows.
			if i == 0 {
				listed.ObservedAt, listed.TranscriptPath, listed.LastRecordAt, listed.Source =
					record.ObservedAt, record.TranscriptPath, record.LastRecordAt, record.Source
			} else {
				if !record.ObservedAt.Before(listed.ObservedAt) {
					listed.ObservedAt = record.ObservedAt
					if record.TranscriptPath != "" {
						listed.TranscriptPath = record.TranscriptPath
					}
				} else if listed.TranscriptPath == "" {
					listed.TranscriptPath = record.TranscriptPath
				}
				if record.LastRecordAt != nil && (listed.LastRecordAt == nil || record.LastRecordAt.After(*listed.LastRecordAt)) {
					listed.LastRecordAt = record.LastRecordAt
				}
				if sourceRank(record.Source) > sourceRank(listed.Source) {
					listed.Source = record.Source
				}
			}

			agent := ProjectSessionAgent{
				AgentUID: record.AgentUID, AgentName: entry.affiliation.AgentName,
				WindowUID: entry.affiliation.WindowUID, Basis: entry.basis,
			}
			if reg != nil {
				_, agent.InRegistry = reg.Agent(record.AgentUID)
			}
			at, seen := agentAt[record.AgentUID]
			if !seen {
				agentAt[record.AgentUID] = len(listed.Agents)
				listed.Agents = append(listed.Agents, agent)
				if entry.basis == BasisRecorded {
					recordedAt[record.AgentUID] = record.ObservedAt
				}
				continue
			}
			held := listed.Agents[at]
			switch {
			case entry.basis == BasisRecorded && held.Basis != BasisRecorded:
				listed.Agents[at] = agent
				recordedAt[record.AgentUID] = record.ObservedAt
			case entry.basis == BasisRecorded && !record.ObservedAt.Before(recordedAt[record.AgentUID]):
				listed.Agents[at] = agent
				recordedAt[record.AgentUID] = record.ObservedAt
			}
		}
		result.Sessions = append(result.Sessions, listed)
	}
	slices.SortStableFunc(result.Sessions, func(a, b ProjectSession) int { return a.ObservedAt.Compare(b.ObservedAt) })
	return result, nil
}
