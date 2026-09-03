package app

// livePaneRow is the neutral projection of one live tmux pane that the
// notify cluster consumes. Producers (today: the attention-side lister in
// attention.go) compute the badge/state semantics up front — ReplyState and
// TitleBadge — so notify code never reaches into attention internals to
// interpret raw pane options or title glyphs.
type livePaneRow struct {
	Session string
	Window  string
	Pane    string
	Socket  string
	Title   string
	// AttentionState is the raw attention state string, carried only so the
	// `notify list --live` JSON keeps surfacing it verbatim.
	AttentionState string
	AIState        string
	Agent          string
	Topic          string
	// Generation-aware fields are populated only by the Registry-backed
	// production decorator. Legacy/provider-neutral rows leave them empty.
	AgentUID             string
	PaneUID              string
	StateDomainID        string
	EndpointGenerationID string
	AuthorityFence       string
	// ReplyState reports that the pane's attention state machine is in the
	// "reply ready" state (the producer's push condition, together with a
	// non-empty Agent).
	ReplyState bool
	// TitleBadge reports that the pane title carries an attention/braille
	// badge prefix (title-only attention, no queue entry).
	TitleBadge bool
}

// livePaneLister lists every pane on the tmux server as neutral live-pane
// rows. It is the seam between the notify cluster (consumer) and the
// attention cluster (producer); notifyCommand receives an implementation via
// constructor wiring in [New].
type livePaneLister interface {
	ListLivePanes() ([]livePaneRow, error)
}
