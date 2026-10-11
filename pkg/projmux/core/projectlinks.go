package core

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
