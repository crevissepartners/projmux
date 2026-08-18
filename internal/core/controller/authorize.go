package controller

import (
	"fmt"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// Candidate is one mutation a producer wants to make, before the policy has
// seen it.
//
// Producers describe what they want in their own terms and never decide whether
// they may have it. That inversion is the point of the kernel: a new producer
// gains the whole refusal set by describing a candidate, and cannot gain a new
// authority by forgetting a check.
type Candidate struct {
	// Key is this candidate's position in the plan's total order.
	Key string
	// Intent is what the write would do.
	Intent Intent
	// Kind is the resource kind label for display.
	Kind string
	// Target is any spelling of the tmux handle: a stable id or the
	// operator-typed target the observation reported for it.
	Target string
	// Field is the option or attribute being written.
	Field string
	// Before and After are the observed and desired values.
	Before, After string
	// Args is the exact tmux argv without a transport prefix.
	Args []string
}

const reasonUnobserved = "planned write names a tmux handle the exact-socket observation does not contain"

// executableVerbs is the closed set of tmux verbs this kernel may run.
//
// It is a denylist inverted into an allowlist because the interesting failure is
// the one nobody wrote a test for. Convergence needs to set an option and to
// rename a window; it never needs to create a session, split a pane, or kill
// anything. Enumerating the two verbs it does need makes "the controller started
// something" impossible to reach by adding a candidate, rather than merely
// unlikely.
var executableVerbs = []string{"rename-window", "set-option"}

var reasonForbiddenVerb = "planned write carries a tmux verb outside the convergence set " +
	strings.Join(executableVerbs, "/")

// Authorize resolves every candidate against the observation, applies the
// policy, and returns the actions with the policy rows they exercised.
//
// Nothing is dropped. A candidate the policy denies becomes an action carrying
// its refusal, because an operator who asked what happened to a drift item is
// owed an answer, and silence is the one answer that cannot be distinguished
// from a bug.
func Authorize(handles Handles, fields GuardFields, grant Grant, candidates []Candidate) ([]Action, []Verdict) {
	actions := make([]Action, 0, len(candidates))
	seen := map[string]bool{}
	var policy []Verdict
	for _, candidate := range candidates {
		action := Action{
			Key: candidate.Key, Surface: SurfaceTmux, Intent: candidate.Intent, Kind: candidate.Kind,
			Target: strings.TrimSpace(candidate.Target), Field: candidate.Field,
			Before: candidate.Before, After: candidate.After, Args: slices.Clone(candidate.Args),
		}
		handle, observed := handles.Lookup(candidate.Target)
		if !observed {
			action.Authority, action.Reason = AuthorityRefuse, reasonUnobserved
			actions = append(actions, action)
			continue
		}
		// The handle's stable id replaces whatever spelling the producer used.
		// Executing against `projmux:2` would re-resolve the index at write
		// time, which is exactly the recycled-handle hazard the guards exist to
		// close.
		action.Target, action.Class, action.Scope = handle.ID, handle.Class, handle.Kind
		verdict := Decide(candidate.Intent, handle.Subject(grant))
		action.Authority, action.Reason = verdict.Authority, verdict.Reason
		if key := verdictKey(verdict); !seen[key] {
			seen[key] = true
			policy = append(policy, verdict)
		}
		if verdict.Allowed() {
			if verb, ok := executableVerb(candidate.Args); !ok {
				action.Authority = AuthorityRefuse
				action.Reason = fmt.Sprintf("%s: %s", reasonForbiddenVerb, verb)
				actions = append(actions, action)
				continue
			}
			action.Guards = handle.Guards(fields)
		}
		actions = append(actions, action)
	}
	return actions, policy
}

func executableVerb(args []string) (string, bool) {
	verb := ""
	if len(args) > 0 {
		verb = strings.TrimSpace(args[0])
	}
	if verb == "" {
		return "<empty>", false
	}
	return verb, slices.Contains(executableVerbs, verb)
}

func verdictKey(verdict Verdict) string {
	return string(verdict.Intent) + "\x00" + string(verdict.Class)
}

// Exercised reports the policy rows this graph actually puts in play,
// independent of any candidate.
//
// A report that listed only the classes a write happened to touch would say
// nothing about the objects the kernel deliberately left alone, and "the
// controller did not start the offline Project" is precisely the claim an
// operator needs evidence for. So every observed class contributes its import
// and delete refusals, and a Registry row with no live handle contributes the
// start refusal that is the reason it stayed offline.
func Exercised(handles Handles, grant Grant, offlineRows bool) []Verdict {
	seen := map[string]bool{}
	var out []Verdict
	add := func(verdict Verdict) {
		key := verdictKey(verdict)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, verdict)
	}
	if offlineRows {
		add(Decide(IntentStart, Subject{Class: resourcegraph.ClassManaged, Grant: grant}))
	}
	for _, id := range handles.IDs() {
		subject := handles.byID[id].Subject(grant)
		add(Decide(IntentImport, subject))
		add(Decide(IntentDelete, subject))
	}
	SortVerdicts(out)
	return out
}
