package codexgeneration

// RetirementVacancy is the closed verdict for "this draining generation has
// nothing left to hand over".
//
// It is deliberately not a relaxation of the version-pair gate. A version-pair
// qualification proves that a thread survives a cross-version resume; a
// generation with no thread to resume has no subject for that proof. Vacancy
// therefore names the one case where the evidence has no referent, and nothing
// wider: a single live obligation, a single Registry record still bound to the
// endpoint, or a shared state domain that could not be read keeps the existing
// fail-closed refusal exactly as it was.
type RetirementVacancy string

const (
	// VacancyVacant is the only verdict that opens the retirement path.
	VacancyVacant RetirementVacancy = "vacant"
	// VacancyEvidenceInvalid rejects a negative count. Evidence that cannot be
	// read as a census is never read as an empty one.
	VacancyEvidenceInvalid RetirementVacancy = "vacancy-evidence-invalid"
	// VacancyObligationsUnprojected refuses a decision taken from a journal
	// snapshot. The obligation set must be projected from a Registry snapshot
	// read for this decision.
	VacancyObligationsUnprojected RetirementVacancy = "obligations-not-freshly-projected"
	// VacancyThreadsUnenumerated refuses a decision taken without reading the
	// shared state domain. Obligations carry no ThreadID, so the store is the
	// only second angle on what the retiring generation still holds.
	VacancyThreadsUnenumerated RetirementVacancy = "state-domain-threads-unenumerated"
	VacancyLiveObligations     RetirementVacancy = "live-obligations-present"
	VacancyEndpointBoundAgents RetirementVacancy = "endpoint-bound-agents-present"
	VacancyEndpointBoundPanes  RetirementVacancy = "endpoint-bound-panes-present"
	VacancyBoundThreadPresent  RetirementVacancy = "state-domain-bound-thread-present"
)

// RetirementVacancyEvidence is the content-free census behind one vacancy
// verdict. Every field is a count or a "this census actually ran" bit; no
// path, thread id, prompt, or Agent identity may enter it.
//
// The two projected/enumerated bits exist because zero is ambiguous. A census
// that never ran also reports zero, and reading that as "nothing to hand over"
// is precisely the failure this model refuses.
type RetirementVacancyEvidence struct {
	// ObligationsProjected records that LiveObligations came from a Registry
	// snapshot projected for this decision, not from the durable journal.
	ObligationsProjected bool `json:"obligationsProjected"`
	// LiveObligations counts freshly projected, non-closed obligations on the
	// exact retiring endpoint.
	LiveObligations int `json:"liveObligations"`
	// EndpointBoundAgents counts Agents whose session ref still names the exact
	// retiring endpoint, including those carrying no ThreadID. An obligation
	// requires a ThreadID, so this is the Registry-side cover for that hole.
	EndpointBoundAgents int `json:"endpointBoundAgents"`
	// EndpointBoundPanes counts Pane activations whose native authority names
	// the exact retiring endpoint. A Pane outlives its Agent record, so this is
	// an axis the obligation census cannot see at all.
	EndpointBoundPanes int `json:"endpointBoundPanes"`
	// ThreadsEnumerated records that the shared state domain was read.
	ThreadsEnumerated bool `json:"threadsEnumerated"`
	// EnumeratedThreads counts the threads that read found. It is reported for
	// the operator, never gated on: the shared domain holds every generation's
	// threads and a populated domain is the normal case.
	EnumeratedThreads int `json:"enumeratedThreads"`
	// BoundThreadsPresent counts enumerated threads that a Registry record
	// still binds to the exact retiring endpoint. This is the cross-check:
	// it ties a claim on the retiring generation to a thread that really
	// exists in the store.
	BoundThreadsPresent int `json:"boundThreadsPresent"`
}

// EvaluateRetirementVacancy returns the single closed reason a draining
// generation is or is not vacant. It is total, pure, and order-stable: the
// first unmet condition wins so an operator reads one cause, not a set.
func EvaluateRetirementVacancy(evidence RetirementVacancyEvidence) RetirementVacancy {
	if evidence.LiveObligations < 0 || evidence.EndpointBoundAgents < 0 ||
		evidence.EndpointBoundPanes < 0 || evidence.EnumeratedThreads < 0 || evidence.BoundThreadsPresent < 0 {
		return VacancyEvidenceInvalid
	}
	if !evidence.ObligationsProjected {
		return VacancyObligationsUnprojected
	}
	if !evidence.ThreadsEnumerated {
		return VacancyThreadsUnenumerated
	}
	if evidence.LiveObligations != 0 {
		return VacancyLiveObligations
	}
	if evidence.EndpointBoundAgents != 0 {
		return VacancyEndpointBoundAgents
	}
	if evidence.EndpointBoundPanes != 0 {
		return VacancyEndpointBoundPanes
	}
	if evidence.BoundThreadsPresent != 0 {
		return VacancyBoundThreadPresent
	}
	return VacancyVacant
}

// Vacant reports whether the verdict opens the retirement path.
func (v RetirementVacancy) Vacant() bool { return v == VacancyVacant }

// String keeps the verdict from gaining contextual material through fmt.
func (v RetirementVacancy) String() string { return string(v) }
