package core

// ProfileInfo contains profile list's exact table cells. Empty optional values
// are "-", Valid is "yes" or "no (<reason>)", Scope is "global" or a Project UID.
type ProfileInfo struct {
	Name, Source, Provider, Instructions, Model, Effort, Roles, Digest, Valid, Scope string
}

// ProfileContent holds caller-owned raw bytes without newline normalization.
type ProfileContent struct {
	Name    string
	Source  string
	Content []byte
	Digest  string
}

// ProfileWriteResult describes an atomically stored file.
type ProfileWriteResult struct {
	Name   string
	Path   string
	Digest string
	Size   int64
}

// ProfileSpec is a parsed profile. Empty values were absent; lists are never null.
type ProfileSpec struct {
	Project      string             `json:"project"`
	Provider     string             `json:"provider"`
	Instructions string             `json:"instructions"`
	Model        string             `json:"model"`
	Effort       string             `json:"effort"`
	Roles        []string           `json:"roles"`
	Permissions  ProfilePermissions `json:"permissions"`
}

// ProfilePermissions is the profile's permissions table.
type ProfilePermissions struct {
	Sandbox  string   `json:"sandbox"`
	Approval string   `json:"approval"`
	Allow    []string `json:"allow"`
	Deny     []string `json:"deny"`
}

// HasPermissions reports whether any permission value is set. Empty tables
// and empty lists do not set permissions.
func (p ProfilePermissions) HasPermissions() bool {
	return p.Sandbox != "" || p.Approval != "" || len(p.Allow) > 0 || len(p.Deny) > 0
}

// ProfilePreview is a write-free candidate; later WriteProfile validates again.
type ProfilePreview struct {
	Name   string      `json:"name"`
	Digest string      `json:"digest"`
	Size   int64       `json:"size"`
	Spec   ProfileSpec `json:"spec"`
	DryRun bool        `json:"dryRun"`
}
