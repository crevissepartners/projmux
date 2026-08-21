package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type rootOperationContract struct {
	Verb           string
	Subject        string
	Project        string
	ControlSession string
	Intentional    bool
}

// rootOperationContracts is the closed verb x root-kind table for Phase 14.
// A row is a route already present in the public or lifecycle surface; this
// slice adds no verb. Intentional marks the two root-kind differences whose
// Project policy must not be copied onto ControlSession.
var rootOperationContracts = []rootOperationContract{
	{Verb: "rename", Subject: "project", Project: "exact Project resource", ControlSession: "not applicable: no public ControlSession verb", Intentional: true},
	{Verb: "rename", Subject: "window", Project: "active Project owner scope", ControlSession: "active ControlSession owner scope"},
	{Verb: "rename", Subject: "pane", Project: "active Project owner scope", ControlSession: "active ControlSession owner scope"},
	{Verb: "rename", Subject: "agent", Project: "active Project owner scope", ControlSession: "active ControlSession owner scope"},
	{Verb: "delete", Subject: "window", Project: "exact Project owner/session", ControlSession: "exact ControlSession owner/session"},
	{Verb: "delete", Subject: "pane", Project: "exact Project owner chain/session", ControlSession: "exact ControlSession owner chain/session"},
	{Verb: "delete", Subject: "agent", Project: "exact Project owner chain/session", ControlSession: "exact ControlSession owner chain/session"},
	{Verb: "focus", Subject: "project", Project: "exact live Project session", ControlSession: "not applicable: no public ControlSession verb", Intentional: true},
	{Verb: "focus", Subject: "window", Project: "exact live root session/window", ControlSession: "exact live root session/window"},
	{Verb: "focus", Subject: "pane", Project: "exact live root session/window/pane", ControlSession: "exact live root session/window/pane"},
	{Verb: "attention", Subject: "pane", Project: "exact live Pane runtime", ControlSession: "exact live Pane runtime"},
	{Verb: "attention", Subject: "window", Project: "exact live Window runtime", ControlSession: "exact live Window runtime"},
	{Verb: "reconcile", Subject: "resources", Project: "classify exact Project owner chain", ControlSession: "classify exact ControlSession owner chain"},
	{Verb: "termination", Subject: "projection", Project: "retain Project owner chain", ControlSession: "retain ControlSession owner chain"},
}

func TestControlRootOperationTableIsClosedAndPrintable(t *testing.T) {
	want := []string{
		"attention/pane", "attention/window",
		"delete/agent", "delete/pane", "delete/window",
		"focus/pane", "focus/project", "focus/window",
		"reconcile/resources",
		"rename/agent", "rename/pane", "rename/project", "rename/window",
		"termination/projection",
	}
	var got []string
	var table strings.Builder
	fmt.Fprintln(&table, "VERB         SUBJECT       PROJECT                              CONTROLSESSION")
	for _, row := range rootOperationContracts {
		key := row.Verb + "/" + row.Subject
		got = append(got, key)
		if strings.TrimSpace(row.Project) == "" || strings.TrimSpace(row.ControlSession) == "" {
			t.Errorf("%s has an unclassified root-kind cell: %+v", key, row)
		}
		controlUnavailable := strings.HasPrefix(row.ControlSession, "not applicable")
		if controlUnavailable != row.Intentional {
			t.Errorf("%s intentional difference = %t, ControlSession cell %q", key, row.Intentional, row.ControlSession)
		}
		fmt.Fprintf(&table, "%-12s %-13s %-36s %s\n", row.Verb, row.Subject, row.Project, row.ControlSession)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("operation inventory drifted\ngot  %v\nwant %v", got, want)
	}
	t.Logf("Phase 14 verb x root kind contract\n%s", table.String())
}

func TestRenameGenericDescendantsUseExactManagedRootNamespace(t *testing.T) {
	for _, row := range []struct {
		kind string
		ref  string
		uid  string
		name string
	}{
		{kind: "window", ref: "home", uid: "win-home", name: "control-window"},
		{kind: "pane", ref: "shell", uid: "pan-home-shell", name: "control-shell"},
		{kind: "agent", ref: "codex", uid: "agt-home", name: "control-agent"},
	} {
		t.Run(row.kind, func(t *testing.T) {
			store := newFakeResourceStore(t)
			addControlReadRoot(t, store)
			active := insideTmux("pan-home-shell", "win-home")
			stdout, stderr, err := runRoute(t, newTestRenameCommandWithActiveTarget(store, active),
				row.kind, row.ref, "--name", row.name)
			if err != nil || stderr != "" {
				t.Fatalf("rename control-owned %s: stdout=%q stderr=%q err=%v", row.kind, stdout, stderr, err)
			}
			_, meta, ok := resourceFor(store.registry, resourceKindTokens[row.kind], row.uid)
			if !ok || meta.Name != row.name || active.calls != 1 || store.writes != 1 {
				t.Fatalf("rename control-owned %s outcome: meta=%+v ok=%t calls=%d writes=%d",
					row.kind, meta, ok, active.calls, store.writes)
			}
		})
	}
}

func TestDeleteGenericDescendantsAcceptControlSessionOwnerChain(t *testing.T) {
	for _, row := range []struct {
		kind string
		uid  string
	}{
		{kind: "window", uid: "win-home"},
		{kind: "pane", uid: "pan-home-shell"},
		{kind: "agent", uid: "agt-home"},
	} {
		t.Run(row.kind, func(t *testing.T) {
			store := newFakeResourceStore(t)
			addControlReadRoot(t, store)
			cmd := newTestDeleteCommand(store, false, false, nil)
			stdout, stderr, err := runRoute(t, cmd, row.kind, "uid:"+row.uid, "--yes")
			if err != nil || stderr != "" {
				t.Fatalf("delete control-owned %s: stdout=%q stderr=%q err=%v", row.kind, stdout, stderr, err)
			}
			if _, _, ok := resourceFor(store.registry, resourceKindTokens[row.kind], row.uid); ok {
				t.Fatalf("delete control-owned %s left uid %s behind", row.kind, row.uid)
			}
			if _, ok := store.registry.ControlSession("ctl-home"); !ok {
				t.Fatalf("delete control-owned %s removed its ControlSession root", row.kind)
			}
			if err := store.registry.Validate(); err != nil {
				t.Fatalf("delete control-owned %s left invalid Registry: %v", row.kind, err)
			}
			if row.kind == "window" && !strings.Contains(stdout, "ControlSession session home") {
				t.Fatalf("delete control-owned Window lost root-kind impact: %q", stdout)
			}
		})
	}
}

func TestTerminationProjectionRetainsControlSessionOwnerChain(t *testing.T) {
	store := newFakeResourceStore(t)
	addControlReadRoot(t, store)
	mutator := fixtureMutator()
	if _, err := mutator.RecordPaneActivation(&store.registry, "pan-home-agent", coremetadata.PaneActivationOptions{
		Generation: "gen-control", RuntimeID: "%41", AgentUID: "agt-home", OperationID: "op-control",
	}); err != nil {
		t.Fatalf("record control-owned activation: %v", err)
	}
	controlBefore, _ := store.registry.ControlSession("ctl-home")
	windowBefore, _ := store.registry.Window("win-home")
	paneBefore, _ := store.registry.Pane("pan-home-agent")

	projection, err := mutator.ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{
		PaneUID: "pan-home-agent", Generation: "gen-control",
	})
	if err != nil || !projection.Changed || projection.AgentUID != "agt-home" || !projection.PaneRetained {
		t.Fatalf("control-owned termination projection = %+v err=%v", projection, err)
	}
	controlAfter, controlOK := store.registry.ControlSession("ctl-home")
	windowAfter, windowOK := store.registry.Window("win-home")
	paneAfter, paneOK := store.registry.Pane("pan-home-agent")
	if !controlOK || !windowOK || !paneOK || controlAfter.Metadata.UID != controlBefore.Metadata.UID ||
		windowAfter.Metadata.OwnerRef == nil || *windowAfter.Metadata.OwnerRef != *windowBefore.Metadata.OwnerRef ||
		paneAfter.Metadata.OwnerRef == nil || *paneAfter.Metadata.OwnerRef != *paneBefore.Metadata.OwnerRef {
		t.Fatalf("termination lost ControlSession owner chain: control=%+v window=%+v pane=%+v",
			controlAfter, windowAfter, paneAfter)
	}
}
