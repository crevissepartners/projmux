package core

// RenameRequest selects exactly one resource by an explicit name or uid: ref.
// Project and Window are optional explicit scopes; Window scopes Panes/Agents.
// SocketPath overrides Options.SocketPath. With neither, rename is Registry-only.
type RenameRequest struct {
	Kind       Kind
	Ref        string
	Project    string
	Window     string
	Name       string
	SocketPath string
}

// RenameResult owns its singular CLI JSON projection and operation receipt.
// Committed remains true when a live mirror fails after the Registry commits;
// callers receive the result together with a typed mirror refusal in that case.
type RenameResult struct {
	Projection Resource
	Receipt    OperationReceipt
	Notice     string
	Committed  bool
}
