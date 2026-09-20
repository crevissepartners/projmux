package metadata

import "testing"

// claudeShapePane builds one Pane in the state a measured sample was observed
// in. The four inputs are exactly the four fields the measurement found
// sufficient to separate the shapes: whether a lease exists, whether the
// session id and generation a cleared lease leaves behind are still there,
// whether the bound process is alive, and whether a termination receipt exists.
func claudeShapePane(registered, residualSession, terminated bool) Pane {
	process := ProcessIdentity{PID: 4242, OwnerUID: 1000, Start: "linux:boot:1"}
	binding := &ClaudeActivationBinding{Process: process}
	if residualSession || registered {
		binding.RegistrationSessionID = "session-1"
		binding.RegistrationGeneration = "registration-1"
	}
	if registered {
		binding.Registration = &ClaudeRegistration{Ready: true, Authority: ClaudeAuthorityRef{
			SessionID: "session-1", Process: process, RegistrationGeneration: "registration-1",
			LeaseProcess: ProcessIdentity{PID: 4243, OwnerUID: 1000, Start: "linux:boot:2"},
		}}
	}
	pane := Pane{}
	pane.Spec.Role = PaneRoleAgent
	pane.Status.Activation = PaneActivation{Generation: "gen-1", RuntimeID: "%7", Claude: binding}
	if terminated {
		pane.Status.LastTermination = &TerminationEvidence{
			Source: TerminationSourceReconcile, Classification: TerminationUnknown,
		}
	}
	return pane
}

// TestClassifyClaudeRegistrationSeparatesMeasuredShapes replays the nine
// isolated samples the shape measurement produced. Their names are kept so a
// future failure points back at the sample that fixed the row, not just at a
// row number.
func TestClassifyClaudeRegistrationSeparatesMeasuredShapes(t *testing.T) {
	for _, testCase := range []struct {
		sample          string
		registered      bool
		residualSession bool
		processAlive    bool
		terminated      bool
		want            ClaudeRegistrationShape
	}{
		{sample: "iso-src", registered: true, residualSession: true, processAlive: true, want: ClaudeRegistrationReady},
		{sample: "iso-brecover", registered: true, residualSession: true, processAlive: true, want: ClaudeRegistrationReady},
		{sample: "iso-arecover2", registered: true, residualSession: true, processAlive: true, want: ClaudeRegistrationReady},
		{sample: "iso-armB", processAlive: true, want: ClaudeRegistrationNeverStarted},
		{sample: "iso-arecover", processAlive: true, want: ClaudeRegistrationNeverStarted},
		{sample: "iso-armA", residualSession: true, processAlive: true, want: ClaudeRegistrationLost},
		{sample: "iso-shapeC2", terminated: true, want: ClaudeRegistrationSessionGone},
		{sample: "iso-shapeC3", terminated: true, want: ClaudeRegistrationSessionGone},
		{sample: "iso-shapeC4", terminated: true, want: ClaudeRegistrationSessionGone},
	} {
		t.Run(testCase.sample, func(t *testing.T) {
			pane := claudeShapePane(testCase.registered, testCase.residualSession, testCase.terminated)
			if got := ClassifyClaudeRegistration(pane, testCase.processAlive); got != testCase.want {
				t.Fatalf("sample %s classified as %q, want %q", testCase.sample, got, testCase.want)
			}
		})
	}
}

// TestClassifyClaudeRegistrationIgnoresAgentPhase is the measurement's own
// warning turned into a guard: a Pane can terminate without its Agent leaving
// Running, so nothing here may read a phase. The classifier takes no Agent at
// all, and this test states why that signature is the contract.
func TestClassifyClaudeRegistrationIgnoresAgentPhase(t *testing.T) {
	gone := claudeShapePane(false, false, true)
	if got := ClassifyClaudeRegistration(gone, true); got != ClaudeRegistrationSessionGone {
		t.Fatalf("a terminated Pane classified as %q even though its process reads alive, want %q", got, ClaudeRegistrationSessionGone)
	}
	live := claudeShapePane(false, true, false)
	if got := ClassifyClaudeRegistration(live, false); got != ClaudeRegistrationSessionGone {
		t.Fatalf("a dead process with a residual session classified as %q, want %q", got, ClaudeRegistrationSessionGone)
	}
}

// TestClassifyClaudeRegistrationHandlesUnboundActivation covers the state no
// isolated sample produced but every freshly created Agent passes through: the
// activation exists and no provider process has been bound to it yet.
func TestClassifyClaudeRegistrationHandlesUnboundActivation(t *testing.T) {
	pane := Pane{}
	pane.Spec.Role = PaneRoleAgent
	pane.Status.Activation = PaneActivation{Generation: "gen-1", RuntimeID: "%7"}
	if got := ClassifyClaudeRegistration(pane, false); got != ClaudeRegistrationNeverStarted {
		t.Fatalf("an activation with no bound process classified as %q, want %q", got, ClaudeRegistrationNeverStarted)
	}
	pane.Status.LastTermination = &TerminationEvidence{Source: TerminationSourceReconcile, Classification: TerminationUnknown}
	if got := ClassifyClaudeRegistration(pane, false); got != ClaudeRegistrationSessionGone {
		t.Fatalf("a terminated unbound activation classified as %q, want %q", got, ClaudeRegistrationSessionGone)
	}
}

// TestClaudeRegistrationShapeDiagnosesEachShapeDifferently fixes the property
// the whole change exists for. Before it, all three shapes refused with one
// byte-identical sentence.
func TestClaudeRegistrationShapeDiagnosesEachShapeDifferently(t *testing.T) {
	seen := map[string]ClaudeRegistrationShape{}
	for _, shape := range []ClaudeRegistrationShape{ClaudeRegistrationNeverStarted, ClaudeRegistrationLost, ClaudeRegistrationSessionGone} {
		diagnosis := shape.Diagnosis()
		if diagnosis == "" {
			t.Fatalf("shape %q has no diagnosis", shape)
		}
		if other, ok := seen[diagnosis]; ok {
			t.Fatalf("shapes %q and %q share the diagnosis %q", other, shape, diagnosis)
		}
		seen[diagnosis] = shape
	}
	if ClaudeRegistrationReady.Diagnosis() != "" {
		t.Fatalf("a ready registration has a diagnosis: %q", ClaudeRegistrationReady.Diagnosis())
	}
}
