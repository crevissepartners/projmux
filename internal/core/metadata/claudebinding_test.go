package metadata

import (
	"reflect"
	"testing"
)

// The helper's Begin is a CAS on the registrationGeneration its SessionStart
// hook observed, so a helper started for an older SessionStart can never fence
// out, or overwrite, a newer claim that committed first.
func TestBeginClaudeRegistrationAfterIsCAS(t *testing.T) {
	t.Parallel()
	reg, m, a, p, current := claudeRouteFixture(t)
	prior := current.Authority.RegistrationGeneration
	newer := current.Authority
	newer.SessionID = "newer-session"
	newer.RegistrationGeneration = "registration-2"
	newer.LeaseProcess.Start = "test:newer-helper"

	if err := m.BeginClaudeRegistrationAfter(&reg, p, a, "activation-1", prior, newer); err != nil {
		t.Fatalf("Begin after the observed generation: %v", err)
	}
	pane, _ := reg.Pane(p)
	if claude := pane.Status.Activation.Claude; claude.RegistrationGeneration != newer.RegistrationGeneration ||
		claude.RegistrationSessionID != newer.SessionID || claude.Registration != nil {
		t.Fatalf("Begin after the observed generation left %#v", claude)
	}

	before := reg.Clone()
	if err := m.BeginClaudeRegistrationAfter(&reg, p, a, "activation-1", prior, newer); err != nil || !reflect.DeepEqual(before, reg) {
		t.Fatalf("repeated Begin of the helper's own generation = %v, changed=%v", err, !reflect.DeepEqual(before, reg))
	}
	if err := m.RecordClaudeRegistration(&reg, p, a, "activation-1", ClaudeRegistration{Authority: newer}); err != nil {
		t.Fatal(err)
	}
	before = reg.Clone()
	if err := m.BeginClaudeRegistrationAfter(&reg, p, a, "activation-1", prior, newer); err != nil || !reflect.DeepEqual(before, reg) {
		t.Fatalf("Begin of the helper's own Ready generation = %v, changed=%v", err, !reflect.DeepEqual(before, reg))
	}

	stale := current.Authority
	stale.SessionID = "stale-session"
	stale.RegistrationGeneration = "registration-3"
	for _, observed := range []string{prior, "", "registration-unknown"} {
		before = reg.Clone()
		if err := m.BeginClaudeRegistrationAfter(&reg, p, a, "activation-1", observed, stale); err == nil || !reflect.DeepEqual(before, reg) {
			t.Fatalf("stale Begin observed %q = %v, changed=%v", observed, err, !reflect.DeepEqual(before, reg))
		}
	}

	before = reg.Clone()
	next := stale
	next.RegistrationGeneration = "registration-4"
	if err := m.BeginClaudeRegistrationAfter(&reg, p, a, "activation-2", newer.RegistrationGeneration, next); err == nil || !reflect.DeepEqual(before, reg) {
		t.Fatal("Begin for another activation wrote Registry")
	}
	next.Process.Start = "test:other-provider"
	if err := m.BeginClaudeRegistrationAfter(&reg, p, a, "activation-1", newer.RegistrationGeneration, next); err == nil || !reflect.DeepEqual(before, reg) {
		t.Fatal("Begin for another provider process wrote Registry")
	}
}
