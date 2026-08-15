package app

import (
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// newTestAgentCommand wires the Agent namespace with a full resume rebinder, so
// the refusal tests below observe the real route rather than a route that could
// only fail. The rebinder's own handles are returned by
// newTestAgentResumeCommand for the tests that need them.
func newTestAgentCommand(t *testing.T, store *fakeResourceStore) (*agentCommand, *recordingArgv, *recordingArgv) {
	t.Helper()
	cmd, _, ai, usage := newTestAgentResumeCommand(t, store, newFakeTmux())
	return cmd, ai, usage
}

// TestAgentDomainForwardsRawArgvToTheHandlersThatAlreadyOwnTheBehavior is the
// parity half of the Agent namespace: every subcommand except `resume` hands
// the current handler the exact argv tail it would have received under the
// current spelling.
//
// `agent usage` is the row acceptance criterion 1 rests on: it forwards to the
// existing usage command with no prefix at all, so the canonical spelling
// cannot diverge from `usage` without changing that one handler.
func TestAgentDomainForwardsRawArgvToTheHandlersThatAlreadyOwnTheBehavior(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		args    []string
		wantAI  []string
		wantUse []string
	}{
		{name: "status read", args: []string{"status"}, wantAI: []string{"status"}},
		{name: "status set", args: []string{"status", "set", "thinking", "%3"}, wantAI: []string{"status", "set", "thinking", "%3"}},
		{name: "topic read", args: []string{"topic"}, wantAI: []string{"topic"}},
		{name: "topic set", args: []string{"topic", "set", "--pane", "%3", "review"}, wantAI: []string{"topic", "set", "--pane", "%3", "review"}},
		{name: "topic clear", args: []string{"topic", "clear"}, wantAI: []string{"topic", "clear"}},
		{name: "integrate", args: []string{"integrate", "codex", "--dry-run"}, wantAI: []string{"integrate", "codex", "--dry-run"}},
		{name: "usage bare", args: []string{"usage"}, wantUse: []string{}},
		{name: "usage json", args: []string{"usage", "--json"}, wantUse: []string{"--json"}},
		{
			name:    "usage keeps every current flag spelling",
			args:    []string{"usage", "--model", "codex", "--window", "weekly", "--json", "--force"},
			wantUse: []string{"--model", "codex", "--window", "weekly", "--json", "--force"},
		},
		{name: "usage relays an unknown flag rather than pre-judging it", args: []string{"usage", "--bogus"}, wantUse: []string{"--bogus"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			agent, ai, usage := newTestAgentCommand(t, store)
			if _, _, err := runRoute(t, agent, test.args...); err != nil {
				t.Fatalf("agent %v error = %v", test.args, err)
			}
			assertForward(t, "ai", ai, test.wantAI)
			assertForward(t, "usage", usage, test.wantUse)
		})
	}
}

func assertForward(t *testing.T, name string, target *recordingArgv, want []string) {
	t.Helper()
	if want == nil {
		if len(target.calls) != 0 {
			t.Fatalf("the %s handler was reached %d times, want 0: %q", name, len(target.calls), target.calls)
		}
		return
	}
	if len(target.calls) != 1 {
		t.Fatalf("the %s handler was reached %d times, want 1", name, len(target.calls))
	}
	if got := target.calls[0]; len(got)+len(want) > 0 && !reflect.DeepEqual(got, want) {
		t.Fatalf("the %s handler received %q, want %q", name, got, want)
	}
}

// TestAgentUsageAndTheLegacyUsageSpellingShareOneHandlerAndOneArgv is
// acceptance criterion 1 stated as an identity rather than a comparison of two
// outputs: the canonical route holds the same handler instance the top-level
// `usage` route holds, and forwards argv with no prefix, so parity is
// structural and cannot drift.
func TestAgentUsageAndTheLegacyUsageSpellingShareOneHandlerAndOneArgv(t *testing.T) {
	t.Parallel()

	app := New()
	if app.agent == nil || app.usage == nil {
		t.Fatal("the application graph is missing the agent or usage command")
	}
	if app.agent.usage != rawArgvCommand(app.usage) {
		t.Fatal("agent usage does not share the top-level usage handler instance")
	}
	handlers := app.routeHandlers()
	for _, token := range []string{"usage", "status", "agent", "ai", "create"} {
		if _, ok := handlers[token]; !ok {
			t.Fatalf("route %q has no handler", token)
		}
	}

	// The argv the canonical spelling hands over is byte-identical to what the
	// legacy spelling receives.
	usage := &recordingArgv{}
	agent := &agentCommand{usage: usage}
	argv := []string{"--model", "codex", "--json"}
	if _, _, err := runRoute(t, agent, append([]string{"usage"}, argv...)...); err != nil {
		t.Fatalf("agent usage error = %v", err)
	}
	if !reflect.DeepEqual(usage.calls[0], argv) {
		t.Fatalf("agent usage forwarded %q, want %q", usage.calls[0], argv)
	}
}

// TestAgentResumeTargetsExactlyOneExistingAgentAndOnlyOfflineOrFailed is
// acceptance criterion 2.
//
// The three properties it pins are: exactly one existing Agent is addressed,
// only Offline and Failed are eligible, and a Running Agent is a usage error
// (exit 2) instead of being reinterpreted as navigation. The last one is why
// the test also asserts nothing was forwarded anywhere: resume never degrades
// into focus, into a picker, or into a new create.
//
// Every row here fails, and every row therefore also re-measures the Phase's
// central guarantee from the selector side: a refusal opens zero registry
// transactions, so no conversation of any kind is started by one.
func TestAgentResumeTargetsExactlyOneExistingAgentAndOnlyOfflineOrFailed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		// phase overrides the phase of the Agent named by args before the run.
		phaseFor map[string]coremetadata.AgentPhase
		args     []string
		// wantUsage is true when the failure must be exit 2.
		wantUsage bool
		// wantEligible is true when the phase gate passes and the route stops
		// on something further down instead.
		wantEligible bool
		want         string
	}{
		{
			// The fixture Agent has no stored conversation, so the phase gate
			// passes and the route then stops on the ref instead. That ordering
			// is the point: Offline is eligible, and what it lacks is a
			// conversation rather than permission.
			name:         "an Offline agent is eligible and stops on its missing session ref",
			args:         []string{"resume", "codex", "--project", "beta"},
			wantEligible: true,
			want:         "has no provider session ref",
		},
		{
			name:         "a Failed agent is eligible too",
			phaseFor:     map[string]coremetadata.AgentPhase{"agt-beta-codex": coremetadata.PhaseFailed},
			args:         []string{"resume", "codex", "--project", "beta"},
			wantEligible: true,
			want:         "has no provider session ref",
		},
		{
			name:         "the uid form addresses the same agent",
			args:         []string{"resume", "uid:agt-beta-codex"},
			wantEligible: true,
			want:         "has no provider session ref",
		},
		{
			name:      "a Running agent is refused, not focused",
			args:      []string{"resume", "codex", "--project", "alpha"},
			wantUsage: true,
			want:      "already owns a managed Pane",
		},
		{
			name:      "a Running agent refusal names focus instead of doing it",
			args:      []string{"resume", "uid:agt-alpha-codex"},
			wantUsage: true,
			want:      "projmux focus pane",
		},
		{
			name:      "a Pending agent is not resumable either",
			phaseFor:  map[string]coremetadata.AgentPhase{"agt-beta-codex": coremetadata.PhasePending},
			args:      []string{"resume", "codex", "--project", "beta"},
			wantUsage: true,
			want:      "resume only rebinds an Offline or Failed Agent",
		},
		{
			name:      "an ambiguous name is refused rather than guessed",
			args:      []string{"resume", "codex"},
			wantUsage: true,
			want:      "want exactly one",
		},
		{
			name:      "a no-match is refused",
			args:      []string{"resume", "nosuch"},
			wantUsage: true,
			want:      "matched no agents",
		},
		{
			name:      "a bare uid is not a selector form",
			args:      []string{"resume", "agt-beta-codex"},
			wantUsage: true,
			want:      "matched no agents",
		},
		{
			name:      "a reference is required",
			args:      []string{"resume"},
			wantUsage: true,
			want:      "requires one Agent reference",
		},
		{
			name:      "a scope alone does not select an agent",
			args:      []string{"resume", "--project", "beta"},
			wantUsage: true,
			want:      "requires one Agent reference",
		},
		{
			name:      "two references are refused",
			args:      []string{"resume", "codex", "other"},
			wantUsage: true,
			want:      "at most one Agent reference",
		},
		{
			name:      "an unknown subcommand is refused",
			args:      []string{"restart"},
			wantUsage: true,
			want:      "agent restart is not available",
		},
		{
			name:      "a subcommand is required",
			args:      nil,
			wantUsage: true,
			want:      "agent requires a subcommand",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			for uid, phase := range test.phaseFor {
				setAgentPhase(t, store, uid, phase)
			}
			before := store.snapshot()
			agent, ai, usage := newTestAgentCommand(t, store)

			stdout, stderr, err := runRoute(t, agent, test.args...)
			if err == nil {
				t.Fatalf("agent %v succeeded, want a refusal", test.args)
			}
			if test.wantUsage && !IsUsageError(err) {
				t.Fatalf("agent %v error is not a usage error: %v", test.args, err)
			}
			if test.wantEligible && IsUsageError(err) {
				t.Fatalf("agent %v was rejected as invalid input: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("agent %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("agent %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			// Resume is never a picker, never a create, and never a focus: no
			// other handler is reached on any path.
			if len(ai.calls) != 0 || len(usage.calls) != 0 {
				t.Fatalf("agent %v forwarded to ai=%q usage=%q, want neither", test.args, ai.calls, usage.calls)
			}
			// Every refusal above is decided against a read-only snapshot, so a
			// half-applied transition can never be left behind.
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("agent %v opened %d transactions and committed %d writes, want 0/0",
					test.args, store.transactions, store.writes)
			}
			if after := store.snapshot(); after != before {
				t.Fatalf("agent %v mutated the registry:\n--- before ---\n%s\n--- after ---\n%s", test.args, before, after)
			}
			_ = stderr
		})
	}
}

// setAgentPhase rewrites one fixture Agent's phase in place.
func setAgentPhase(t *testing.T, store *fakeResourceStore, uid string, phase coremetadata.AgentPhase) {
	t.Helper()
	for i := range store.registry.Agents {
		if store.registry.Agents[i].Metadata.UID != uid {
			continue
		}
		store.registry.Agents[i].Status.Phase = phase
		if phase != coremetadata.PhaseRunning {
			store.registry.Agents[i].Status.PaneRef = ""
		}
		return
	}
	t.Fatalf("fixture has no agent %q", uid)
}

// TestAgentPhasesFollowTheDeclaredPendingRunningOfflineFailedTransitions is the
// Agent state table.
//
// It pins both halves of the lifecycle contract: which transitions the model
// permits, and how a managed-Pane exit classification maps onto a phase. A
// normal exit and an explicit deletion resolve to Offline; a launch failure and
// an abnormal exit resolve to Failed.
func TestAgentPhasesFollowTheDeclaredPendingRunningOfflineFailedTransitions(t *testing.T) {
	t.Parallel()

	phases := coremetadata.AgentPhases()
	if want := []coremetadata.AgentPhase{
		coremetadata.PhasePending, coremetadata.PhaseRunning,
		coremetadata.PhaseOffline, coremetadata.PhaseFailed,
	}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("agent phases = %v, want %v", phases, want)
	}

	for _, test := range []struct {
		from, to coremetadata.AgentPhase
		want     bool
	}{
		// The forward path a created Agent takes.
		{coremetadata.PhasePending, coremetadata.PhaseRunning, true},
		{coremetadata.PhaseRunning, coremetadata.PhaseOffline, true},
		{coremetadata.PhaseRunning, coremetadata.PhaseFailed, true},
		{coremetadata.PhasePending, coremetadata.PhaseFailed, true},
		// Offline and Failed stay resumable: that is what makes an Agent outlive
		// its Pane.
		{coremetadata.PhaseOffline, coremetadata.PhasePending, true},
		{coremetadata.PhaseOffline, coremetadata.PhaseRunning, true},
		{coremetadata.PhaseFailed, coremetadata.PhasePending, true},
		{coremetadata.PhaseFailed, coremetadata.PhaseRunning, true},
		// A Running Agent never walks back to Pending; resume is the only way in
		// and it refuses a Running target.
		{coremetadata.PhaseRunning, coremetadata.PhasePending, false},
	} {
		if got := coremetadata.CanTransitionAgent(test.from, test.to); got != test.want {
			t.Fatalf("CanTransitionAgent(%s, %s) = %v, want %v", test.from, test.to, got, test.want)
		}
	}

	for _, test := range []struct {
		exit coremetadata.AgentExit
		want coremetadata.AgentPhase
	}{
		{coremetadata.AgentExitNormal, coremetadata.PhaseOffline},
		{coremetadata.AgentExitDeleted, coremetadata.PhaseOffline},
		{coremetadata.AgentExitAbnormal, coremetadata.PhaseFailed},
		{coremetadata.AgentExitLaunchFailure, coremetadata.PhaseFailed},
	} {
		got, ok := test.exit.Phase()
		if !ok || got != test.want {
			t.Fatalf("exit %q resolved to %q/%v, want %q", test.exit, got, ok, test.want)
		}
	}
	if _, ok := coremetadata.AgentExit("teleported").Phase(); ok {
		t.Fatal("an unknown exit classification resolved to a phase")
	}
	// The resume gate consumes exactly the two resumable phases.
	if !reflect.DeepEqual(resumableAgentPhases, []coremetadata.AgentPhase{coremetadata.PhaseOffline, coremetadata.PhaseFailed}) {
		t.Fatalf("resumable phases = %v, want [Offline Failed]", resumableAgentPhases)
	}
}

// TestAPaneExitAndAnExplicitDeletePaneLeaveTheAgentOfflineNotDeleted is
// acceptance criterion 4, measured on both paths that end a managed Pane.
//
// The assertion is that the Agent resource survives with its uid, name, and
// owner intact and only its phase and paneRef change, because an Agent that
// disappeared with its Pane could never be resumed.
func TestAPaneExitAndAnExplicitDeletePaneLeaveTheAgentOfflineNotDeleted(t *testing.T) {
	t.Parallel()

	t.Run("a normal managed pane exit", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		registry := store.registry.Clone()
		agent, err := store.mutator().ReleaseAgentPane(&registry, "agt-alpha-codex", coremetadata.AgentExitNormal, "")
		if err != nil {
			t.Fatalf("ReleaseAgentPane: %v", err)
		}
		assertAgentSurvivedAsOffline(t, registry, agent.Metadata.UID)
	})

	t.Run("an abnormal exit fails the agent but still keeps it", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		registry := store.registry.Clone()
		if _, err := store.mutator().ReleaseAgentPane(&registry, "agt-alpha-codex", coremetadata.AgentExitAbnormal, "crashed"); err != nil {
			t.Fatalf("ReleaseAgentPane: %v", err)
		}
		survivor, ok := registry.Agent("agt-alpha-codex")
		if !ok {
			t.Fatal("an abnormal exit deleted the Agent")
		}
		if survivor.Status.Phase != coremetadata.PhaseFailed {
			t.Fatalf("phase = %q, want Failed", survivor.Status.Phase)
		}
		if _, ok := registry.Pane("pan-alpha-codex"); ok {
			t.Fatal("the managed Pane survived its own exit")
		}
	})

	t.Run("an explicit delete pane through the public route", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		_, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
			"pane", "codex-pane", "--project", "alpha", "--window", "main", "--yes")
		if err != nil {
			t.Fatalf("delete pane error = %v", err)
		}
		assertAgentSurvivedAsOffline(t, store.registry, "agt-alpha-codex")

		// A resumable Agent is exactly what the resume gate then accepts, which
		// is what makes the two criteria one lifecycle rather than two rules.
		agent, _, _ := newTestAgentCommand(t, store)
		_, _, err = runRoute(t, agent, "resume", "uid:agt-alpha-codex")
		if err == nil || IsUsageError(err) {
			t.Fatalf("resume of the now-Offline agent = %v, want a non-usage state failure", err)
		}
		if !strings.Contains(err.Error(), "has no provider session ref") {
			t.Fatalf("resume of the now-Offline agent = %q, want the missing-ref refusal", err)
		}
	})
}

func assertAgentSurvivedAsOffline(t *testing.T, registry coremetadata.Registry, uid string) {
	t.Helper()
	agent, ok := registry.Agent(uid)
	if !ok {
		t.Fatalf("agent %q was deleted along with its managed Pane", uid)
	}
	if agent.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("agent %q phase = %q, want Offline", uid, agent.Status.Phase)
	}
	if agent.Status.PaneRef != "" {
		t.Fatalf("agent %q still points at pane %q", uid, agent.Status.PaneRef)
	}
	if agent.Metadata.Name == "" || agent.Metadata.OwnerRef == nil {
		t.Fatalf("agent %q lost its identity or owner: %#v", uid, agent.Metadata)
	}
	if _, ok := registry.Pane("pan-alpha-codex"); ok {
		t.Fatalf("the managed Pane of agent %q survived", uid)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("the registry no longer validates after the pane ended: %v", err)
	}
}

// TestAgentRoutesStayOnTheCurrentPreDispatchBehavior records where the Agent
// namespace sits relative to the legacy-hook filesystem migration.
//
// `agent` and `create` are aliases over handlers that run it today, so they
// keep running it: the read verbs are the ones the contract exempts, and
// silently changing a mutation route's pre-dispatch behavior would be a
// parity break in the other direction.
func TestAgentRoutesStayOnTheCurrentPreDispatchBehavior(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{
		{"agent", "status"},
		{"agent", "topic", "set", "review"},
		{"agent", "usage"},
		{"create", "agent", "--provider", "codex"},
		{"create", "pane"},
		{"create", "codex"},
	} {
		if !shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = false, want the same behavior as the spelling it aliases", argv)
		}
	}
	// Help still short-circuits everything, at every depth of the new nodes.
	for _, argv := range [][]string{
		{"agent", "--help"},
		{"agent", "resume", "--help"},
		{"create", "agent", "--help"},
		{"create", "codex", "-h"},
	} {
		if shouldRunLegacyHookMigrations(argv) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = true, want the help boundary to short-circuit", argv)
		}
	}
}
