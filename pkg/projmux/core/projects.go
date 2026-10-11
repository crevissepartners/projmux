package core

// CreateProjectRequest registers one absolute Root. Name is optional; Labels
// follow the CLI's repeated key=value grammar, in order (last duplicate wins).
type CreateProjectRequest struct {
	Root   string
	Name   string
	Labels []string
}

// CreateProjectResult describes Registry-only bootstrap. Windows and Panes
// include the existing graph on reuse. Projection is the CLI -o json document;
// Receipt is the CLI -o receipt document. Every returned value is caller-owned.
type CreateProjectResult struct {
	Created    bool
	Reused     bool
	Project    Project
	Windows    []Window
	Panes      []Pane
	Projection ResourceList
	Receipt    OperationReceipt
}
