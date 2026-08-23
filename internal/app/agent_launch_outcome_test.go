package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestAgentLaunchOutcomeTableIsClosedAndPrintable(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"pre-runtime failure":     true,
		"created+acknowledged":    true,
		"created+unconfirmed":     true,
		"delayed acknowledgement": true,
	}
	rows := agentLaunchOutcomeTable
	if len(rows) != len(want) {
		t.Fatalf("outcome rows = %d, want %d: %#v", len(rows), len(want), rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !want[row.Outcome] || seen[row.Outcome] {
			t.Fatalf("outcome %q is outside the closed set or duplicated", row.Outcome)
		}
		seen[row.Outcome] = true
		for name, value := range map[string]string{
			"outcome": row.Outcome, "rc": row.RC, "stdout": row.Stdout,
			"resources": row.Resources, "activation": row.Activation, "diagnostic": row.Diagnostic,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s row has an empty %s cell: %#v", row.Outcome, name, row)
			}
		}
		printed := fmt.Sprintf("%s | rc=%s | stdout=%s | resources=%s | activation=%s | diagnostic=%s",
			row.Outcome, row.RC, row.Stdout, row.Resources, row.Activation, row.Diagnostic)
		for _, value := range []string{row.Outcome, row.RC, row.Stdout, row.Resources, row.Activation, row.Diagnostic} {
			if !strings.Contains(printed, value) {
				t.Fatalf("printed row %q omitted %q", printed, value)
			}
		}
	}
}

func TestDelayedProviderActivationReturnsValidatedPaneHandle(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{aiModeCodex, aiModeClaude} {
		t.Run(provider, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)
			// Installed providers can spend roughly three seconds reaching
			// SessionStart before the initial-task hook itself is delayed. The
			// total exceeds five seconds while each contract stage stays bounded.
			launcher.activationStartupDelay = 3 * time.Second
			launcher.activationDelay = 2100 * time.Millisecond

			stdout, _, err := runRoute(t, create,
				"agent", "--provider", provider, "--project", "uid:prj-alpha",
				"--window", "uid:win-alpha-review", "-o", "pane-id", "--", "initial task")
			if err != nil {
				t.Fatalf("delayed acknowledged create: %v", err)
			}
			if len(launcher.startupTimeouts) != 1 || launcher.startupTimeouts[0] != agentActivationStartupDeadline {
				t.Fatalf("startup deadline = %v, want %v", launcher.startupTimeouts, agentActivationStartupDeadline)
			}
			if len(launcher.activationTimeouts) != 1 || launcher.activationTimeouts[0] != agentActivationAcknowledgementDeadline {
				t.Fatalf("acknowledgement deadline = %v, want %v", launcher.activationTimeouts, agentActivationAcknowledgementDeadline)
			}
			if launcher.activationDelay <= 2*time.Second || launcher.activationDelay >= agentActivationAcknowledgementDeadline ||
				launcher.activationStartupDelay+launcher.activationDelay <= 5*time.Second {
				t.Fatalf("fixture startup/delay = %v/%v, want >2s acknowledgement and >5s total within independent bounds",
					launcher.activationStartupDelay, launcher.activationDelay)
			}
			if !regexp.MustCompile(`^%[0-9]+\n$`).MatchString(stdout) {
				t.Fatalf("stdout = %q, want one exact %%N line", stdout)
			}
			agent := agentNamed(t, store, "win-alpha-review", provider)
			if agent.Status.Activation.State != coremetadata.ActivationAcknowledged ||
				agent.Status.Activation.Source != string(coremetadata.InteractionSourceProviderHook) {
				t.Fatalf("activation = %+v", agent.Status.Activation)
			}
		})
	}
}

func TestExactProviderStartupOpensIndependentAcknowledgementWindow(t *testing.T) {
	for _, provider := range []string{aiModeCodex, aiModeClaude} {
		t.Run(provider, func(t *testing.T) {
			h := newSessionRefHarness(t, provider)
			agent, _ := h.registry.Agent(h.agentUID)
			agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
			runner := &activationAuthorityRunner{paneUID: h.paneUID}
			now := sessionRefObservedAt
			h.cmd.now = func() time.Time { return now }
			startupObserved := false
			acknowledgementObserved := false
			h.cmd.sleep = func(d time.Duration) {
				now = now.Add(d)
				elapsed := now.Sub(sessionRefObservedAt)
				if !startupObserved && elapsed >= 3*time.Second {
					startupObserved = true
					h.ingest(t, []string{provider + "-hook"}, providerStartupPayload(provider))
					activation := h.agent(t).Status.Activation
					if activation.State != coremetadata.ActivationPending ||
						activation.Source != string(coremetadata.InteractionSourceProviderHook) {
						t.Fatalf("SessionStart activation = %+v, want exact pending readiness", activation)
					}
				}
				if startupObserved && !acknowledgementObserved && elapsed >= 5100*time.Millisecond {
					acknowledgementObserved = true
					h.ingest(t, []string{provider + "-hook"}, providerPromptPayload(provider))
				}
			}

			acknowledged, source, err := h.cmd.AwaitAgentActivation(context.Background(), runner, "%7",
				agentActivationStartupDeadline, agentActivationAcknowledgementDeadline)
			if err != nil || !acknowledged || source != string(coremetadata.InteractionSourceProviderHook) {
				t.Fatalf("staged activation acknowledged=%t source=%q err=%v", acknowledged, source, err)
			}
			if elapsed := now.Sub(sessionRefObservedAt); elapsed <= agentActivationAcknowledgementDeadline || elapsed >= agentActivationStartupDeadline+agentActivationAcknowledgementDeadline {
				t.Fatalf("total staged wait = %v, want >5s and <10s", elapsed)
			}
		})
	}
}

func TestCodexSessionStartRuntimeNotifyPreservesQueueWithoutAcknowledging(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	agent, _ := h.registry.Agent(h.agentUID)
	agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
	home, err := h.cmd.homeDir()
	if err != nil {
		t.Fatal(err)
	}
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
		Version: 1,
		Providers: map[string]config.AIHookProviderActions{
			aiHookProviderCodex: {Events: map[string]string{"SessionStart": aiHookActionNotify}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	h.cmd.readFile = os.ReadFile
	bindingRead := h.cmd.readCommand
	notifyRead := codexHookIngestReadCommand("%7")
	h.cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if out, err := bindingRead(ctx, name, args...); err == nil {
			return out, nil
		}
		return notifyRead(ctx, name, args...)
	}
	queue := &stubNotifyStore{}
	h.cmd.producer = &storeAttentionNotifyProducer{store: queue, ttl: time.Minute}

	h.ingest(t, []string{"codex-hook"}, providerStartupPayload(aiModeCodex))
	if len(queue.pushed) != 1 {
		t.Fatalf("SessionStart queue pushes = %d, want 1: %#v", len(queue.pushed), queue.pushed)
	}
	if got := queue.pushed[0].Metadata["event"]; got != "SessionStart" {
		t.Fatalf("SessionStart queue event = %q, want SessionStart", got)
	}
	activation := h.agent(t).Status.Activation
	if activation.State != coremetadata.ActivationPending ||
		activation.Source != string(coremetadata.InteractionSourceProviderHook) || activation.ObservedAt.IsZero() {
		t.Fatalf("runtime-notify SessionStart activation = %+v, want pending readiness", activation)
	}

	h.ingest(t, []string{"codex-hook"}, providerPromptPayload(aiModeCodex))
	activation = h.agent(t).Status.Activation
	if activation.State != coremetadata.ActivationAcknowledged ||
		activation.Source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("exact UserPromptSubmit activation = %+v, want acknowledged", activation)
	}
}

func TestWrongProviderSessionStartCannotOpenReadiness(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	agent, _ := h.registry.Agent(h.agentUID)
	agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}

	h.ingest(t, []string{"claude-hook"}, providerStartupPayload(aiModeClaude))
	activation := h.agent(t).Status.Activation
	if activation.State != coremetadata.ActivationPending || activation.Source != "" || !activation.ObservedAt.IsZero() {
		t.Fatalf("wrong-provider SessionStart opened readiness: %+v", activation)
	}
}

func TestActivationStartupAndAcknowledgementStagesAreIndependentlyBounded(t *testing.T) {
	for _, test := range []struct {
		name           string
		startupAt      time.Duration
		wantTotalBound time.Duration
	}{
		{name: "provider never starts", wantTotalBound: agentActivationStartupDeadline},
		{name: "provider starts but never acknowledges", startupAt: 3 * time.Second, wantTotalBound: 8 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newSessionRefHarness(t, aiModeClaude)
			agent, _ := h.registry.Agent(h.agentUID)
			agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
			runner := &activationAuthorityRunner{paneUID: h.paneUID}
			now := sessionRefObservedAt
			h.cmd.now = func() time.Time { return now }
			startupObserved := false
			duplicateStartupObserved := false
			h.cmd.sleep = func(d time.Duration) {
				now = now.Add(d)
				if test.startupAt > 0 && !startupObserved && now.Sub(sessionRefObservedAt) >= test.startupAt {
					startupObserved = true
					h.ingest(t, []string{"claude-hook"}, providerStartupPayload(aiModeClaude))
				}
				if startupObserved && !duplicateStartupObserved && now.Sub(sessionRefObservedAt) >= test.startupAt+time.Second {
					duplicateStartupObserved = true
					h.ingest(t, []string{"claude-hook"}, providerStartupPayload(aiModeClaude))
				}
			}

			acknowledged, _, err := h.cmd.AwaitAgentActivation(context.Background(), runner, "%7",
				agentActivationStartupDeadline, agentActivationAcknowledgementDeadline)
			if err != nil || acknowledged {
				t.Fatalf("bounded wait acknowledged=%t err=%v", acknowledged, err)
			}
			if elapsed := now.Sub(sessionRefObservedAt); elapsed != test.wantTotalBound {
				t.Fatalf("bounded wait = %v, want %v", elapsed, test.wantTotalBound)
			}
		})
	}
}

func TestStaleGenerationSessionStartCannotOpenAcknowledgementWindow(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	agent, _ := h.registry.Agent(h.agentUID)
	agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
	mutator := coremetadata.Mutator{Now: func() time.Time { return sessionRefObservedAt }}
	if _, err := mutator.RecordPaneActivation(h.registry, h.paneUID, coremetadata.PaneActivationOptions{
		Generation: "gen-replacement", AgentUID: h.agentUID, OperationID: "op-replacement",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.ObservePaneActivationRuntime(h.registry, h.paneUID, "gen-replacement", "%7"); err != nil {
		t.Fatal(err)
	}

	// The hook still carries gen-session-ref from the replaced provider process.
	h.ingest(t, []string{"codex-hook"}, providerStartupPayload(aiModeCodex))
	activation := h.agent(t).Status.Activation
	if activation.State != coremetadata.ActivationPending || activation.Source != "" || !activation.ObservedAt.IsZero() {
		t.Fatalf("stale SessionStart opened acknowledgement window: %+v", activation)
	}
}

func providerStartupPayload(provider string) string {
	if provider == aiModeClaude {
		return `{"hook_event_name":"SessionStart","session_id":"claude-session-1","cwd":"/src/app"}`
	}
	return `{"hook_event_name":"SessionStart","thread_id":"codex-thread-1","session_id":"codex-session-1","cwd":"/src/app"}`
}

func providerPromptPayload(provider string) string {
	if provider == aiModeClaude {
		return `{"hook_event_name":"UserPromptSubmit","session_id":"claude-session-1","cwd":"/src/app"}`
	}
	return `{"hook_event_name":"UserPromptSubmit","thread_id":"codex-thread-1","session_id":"codex-session-1","cwd":"/src/app"}`
}

func TestActivationAcknowledgementWinsTheTimeoutRace(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
	launcher.activationAcknowledged = false
	launcher.activationBeforeReturn = func() {
		if _, err := create.store.update(func(registry *coremetadata.Registry) error {
			for i := range registry.Agents {
				agent := &registry.Agents[i]
				if agent.Metadata.OwnerRef.UID == "win-alpha-review" && agent.Metadata.Name == "codex" {
					_, err := create.store.mutator().SetAgentActivation(registry, agent.Metadata.UID,
						coremetadata.ActivationAcknowledged, string(coremetadata.InteractionSourceProviderHook), "")
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("provider acknowledgement commit: %v", err)
		}
	}

	stdout, _, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "uid:prj-alpha",
		"--window", "uid:win-alpha-review", "-o", "pane-id", "--", "initial task")
	if err != nil {
		t.Fatalf("timeout race returned failure after acknowledgement: %v", err)
	}
	if !regexp.MustCompile(`^%[0-9]+\n$`).MatchString(stdout) {
		t.Fatalf("stdout = %q, want exact acknowledged Pane handle", stdout)
	}
	committed := agentNamed(t, store, "win-alpha-review", "codex")
	if committed.Status.Activation.State != coremetadata.ActivationAcknowledged || committed.Status.Activation.Reason != "" {
		t.Fatalf("timeout writer downgraded acknowledgement: %+v", committed.Status.Activation)
	}
}

func TestLateActivationRefinesOnlyTheSameBindingGeneration(t *testing.T) {
	t.Parallel()
	h := newSessionRefHarness(t, aiModeCodex)
	for i := range h.registry.Agents {
		if h.registry.Agents[i].Metadata.UID == h.agentUID {
			h.registry.Agents[i].Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationUnconfirmed}
		}
	}
	mutator := coremetadata.Mutator{Now: func() time.Time { return sessionRefObservedAt }}
	if _, err := mutator.RecordPaneActivation(h.registry, h.paneUID, coremetadata.PaneActivationOptions{
		Generation: "gen-replacement", AgentUID: h.agentUID, OperationID: "op-replacement",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.ObservePaneActivationRuntime(h.registry, h.paneUID, "gen-replacement", "%7"); err != nil {
		t.Fatal(err)
	}

	// The hook inherited the generation of the process that was replaced. Even
	// though the durable Pane uid, Agent uid and raw %N are all reused, that
	// evidence cannot acknowledge the replacement.
	if _, managed, err := h.cmd.persistManagedAgentInteractionWithActivationPolicy("%7", coremetadata.InteractionInProgress,
		string(coremetadata.InteractionSourceProviderHook), true); err != nil || !managed {
		t.Fatalf("stale hook projection managed=%t err=%v", managed, err)
	}
	if got := h.agent(t).Status.Activation.State; got != coremetadata.ActivationUnconfirmed {
		t.Fatalf("stale generation refined replacement to %q", got)
	}

	// RuntimeID is part of the same exact binding guard.
	baseEnv := h.cmd.lookupEnv
	h.cmd.lookupEnv = func(name string) string {
		switch name {
		case internalActivationPaneUIDEnv:
			return h.paneUID
		case internalActivationGenerationEnv:
			return "gen-replacement"
		default:
			return baseEnv(name)
		}
	}
	pane, _ := h.registry.Pane(h.paneUID)
	pane.Status.Activation.RuntimeID = "%99"
	if _, managed, err := h.cmd.persistManagedAgentInteractionWithActivationPolicy("%7", coremetadata.InteractionInProgress,
		string(coremetadata.InteractionSourceProviderHook), true); err != nil || !managed {
		t.Fatalf("wrong-runtime hook projection managed=%t err=%v", managed, err)
	}
	if got := h.agent(t).Status.Activation.State; got != coremetadata.ActivationUnconfirmed {
		t.Fatalf("wrong runtime handle refined activation to %q", got)
	}

	pane.Status.Activation.RuntimeID = "%7"
	if _, managed, err := h.cmd.persistManagedAgentInteractionWithActivationPolicy("%7", coremetadata.InteractionInProgress,
		string(coremetadata.InteractionSourceProviderHook), true); err != nil || !managed {
		t.Fatalf("exact hook projection managed=%t err=%v", managed, err)
	}
	if got := h.agent(t).Status.Activation.State; got != coremetadata.ActivationAcknowledged {
		t.Fatalf("same exact binding did not refine activation: %q", got)
	}
}

func TestRoadmapWorkerLaunchUsesExactOwnerPane(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	window := seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	owner := newFakeTmuxPane(tmux.mint("%"))
	owner.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
	window.panes = append(window.panes, owner)
	create, _ := newTestAgentCreateCommand(t, store, tmux)

	stdout, _, err := runRoute(t, create,
		"agent", "--provider", "codex",
		"--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "--pane", "uid:pan-alpha-codex",
		"-o", "pane-id", "--", "Phase 16")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^%[0-9]+\n$`).MatchString(stdout) {
		t.Fatalf("stdout = %q", stdout)
	}
	found := false
	for _, call := range tmux.calls {
		argv := tmuxCommandArgv(call)
		if len(argv) > 0 && argv[0] == "split-window" {
			found = true
			if got := flagValue(argv, "-t"); got != owner.id {
				t.Fatalf("split anchor = %q, want current owner Pane %q", got, owner.id)
			}
		}
	}
	if !found {
		t.Fatal("worker launch issued no split")
	}

	for _, test := range []struct {
		name string
		pane string
	}{
		{name: "missing", pane: "uid:pane-missing"},
		{name: "cross-window", pane: "uid:pan-alpha-review"},
		{name: "foreign-project", pane: "uid:pan-beta-zsh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestAgentCreateCommand(t, store, tmux)
			before, writes, panes := store.snapshot(), store.writes, tmux.paneCount()
			out, _, err := runRoute(t, create,
				"agent", "--provider", "codex", "--project", "uid:prj-alpha",
				"--window", "uid:win-alpha-main", "--pane", test.pane, "-o", "pane-id", "--", "Phase 16")
			if err == nil || out != "" {
				t.Fatalf("invalid exact anchor stdout/error = %q / %v", out, err)
			}
			if store.snapshot() != before || store.writes != writes || tmux.paneCount() != panes || tmux.argvContains("split-window") || tmux.argvContains("new-window") {
				t.Fatalf("invalid exact anchor mutated state: writes=%d panes=%d registry=%s", store.writes-writes, tmux.paneCount()-panes, store.snapshot())
			}
		})
	}
}

func TestWindowOnlyCreateNeverGuessesAroundADeadPrimaryAnchor(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	// The Window is live, but only the Agent-owned alternate Pane is live. Its
	// primary shell Pane remains in Registry and has no runtime binding.
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-codex")
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	before, panes := store.snapshot(), tmux.paneCount()

	stdout, _, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id")
	if err == nil || stdout != "" {
		t.Fatalf("dead-primary create stdout/error = %q / %v", stdout, err)
	}
	if store.snapshot() != before || store.writes != 0 || tmux.paneCount() != panes || tmux.argvContains("split-window") {
		raw, _ := json.Marshal(store.registry)
		t.Fatalf("dead primary guessed alternate: writes=%d panes=%d registry=%s", store.writes, tmux.paneCount(), raw)
	}
}
