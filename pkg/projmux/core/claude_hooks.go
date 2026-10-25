package core

// ClaudeHooksRequest selects the plan PreviewClaudeHooks makes: installing the
// projmux-managed Claude Code hooks, or with Remove removing them, as
// `projmux agent integrate claude` and its --remove do.
type ClaudeHooksRequest struct {
	Remove bool
}

// ClaudeHooksPlan is what `projmux agent integrate claude` would do to the
// Claude Code settings file at Path. Exists reports that the file was there;
// Current is its content ("" when it is not) and Base the lowercase hex
// SHA-256 of that content ("" when it is not). Next is the content the
// integration plans, and Changed whether the command would write it. Changed
// is the command's own decision and is not always Next != Current: a removal
// with nothing projmux-managed to remove keeps Changed false even when Next
// is the same settings re-encoded.
//
// Conflict, when set, explains why the integration refuses; ApplyClaudeHooks
// refuses it before writing or returning a no-change result. An install plan with a Conflict has Next
// equal to Current and Changed false, but a removal plan may still carry the
// cleaned Next and Changed true. Action is the command's one-line outcome
// text.
type ClaudeHooksPlan struct {
	Path     string
	Exists   bool
	Changed  bool
	Base     string
	Current  string
	Next     string
	Conflict string
	Action   string
}

// ApplyClaudeHooksRequest is a plan to carry out. Base is the Base of the
// ClaudeHooksPlan it was previewed from; the apply is refused when the file no
// longer holds it.
type ApplyClaudeHooksRequest struct {
	Remove bool
	Base   string
}

// ApplyClaudeHooksResult is what an apply did: Changed reports that it wrote
// the settings file at Path, and Action is the command's outcome text.
type ApplyClaudeHooksResult struct {
	Changed bool
	Path    string
	Action  string
}
