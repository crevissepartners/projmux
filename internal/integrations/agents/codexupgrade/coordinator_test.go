package codexupgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
)

type fakeRollingRuntime struct {
	mu               sync.Mutex
	starts           int
	cleanups         int
	running          bool
	proof            codexgenerationhost.LaunchProof
	observe          func(GenerationConfig, codexgenerationhost.LaunchProof, string) error
	authorizeReached chan struct{}
	authorizeRelease chan struct{}
}

func TestCoordinatorExactPhase0TuplePreflightRefusesBeforeEveryMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Request)
	}{
		{
			name: "same old private root and socket",
			mutate: func(_ *testing.T, request *Request) {
				request.Target.PrivateRoot = request.Current.Config.PrivateRoot
				request.Target.SocketPath = request.Current.Config.SocketPath
			},
		},
		{
			name: "different shared state-domain path",
			mutate: func(t *testing.T, request *Request) {
				other := filepath.Join(filepath.Dir(request.Target.StateDomainPath), "other-state-domain")
				if err := os.Mkdir(other, 0o700); err != nil {
					t.Fatal(err)
				}
				request.Target.StateDomainPath = other
			},
		},
		{
			name: "qualification versions differ from verified manifests",
			mutate: func(_ *testing.T, request *Request) {
				request.Qualification = codexgeneration.EvaluateQualification(
					codexgeneration.VersionPair{Old: "0.150.0", New: "0.152.1"}, request.Qualification.Evidence)
			},
		},
		{
			name: "target bundle ID differs from verified lease",
			mutate: func(_ *testing.T, request *Request) {
				request.TargetBundleID = "sha256:not-the-qualified-target"
			},
		},
		{
			name: "candidate private root overlaps immutable lease",
			mutate: func(_ *testing.T, request *Request) {
				request.Target.PrivateRoot = request.Target.LeaseRoot
				request.Target.SocketPath = filepath.Join(request.Target.PrivateRoot, "app-server.sock")
			},
		},
		{
			name: "candidate private root overlaps shared state",
			mutate: func(_ *testing.T, request *Request) {
				request.Target.PrivateRoot = request.Target.StateDomainPath
				request.Target.SocketPath = filepath.Join(request.Target.PrivateRoot, "app-server.sock")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testRollingRequest(t)
			test.mutate(t, &request)
			coordinator, runtime, registry := testRollingCoordinator(t, request)
			plan := coordinator.Plan(context.Background(), request)
			if plan.Decision != DecisionBlocked || plan.Mutations != (codexgeneration.MutationCount{}) ||
				plan.Phase5 != (codexgeneration.RollingOperationMutations{}) {
				t.Fatalf("preflight plan = %+v", plan)
			}
			if _, err := coordinator.Apply(context.Background(), request); err == nil {
				t.Fatal("invalid exact Phase 0 tuple reached Apply")
			}
			_, exists, err := coordinator.Journal.Load()
			if err != nil {
				t.Fatal(err)
			}
			if exists || runtime.starts != 0 || runtime.cleanups != 0 || runtime.running || registry.barriers != 0 ||
				registry.convergences != 0 || registry.providerWrites != 0 || registry.tmuxWrites != 0 {
				t.Fatalf("preflight mutation: journal=%t runtime=%+v registry=%+v", exists, runtime, registry)
			}
		})
	}
}

func (runtime *fakeRollingRuntime) Observe(_ context.Context, cfg GenerationConfig, proof codexgenerationhost.LaunchProof, tuiPath string) error {
	if runtime.observe != nil {
		return runtime.observe(cfg, proof, tuiPath)
	}
	return nil
}

func (runtime *fakeRollingRuntime) Prepare(_ context.Context, _ GenerationConfig, _ string, authorizeLaunch func(func() error) error, afterLaunch func() error, publish func(codexgenerationhost.LaunchProof) error) error {
	if runtime.authorizeReached != nil {
		close(runtime.authorizeReached)
		<-runtime.authorizeRelease
	}
	start := func() error {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if !runtime.running {
			runtime.running = true
			runtime.starts++
		}
		return nil
	}
	if authorizeLaunch != nil {
		if err := authorizeLaunch(start); err != nil {
			return err
		}
	} else if err := start(); err != nil {
		return err
	}
	if afterLaunch != nil {
		if err := afterLaunch(); err != nil {
			return err
		}
	}
	return publish(runtime.proof)
}

func TestManagedCurrentActivationAfterDefaultUpgradePreservesOldAgentAndForeignLifecycle(t *testing.T) {
	request := testRollingRequest(t)
	newLease := testRollingLease(t, filepath.Join(filepath.Dir(request.Target.LeaseRoot), "managed-0.153.0"), "0.153.0")
	request.Target.Endpoint.EndpointGenerationID = "codex-0.153.0"
	request.Target.LeaseRoot = newLease.Root
	request.TargetBundleID = newLease.ID
	request.TargetTUIPath = newLease.Paths(codexbundle.RoleTUI)[0]
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	oldEndpoint := request.Current.Generation.Endpoint
	oldAgent := metadata.Agent{
		APIVersion: metadata.APIVersion,
		Kind:       metadata.KindAgent,
		Metadata: metadata.ObjectMeta{
			UID: "agent-old", Name: "agent-old",
		},
		Spec: metadata.AgentSpec{Provider: "codex"},
		Status: metadata.AgentStatus{
			Phase:   metadata.PhaseRunning,
			PaneRef: "pane-old",
			Interaction: metadata.AgentInteraction{
				Kind: metadata.InteractionApprovalRequired,
			},
			SessionRef: &metadata.AgentSessionRef{
				Provider: "codex",
				Codex: &metadata.CodexSessionRef{
					ThreadID: "thread-old", HasStartedTurn: true,
					Endpoint: &oldEndpoint,
					Lifecycle: &metadata.CodexGenerationLifecycleRef{
						State: metadata.CodexGenerationCurrent,
					},
				},
			},
		},
	}
	registry.registry.Agents = append(registry.registry.Agents, oldAgent)

	activation := ManagedCurrentActivation{
		OperationRef:   "managed-activation-default-upgrade",
		OldEndpoint:    oldEndpoint,
		OldOwner:       codexgeneration.OwnerUnmanaged,
		OldVersion:     "0.151.0",
		Target:         request.Target,
		TargetBundleID: request.TargetBundleID,
		TargetTUIPath:  request.TargetTUIPath,
		TargetVersion:  "0.153.0",
	}
	journal, err := coordinator.ActivateManagedCurrent(context.Background(), activation)
	if err != nil {
		t.Fatalf("ordinary create managed-current activation: %v", err)
	}
	current, ok := journal.CurrentRoute()
	if !ok || !current.Generation.Endpoint.Same(request.Target.Endpoint) || current.Generation.Owner != codexgeneration.OwnerProjmuxPrivate || !current.Ready || current.Proof == nil {
		t.Fatalf("managed Current route = %+v, found=%t", current, ok)
	}
	oldRoute, ok := journal.Route(oldEndpoint)
	if !ok || oldRoute.Generation.State != codexgeneration.StateDraining || oldRoute.Generation.Owner != codexgeneration.OwnerUnmanaged || oldRoute.Version != "0.151.0" || oldRoute.Ready || oldRoute.Proof != nil {
		t.Fatalf("foreign old route = %+v, found=%t", oldRoute, ok)
	}
	if current.Version != "0.153.0" {
		t.Fatalf("managed Current version = %q", current.Version)
	}
	gotAgent, ok := registry.registry.Agent("agent-old")
	if !ok || gotAgent.Status.PaneRef != "pane-old" || gotAgent.Status.SessionRef == nil || gotAgent.Status.SessionRef.Codex == nil ||
		gotAgent.Status.SessionRef.Codex.ThreadID != "thread-old" || !gotAgent.Status.SessionRef.Codex.Endpoint.Same(oldEndpoint) ||
		gotAgent.Status.SessionRef.Codex.Lifecycle == nil || gotAgent.Status.SessionRef.Codex.Lifecycle.State != metadata.CodexGenerationDraining ||
		gotAgent.Status.Interaction.Kind != metadata.InteractionApprovalRequired {
		t.Fatalf("old Agent continuity = %#v", gotAgent.Status)
	}
	if runtime.starts != 1 || journal.Operation == nil || journal.Operation.Mutations.ForeignAdoption != 0 || journal.Operation.Mutations.OldEndpointStop != 0 ||
		journal.Operation.Mutations.SuccessorResume != 0 || journal.Operation.Mutations.EndpointRefCAS != 0 || journal.Operation.Mutations.PaneRelaunch != 0 {
		t.Fatalf("activation effects runtime=%+v operation=%+v", runtime, journal.Operation)
	}
	if _, err := coordinator.Journal.Update(context.Background(), func(stored *Journal, _ bool) error {
		for index := range stored.Routes {
			if stored.Routes[index].Generation.Endpoint.Same(request.Target.Endpoint) {
				stored.Routes[index].Version = ""
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("clear optional route version: %v", err)
	}
	if _, err := coordinator.Journal.Update(context.Background(), func(stored *Journal, _ bool) error {
		for index := range stored.Routes {
			if stored.Routes[index].Generation.Endpoint.Same(request.Target.Endpoint) {
				stored.Routes[index].Version = "0.153.0"
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("persist route-only version change: %v", err)
	}
	reloaded, exists, err := coordinator.Journal.Load()
	if err != nil || !exists {
		t.Fatalf("reload versioned routes exists=%t err=%v", exists, err)
	}
	reloadedCurrent, _ := reloaded.CurrentRoute()
	if reloadedCurrent.Version != "0.153.0" {
		t.Fatalf("route-only version change was dropped: %+v", reloadedCurrent)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Journal)
	}{
		{
			name: "unsafe external version",
			mutate: func(journal *Journal) {
				journal.Routes[0].Version = "not/a-safe-version"
			},
		},
		{
			name: "external canonical endpoint mismatch",
			mutate: func(journal *Journal) {
				journal.Routes[0].Version = "0.150.0"
				journal.Routes[0].Generation.BundleID = "external-0.150.0"
			},
		},
		{
			name: "private canonical endpoint mismatch",
			mutate: func(journal *Journal) {
				for index := range journal.Routes {
					if journal.Routes[index].Generation.Owner == codexgeneration.OwnerProjmuxPrivate {
						journal.Routes[index].Version = "0.153.1"
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			corrupted := reloaded
			corrupted.Routes = append([]GenerationRoute(nil), reloaded.Routes...)
			test.mutate(&corrupted)
			if err := corrupted.Validate(); err == nil {
				t.Fatal("corrupt route version unexpectedly validated")
			}
		})
	}
}

func TestManagedCurrentActivationRejectsNonCanonicalGenerationIdentityBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ManagedCurrentActivation)
	}{
		{
			name: "old endpoint does not name observed version",
			mutate: func(activation *ManagedCurrentActivation) {
				activation.OldEndpoint.EndpointGenerationID = "codex-9.9.9"
			},
		},
		{
			name: "target endpoint does not name managed version",
			mutate: func(activation *ManagedCurrentActivation) {
				activation.Target.Endpoint.EndpointGenerationID = "codex-9.9.9"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := testRollingRequest(t)
			coordinator, runtime, registry := testRollingCoordinator(t, request)
			activation := ManagedCurrentActivation{
				OperationRef: "managed-activation-canonical-identity", OldEndpoint: request.Current.Generation.Endpoint,
				OldOwner: codexgeneration.OwnerUnmanaged, OldVersion: "0.151.0", Target: request.Target,
				TargetBundleID: request.TargetBundleID, TargetTUIPath: request.TargetTUIPath, TargetVersion: "0.152.1",
			}
			test.mutate(&activation)
			if _, err := coordinator.ActivateManagedCurrent(context.Background(), activation); err == nil || !strings.Contains(err.Error(), "request-invalid") {
				t.Fatalf("non-canonical activation = %v", err)
			}
			_, exists, err := coordinator.Journal.Load()
			if err != nil {
				t.Fatal(err)
			}
			if exists || runtime.starts != 0 || runtime.cleanups != 0 || registry.barriers != 0 || registry.convergences != 0 {
				t.Fatalf("non-canonical identity mutated state: journal=%t runtime=%+v registry=%+v", exists, runtime, registry)
			}
		})
	}
}

func TestManagedCurrentActivationRefusesMismatchedExistingCurrentWithoutFurtherMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*GenerationRoute)
	}{
		{
			name: "version",
			mutate: func(route *GenerationRoute) {
				route.Version = ""
			},
		},
		{
			name: "bundle",
			mutate: func(route *GenerationRoute) {
				route.Generation.BundleID = "sha256-mismatched-existing-current"
				proof := *route.Proof
				proof.BundleID = route.Generation.BundleID
				route.Proof = &proof
			},
		},
		{
			name: "config",
			mutate: func(route *GenerationRoute) {
				route.Config.RequiredProtocol.Max++
			},
		},
		{
			name: "tui path",
			mutate: func(route *GenerationRoute) {
				route.TUIPath += "-mismatched"
			},
		},
		{
			name: "launch operation",
			mutate: func(route *GenerationRoute) {
				route.LaunchOperationRef = "other-managed-activation"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := testRollingRequest(t)
			coordinator, runtime, registry := testRollingCoordinator(t, request)
			activation := ManagedCurrentActivation{
				OperationRef: "managed-activation-existing-current", OldEndpoint: request.Current.Generation.Endpoint,
				OldOwner: codexgeneration.OwnerUnmanaged, OldVersion: "0.151.0", Target: request.Target,
				TargetBundleID: request.TargetBundleID, TargetTUIPath: request.TargetTUIPath, TargetVersion: "0.152.1",
			}
			if _, err := coordinator.ActivateManagedCurrent(context.Background(), activation); err != nil {
				t.Fatalf("seed completed activation: %v", err)
			}
			if _, err := coordinator.Journal.Update(context.Background(), func(stored *Journal, _ bool) error {
				for index := range stored.Routes {
					if stored.Routes[index].Generation.Endpoint.Same(request.Target.Endpoint) {
						test.mutate(&stored.Routes[index])
						return nil
					}
				}
				return errors.New("managed Current route missing")
			}); err != nil {
				t.Fatalf("persist structurally valid mismatch: %v", err)
			}
			beforeBody, err := os.ReadFile(coordinator.Journal.Path())
			if err != nil {
				t.Fatal(err)
			}
			beforeStarts, beforeCleanups := runtime.starts, runtime.cleanups
			beforeBarriers, beforeConvergences := registry.barriers, registry.convergences
			if _, err := coordinator.ActivateManagedCurrent(context.Background(), activation); err == nil ||
				!strings.Contains(err.Error(), "existing-pool-requires-operator-inspection") {
				t.Fatalf("mismatched existing Current activation = %v", err)
			}
			afterBody, err := os.ReadFile(coordinator.Journal.Path())
			if err != nil {
				t.Fatal(err)
			}
			if string(afterBody) != string(beforeBody) || runtime.starts != beforeStarts || runtime.cleanups != beforeCleanups ||
				registry.barriers != beforeBarriers || registry.convergences != beforeConvergences {
				t.Fatalf("mismatched existing Current mutated state: journal-changed=%t runtime=%+v registry=%+v",
					string(afterBody) != string(beforeBody), runtime, registry)
			}
		})
	}
}

func TestManagedCurrentActivationReopenAfterAdmissionConvergesDrainBeforeCreate(t *testing.T) {
	request := testRollingRequest(t)
	newLease := testRollingLease(t, filepath.Join(filepath.Dir(request.Target.LeaseRoot), "managed-0.153.0-reopen"), "0.153.0")
	request.Target.Endpoint.EndpointGenerationID = "codex-0.153.0"
	request.Target.LeaseRoot = newLease.Root
	request.TargetBundleID = newLease.ID
	request.TargetTUIPath = newLease.Paths(codexbundle.RoleTUI)[0]
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	oldEndpoint := request.Current.Generation.Endpoint
	registry.registry.Agents = append(registry.registry.Agents, metadata.Agent{
		APIVersion: metadata.APIVersion, Kind: metadata.KindAgent,
		Metadata: metadata.ObjectMeta{UID: "agent-reopen", Name: "agent-reopen"},
		Spec:     metadata.AgentSpec{Provider: "codex"},
		Status: metadata.AgentStatus{Phase: metadata.PhaseRunning, PaneRef: "pane-reopen", SessionRef: &metadata.AgentSessionRef{
			Provider: "codex", Codex: &metadata.CodexSessionRef{ThreadID: "thread-reopen", HasStartedTurn: true, Endpoint: &oldEndpoint,
				Lifecycle: &metadata.CodexGenerationLifecycleRef{State: metadata.CodexGenerationCurrent}},
		}},
	})
	activation := ManagedCurrentActivation{
		OperationRef: "managed-activation-reopen", OldEndpoint: oldEndpoint, OldOwner: codexgeneration.OwnerUnmanaged, OldVersion: "0.151.0",
		Target: request.Target, TargetBundleID: request.TargetBundleID, TargetTUIPath: request.TargetTUIPath, TargetVersion: "0.153.0",
	}
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterAdmission {
			return errors.New("crash after admission")
		}
		return nil
	}
	if _, err := coordinator.ActivateManagedCurrent(context.Background(), activation); err == nil || !strings.Contains(err.Error(), "crash after admission") {
		t.Fatalf("post-admission crash = %v", err)
	}
	interrupted, exists, err := coordinator.Journal.Load()
	if err != nil || !exists || interrupted.Operation == nil || !interrupted.Operation.AdmissionCommitted || interrupted.Operation.DrainPublished || interrupted.CurrentGenerationID != request.Target.Endpoint.EndpointGenerationID {
		t.Fatalf("interrupted activation journal = %+v exists=%t err=%v", interrupted, exists, err)
	}
	reopened := &Coordinator{Journal: NewStore(coordinator.Journal.Path()), Registry: registry, Runtime: runtime}
	converged, err := reopened.ActivateManagedCurrent(context.Background(), activation)
	if err != nil {
		t.Fatalf("reopen activation: %v", err)
	}
	if converged.Operation == nil || !converged.Operation.AdmissionCommitted || !converged.Operation.DrainPublished || runtime.starts != 1 ||
		converged.Operation.Mutations.OldEndpointStop != 0 || converged.Operation.Mutations.ForeignAdoption != 0 || converged.Operation.Mutations.SuccessorResume != 0 {
		t.Fatalf("reopen convergence runtime=%+v operation=%+v", runtime, converged.Operation)
	}
	agent, _ := registry.registry.Agent("agent-reopen")
	if agent.Status.PaneRef != "pane-reopen" || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.ThreadID != "thread-reopen" || !agent.Status.SessionRef.Codex.Endpoint.Same(oldEndpoint) ||
		agent.Status.SessionRef.Codex.Lifecycle == nil || agent.Status.SessionRef.Codex.Lifecycle.State != metadata.CodexGenerationDraining {
		t.Fatalf("reopen old Agent continuity = %#v", agent.Status)
	}
}

func TestCoordinatorObserveFailureNamesCandidatePublicationOrAdmissionStage(t *testing.T) {
	for _, test := range []struct {
		name              string
		failTargetObserve int
		want              string
		notWant           string
	}{
		{name: "candidate publication", failTargetObserve: 1, want: "candidate publication observe failed", notWant: "admission candidate observe failed"},
		{name: "admission", failTargetObserve: 2, want: "admission candidate observe failed", notWant: "candidate publication observe failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := testRollingRequest(t)
			coordinator, runtime, _ := testRollingCoordinator(t, request)
			targetObserves := 0
			runtime.observe = func(cfg GenerationConfig, _ codexgenerationhost.LaunchProof, _ string) error {
				if cfg.Endpoint.Same(request.Target.Endpoint) {
					targetObserves++
					if targetObserves == test.failTargetObserve {
						return errors.New("simulated target proof drift")
					}
				}
				return nil
			}
			_, err := coordinator.Apply(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), test.notWant) {
				t.Fatalf("observe failure = %v, want %q and not %q", err, test.want, test.notWant)
			}
		})
	}
}

func TestCoordinatorWrongAbsoluteTUIPathNeverCommitsAdmission(t *testing.T) {
	request := testRollingRequest(t)
	request.TargetTUIPath = filepath.Join(filepath.Dir(request.TargetTUIPath), "wrong-codex")
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("wrong absolute TUI path reached admission")
	}
	_, exists, err := coordinator.Journal.Load()
	if err != nil {
		t.Fatal(err)
	}
	if exists || runtime.starts != 0 || registry.barriers != 0 || registry.convergences != 0 || registry.providerWrites != 0 || registry.tmuxWrites != 0 {
		t.Fatalf("wrong TUI effects journal=%t runtime=%+v registry=%+v", exists, runtime, registry)
	}
}

func TestCoordinatorTUIDriftAfterReadyProofFailsInsideAdmissionBarrier(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	targetObservations := 0
	runtime.observe = func(cfg GenerationConfig, _ codexgenerationhost.LaunchProof, _ string) error {
		if cfg.Endpoint.Same(request.Target.Endpoint) {
			targetObservations++
			if targetObservations == 2 {
				return errors.New("RoleTUI artifact drifted")
			}
		}
		return nil
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("post-proof TUI drift reached admission")
	}
	journal, _, _ := coordinator.Journal.Load()
	if journal.Operation == nil || !journal.Operation.CandidateReady || journal.Operation.AdmissionCommitted || journal.Operation.Mutations.AdmissionCommit != 0 || journal.CurrentGenerationID != request.Current.Generation.Endpoint.EndpointGenerationID || registry.convergences != 0 || registry.providerWrites != 0 || registry.tmuxWrites != 0 {
		t.Fatalf("TUI drift effects journal=%+v registry=%+v", journal.Operation, registry)
	}
	assertPhase5Zero(t, journal.Operation.Mutations)
}

func TestCoordinatorAbortFencePreventsStalePlannedResumeFromLaunching(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	runtime.authorizeReached = make(chan struct{})
	runtime.authorizeRelease = make(chan struct{})
	resumeDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Apply(context.Background(), request)
		resumeDone <- err
	}()
	<-runtime.authorizeReached
	aborted, err := coordinator.Abort(context.Background(), request.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	close(runtime.authorizeRelease)
	if err := <-resumeDone; err == nil {
		t.Fatal("stale Resume launched after durable abort fence")
	}
	if runtime.starts != 0 || runtime.cleanups != 0 || aborted.Operation == nil || !aborted.Operation.Aborted || len(aborted.Routes) != 1 || registry.barriers != 0 || registry.convergences != 0 {
		t.Fatalf("stale resume effects journal=%+v runtime=%+v registry=%+v", aborted.Operation, runtime, registry)
	}
	assertPhase5Zero(t, aborted.Operation.Mutations)
}

func TestCoordinatorAbortAndAdmissionRaceNeverStopsCurrent(t *testing.T) {
	t.Run("abort intent wins", func(t *testing.T) {
		request := testRollingRequest(t)
		coordinator, runtime, registry := testRollingCoordinator(t, request)
		coordinator.Failpoint = func(point string) error {
			if point == FailAfterCandidate {
				return errors.New("pause at candidate ready")
			}
			return nil
		}
		if _, err := coordinator.Apply(context.Background(), request); err == nil {
			t.Fatal("apply crossed candidate-ready barrier")
		}
		enteredAdmission := make(chan struct{})
		releaseAdmission := make(chan struct{})
		var once sync.Once
		runtime.observe = func(cfg GenerationConfig, _ codexgenerationhost.LaunchProof, _ string) error {
			if cfg.Endpoint.Same(request.Target.Endpoint) {
				once.Do(func() { close(enteredAdmission) })
				<-releaseAdmission
			}
			return nil
		}
		coordinator.Failpoint = nil
		resumeDone := make(chan error, 1)
		go func() {
			_, err := coordinator.Resume(context.Background(), request.OperationRef)
			resumeDone <- err
		}()
		<-enteredAdmission
		aborted, err := coordinator.Abort(context.Background(), request.OperationRef)
		if err != nil {
			t.Fatal(err)
		}
		close(releaseAdmission)
		if err := <-resumeDone; err == nil {
			t.Fatal("stale admission committed after abort intent won")
		}
		if aborted.Operation == nil || aborted.Operation.AdmissionCommitted || aborted.CurrentGenerationID != request.Current.Generation.Endpoint.EndpointGenerationID || runtime.cleanups != 1 || runtime.running || registry.convergences != 0 {
			t.Fatalf("abort-wins receipt=%+v runtime=%+v registry=%+v", aborted.Operation, runtime, registry)
		}
		assertPhase5Zero(t, aborted.Operation.Mutations)
	})

	t.Run("admission commit wins", func(t *testing.T) {
		request := testRollingRequest(t)
		coordinator, runtime, _ := testRollingCoordinator(t, request)
		coordinator.Failpoint = func(point string) error {
			if point == FailAfterCandidate {
				return errors.New("pause at candidate ready")
			}
			return nil
		}
		if _, err := coordinator.Apply(context.Background(), request); err == nil {
			t.Fatal("apply crossed candidate-ready barrier")
		}
		admitted := make(chan struct{})
		var once sync.Once
		coordinator.Failpoint = func(point string) error {
			if point == FailAfterAdmission {
				once.Do(func() { close(admitted) })
			}
			return nil
		}
		resumeDone := make(chan error, 1)
		go func() {
			_, err := coordinator.Resume(context.Background(), request.OperationRef)
			resumeDone <- err
		}()
		<-admitted
		if _, err := coordinator.Abort(context.Background(), request.OperationRef); err == nil {
			t.Fatal("abort cleaned a candidate after admission-current won")
		}
		if err := <-resumeDone; err != nil {
			t.Fatal(err)
		}
		journal, _, _ := coordinator.Journal.Load()
		if journal.Operation == nil || !journal.Operation.AdmissionCommitted || journal.Operation.Mutations.AdmissionCommit != 1 || runtime.cleanups != 0 || !runtime.running {
			t.Fatalf("admission-wins receipt=%+v runtime=%+v", journal.Operation, runtime)
		}
		assertPhase5Zero(t, journal.Operation.Mutations)
	})
}

func TestCoordinatorAbortedOperationRefCannotBeReplayed(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterPrewrite {
			return errors.New("stop before candidate")
		}
		return nil
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("apply crossed prewrite failpoint")
	}
	coordinator.Failpoint = nil
	aborted, err := coordinator.Abort(context.Background(), request.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(coordinator.Journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("terminal aborted operation ref was reused")
	}
	after, err := os.ReadFile(coordinator.Journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || runtime.starts != 0 || runtime.cleanups != 0 || registry.barriers != 0 || registry.convergences != 0 || aborted.Operation.Mutations != (codexgeneration.RollingOperationMutations{}) {
		t.Fatalf("aborted-ref replay effects journal=%+v runtime=%+v registry=%+v", aborted.Operation, runtime, registry)
	}
	assertPhase5Zero(t, aborted.Operation.Mutations)
}

func TestCoordinatorRetainedRetiredRoutesDoNotConsumeThePreparingSlot(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, _ := testRollingCoordinator(t, request)
	retiredEndpoint := metadata.CodexEndpointRef{StateDomainID: request.Current.Generation.Endpoint.StateDomainID, EndpointGenerationID: "generation-retired"}
	retired := GenerationRoute{
		Generation: codexgeneration.Generation{Endpoint: retiredEndpoint, State: codexgeneration.StateRetired, Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: "bundle-retired"},
		Config:     GenerationConfig{Endpoint: retiredEndpoint, StateDomainPath: request.Target.StateDomainPath, PrivateRoot: filepath.Join(filepath.Dir(request.Target.PrivateRoot), "retired"), SocketPath: filepath.Join(filepath.Dir(request.Target.PrivateRoot), "retired", "app-server.sock"), LeaseRoot: request.Target.LeaseRoot, RequiredProtocol: request.Target.RequiredProtocol},
		TUIPath:    request.TargetTUIPath,
	}
	if _, err := coordinator.Journal.Update(context.Background(), func(journal *Journal, exists bool) error {
		if exists {
			t.Fatal("unexpected journal")
		}
		*journal = Journal{Version: JournalVersion, StateDomainID: request.Current.Generation.Endpoint.StateDomainID, CurrentGenerationID: request.Current.Generation.Endpoint.EndpointGenerationID, Routes: []GenerationRoute{retired, request.Current}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := coordinator.Apply(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, route := range completed.Routes {
		if route.Generation.State != codexgeneration.StateRetired {
			live++
		}
	}
	if runtime.starts != 1 || live != 2 || len(completed.Routes) != 3 {
		t.Fatalf("retained-retired upgrade routes=%+v starts=%d live=%d", completed.Routes, runtime.starts, live)
	}
}

func (runtime *fakeRollingRuntime) Cleanup(context.Context, GenerationConfig, string, *codexgenerationhost.LaunchProof) (bool, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.running {
		return false, nil
	}
	runtime.running = false
	runtime.cleanups++
	return true, nil
}

type fakeRollingRegistry struct {
	mu             sync.Mutex
	registry       metadata.Registry
	barriers       int
	convergences   int
	providerWrites int
	tmuxWrites     int
}

func (store *fakeRollingRegistry) LoadSnapshot() (metadata.Registry, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.registry.Clone(), nil
}

func (store *fakeRollingRegistry) WithAdmissionBarrier(fn func(metadata.Registry) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.barriers++
	return fn(store.registry.Clone())
}

func (store *fakeRollingRegistry) UpdateConvergent(fn func(*metadata.Registry) error) (metadata.Registry, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	working := store.registry.Clone()
	if err := fn(&working); err != nil {
		return metadata.Registry{}, false, err
	}
	changed := !reflect.DeepEqual(working, store.registry)
	if changed {
		store.registry = working
		store.convergences++
	}
	return store.registry.Clone(), changed, nil
}

func testRollingRequest(t *testing.T) Request {
	t.Helper()
	root := t.TempDir()
	domain := filepath.Join(root, "state-domain")
	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	for _, path := range []string{domain, oldRoot, newRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldLease := testRollingLease(t, filepath.Join(root, "old-bundle"), "0.151.0")
	newLease := testRollingLease(t, filepath.Join(root, "new-bundle"), "0.152.1")
	oldEndpoint := metadata.CodexEndpointRef{StateDomainID: "domain-one", EndpointGenerationID: "codex-0.151.0"}
	newEndpoint := metadata.CodexEndpointRef{StateDomainID: "domain-one", EndpointGenerationID: "codex-0.152.1"}
	oldSocket := filepath.Join(oldRoot, "app-server.sock")
	oldServer := oldLease.Paths(codexbundle.RoleServer)[0]
	oldTUI := oldLease.Paths(codexbundle.RoleTUI)[0]
	oldProof := codexgenerationhost.LaunchProof{
		Endpoint:          codexgenerationhost.EndpointIdentity{StateDomainID: oldEndpoint.StateDomainID, EndpointGenerationID: oldEndpoint.EndpointGenerationID},
		EndpointRuntimeID: "runtime-old", PID: 101, ProcessGroupID: 101,
		SocketPath: oldSocket, ExecutablePath: oldServer,
		ExecutableSHA256: rollingArtifactSHA(oldLease, oldServer), BundleID: oldLease.ID,
	}
	qualification := codexgeneration.EvaluateQualification(codexgeneration.VersionPair{Old: "0.151.0", New: "0.152.1"}, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
		DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
		PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
	})
	return Request{
		OperationRef: "upgrade-one",
		Current: GenerationRoute{
			Generation: codexgeneration.Generation{Endpoint: oldEndpoint, State: codexgeneration.StateCurrent, Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: oldLease.ID},
			Config:     GenerationConfig{Endpoint: oldEndpoint, StateDomainPath: domain, PrivateRoot: oldRoot, SocketPath: oldSocket, LeaseRoot: oldLease.Root, RequiredProtocol: codexbundle.ProtocolRange{Min: 1, Max: 1}},
			TUIPath:    oldTUI, Ready: true, Proof: &oldProof,
		},
		Target:         GenerationConfig{Endpoint: newEndpoint, StateDomainPath: domain, PrivateRoot: newRoot, SocketPath: filepath.Join(newRoot, "app-server.sock"), LeaseRoot: newLease.Root, RequiredProtocol: codexbundle.ProtocolRange{Min: 1, Max: 1}},
		TargetBundleID: newLease.ID, TargetTUIPath: newLease.Paths(codexbundle.RoleTUI)[0], Qualification: qualification,
	}
}

func testRollingLease(t *testing.T, root, version string) codexbundle.Lease {
	t.Helper()
	source, store := filepath.Join(root, "source"), filepath.Join(root, "store")
	var specs []codexbundle.ArtifactSpec
	for _, relative := range codexgenerationhost.CompleteBundleArtifactPaths() {
		full := filepath.Join(source, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("#!/bin/sh\n# "+version+" "+relative+"\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		roles := []codexbundle.Role{codexbundle.RoleHelper}
		if relative == "bin/codex" {
			roles = []codexbundle.Role{codexbundle.RoleServer, codexbundle.RoleTUI}
		}
		specs = append(specs, codexbundle.ArtifactSpec{Path: relative, Roles: roles})
	}
	protocol := codexbundle.ProtocolRange{Min: 1, Max: 1}
	manifest, err := codexbundle.Inspect(source, version, protocol, specs)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := codexbundle.Create(store, source, manifest, protocol)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func rollingArtifactSHA(lease codexbundle.Lease, absolute string) string {
	for _, artifact := range lease.Manifest.Artifacts {
		if filepath.Join(lease.Root, filepath.FromSlash(artifact.Path)) == absolute {
			return artifact.SHA256
		}
	}
	return ""
}

func testRollingCoordinator(t *testing.T, request Request) (*Coordinator, *fakeRollingRuntime, *fakeRollingRegistry) {
	t.Helper()
	runtime := &fakeRollingRuntime{proof: codexgenerationhost.LaunchProof{
		Endpoint:          codexgenerationhost.EndpointIdentity{StateDomainID: request.Target.Endpoint.StateDomainID, EndpointGenerationID: request.Target.Endpoint.EndpointGenerationID},
		EndpointRuntimeID: "runtime-new", PID: 202, ProcessGroupID: 202,
		SocketPath: request.Target.SocketPath, ExecutablePath: filepath.Join(request.Target.LeaseRoot, "bin", "codex"), BundleID: request.TargetBundleID,
	}}
	registry := &fakeRollingRegistry{registry: metadata.NewRegistry()}
	return &Coordinator{Journal: NewStore(filepath.Join(t.TempDir(), "rolling.json")), Registry: registry, Runtime: runtime}, runtime, registry
}

func assertPhase5Zero(t *testing.T, effects codexgeneration.RollingOperationMutations) {
	t.Helper()
	if effects.OldEndpointStop != 0 || effects.SuccessorResume != 0 || effects.EndpointRefCAS != 0 ||
		effects.PaneRelaunch != 0 || effects.Retirement != 0 || effects.LeaseRelease != 0 || effects.ForeignAdoption != 0 {
		t.Fatalf("Phase 5 effect escaped: %+v", effects)
	}
}

func TestCoordinatorCandidateReadyAbortCleansExactCandidateAndKeepsSevenPhase5EffectsZero(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterCandidate {
			return errors.New("crash after durable proof")
		}
		return nil
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("apply crossed candidate-ready failpoint")
	}
	journal, exists, err := coordinator.Journal.Load()
	if err != nil || !exists || journal.Operation == nil || !journal.Operation.CandidateReady || journal.Operation.AdmissionCommitted {
		t.Fatalf("candidate-ready journal = (%+v,%t,%v)", journal, exists, err)
	}
	coordinator.Failpoint = nil
	aborted, err := coordinator.Abort(context.Background(), request.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	if aborted.Operation == nil || !aborted.Operation.Aborted || aborted.Operation.Mutations.CandidateCleanup != 1 || len(aborted.Routes) != 1 {
		t.Fatalf("abort receipt/routes = %+v", aborted)
	}
	assertPhase5Zero(t, aborted.Operation.Mutations)
	if runtime.cleanups != 1 || runtime.running || registry.barriers != 0 || registry.providerWrites != 0 || registry.tmuxWrites != 0 {
		t.Fatalf("abort effects runtime=%+v registry=%+v", runtime, registry)
	}
}

func TestCoordinatorResumeAfterLaunchBeforeProofReusesOneCandidateAndLeavesNoDuplicate(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterCandidateLaunch {
			return errors.New("coordinator process died")
		}
		return nil
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("apply crossed launch-before-proof failpoint")
	}
	crashed, _, err := coordinator.Journal.Load()
	if err != nil || crashed.Operation == nil || !crashed.Operation.CandidateStarted || crashed.Operation.CandidateReady {
		t.Fatalf("launch receipt = %+v err=%v", crashed.Operation, err)
	}
	coordinator.Failpoint = nil
	completed, err := coordinator.Resume(context.Background(), request.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.starts != 1 || !runtime.running || registry.barriers != 1 || completed.Operation == nil || completed.Operation.Mutations.CandidateStart != 1 || completed.Operation.Mutations.AdmissionCommit != 1 {
		t.Fatalf("resume effects journal=%+v runtime=%+v registry=%+v", completed.Operation, runtime, registry)
	}
	assertPhase5Zero(t, completed.Operation.Mutations)
	again, err := coordinator.Resume(context.Background(), request.OperationRef)
	if err != nil || again.Operation.Mutations != completed.Operation.Mutations || registry.barriers != 1 || runtime.starts != 1 {
		t.Fatalf("second resume = %+v err=%v runtime=%+v registry=%+v", again.Operation, err, runtime, registry)
	}
}

func TestCoordinatorLaunchBeforeProofAbortCleansRecoveredCandidateWithoutPhase5Effects(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, _ := testRollingCoordinator(t, request)
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterCandidateLaunch {
			return errors.New("coordinator process died")
		}
		return nil
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("apply crossed launch-before-proof failpoint")
	}
	aborted, err := coordinator.Abort(context.Background(), request.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.cleanups != 1 || runtime.running || aborted.Operation == nil || aborted.Operation.Mutations.CandidateCleanup != 1 {
		t.Fatalf("recovered abort = %+v runtime=%+v", aborted.Operation, runtime)
	}
	assertPhase5Zero(t, aborted.Operation.Mutations)
}

func TestCoordinatorTwoSlotThirdUpgradeRefusesWithEveryMutationSurfaceAtZero(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, runtime, registry := testRollingCoordinator(t, request)
	if _, err := coordinator.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(coordinator.Journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	starts, barriers, convergences := runtime.starts, registry.barriers, registry.convergences
	third := request
	third.OperationRef = "upgrade-three"
	third.Current = GenerationRoute{}
	journal, _, _ := coordinator.Journal.Load()
	third.Current, _ = journal.CurrentRoute()
	third.Target.Endpoint.EndpointGenerationID = "generation-three"
	third.Target.PrivateRoot = filepath.Join(filepath.Dir(third.Target.PrivateRoot), "three")
	third.Target.SocketPath = filepath.Join(third.Target.PrivateRoot, "app-server.sock")
	if err := os.Mkdir(third.Target.PrivateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	third.Qualification = codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.152.1", New: "0.152.1"}, request.Qualification.Evidence)
	plan := coordinator.Plan(context.Background(), third)
	if plan.Decision != DecisionBlocked || plan.Mutations != (codexgeneration.MutationCount{}) || plan.Phase5 != (codexgeneration.RollingOperationMutations{}) {
		t.Fatalf("third-upgrade plan = %+v", plan)
	}
	if _, err := coordinator.Apply(context.Background(), third); err == nil {
		t.Fatal("third upgrade applied to a full two-slot pool")
	}
	after, err := os.ReadFile(coordinator.Journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || runtime.starts != starts || registry.barriers != barriers || registry.convergences != convergences || registry.providerWrites != 0 || registry.tmuxWrites != 0 {
		t.Fatalf("blocked third upgrade mutated state: runtime=%+v registry=%+v", runtime, registry)
	}
}

func TestCoordinatorRepeatedDrainingResumeReusesOneGenerationHandoverOperation(t *testing.T) {
	request := testRollingRequest(t)
	coordinator, _, _ := testRollingCoordinator(t, request)
	if _, err := coordinator.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	ref1, created1, err := coordinator.RequestHandover(context.Background(), request.Current.Generation.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ref2, created2, err := coordinator.RequestHandover(context.Background(), request.Current.Generation.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	journal, _, _ := coordinator.Journal.Load()
	if ref1 != request.OperationRef || ref2 != ref1 || !created1 || created2 || journal.Operation.Mutations.HandoverRequest != 1 {
		t.Fatalf("handover refs=(%q,%q) created=(%t,%t) receipt=%+v", ref1, ref2, created1, created2, journal.Operation)
	}
	assertPhase5Zero(t, journal.Operation.Mutations)
}

func TestCoordinatorEveryFailpointCrashRestartConvergesAtMostOnce(t *testing.T) {
	failpoints := []string{
		FailBeforePrewrite,
		FailAfterPrewrite,
		FailBeforeCandidate,
		FailAfterCandidateLaunch,
		FailAfterCandidate,
		FailBeforeAdmission,
		FailAfterAdmission,
		FailBeforeDrain,
		FailAfterDrainRegistry,
		FailAfterDrainReceipt,
	}
	for _, point := range failpoints {
		t.Run(point, func(t *testing.T) {
			request := testRollingRequest(t)
			coordinator, runtime, registry := testRollingCoordinator(t, request)
			fired := false
			coordinator.Failpoint = func(candidate string) error {
				if candidate == point && !fired {
					fired = true
					return errors.New("simulated process death at " + point)
				}
				return nil
			}
			if _, err := coordinator.Apply(context.Background(), request); err == nil || !fired {
				t.Fatalf("failpoint %s did not interrupt Apply: %v", point, err)
			}
			// Reconstruct both coordinator and Store as a process restart would.
			restarted := &Coordinator{Journal: NewStore(coordinator.Journal.Path()), Registry: registry, Runtime: runtime}
			journal, exists, err := restarted.Journal.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !exists {
				journal, err = restarted.Apply(context.Background(), request)
			} else {
				journal, err = restarted.Resume(context.Background(), request.OperationRef)
			}
			if err != nil {
				t.Fatalf("restart after %s: %v", point, err)
			}
			if journal.CurrentGenerationID != request.Target.Endpoint.EndpointGenerationID || journal.Operation == nil {
				t.Fatalf("restart after %s did not converge: %+v", point, journal)
			}
			if _, _, err := restarted.RequestHandover(context.Background(), request.Current.Generation.Endpoint); err != nil {
				t.Fatal(err)
			}
			journal, _, err = restarted.Journal.Load()
			if err != nil {
				t.Fatal(err)
			}
			effects := journal.Operation.Mutations
			if runtime.starts != 1 || effects.CandidateStart != 1 || effects.AdmissionCommit != 1 || effects.DrainPublish != 1 || effects.HandoverRequest != 1 {
				t.Fatalf("failpoint %s did not converge every effect exactly once: starts=%d effects=%+v", point, runtime.starts, effects)
			}
			assertPhase5Zero(t, effects)
		})
	}
}

func TestRollingJournalAtomicWriteCrashHooksRestartWithoutDuplicateEffects(t *testing.T) {
	for _, hookName := range []string{"after-temp-write", "before-rename"} {
		t.Run(hookName, func(t *testing.T) {
			request := testRollingRequest(t)
			coordinator, runtime, registry := testRollingCoordinator(t, request)
			fired := false
			crash := func() error {
				if fired {
					return nil
				}
				fired = true
				return errors.New("simulated journal writer death")
			}
			if hookName == "after-temp-write" {
				coordinator.Journal.hooks.afterTempWrite = crash
			} else {
				coordinator.Journal.hooks.beforeRename = crash
			}
			if _, err := coordinator.Apply(context.Background(), request); err == nil || !fired {
				t.Fatalf("journal hook %s did not interrupt Apply: %v", hookName, err)
			}
			restarted := &Coordinator{Journal: NewStore(coordinator.Journal.Path()), Registry: registry, Runtime: runtime}
			journal, exists, err := restarted.Journal.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !exists {
				journal, err = restarted.Apply(context.Background(), request)
			} else {
				journal, err = restarted.Resume(context.Background(), request.OperationRef)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := restarted.RequestHandover(context.Background(), request.Current.Generation.Endpoint); err != nil {
				t.Fatal(err)
			}
			journal, _, err = restarted.Journal.Load()
			if err != nil {
				t.Fatal(err)
			}
			if journal.Operation == nil || journal.CurrentGenerationID != request.Target.Endpoint.EndpointGenerationID || runtime.starts != 1 ||
				journal.Operation.Mutations.CandidateStart != 1 || journal.Operation.Mutations.AdmissionCommit != 1 ||
				journal.Operation.Mutations.DrainPublish != 1 || journal.Operation.Mutations.HandoverRequest != 1 {
				t.Fatalf("journal hook %s did not converge every effect exactly once: journal=%+v starts=%d", hookName, journal.Operation, runtime.starts)
			}
			assertPhase5Zero(t, journal.Operation.Mutations)
		})
	}
}
