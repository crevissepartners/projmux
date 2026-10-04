package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func processResumeQueryFixture(t *testing.T) coremetadata.Registry {
	t.Helper()
	h := newSessionRefHarness(t, "claude")
	pane, _ := h.registry.Pane(h.paneUID)
	agent, _ := h.registry.Agent(h.agentUID)
	window, _ := h.registry.Window(agent.Metadata.OwnerUID())
	b := coremetadata.ProcessBinding{HostInstanceID: "host", ProjectUID: window.Metadata.OwnerUID(), WindowUID: window.Metadata.UID, AgentUID: h.agentUID, PaneUID: h.paneUID, Generation: "retired", OperationID: "connection"}
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{}
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: "claude", Binding: b, SessionID: "session", ConnectionID: "connection", TurnID: "interrupted", ResumeState: coremetadata.ProcessResumable, Pending: []coremetadata.ProcessRecordedControl{{ID: "question", Kind: "question", ConnectionID: "connection", SessionID: "session", TurnID: "interrupted"}}}
	agent.Status.Phase = coremetadata.PhaseOffline
	agent.Metadata.Labels = map[string]string{"team": "fixture"}
	if err := h.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return h.registry.Clone()
}

func TestProcessSupportedResumableQueryFiltersAndClones(t *testing.T) {
	reg := processResumeQueryFixture(t)
	before := reg.Clone()
	got := listResumableProcessAgents(reg, processResumeFilter{})
	if len(got) != 1 || got[0].Previous.InterruptedTurn != "interrupted" || len(got[0].Previous.Expired) != 1 {
		t.Fatalf("candidates: %+v", got)
	}
	b := got[0].Record.Binding
	for _, f := range []processResumeFilter{{Project: b.ProjectUID}, {Window: b.WindowUID}, {Labels: map[string]string{"team": "fixture"}}} {
		if len(listResumableProcessAgents(reg, f)) != 1 {
			t.Fatal("matching filter rejected", f)
		}
	}
	for _, f := range []processResumeFilter{{Project: "other"}, {Window: "other"}, {Labels: map[string]string{"team": "other"}}, {Labels: map[string]string{"missing": ""}}} {
		if len(listResumableProcessAgents(reg, f)) != 0 {
			t.Fatal("foreign filter admitted", f)
		}
	}
	got[0].Record.Pending[0].ID = "changed"
	got[0].Previous.Expired[0].ID = "changed"
	got[0].Pane.Status.ProcessSession.Pending[0].ID = "changed"
	got[0].Agent.Metadata.Labels["team"] = "changed"
	if !reflect.DeepEqual(reg, before) {
		t.Fatal("projection mutated registry")
	}
}

func TestProcessSupportedResumableRefusalTokens(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		alive       bool
		change      func(*coremetadata.Registry)
	}{
		{name: "eligible"},
		{name: "live owner", token: processResumeOwned, alive: true},
		{name: "unknown", token: processResumeNotResumable, change: func(r *coremetadata.Registry) {
			for i := range r.Panes {
				if s := r.Panes[i].Status.ProcessSession; s != nil {
					s.ResumeState = coremetadata.ProcessResumeUnknown
				}
			}
		}},
		{name: "running", token: processResumeRefused, change: func(r *coremetadata.Registry) {
			for i := range r.Agents {
				r.Agents[i].Status.Phase = coremetadata.PhaseRunning
			}
		}},
		{name: "invalid", token: processResumeRefused, change: func(r *coremetadata.Registry) { r.SchemaVersion = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := processResumeQueryFixture(t)
			uid := listResumableProcessAgents(reg, processResumeFilter{})[0].Agent.Metadata.UID
			if tc.change != nil {
				tc.change(&reg)
			}
			before := reg.Clone()
			err := processResumeRefusal(reg, uid, tc.alive)
			if tc.token == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, processhost.ErrResumeRefused) || !strings.HasPrefix(err.Error(), tc.token+":") {
				t.Fatalf("token %q: %v", tc.token, err)
			}
			if !reflect.DeepEqual(reg, before) {
				t.Fatal("predicate wrote registry")
			}
		})
	}
}
