package core

// AgentGuidanceFile describes the user addition independently of effective policy.
// Revision identifies current user bytes, policy, and active fragment inputs.
type AgentGuidanceFile struct {
	Path          string `json:"path"`
	Present       bool   `json:"present"`
	Text          string `json:"text"`
	Bytes         int    `json:"bytes"`
	Digest        string `json:"digest"`
	Revision      string `json:"revision"`
	MaxBytes      int    `json:"maxBytes"`
	DefaultText   string `json:"defaultText"`
	DefaultDigest string `json:"defaultDigest"`
}

// AgentGuidanceValidation checks both tmux and process assembly without writes.
type AgentGuidanceValidation struct {
	Text                  string `json:"text"`
	Bytes                 int    `json:"bytes"`
	Digest                string `json:"digest"`
	Revision              string `json:"revision"`
	MaxBytes              int    `json:"maxBytes"`
	AssembledBytes        int    `json:"assembledBytes"`
	ProcessAssembledBytes int    `json:"processAssembledBytes"`
}
type AgentGuidancePart struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Present bool   `json:"present"`
	Enabled bool   `json:"enabled"`
}

// AgentGuidanceInspection retains the CLI show view and its migration behavior.
type AgentGuidanceInspection struct {
	Disabled          bool                `json:"disabled"`
	DisabledFragments []string            `json:"disabledFragments"`
	Parts             []AgentGuidancePart `json:"parts"`
	Text              string              `json:"text"`
	Digest            string              `json:"digest"`
	MigrationNotice   string              `json:"-"`
	Source            string              `json:"-"`
}
