package codexgeneration

import "testing"

func TestEvaluateRetirementVacancyOpensOnlyForACompleteZeroCensus(t *testing.T) {
	complete := RetirementVacancyEvidence{ObligationsProjected: true, ThreadsEnumerated: true, EnumeratedThreads: 899}
	for _, testCase := range []struct {
		name     string
		evidence RetirementVacancyEvidence
		want     RetirementVacancy
	}{
		{"complete zero census is vacant", complete, VacancyVacant},
		{"a populated shared domain does not block", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.EnumeratedThreads = 100000
			return evidence
		}(), VacancyVacant},
		{"an empty shared domain is still a census", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.EnumeratedThreads = 0
			return evidence
		}(), VacancyVacant},
		{"an unprojected obligation set is never vacant", RetirementVacancyEvidence{ThreadsEnumerated: true}, VacancyObligationsUnprojected},
		{"an unread state domain is never vacant", RetirementVacancyEvidence{ObligationsProjected: true}, VacancyThreadsUnenumerated},
		{"zero of both is not vacant when neither census ran", RetirementVacancyEvidence{}, VacancyObligationsUnprojected},
		{"one live obligation blocks", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.LiveObligations = 1
			return evidence
		}(), VacancyLiveObligations},
		{"an endpoint-bound Agent with no obligation blocks", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.EndpointBoundAgents = 1
			return evidence
		}(), VacancyEndpointBoundAgents},
		{"an endpoint-bound Pane blocks", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.EndpointBoundPanes = 1
			return evidence
		}(), VacancyEndpointBoundPanes},
		{"a bound thread present in the store blocks", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.BoundThreadsPresent = 1
			return evidence
		}(), VacancyBoundThreadPresent},
		{"a negative count is invalid evidence, not an empty census", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.LiveObligations = -1
			return evidence
		}(), VacancyEvidenceInvalid},
		{"a negative thread count is invalid evidence", func() RetirementVacancyEvidence {
			evidence := complete
			evidence.EnumeratedThreads = -1
			return evidence
		}(), VacancyEvidenceInvalid},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := EvaluateRetirementVacancy(testCase.evidence)
			if got != testCase.want {
				t.Fatalf("verdict=%s want=%s evidence=%+v", got, testCase.want, testCase.evidence)
			}
			if got.Vacant() != (testCase.want == VacancyVacant) {
				t.Fatalf("Vacant()=%t for verdict %s", got.Vacant(), got)
			}
		})
	}
}

// TestRetirementVacancyReportsOneCauseInAStableOrder holds the operator
// contract: a mixed census names the primary criterion, not a set.
func TestRetirementVacancyReportsOneCauseInAStableOrder(t *testing.T) {
	mixed := RetirementVacancyEvidence{
		ObligationsProjected: true, ThreadsEnumerated: true, EnumeratedThreads: 3,
		LiveObligations: 2, EndpointBoundAgents: 3, EndpointBoundPanes: 4, BoundThreadsPresent: 1,
	}
	if got := EvaluateRetirementVacancy(mixed); got != VacancyLiveObligations {
		t.Fatalf("verdict=%s want=%s", got, VacancyLiveObligations)
	}
	mixed.LiveObligations = 0
	if got := EvaluateRetirementVacancy(mixed); got != VacancyEndpointBoundAgents {
		t.Fatalf("verdict=%s want=%s", got, VacancyEndpointBoundAgents)
	}
	// An unread state domain outranks every count: the census that did not run
	// can never be reported as a clean one.
	mixed.ThreadsEnumerated = false
	if got := EvaluateRetirementVacancy(mixed); got != VacancyThreadsUnenumerated {
		t.Fatalf("verdict=%s want=%s", got, VacancyThreadsUnenumerated)
	}
}
