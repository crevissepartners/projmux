package metadata

// Creator annotations record which Agent's Pane an explicit `create agent` or
// `create window --provider` ran in. They are provenance, not authentication:
// like a message `--source`, they say where a create was observed to come
// from and grant nothing. An absent key does not mean a human created the
// Agent; creates from the UI intents, the web API, and Codex's app-server
// lane deliberately leave them empty.
const (
	// AnnotationCreatorAgent is the bare UID of the Agent whose Pane ran the
	// create.
	AnnotationCreatorAgent = "projmux.io/creator-agent"
	// AnnotationCreatorPane is the bare UID of that Agent's managed Pane.
	AnnotationCreatorPane = "projmux.io/creator-pane"
	// AnnotationCreatorBasis names the evidence the other two keys rest on.
	AnnotationCreatorBasis = "projmux.io/creator-basis"

	// CreatorBasisPaneChain is the only basis: the create's ambient tmux Pane
	// is the creator's live managed Pane on the create's own app server, and
	// the create process descends from that Pane's process.
	CreatorBasisPaneChain = "pane-chain"
)

// CreatorAnnotations returns a fresh map holding the three creator keys.
func CreatorAnnotations(agentUID, paneUID string) map[string]string {
	return map[string]string{
		AnnotationCreatorAgent: agentUID,
		AnnotationCreatorPane:  paneUID,
		AnnotationCreatorBasis: CreatorBasisPaneChain,
	}
}
