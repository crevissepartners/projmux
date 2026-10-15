package core

import "time"

// SessionSource records how a history row was obtained.
type SessionSource string

const (
	SessionSourceObserved  SessionSource = "observed"
	SessionSourceCurrent   SessionSource = "current"
	SessionSourceEstimated SessionSource = "estimated"
)

// SessionAffiliationBasis records whether affiliation was recorded or resolved.
type SessionAffiliationBasis string

const (
	SessionAffiliationRecorded SessionAffiliationBasis = "recorded"
	SessionAffiliationRegistry SessionAffiliationBasis = "registry"
)

// AgentSession is one CLI session history row. TranscriptPath is metadata only;
// the Codex current/history contract may leave it empty.
type AgentSession struct {
	AgentUID       string        `json:"agentUID"`
	Provider       string        `json:"provider"`
	SessionID      string        `json:"sessionId"`
	TranscriptPath string        `json:"transcriptPath"`
	ObservedAt     time.Time     `json:"observedAt"`
	Source         SessionSource `json:"source"`
	// LastRecordAt is the timestamp of the transcript's last record. Only an
	// estimated row carries it (its observedAt is the first record's); an
	// observed or current row omits the key.
	LastRecordAt *time.Time `json:"lastRecordAt,omitempty"`
	ProjectUID   string     `json:"projectUID,omitempty"`
	WindowUID    string     `json:"windowUID,omitempty"`
	AgentName    string     `json:"agentName,omitempty"`
	// AffiliationBasis is SessionAffiliationRegistry on a row Attribute wrote, and empty
	// on every other row.
	AffiliationBasis SessionAffiliationBasis `json:"affiliationBasis,omitempty"`
}

// AgentSessionsList matches agent sessions list JSON, including empty arrays.
type AgentSessionsList struct {
	AgentUID     string         `json:"agentUID"`
	AgentName    string         `json:"agentName"`
	Sessions     []AgentSession `json:"sessions"`
	CorruptLines int            `json:"corruptLines"`
}

// ProjectSessionsList matches agent sessions project JSON. An empty ProjectName
// identifies a removed Project read by exact UID.
type ProjectSessionsList struct {
	ProjectUID   string           `json:"projectUID"`
	ProjectName  string           `json:"projectName"`
	Sessions     []ProjectSession `json:"sessions"`
	Unattributed int              `json:"unattributed"`
	Ambiguous    int              `json:"ambiguous"`
	CorruptLines int              `json:"corruptLines"`
}

// ProjectSession is one attributed conversation and its participating Agents.
type ProjectSession struct {
	Provider       string                `json:"provider"`
	SessionID      string                `json:"sessionId"`
	TranscriptPath string                `json:"transcriptPath"`
	ObservedAt     time.Time             `json:"observedAt"`
	LastRecordAt   *time.Time            `json:"lastRecordAt,omitempty"`
	Source         SessionSource         `json:"source"`
	Agents         []ProjectSessionAgent `json:"agents"`
}

// ProjectSessionAgent is one Agent a project session's attributed rows name.
type ProjectSessionAgent struct {
	AgentUID  string `json:"agentUID"`
	AgentName string `json:"agentName"`
	WindowUID string `json:"windowUID"`
	// InRegistry reports whether the Agent exists in the current Registry. A
	// deleted Agent stays listed through the affiliation its rows recorded.
	InRegistry bool                    `json:"inRegistry"`
	Basis      SessionAffiliationBasis `json:"basis"`
}
