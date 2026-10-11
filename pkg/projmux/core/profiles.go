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
