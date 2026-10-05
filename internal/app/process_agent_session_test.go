package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func sessionBindingFixture(t *testing.T, provider string) (*intmetadata.Store, processhost.Binding) {
	t.Helper()
	reg, agent, paneUID := processDeleteFixture(t, provider)
	pane, _ := reg.Pane(paneUID)
	binding := processSchemaBinding(pane.Status.ProcessSession.Binding)
	pane.Status.ProcessSession.ConnectionID = binding.Operation
	if provider == aiModeClaude {
		pane.Status.ProcessSession.SessionID = "session"
	} else {
		pane.Status.ProcessSession.ThreadID = "thread"
	}
	a, _ := reg.Agent(agent)
	a.Status.SessionRef = nil
	store := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
	if _, _, err := store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	return store, binding
}

func TestProcessSessionBindingCommitAndBackfill(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			store, b := sessionBindingFixture(t, provider)
			noMutation := func(*coremetadata.Registry) error { return nil }
			// A failed mutation cannot commit a ref or append history.
			before, _ := os.ReadFile(store.Path())
			if err := updateProcessAgentSession(store.Path(), b, nil, true, true, nil, func(*coremetadata.Registry) error { return errors.New("rollback") }); err == nil {
				t.Fatal("failed mutation accepted")
			}
			after, _ := os.ReadFile(store.Path())
			if !bytes.Equal(before, after) {
				t.Fatal("failed transaction changed Registry")
			}
			state := filepath.Dir(filepath.Dir(store.Path()))
			if _, err := os.Stat(sessionhistory.Path(state)); !os.IsNotExist(err) {
				t.Fatal("failed transaction appended history", err)
			}
			if err := updateProcessAgentSession(store.Path(), b, nil, true, true, nil, noMutation); err != nil {
				t.Fatal(err)
			}
			reg, _ := store.LoadReadOnly()
			agent, _ := reg.Agent(b.Agent)
			if agent.Status.SessionRef == nil || agent.Status.SessionRef.Provider != provider {
				t.Fatal("missing ref", agent)
			}
			ref := agent.Status.SessionRef.Clone()
			history, err := sessionhistory.Read(state, b.Agent)
			if err != nil || len(history.Records) != 1 {
				t.Fatal("missing history", history, err)
			}
			requireRowAffiliation(t, &reg, history.Records[0])
			historyBytes, _ := os.ReadFile(sessionhistory.Path(state))
			if err := updateProcessAgentSession(store.Path(), b, nil, false, true, nil, noMutation); err != nil {
				t.Fatal(err)
			}
			reg, _ = store.LoadReadOnly()
			agent, _ = reg.Agent(b.Agent)
			afterHistory, _ := os.ReadFile(sessionhistory.Path(state))
			if !reflect.DeepEqual(ref, agent.Status.SessionRef) || !bytes.Equal(historyBytes, afterHistory) {
				t.Fatal("same conversation rewrote ref/history")
			}
		})
	}
}

func TestProcessSnapshotAndWaitBackfillLegacyAgent(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, writer := range []string{"snapshot", "wait"} {
			t.Run(provider+"/"+writer, func(t *testing.T) {
				store, b := sessionBindingFixture(t, provider)
				reg, _ := store.LoadReadOnly()
				pane, _ := reg.Pane(b.Pane)
				conversation := pane.Status.ProcessSession.SessionID
				if provider == aiModeCodex {
					conversation = pane.Status.ProcessSession.ThreadID
				}
				result := processAgentCreateResult{Binding: b, Provider: provider, registryPath: store.Path()}
				snap := processhost.Snapshot{Binding: b, Provider: provider, State: "ready", Session: conversation, Connection: b.Operation}
				var err error
				if writer == "snapshot" {
					err = result.recordProcessSnapshot(snap)
				} else {
					snap.State = "exited"
					snap.Exit = &processhost.Exit{Code: 0}
					err = result.persistProcessWait(snap)
				}
				if err != nil {
					t.Fatal(err)
				}
				reg, _ = store.LoadReadOnly()
				agent, _ := reg.Agent(b.Agent)
				if agent.Status.SessionRef == nil {
					t.Fatal("writer did not backfill")
				}
				history, err := sessionhistory.Read(filepath.Dir(filepath.Dir(store.Path())), b.Agent)
				if err != nil || len(history.Records) != 1 {
					t.Fatal(history, err)
				}
			})
		}
	}
}

func TestProcessSessionBindingCannotRepairTmuxOrForeignGeneration(t *testing.T) {
	for _, wrong := range []string{"tmux", "generation", "unconfirmed"} {
		t.Run(wrong, func(t *testing.T) {
			store, b := sessionBindingFixture(t, aiModeClaude)
			if wrong == "generation" {
				b.Generation = "foreign"
			}
			if wrong == "tmux" || wrong == "unconfirmed" {
				_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					p, _ := reg.Pane(b.Pane)
					if wrong == "tmux" {
						p.Spec.Runtime.Kind = coremetadata.RuntimeTmux
						p.Status.Activation = coremetadata.PaneActivation{}
						p.Status.ProcessSession = nil
					} else {
						p.Status.ProcessSession.SessionID = ""
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			err := updateProcessAgentSession(store.Path(), b, nil, true, true, nil, func(*coremetadata.Registry) error { return nil })
			if wrong != "unconfirmed" && err == nil {
				t.Fatal("foreign binding accepted")
			}
			reg, _ := store.LoadReadOnly()
			a, _ := reg.Agent(b.Agent)
			if a.Status.SessionRef != nil {
				t.Fatal("unconfirmed or foreign ref recorded")
			}
		})
	}
}

func TestProcessResumeReservationBackfillsLegacyAgent(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			store, b := sessionBindingFixture(t, provider)
			_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error { recordFixtureWait(t, reg, b.Pane); return nil })
			if err != nil {
				t.Fatal(err)
			}
			reg, _ := store.LoadReadOnly()
			candidates := listResumableProcessAgents(reg, processResumeFilter{})
			if len(candidates) != 1 {
				t.Fatalf("candidate %+v", candidates)
			}
			state := filepath.Dir(filepath.Dir(store.Path()))
			resource := &resourceStore{stateDir: func() (string, error) { return state, nil }, mutator: intmetadata.DefaultMutator, update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
				r, _, err := store.UpdateConvergent(fn)
				return r, err
			}}
			command := &agentCommand{rebind: &agentRebinder{create: &createCommand{store: resource}}}
			next := b
			next.Host = "new-host"
			next.Generation = "new-gen"
			next.Operation = "new-op"
			if err := command.reserveProcessResume(context.Background(), candidates[0], agentSettingsLaunch{}, next); err != nil {
				t.Fatal(err)
			}
			reg, _ = store.LoadReadOnly()
			a, _ := reg.Agent(b.Agent)
			if a.Status.SessionRef == nil {
				t.Fatal("resume reservation did not backfill")
			}
			history, err := sessionhistory.Read(state, b.Agent)
			if err != nil || len(history.Records) != 1 {
				t.Fatal(history, err)
			}
		})
	}
}

func TestTmuxResumeKeepsSessionRefAndHistoryBytes(t *testing.T) {
	store := newFakeResourceStore(t)
	ref := resumeFixtureRef(resourceFixtureClock)
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	command, launcher, _, _ := newTestAgentResumeCommand(t, store, newFakeTmux())
	enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)
	state := t.TempDir()
	command.rebind.create.store.stateDir = func() (string, error) { return state, nil }
	agent, _ := store.registry.Agent("agt-beta-codex")
	row, ok := sessionhistory.ObservedRecordFor(&store.registry, agent.Metadata.UID, agent.Status.SessionRef)
	if !ok {
		t.Fatal("no tmux row")
	}
	if err := sessionhistory.Append(state, row); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(agent.Status.SessionRef)
	beforeHistory, _ := os.ReadFile(sessionhistory.Path(state))
	stdout, stderr, err := runRoute(t, command, "resume", "codex", "--project", "beta")
	if err != nil || stdout != "agent/codex resumed\n" || stderr != "" {
		t.Fatalf("tmux resume bytes: %q %q %v", stdout, stderr, err)
	}
	agent, _ = store.registry.Agent("agt-beta-codex")
	after, _ := json.Marshal(agent.Status.SessionRef)
	afterHistory, _ := os.ReadFile(sessionhistory.Path(state))
	if !bytes.Equal(before, after) || !bytes.Equal(beforeHistory, afterHistory) {
		t.Fatalf("tmux resume changed ref/history: %s => %s", before, after)
	}
}

func TestProcessInitialBindingIsAtomicWithConfirmedConversation(t *testing.T) {
	store, b := sessionBindingFixture(t, aiModeClaude)
	_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		p, _ := reg.Pane(b.Pane)
		p.Status.ProcessSession.SessionID = ""
		p.Status.ProcessSession.ConnectionID = ""
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	creator := &createCommand{}
	if err := creator.processCreateTransactions(store.Path()).Commit(context.Background(), b, "verified-session"); err != nil {
		t.Fatal(err)
	}
	reg, _ := store.LoadReadOnly()
	p, _ := reg.Pane(b.Pane)
	a, _ := reg.Agent(b.Agent)
	if p.Status.ProcessSession.SessionID != "verified-session" || a.Status.SessionRef == nil || a.Status.SessionRef.Claude.SessionID != p.Status.ProcessSession.SessionID {
		t.Fatal("init did not bind both atomically")
	}
}

func TestProcessWaitBeforeResumeInitPreservesEstablishedConversation(t *testing.T) {
	store, b := sessionBindingFixture(t, aiModeClaude)
	_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		p, _ := reg.Pane(b.Pane)
		old := p.Status.ProcessSession.Binding
		old.Generation = "prior-gen"
		old.OperationID = "prior-op"
		p.Status.ProcessSession.History = &coremetadata.ProcessResumeHistory{Binding: old, SessionID: p.Status.ProcessSession.SessionID}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := processAgentCreateResult{Binding: b, Provider: aiModeClaude, registryPath: store.Path()}
	if err := owner.persistProcessWait(processhost.Snapshot{Binding: b, Provider: aiModeClaude, State: "exited", Exit: &processhost.Exit{Code: 1}}); err != nil {
		t.Fatal(err)
	}
	reg, _ := store.LoadReadOnly()
	a, _ := reg.Agent(b.Agent)
	p, _ := reg.Pane(b.Pane)
	if a.Status.SessionRef == nil || a.Status.SessionRef.Claude.SessionID != "session" || p.Status.ProcessSession.SessionID != "session" || p.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable {
		t.Fatal("failed resume discarded established conversation", a.Status.SessionRef, p.Status.ProcessSession)
	}
}

func TestTmuxCreateSessionRefAndHistoryBytes(t *testing.T) {
	create, store, _, _, _ := newCodexPersonaCreate(t)
	state := t.TempDir()
	create.store.stateDir = func() (string, error) { return state, nil }
	_, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "review this")
	if err != nil || stderr != "" {
		t.Fatal(err, stderr)
	}
	a := agentNamed(t, store, "win-alpha-main", "agent-test-1")
	ref, _ := json.Marshal(a.Status.SessionRef)
	history, err := os.ReadFile(sessionhistory.Path(state))
	if err != nil {
		t.Fatal(err)
	}
	wantRef := `{"provider":"codex","observedAt":"2026-08-15T09:00:00Z","codex":{"threadId":"thread-codex-persona","hasStartedTurn":true,"endpoint":{"stateDomainID":"test-domain","endpointGenerationID":"generation-current"},"lifecycle":{"state":"current"}}}`
	wantHistory := "\n" + `{"agentUID":"agent-test-1","provider":"codex","sessionId":"thread-codex-persona","transcriptPath":"","observedAt":"2026-08-15T09:00:00Z","source":"observed","projectUID":"prj-alpha","windowUID":"win-alpha-main","agentName":"agent-test-1"}` + "\n"
	if string(ref) != wantRef || string(history) != wantHistory {
		t.Fatalf("tmux create bytes changed: %s %q", ref, history)
	}
}
