package core

// ProjectLinkRulesOptions controls Registry admission for one link rules call.
// The zero value requires a valid Registry. Opting in never repairs the Registry.
type ProjectLinkRulesOptions struct {
	AllowDegradedRegistry bool `json:"allowDegradedRegistry,omitempty"`
}

// ProjectLinkRules is the stored input format. URLs is a name-to-URL map.
type ProjectLinkRules struct {
	Jira  []string          `json:"jira"`
	Repo  []string          `json:"repo"`
	URLs  map[string]string `json:"urls"`
	Links []ProjectLink     `json:"links"`
}
type ProjectLink struct {
	LabelKey string `json:"labelKey"`
	Template string `json:"template"`
}

// ProjectLinkRulesResult includes examples and the Project's available variables.
// Every list is non-null; named URLs and Project labels are sorted by name.
type ProjectLinkRulesResult struct {
	Project          string                `json:"project"`
	Jira             []string              `json:"jira"`
	Repo             []string              `json:"repo"`
	URLs             []ProjectLinkNamedURL `json:"urls"`
	Links            []ProjectLinkExample  `json:"links"`
	ExampleValue     string                `json:"exampleValue"`
	Placeholders     []string              `json:"placeholders"`
	ProjectVariables []ProjectLinkVariable `json:"projectVariables"`
	DryRun           bool                  `json:"dryRun,omitempty"`
}
type ProjectLinkNamedURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type ProjectLinkExample struct {
	LabelKey string `json:"labelKey"`
	Template string `json:"template"`
	Example  string `json:"example,omitempty"`
}
type ProjectLinkVariable struct {
	Placeholder string `json:"placeholder"`
	Value       string `json:"value"`
}

// FileIdentity is the stat identity of one regular file. Writers that replace
// a file atomically change at least one field. Compare whole values; no single
// field is an identity on its own, and an identity says nothing about the
// file's content, its Project or who may read it.
type FileIdentity struct {
	Device                uint64
	Inode                 uint64
	Size                  int64
	ModTimeNanoseconds    int64
	ChangeTimeSeconds     int64
	ChangeTimeNanoseconds int64
}

// ProjectLinkRulesRead is one Project's stored link rules with the identity
// of the file they were read from. FileIdentity is set only when the stats
// taken immediately before and after the read succeed, find a regular file and
// agree; otherwise, including for a missing file, it is nil.
type ProjectLinkRulesRead struct {
	Rules        ProjectLinkRules
	FileIdentity *FileIdentity
}
