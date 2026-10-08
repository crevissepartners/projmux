package metadata

// Creator annotations record where an Agent's create came from, together with
// the kind of evidence behind it. They are written on `create agent` (every
// spelling), `create window --provider`, and the UI intents, on the new Agent
// and its managed Pane in the transaction that commits them. They are
// provenance, not authentication: like a message `--source`, they grant
// nothing and no permission, route, or selector reads them.
//
// A record carries exactly one basis and only the keys that basis proves:
//
//   - pane-chain: creator-agent and creator-pane, observed.
//   - process-chain: the same keys, observed from a live provider child.
//   - explicit: creator-agent only, declared by the caller.
//   - operator: creator-client only, stated by an in-process operator client.
//
// An absent basis means no evidence at all, which is not the same as a human
// having created the Agent. The creator Agent is never the created Agent.
const (
	// AnnotationCreatorAgent is the bare UID of the creator Agent: the Agent
	// whose Pane ran the create (pane-chain), or the Agent the caller named
	// (explicit).
	AnnotationCreatorAgent = "projmux.io/creator-agent"
	// AnnotationCreatorPane is the bare UID of that Agent's managed Pane. Only
	// the pane-chain and process-chain bases prove it.
	AnnotationCreatorPane = "projmux.io/creator-pane"
	// AnnotationCreatorClient is the operator client name (see package
	// operatorclient) of an operator record.
	AnnotationCreatorClient = "projmux.io/creator-client"
	// AnnotationCreatorBasis names the evidence the other keys rest on.
	AnnotationCreatorBasis = "projmux.io/creator-basis"

	// CreatorBasisPaneChain is observation: the create's ambient tmux Pane is
	// the creator's live managed Pane on the create's own app server, and the
	// create process descends from that Pane's process. It wins over every
	// other CLI observation basis; in-process operator overrides remain separate.
	CreatorBasisPaneChain = "pane-chain"
	// CreatorBasisProcessChain observes a live process provider ancestor.
	CreatorBasisProcessChain = "process-chain"
	// CreatorBasisExplicit is the caller's declaration (`--creator
	// uid:<agent>`) naming an Agent that exists in the Registry. It can be
	// forged, which is what this basis says; it is recorded only when no
	// pane or process chain is.
	CreatorBasisExplicit = "explicit"
	// CreatorBasisOperator is an in-process operator client acting for the
	// operator. Only in-process code can state it; no argv spelling does.
	CreatorBasisOperator = "operator"
)

// CreatorAnnotations returns a fresh map holding the three pane-chain keys.
func CreatorAnnotations(agentUID, paneUID string) map[string]string {
	return map[string]string{
		AnnotationCreatorAgent: agentUID,
		AnnotationCreatorPane:  paneUID,
		AnnotationCreatorBasis: CreatorBasisPaneChain,
	}
}

// ExplicitCreatorAnnotations returns a fresh map holding the explicit basis and
// the declared creator Agent. No Pane is recorded: a declaration cannot prove
// which Pane the caller ran in.
func ExplicitCreatorAnnotations(agentUID string) map[string]string {
	return map[string]string{
		AnnotationCreatorAgent: agentUID,
		AnnotationCreatorBasis: CreatorBasisExplicit,
	}
}

// OperatorCreatorAnnotations returns a fresh map holding the operator basis
// and the client name. Callers validate the name first; building an operator
// record is confined to in-process producers.
func OperatorCreatorAnnotations(client string) map[string]string {
	return map[string]string{
		AnnotationCreatorClient: client,
		AnnotationCreatorBasis:  CreatorBasisOperator,
	}
}

// ProcessCreatorAnnotations records the observed provider child's managed Pane.
func ProcessCreatorAnnotations(agentUID, paneUID string) map[string]string {
	return map[string]string{
		AnnotationCreatorAgent: agentUID,
		AnnotationCreatorPane:  paneUID,
		AnnotationCreatorBasis: CreatorBasisProcessChain,
	}
}
