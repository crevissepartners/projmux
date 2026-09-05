package codexhandover

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

// mutableRegistry is the writable snapshot seam the rolling coordinator needs
// to fire a handover request. The handover coordinator only reads it.
type mutableRegistry struct{ registry metadata.Registry }

func (store *mutableRegistry) LoadSnapshot() (metadata.Registry, error) {
	return store.registry.Clone(), nil
}

func (store *mutableRegistry) WithAdmissionBarrier(fn func(metadata.Registry) error) error {
	return fn(store.registry.Clone())
}

func (store *mutableRegistry) UpdateConvergent(fn func(*metadata.Registry) error) (metadata.Registry, bool, error) {
	working := store.registry.Clone()
	if err := fn(&working); err != nil {
		return metadata.Registry{}, false, err
	}
	changed := !reflect.DeepEqual(working, store.registry)
	if changed {
		store.registry = working
	}
	return store.registry.Clone(), changed, nil
}

type vacancyFixture struct {
	coordinator *Coordinator
	request     Request
	effects     *recordingEffects
	store       *codexupgrade.Store
	registry    *mutableRegistry
	domainPath  string
	old         metadata.CodexEndpointRef
	successor   metadata.CodexEndpointRef
}

type vacancyOptions struct {
	// owner of the retiring generation. The machine that motivated this slice
	// has an unmanaged old generation, so that is the default.
	owner codexgeneration.OwnerClass
	// staleObligations are the durable journal rows left by the last drain
	// publication. Their Agents no longer exist, which is why they can never be
	// re-projected or traced.
	staleObligations []codexgeneration.ObligationState
	// domainThreads are thread ids the shared state domain holds.
	domainThreads []string
	// noThreadStore omits the shared thread directory entirely.
	noThreadStore bool
	// qualified writes a Phase-2-ready version-pair receipt into the journal.
	qualified bool
	// requested fires the rolling handover request up front.
	requested bool
	// mutate is the last chance to shape the Registry snapshot.
	mutate func(*metadata.Registry, metadata.CodexEndpointRef)
}

func newVacancyFixture(t *testing.T, options vacancyOptions) *vacancyFixture {
	t.Helper()
	if options.owner == "" {
		options.owner = codexgeneration.OwnerUnmanaged
	}
	domain, oldID, newID := "domain-one", "generation-old", "generation-new"
	old := metadata.CodexEndpointRef{StateDomainID: domain, EndpointGenerationID: oldID}
	successor := metadata.CodexEndpointRef{StateDomainID: domain, EndpointGenerationID: newID}

	domainPath := t.TempDir()
	if !options.noThreadStore {
		threads := options.domainThreads
		if threads == nil {
			threads = []string{"01a071d3-1fbf-72a0-af54-7087c184e0b2"}
		}
		writeStateDomainThreads(t, domainPath, threads...)
	}

	var stale []codexgeneration.AgentObligation
	for index, state := range options.staleObligations {
		stale = append(stale, codexgeneration.AgentObligation{
			AgentUID: fmt.Sprintf("ghost-%d", index), EndpointGenerationID: oldID, State: state,
		})
	}

	rolling, err := codexgeneration.NewRollingUpgradeOperation("upgrade-one", domain, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	rolling, _, _ = rolling.RecordCandidateLaunchIntent()
	rolling, _, _ = rolling.RecordCandidateStart()
	rolling, _, _ = rolling.RecordAction(codexgeneration.RollingActionPrepareCandidate, nil)
	rolling, _, _ = rolling.RecordAction(codexgeneration.RollingActionCommitAdmission, nil)
	ledger, ledgerErr := codexgeneration.ProjectDrainLedger(oldID, stale)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	rolling, _, _ = rolling.RecordAction(codexgeneration.RollingActionPublishDrain, ledger)
	if options.requested {
		rolling, _, _ = rolling.RequestGenerationHandover()
	}

	journal := codexupgrade.Journal{
		Version: codexupgrade.JournalVersion, StateDomainID: domain, CurrentGenerationID: newID,
		Routes: []codexupgrade.GenerationRoute{
			vacancyRoute(domain, old, codexgeneration.StateDraining, options.owner, "old", domainPath),
			vacancyRoute(domain, successor, codexgeneration.StateCurrent, codexgeneration.OwnerProjmuxPrivate, "new", domainPath),
		},
		Obligations: stale, Operation: &rolling,
	}
	if options.qualified {
		qualification := codexgeneration.EvaluateQualification(
			codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.0"},
			codexgeneration.QualificationEvidence{SharedStateDomain: true, DistinctPrivateEndpoints: true,
				DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true,
				OldStoppedBeforeResume: true, PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true,
				BundleSourceRemovalLaunch: true, BundleDriftRefused: true, ProtocolMismatchRefused: true})
		journal.Qualification = &qualification
	}

	registry := &mutableRegistry{}
	if options.mutate != nil {
		options.mutate(&registry.registry, old)
	}

	store := codexupgrade.NewStateStore(t.TempDir())
	if _, err := store.Update(context.Background(), func(current *codexupgrade.Journal, _ bool) error {
		*current = journal
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	effects := &recordingEffects{}
	coordinator := &Coordinator{
		Journal: store, Registry: registry, Effects: effects,
		Requester:  &codexupgrade.Coordinator{Journal: store, Registry: registry},
		Observe:    func(context.Context, codexupgrade.GenerationRoute) error { return nil },
		CanRecover: func(codexupgrade.GenerationRoute) error { return nil },
	}
	return &vacancyFixture{coordinator: coordinator, request: Request{OperationRef: "handover-one", RollingOperationRef: "upgrade-one"},
		effects: effects, store: store, registry: registry, domainPath: domainPath, old: old, successor: successor}
}

func vacancyRoute(domain string, endpoint metadata.CodexEndpointRef, state codexgeneration.GenerationState,
	owner codexgeneration.OwnerClass, suffix, domainPath string,
) codexupgrade.GenerationRoute {
	generation := codexgeneration.Generation{Endpoint: endpoint, State: state, Owner: owner, BundleID: "bundle-" + suffix}
	if owner != codexgeneration.OwnerProjmuxPrivate {
		// An unmanaged route carries no configuration at all: the shared state
		// domain path has to come from a managed sibling.
		return codexupgrade.GenerationRoute{Generation: generation}
	}
	socket := "/run/" + suffix + "/codex-" + endpoint.EndpointGenerationID + ".sock"
	return codexupgrade.GenerationRoute{
		Generation: generation,
		Config: codexupgrade.GenerationConfig{Endpoint: endpoint, StateDomainPath: domainPath, PrivateRoot: "/run/" + suffix,
			SocketPath: socket, LeaseRoot: "/lease/" + suffix, RequiredProtocol: codexbundle.ProtocolRange{Min: 1, Max: 1}},
		TUIPath: "/lease/" + suffix + "/bin/codex", LaunchOperationRef: "launch-" + suffix, Ready: true,
		Proof: &codexgenerationhost.LaunchProof{
			Endpoint:          codexgenerationhost.EndpointIdentity{StateDomainID: domain, EndpointGenerationID: endpoint.EndpointGenerationID},
			EndpointRuntimeID: "runtime-" + suffix, SocketPath: socket, BundleID: "bundle-" + suffix},
	}
}

func writeStateDomainThreads(t *testing.T, domainPath string, threads ...string) {
	t.Helper()
	dir := filepath.Join(domainPath, "sessions", "2026", "09", "05")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, thread := range threads {
		name := filepath.Join(dir, "rollout-2026-09-05T22-47-36-"+thread+".jsonl")
		if err := os.WriteFile(name, []byte("{\"type\":\"session_meta\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func stateDomainThreadFiles(t *testing.T, domainPath string) []string {
	t.Helper()
	var files []string
	root := filepath.Join(domainPath, "sessions")
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// liveGenerationRoutes counts the routes still occupying a pool slot. Retired
// receipt rows do not occupy one.
func liveGenerationRoutes(journal codexupgrade.Journal) int {
	live := 0
	for _, route := range journal.Routes {
		if route.Generation.State != codexgeneration.StateRetired {
			live++
		}
	}
	return live
}

func boundAgent(registry *metadata.Registry, endpoint metadata.CodexEndpointRef, uid, threadID string, startedTurn bool) {
	agent := metadata.Agent{APIVersion: metadata.APIVersion, Kind: metadata.KindAgent,
		Metadata: metadata.ObjectMeta{UID: uid, Name: uid, OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window-one"}},
		Spec:     metadata.AgentSpec{Provider: "codex", Workspace: metadata.AgentWorkspace{CWD: "/work"}},
		Status: metadata.AgentStatus{Phase: metadata.PhaseRunning, PaneRef: uid + "-pane",
			Interaction: metadata.AgentInteraction{Kind: metadata.InteractionResponseComplete},
			SessionRef: &metadata.AgentSessionRef{Provider: "codex", ObservedAt: time.Unix(1, 0),
				Codex: &metadata.CodexSessionRef{ThreadID: threadID, HasStartedTurn: startedTurn, Endpoint: &endpoint}}}}
	registry.Agents = append(registry.Agents, agent)
}

func boundPane(registry *metadata.Registry, endpoint metadata.CodexEndpointRef, uid, threadID string) {
	registry.Panes = append(registry.Panes, metadata.Pane{APIVersion: metadata.APIVersion, Kind: metadata.KindPane,
		Metadata: metadata.ObjectMeta{UID: uid, Name: uid},
		Spec:     metadata.PaneSpec{Role: metadata.PaneRoleAgent, CWD: "/work"},
		Status: metadata.PaneStatus{Activation: metadata.PaneActivation{Generation: "pane-generation-" + uid, RuntimeID: "%1",
			Codex: &metadata.CodexActivationBinding{ThreadID: threadID, Authority: &metadata.CodexAuthorityRef{
				StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
				BrokerRuntimeID: "broker-one", ConnectionEpoch: 1, BindingEpoch: 1}}}}})
}

// TestVacantGenerationRetiresWithoutAVersionPairReceipt is the Phase acceptance
// fixture: an obligation-free draining generation reaches `retired`, its
// obligation rows disappear, the pool falls back to one live slot, and not one
// thread file in the shared state domain is touched.
func TestVacantGenerationRetiresWithoutAVersionPairReceipt(t *testing.T) {
	for _, owner := range []codexgeneration.OwnerClass{codexgeneration.OwnerUnmanaged, codexgeneration.OwnerProjmuxPrivate} {
		t.Run(string(owner), func(t *testing.T) {
			fixture := newVacancyFixture(t, vacancyOptions{owner: owner, staleObligations: []codexgeneration.ObligationState{
				codexgeneration.ObligationCompletedPersisted, codexgeneration.ObligationNoTurn,
				codexgeneration.ObligationActive, codexgeneration.ObligationUnknown,
			}})
			before := stateDomainThreadFiles(t, fixture.domainPath)
			if len(before) != 1 {
				t.Fatalf("state domain fixture files=%q", before)
			}

			plan := fixture.coordinator.Plan(context.Background(), fixture.request)
			if plan.Decision != DecisionRequestHandover || len(plan.Blockers) != 0 {
				t.Fatalf("plan=%+v", plan)
			}
			if plan.Vacancy != codexgeneration.VacancyVacant || plan.VacancyEvidence == nil {
				t.Fatalf("vacancy=%s evidence=%+v", plan.Vacancy, plan.VacancyEvidence)
			}
			if want := (codexgeneration.RetirementVacancyEvidence{ObligationsProjected: true, ThreadsEnumerated: true, EnumeratedThreads: 1}); *plan.VacancyEvidence != want {
				t.Fatalf("evidence=%+v want=%+v", *plan.VacancyEvidence, want)
			}
			if len(plan.Targets) != 0 || len(plan.Choices) != 0 {
				t.Fatalf("a vacant generation cannot produce a handover subject: %+v", plan)
			}

			request := fixture.request
			if owner != codexgeneration.OwnerProjmuxPrivate {
				request.OwnerStopReceipt = &codexgeneration.OwnerStopReceipt{ReceiptID: "owner-stop-one", Endpoint: fixture.old}
			}
			journal, err := fixture.coordinator.Apply(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete || !journal.Handover.Retired {
				t.Fatalf("handover=%+v", journal.Handover)
			}
			route, ok := journal.Route(fixture.old)
			if !ok || route.Generation.State != codexgeneration.StateRetired {
				t.Fatalf("old route=%+v ok=%t", route, ok)
			}
			if slices.ContainsFunc(journal.Obligations, func(obligation codexgeneration.AgentObligation) bool {
				return obligation.EndpointGenerationID == fixture.old.EndpointGenerationID
			}) {
				t.Fatalf("retired generation still carries obligations: %+v", journal.Obligations)
			}
			if live := liveGenerationRoutes(journal); live != 1 {
				t.Fatalf("live generation routes=%d want 1", live)
			}
			if err := journal.Validate(); err != nil {
				t.Fatalf("post-retirement journal is invalid: %v", err)
			}
			if after := stateDomainThreadFiles(t, fixture.domainPath); !reflect.DeepEqual(before, after) {
				t.Fatalf("state domain changed: before=%q after=%q", before, after)
			}
			if !slices.Contains(fixture.effects.calls, "handover-one:retire") {
				t.Fatalf("calls=%q", fixture.effects.calls)
			}
		})
	}
}

// TestVacantRetirementLeavesTheDrainingCapAndSlotArithmeticAlone holds the
// explicit non-goal: the freed slot is freed by a retirement receipt, not by a
// changed topology rule.
func TestVacantRetirementLeavesTheDrainingCapAndSlotArithmeticAlone(t *testing.T) {
	fixture := newVacancyFixture(t, vacancyOptions{})
	request := fixture.request
	request.OwnerStopReceipt = &codexgeneration.OwnerStopReceipt{ReceiptID: "owner-stop-one", Endpoint: fixture.old}
	journal, err := fixture.coordinator.Apply(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	pool := journal.Pool()
	if err := pool.Validate(); err != nil {
		t.Fatalf("retired pool must still validate: %v", err)
	}
	// A second draining generation is refused exactly as before.
	pool.Generations = append(pool.Generations, codexgeneration.Generation{
		Endpoint: metadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: "generation-third"},
		State:    codexgeneration.StateDraining, Owner: codexgeneration.OwnerUnmanaged, BundleID: "bundle-third"})
	pool.Generations = append(pool.Generations, codexgeneration.Generation{
		Endpoint: metadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: "generation-fourth"},
		State:    codexgeneration.StateDraining, Owner: codexgeneration.OwnerUnmanaged, BundleID: "bundle-fourth"})
	if refusal := codexgeneration.RefusalOf(pool.Validate()); refusal != codexgeneration.RefusalMultipleDraining {
		t.Fatalf("refusal=%s want=%s", refusal, codexgeneration.RefusalMultipleDraining)
	}
}

// TestOccupiedGenerationStaysFailClosed is acceptance criterion three: one
// thing left to hand over keeps every existing refusal in place.
func TestOccupiedGenerationStaysFailClosed(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		options vacancyOptions
		noStale bool
		want    codexgeneration.RetirementVacancy
	}{
		{
			name: "one live obligation",
			options: vacancyOptions{mutate: func(registry *metadata.Registry, old metadata.CodexEndpointRef) {
				boundAgent(registry, old, "agent-live", "01a071d3-1fbf-72a0-af54-7087c184e0b2", true)
			}},
			want: codexgeneration.VacancyLiveObligations,
		},
		{
			// The journal ledger is empty and the fresh projection is not. The
			// decision follows the projection, which is the whole point of
			// reading it at decision time.
			name: "stale journal is empty but a live Agent is still bound",
			options: vacancyOptions{mutate: func(registry *metadata.Registry, old metadata.CodexEndpointRef) {
				boundAgent(registry, old, "agent-live", "01a071d3-1fbf-72a0-af54-7087c184e0b2", true)
			}},
			noStale: true,
			want:    codexgeneration.VacancyLiveObligations,
		},
		{
			name: "an Agent bound with no ThreadID yields no obligation and still blocks",
			options: vacancyOptions{mutate: func(registry *metadata.Registry, old metadata.CodexEndpointRef) {
				boundAgent(registry, old, "agent-threadless", "", false)
			}},
			want: codexgeneration.VacancyEndpointBoundAgents,
		},
		{
			name: "a Pane activation outliving its Agent still blocks",
			options: vacancyOptions{mutate: func(registry *metadata.Registry, old metadata.CodexEndpointRef) {
				boundPane(registry, old, "pane-orphan", "01a071d3-1fbf-72a0-af54-7087c184e0b2")
			}},
			want: codexgeneration.VacancyEndpointBoundPanes,
		},
		{
			name:    "an unreadable shared state domain is never vacancy",
			options: vacancyOptions{noThreadStore: true},
			want:    codexgeneration.VacancyThreadsUnenumerated,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if !testCase.noStale {
				testCase.options.staleObligations = []codexgeneration.ObligationState{codexgeneration.ObligationCompletedPersisted}
			}
			fixture := newVacancyFixture(t, testCase.options)
			plan := fixture.coordinator.Plan(context.Background(), fixture.request)
			if plan.Decision != DecisionBlocked {
				t.Fatalf("plan=%+v", plan)
			}
			if plan.Vacancy != testCase.want {
				t.Fatalf("vacancy=%s want=%s evidence=%+v", plan.Vacancy, testCase.want, plan.VacancyEvidence)
			}
			// Both original refusals stand, unchanged.
			for _, blocker := range []string{"exact-rolling-handover-not-requested", "version-pair-not-qualified"} {
				if !slices.Contains(plan.Blockers, blocker) {
					t.Fatalf("blockers=%q missing %q", plan.Blockers, blocker)
				}
			}
			request := fixture.request
			request.OwnerStopReceipt = &codexgeneration.OwnerStopReceipt{ReceiptID: "owner-stop-one", Endpoint: fixture.old}
			if _, err := fixture.coordinator.Apply(context.Background(), request); err == nil {
				t.Fatal("apply must refuse an occupied generation")
			}
			journal, _, err := fixture.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if journal.Handover != nil || journal.Operation.HandoverRequested {
				t.Fatalf("a refused plan mutated the journal: handover=%+v operation=%+v", journal.Handover, journal.Operation)
			}
			route, _ := journal.Route(fixture.old)
			if route.Generation.State != codexgeneration.StateDraining {
				t.Fatalf("old route state=%s", route.Generation.State)
			}
			if len(fixture.effects.calls) != 0 {
				t.Fatalf("a refused plan produced effects: %q", fixture.effects.calls)
			}
		})
	}
}

// TestQualifiedGenerationDoesNotNeedAVacancyCensus keeps the ordinary rolling
// path free of the new read: a Phase-2-ready receipt plus a fired request is
// the same decision it always was, with no state-domain census at all.
func TestQualifiedGenerationDoesNotNeedAVacancyCensus(t *testing.T) {
	fixture := newVacancyFixture(t, vacancyOptions{qualified: true, requested: true, noThreadStore: true})
	fixture.coordinator.EnumerateThreads = func(string) ([]string, error) {
		t.Fatal("a qualified, requested handover must not census the state domain")
		return nil, nil
	}
	plan := fixture.coordinator.Plan(context.Background(), fixture.request)
	if plan.Decision != DecisionAwaitingOwnerStop || len(plan.Blockers) != 0 {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Vacancy != "" || plan.VacancyEvidence != nil {
		t.Fatalf("vacancy=%s evidence=%+v", plan.Vacancy, plan.VacancyEvidence)
	}
}

// TestVacantRetirementRefusesWithoutARequesterSeam keeps the request an
// explicit capability rather than something Apply improvises.
func TestVacantRetirementRefusesWithoutARequesterSeam(t *testing.T) {
	fixture := newVacancyFixture(t, vacancyOptions{staleObligations: []codexgeneration.ObligationState{codexgeneration.ObligationCompletedPersisted}})
	fixture.coordinator.Requester = nil
	request := fixture.request
	request.OwnerStopReceipt = &codexgeneration.OwnerStopReceipt{ReceiptID: "owner-stop-one", Endpoint: fixture.old}
	if _, err := fixture.coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("apply must refuse when it cannot fire the exact handover request")
	}
	journal, _, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if journal.Handover != nil || journal.Operation.HandoverRequested {
		t.Fatalf("journal mutated: handover=%+v operation=%+v", journal.Handover, journal.Operation)
	}
}

// TestVacantRetirementIsIdempotentAcrossEveryFailpoint holds the crash contract
// for the new entry: a request that already fired is observed, not repeated.
func TestVacantRetirementIsIdempotentAcrossEveryFailpoint(t *testing.T) {
	for _, failpoint := range []string{FailBeforePrewrite, FailAfterPrewrite, FailAfterIntent, FailAfterEffect, FailAfterReceipt} {
		t.Run(failpoint, func(t *testing.T) {
			fixture := newVacancyFixture(t, vacancyOptions{staleObligations: []codexgeneration.ObligationState{codexgeneration.ObligationCompletedPersisted}})
			request := fixture.request
			request.OwnerStopReceipt = &codexgeneration.OwnerStopReceipt{ReceiptID: "owner-stop-one", Endpoint: fixture.old}
			fired := false
			fixture.coordinator.Failpoint = func(point string) error {
				if point == failpoint && !fired {
					fired = true
					return errors.New("crash")
				}
				return nil
			}
			_, _ = fixture.coordinator.Apply(context.Background(), request)
			if !fired {
				t.Fatalf("failpoint %s never fired", failpoint)
			}
			fixture.coordinator.Failpoint = nil
			journal, err := fixture.coordinator.Apply(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
				t.Fatalf("handover=%+v", journal.Handover)
			}
			if got := countCall(fixture.effects.calls, "handover-one:retire"); got != 1 {
				t.Fatalf("retire effects=%d calls=%q", got, fixture.effects.calls)
			}
			if live := liveGenerationRoutes(journal); live != 1 {
				t.Fatalf("live generation routes=%d want 1", live)
			}
		})
	}
}

func TestEnumerateStateDomainThreadsReadsNamesOnly(t *testing.T) {
	domainPath := t.TempDir()
	writeStateDomainThreads(t, domainPath, "01a071d3-1fbf-72a0-af54-7087c184e0b2", "0198f8aa-2b3c-7000-8000-0123456789ab")
	sessions := filepath.Join(domainPath, "sessions")
	// Neither a non-rollout file nor an unrelated extension is a thread.
	if err := os.WriteFile(filepath.Join(sessions, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "rollout-broken.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	threads, err := enumerateStateDomainThreads(domainPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0198f8aa-2b3c-7000-8000-0123456789ab", "01a071d3-1fbf-72a0-af54-7087c184e0b2"}
	if !reflect.DeepEqual(threads, want) {
		t.Fatalf("threads=%q want=%q", threads, want)
	}
	if _, err := enumerateStateDomainThreads(filepath.Join(domainPath, "missing")); err == nil {
		t.Fatal("a missing state domain must report an error, not an empty census")
	}
}
