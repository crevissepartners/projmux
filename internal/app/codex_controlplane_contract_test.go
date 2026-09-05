package app

// The regression-detection ledger for the Codex control plane.
//
// Three defects lived through a neighbouring eight-phase track closing green
// from end to end, and the reason was not a weak track: it was that no test
// reached these surfaces, and the one that did pinned a broken state as its
// passing condition. Shipping the fixes without this ledger would leave the
// same blind spot behind under new code.
//
// It lives in the test tree on purpose. It is a statement about which tests
// hold which guarantee, and nothing the application does at runtime may depend
// on it; the product surface is the diagnosis in codex_controlplane_surfaces.go.

// codexControlPlaneSurfaceContract ties each diagnosed surface to the product
// contract whose Guarantee it renders.
var codexControlPlaneSurfaceContract = map[string]string{
	codexSurfaceBrokerDiagnostics:    "C-1",
	codexSurfaceHookAttribution:      "C-2",
	codexSurfaceObserverReason:       "C-3",
	codexSurfaceConnectionContinuity: "C-3-continuity",
	codexSurfaceTurnAdmission:        "C-5",
	codexSurfaceHookDelivery:         "C-2-delivery",
	codexSurfacePaneOwnership:        "C-2-ownership",
}

// codexControlPlaneContractEnforcement is each contract's Enforcement cell,
// carried as test names a build can resolve.
//
// Squash merges erase the branch that tied a fix to the test that holds it, so
// a cell naming a test that no longer exists reads as enforced while enforcing
// nothing. TestControlPlaneContractCellsNameLiveTests resolves every name here
// against the declarations in the tree, which turns the roadmap's manual
// release check into one a build performs.
var codexControlPlaneContractEnforcement = map[string][]string{
	// C-1: the broker diagnosis dials the endpoint the runtime published,
	// rather than a key it assumed. Assuming it reported a live broker as
	// absent, and an operator killed a process on that answer.
	"C-1": {
		"TestBrokerDiagnosticsDialTheEndpointTheRuntimePublished",
		"TestBrokerDiagnosticsStayAbsentWhenOnlyAnUnpublishedArtifactRemains",
		"TestBrokerDiagnosticsSelectEveryPublishedEndpointDeterministically",
		"TestCodexBrokerDiagnosticsSurfaceNamesThePublishedButUnreachableRuntime",
	},
	// C-2: a hook event is attributed to the Pane that owns it, from an
	// identity the hook was handed rather than one inherited from whatever
	// environment happened to launch it.
	"C-2": {
		"TestProviderHookCommandsCarryTheirOwnPaneIdentity",
		"TestProviderHookPaneArgumentIsProviderNeutral",
		"TestCodexIntegrationStillOwnsThePaneBlindHookCommand",
		"TestCodexIntegrationConvergesThePaneBlindHookCommandForward",
		"TestMatchAIPaneResolvesTheHandedPaneWithoutTmuxEnvironment",
		"TestMatchAIPanePrefersTheHandedPaneOverInheritedEvidence",
		"TestMatchAIPaneKeepsTheEstablishedFallbackWhenNothingWasHandedOver",
		"TestMatchAIPaneRefusesAHandedPaneThatIsGone",
		"TestAttributionFailureReasonsAreDistinctAndClosed",
		"TestHookIngestRoutesAcceptTheExplicitPaneArgument",
		"TestHookAttributionSurfaceSeparatesTotalFailureFromPartial",
	},
	// C-3: every authority transition carries a captured reason and leaves a
	// durable record, so a flapping observer can be told from a frozen one
	// after the fact.
	"C-3": {
		"TestCodexObserverEventLoopExitsCarryTheirOwnReasonToken",
		"TestCodexObserverExitPathsAreOneToOneWithTokens",
		"TestCodexObserverReasonVocabularyIsClosed",
		"TestCodexBrokerEpochRecordsWhyItsStreamClosed",
		"TestCodexObserverJournalRecordsConnectDisconnectReconnectInOrder",
		"TestCodexObserverJournalCoalescesRepeatedTransitions",
		"TestCodexObserverJournalRecordFieldsAreWhitelisted",
		"TestManagedCodexAuthorityDoctorSeparatesFlappingFrozenAndStopped",
		"TestObserverReasonSurfaceRefusesToCallAnUncapturedReasonHealthy",
	},
	// C-3 continuity: the upstream connection survives a message past the
	// frame bound instead of dying on it, which is what held the control plane
	// in a reconnect cycle for hours.
	"C-3-continuity": {
		"TestClientDropsAnOversizedAnswerAndKeepsTheConnection",
		"TestClientKeepsServingRequestsAfterAnOversizedAnswer",
		"TestWebsocketStreamDropsAnOversizedMessageAndStaysFramed",
		"TestOversizedAnswerEndsOneMutationAndNotTheConnectionEpoch",
		"TestOversizedAnswerIsToldApartFromADisconnectBoundary",
		"TestConnectionContinuitySurfaceReportsCumulativeReconnects",
	},
	// C-2 delivery: an attributed hook event changes the Pane it reached, and a
	// write that cannot happen leaves a bounded reason rather than a raw
	// process-exit string. Awaiting the Phase 7 vocabulary; the property gate
	// below holds without naming its tokens.
	"C-2-delivery": {
		"TestHookDeliverySurfaceTreatsAnOpaqueFailureAsBroken",
		"TestDeliveryHealthCountsOnlyAttributedEvents",
		"TestOpaqueDeliveryReasonsNeverReachTheDiagnosis",
	},
	// C-2 ownership: an attributed hook event landed on a Pane of its own
	// provider.
	//
	// The two entries here are the runtime half, and they are provider-neutral:
	// they judge attributions the log actually recorded, against the provider
	// the Registry records for the Pane each one landed on. That half needs no
	// vocabulary, which is why it exists already.
	//
	// The resolver half is still owed, and it cannot be written here yet: it
	// names symbols the ownership fix introduces, and restating them as string
	// literals would let a rename walk past this gate silently. Five properties
	// are waiting on that rebase, and the fifth is the one most easily missed:
	//
	//  1. the composite "no other match" refusal is not swallowed by a
	//     downstream general token;
	//  2. a rejected explicit identity flows on to the Registry step and the
	//     established ladder rather than being dropped;
	//  3. the fall-through skips the inherited-environment step, which carries
	//     the same envelope that was just rejected and would readmit the leak;
	//  4. the three older explicit refusals keep their existing behaviour and
	//     do not fall through, so this gate cannot mistake Phase 2's contract
	//     for a regression;
	//  5. the Pane the fall-through finally resolves is not foreign either.
	//     The ladder's working-directory step matches on path alone, and on a
	//     machine where two providers share a repository that step can hand
	//     back the very Pane the explicit check refused. Closing the front door
	//     and leaving that one open would pass every other property here.
	"C-2-ownership": {
		"TestPaneOwnershipSurfaceReportsAForeignAttributionAsBroken",
		"TestOwnershipHealthCallsOnlyAPositivelyForeignPaneForeign",
	},
	// C-5: a turn admission judges a completed authority transition, never a
	// triple sampled between two of its three writes.
	"C-5": {
		"TestSettledCodexAuthorityAdmissionIgnoresTornAuthorityWrites",
		"TestCodexAuthorityAdmissionTakesTheExactWriterFence",
		"TestConcurrentCodexAuthorityTransitionsNeverExposeATornAdmissionSnapshot",
		"TestSettledCodexAuthorityReadKeepsTheRegistryRefusalForAPanelessAgent",
		"TestTurnAdmissionSurfaceReportsATornAuthoritySnapshotAsBroken",
	},
}
