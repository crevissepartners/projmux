package codexgeneration

import "testing"

func TestColdRecoveryAlwaysPrefersExactSameGenerationBeforeQualifiedFallback(t *testing.T) {
	for _, test := range []struct {
		name     string
		evidence ColdRecoveryEvidence
		want     ColdRecoveryDecision
	}{
		{name: "same generation wins even when fallback qualified", evidence: ColdRecoveryEvidence{Owner: OwnerProjmuxPrivate, SameGenerationBundle: true, SameGenerationLaunchAuth: true, QualifiedVersionPair: true}, want: ColdRecoveryRestartSameGeneration},
		{name: "qualified fallback only after same generation unavailable", evidence: ColdRecoveryEvidence{Owner: OwnerProjmuxPrivate, QualifiedVersionPair: true}, want: ColdRecoveryQualifiedHandover},
		{name: "foreign exact process is never adopted", evidence: ColdRecoveryEvidence{Owner: OwnerUnmanaged, SameGenerationBundle: true, SameGenerationLaunchAuth: true, QualifiedVersionPair: true}, want: ColdRecoveryQualifiedHandover},
		{name: "official manager remains external", evidence: ColdRecoveryEvidence{Owner: OwnerOfficialManaged, SameGenerationBundle: true, SameGenerationLaunchAuth: true, QualifiedVersionPair: true}, want: ColdRecoveryQualifiedHandover},
		{name: "unqualified fallback blocks", evidence: ColdRecoveryEvidence{Owner: OwnerUnknown}, want: ColdRecoveryBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := DecideColdRecovery(test.evidence); got != test.want {
				t.Fatalf("decision=%s want=%s", got, test.want)
			}
		})
	}
}
