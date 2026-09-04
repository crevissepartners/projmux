package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// newInteractiveOnlyCodexCreate wires one canonical Agent create whose native
// controller records every call and would succeed if it were reached. A test
// that asks for `--interactive-only` and still sees a recorded call has proved
// the opt-out leaked, rather than only that no thread was bound.
func newInteractiveOnlyCodexCreate(t *testing.T) (*createCommand, *fakeResourceStore, *fakeTmux, *fakeAgentLauncher, *fakeNativeThreadController, *fakeNativePaneLauncher) {
	t.Helper()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, legacy := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{
		createBinding: codexappserver.ThreadBinding{ThreadID: "thread-must-not-bind", TurnID: "turn-must-not-bind"},
		resumeBinding: codexappserver.ThreadBinding{ThreadID: "thread-must-not-bind"},
	}
	panes := &fakeNativePaneLauncher{}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
	return create, store, tmux, legacy, native, panes
}

// TestPromptedNativeCodexCreateIssuesOneTurnAndNeverRepeatsThePromptInPaneArgv
// pins the payload accounting of a default native create.
//
// One `--` operand is one native turn on one thread. The launched Pane joins
// that thread and must not carry the prompt as argv, because a prompt that
// reaches both the app-server turn and the provider's own command line is the
// same instruction submitted twice.
func TestPromptedNativeCodexCreateIssuesOneTurnAndNeverRepeatsThePromptInPaneArgv(t *testing.T) {
	const prompt = "exactly-one-payload-token"
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "canonical", args: []string{"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", prompt}},
		{name: "provider shortcut", args: []string{"codex", "--project", "alpha", "--window", "main", "--", prompt}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, legacy := newTestAgentCreateCommand(t, store, tmux)
			native := &fakeNativeThreadController{
				createBinding: codexappserver.ThreadBinding{ThreadID: "thread-one-turn", TurnID: "turn-one-turn"},
			}
			panes := &fakeNativePaneLauncher{}
			create.codexNative = native
			create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}

			stdout, stderr, err := runRoute(t, create, test.args...)
			if err != nil || stderr != "" || strings.TrimSpace(stdout) == "" {
				t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}

			// Exactly one native create carrying exactly the operand, and no
			// resume: one payload is one turn on one thread.
			if len(native.creates) != 1 || native.creates[0].prompt != prompt || len(native.resumes) != 0 {
				t.Fatalf("native creates=%+v resumes=%+v", native.creates, native.resumes)
			}
			// The hook acknowledgement lane is the other way a prompt gets
			// submitted. The plain argv is still constructed before the native
			// attempt, but a native turn must neither launch it nor open the
			// hook acknowledgement window behind it.
			if len(legacy.activationPanes) != 0 || len(legacy.bound) != 0 {
				t.Fatalf("native turn also opened the hook prompt lane: activation=%v bound=%+v", legacy.activationPanes, legacy.bound)
			}
			calls := splitWindowCalls(tmux)
			if len(calls) != 1 {
				t.Fatalf("native create issued %d splits, want exactly one: %v", len(calls), calls)
			}
			joined := strings.Join(calls[0], " ")
			if strings.Contains(joined, prompt) {
				t.Fatalf("Pane argv repeated the prompt: %v", calls[0])
			}
			if !strings.Contains(joined, "thread-one-turn") {
				t.Fatalf("Pane argv did not join the created thread: %v", calls[0])
			}
			if len(panes.bound) != 1 || panes.bound[0].threadID != "thread-one-turn" {
				t.Fatalf("native Pane bindings = %+v", panes.bound)
			}
			raw, marshalErr := json.Marshal(store.registry)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(raw), prompt) {
				t.Fatalf("prompt persisted in the Registry: %s", raw)
			}
		})
	}
}

// TestInteractiveOnlyIsTheOnlyPlainCodexLaneAndBothSpellingsAreEquivalent is
// the public surface of the opt-out.
//
// `--interactive-only` remains the explicit way to opt a prompted create out
// of native control. The permanent payload-free fallback does not change its
// spelling, argv, stdout, or other-provider refusal contract.
func TestInteractiveOnlyIsTheOnlyPlainCodexLaneAndBothSpellingsAreEquivalent(t *testing.T) {
	t.Run("canonical and shortcut produce equivalent output on one plain lane", func(t *testing.T) {
		var outputs []string
		for _, args := range [][]string{
			{"agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "main", "--", "interactive task"},
			{"codex", "--interactive-only", "--project", "alpha", "--window", "main", "--", "interactive task"},
		} {
			create, store, tmux, legacy, native, panes := newInteractiveOnlyCodexCreate(t)
			stdout, stderr, err := runRoute(t, create, args...)
			if err != nil || stderr != "" {
				t.Fatalf("%v: stdout=%q stderr=%q err=%v", args, stdout, stderr, err)
			}
			outputs = append(outputs, stdout)

			// No native authority is consulted at all: the operator said this
			// Agent has none.
			if len(native.creates) != 0 || len(native.resumes) != 0 {
				t.Fatalf("%v consulted the native controller: creates=%+v resumes=%+v", args, native.creates, native.resumes)
			}
			if len(panes.plans) != 0 || len(panes.bound) != 0 || len(panes.lifecycle) != 0 {
				t.Fatalf("%v gained native Pane state: plans=%+v bound=%+v lifecycle=%+v", args, panes.plans, panes.bound, panes.lifecycle)
			}
			// The plain CLI lane keeps its whole contract: the payload is the
			// provider's initial task and the hook acknowledges it.
			if len(legacy.plans) != 1 || !slices.Equal(legacy.plans[0].payload, []string{"interactive task"}) ||
				len(legacy.bound) != 1 || len(legacy.activationPanes) != 1 {
				t.Fatalf("%v plain lane = plans:%+v bound:%+v activation:%v", args, legacy.plans, legacy.bound, legacy.activationPanes)
			}
			calls := splitWindowCalls(tmux)
			if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "interactive task") {
				t.Fatalf("%v split argv = %v, want one plain Codex CLI launch carrying the payload", args, calls)
			}
			if strings.Contains(strings.Join(calls[0], " "), "--remote") {
				t.Fatalf("%v launched a native remote resume: %v", args, calls[0])
			}
			agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
			pane, ok := store.registry.Pane(agent.Status.PaneRef)
			if !ok || agent.Status.SessionRef != nil || pane.Status.Activation.Codex != nil {
				t.Fatalf("%v bound native identity: agent=%#v pane=%#v", args, agent.Status, pane.Status.Activation)
			}
			if agent.Status.Activation.Source != string(coremetadata.InteractionSourceProviderHook) {
				t.Fatalf("%v changed the hook activation contract: %#v", args, agent.Status.Activation)
			}
		}
		if outputs[0] != outputs[1] {
			t.Fatalf("canonical stdout %q != shortcut stdout %q", outputs[0], outputs[1])
		}
	})

	t.Run("the manifest advertises the flag on both spellings and on neither other provider", func(t *testing.T) {
		var create cli.Route
		for _, route := range cli.Routes() {
			if route.Name == "create" {
				create = route
			}
		}
		if create.Name == "" {
			t.Fatal("create is not in the manifest")
		}
		advertised := func(usage []string) bool {
			return slices.ContainsFunc(usage, func(line string) bool { return strings.Contains(line, "--interactive-only") })
		}
		children := map[string]cli.Route{}
		for _, child := range create.Children {
			children[child.Name] = child
		}
		for _, name := range []string{"agent", "codex"} {
			child, ok := children[name]
			if !ok || !advertised(child.Usage) {
				t.Fatalf("create %s usage does not advertise --interactive-only: %v", name, child.Usage)
			}
		}
		for _, name := range []string{"claude", "antigravity", "pane", "window"} {
			if child, ok := children[name]; ok && advertised(child.Usage) {
				t.Fatalf("create %s advertises a Codex-only flag: %v", name, child.Usage)
			}
		}
		var agentLine, codexLine, otherProviderLine string
		for _, line := range create.Usage {
			switch {
			case strings.HasPrefix(line, "projmux create agent "):
				agentLine = line
			case strings.HasPrefix(line, "projmux create codex "):
				codexLine = line
			case strings.HasPrefix(line, "projmux create claude|antigravity "):
				otherProviderLine = line
			}
		}
		if !strings.Contains(agentLine, "--interactive-only") || !strings.Contains(codexLine, "--interactive-only") {
			t.Fatalf("top-level create usage lines = %v", create.Usage)
		}
		if otherProviderLine == "" || strings.Contains(otherProviderLine, "--interactive-only") {
			t.Fatalf("top-level non-Codex shortcut line = %q", otherProviderLine)
		}
	})

	t.Run("help renders the flag identically for both spellings", func(t *testing.T) {
		render := func(argv ...string) string {
			t.Helper()
			var stdout, stderr bytes.Buffer
			if err := (&App{}).Run(argv, &stdout, &stderr); err != nil {
				t.Fatalf("%v help error = %v", argv, err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("%v help stderr = %q", argv, stderr.String())
			}
			return stdout.String()
		}
		for _, argv := range [][]string{{"create", "agent", "--help"}, {"create", "codex", "--help"}} {
			if !strings.Contains(render(argv...), "--interactive-only") {
				t.Fatalf("%v help does not name --interactive-only", argv)
			}
		}
		if strings.Contains(render("create", "claude", "--help"), "--interactive-only") {
			t.Fatalf("create claude help names a Codex-only flag")
		}
	})

	t.Run("a non-Codex provider refuses the flag before the transaction", func(t *testing.T) {
		for _, provider := range []string{"claude", "antigravity"} {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, legacy := newTestAgentCreateCommand(t, store, tmux)
			before, panes := store.snapshot(), tmux.paneCount()

			stdout, _, err := runRoute(t, create,
				"agent", "--provider", provider, "--interactive-only", "--project", "alpha", "--window", "main", "--", "task")
			if err == nil || !IsUsageError(err) || stdout != "" {
				t.Fatalf("%s --interactive-only stdout=%q err=%v, want a usage error", provider, stdout, err)
			}
			if !strings.Contains(err.Error(), "--interactive-only") || !strings.Contains(err.Error(), provider) {
				t.Fatalf("%s refusal = %q", provider, err)
			}
			if store.snapshot() != before || store.writes != 0 || store.transactions != 0 ||
				tmux.paneCount() != panes || len(legacy.plans) != 0 || tmux.argvContains("split-window") {
				t.Fatalf("%s --interactive-only mutated state: transactions=%d writes=%d plans=%+v",
					provider, store.transactions, store.writes, legacy.plans)
			}
		}
	})

	t.Run("the shortcut refuses the flag for a provider it does not name", func(t *testing.T) {
		store := newFakeResourceStore(t)
		create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
		stdout, _, err := runRoute(t, create, "claude", "--interactive-only", "--project", "alpha", "--window", "main")
		if err == nil || !IsUsageError(err) || stdout != "" {
			t.Fatalf("create claude --interactive-only stdout=%q err=%v, want a usage error", stdout, err)
		}
	})
}

// TestDefaultNativeCodexFanOutRefusesWithZeroMutationsAndInteractiveOnlyKeepsCardinality
// is the multi-Window transaction fixture.
//
// A native create owns one app-server thread, and a Registry rollback cannot
// delete threads, so a prompted create whose selector resolved several Windows
// has no atomic native shape. Silently dropping every target onto the plain CLI
// lane is the degradation this route no longer performs, so the fan-out refuses
// before the first allocation. `--interactive-only` still asks for that
// fan-out on purpose, and keeps its existing Agent-per-Window cardinality.
func TestDefaultNativeCodexFanOutRefusesWithZeroMutationsAndInteractiveOnlyKeepsCardinality(t *testing.T) {
	t.Run("default native fan-out refuses before any allocation", func(t *testing.T) {
		create, store, tmux, legacy, native, panes := newInteractiveOnlyCodexCreate(t)
		before, panesBefore, windows := store.snapshot(), tmux.paneCount(), tmux.windowCount()

		stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "-o", "pane-id", "--", "fan-out prompt")
		if err == nil || stdout != "" || stderr != "" {
			t.Fatalf("fan-out stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		if !IsUsageError(err) {
			t.Fatalf("fan-out refusal is not a usage error (exit 2): %v", err)
		}
		for _, required := range []string{"--interactive-only", "one native Codex thread"} {
			if !strings.Contains(err.Error(), required) {
				t.Fatalf("fan-out refusal = %q, missing %q", err, required)
			}
		}
		// Zero mutations of every kind: no thread, no Pane, no Registry write,
		// no tmux object, and no launch plan turned into a running process.
		if len(native.creates) != 0 || len(native.resumes) != 0 {
			t.Fatalf("refused fan-out created a thread: %+v / %+v", native.creates, native.resumes)
		}
		if store.snapshot() != before || store.writes != 0 {
			t.Fatalf("refused fan-out wrote the Registry: writes=%d\n%s", store.writes, store.snapshot())
		}
		if tmux.paneCount() != panesBefore || tmux.windowCount() != windows ||
			tmux.argvContains("split-window") || tmux.argvContains("new-window") || tmux.argvContains("new-session") {
			t.Fatalf("refused fan-out mutated tmux: panes=%d windows=%d", tmux.paneCount()-panesBefore, tmux.windowCount()-windows)
		}
		if len(legacy.bound) != 0 || len(legacy.activationPanes) != 0 || len(panes.bound) != 0 {
			t.Fatalf("refused fan-out bound a Pane: legacy=%+v activation=%v native=%+v", legacy.bound, legacy.activationPanes, panes.bound)
		}
	})

	t.Run("interactive-only keeps the existing fan-out cardinality", func(t *testing.T) {
		create, store, tmux, legacy, native, panes := newInteractiveOnlyCodexCreate(t)
		before := len(store.registry.Agents)

		stdout, stderr, err := runRoute(t, create,
			"agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "-o", "pane-id", "--", "fan-out prompt")
		if err != nil || stderr != "" {
			t.Fatalf("interactive-only fan-out stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		if len(strings.Fields(stdout)) != 2 {
			t.Fatalf("interactive-only fan-out stdout = %q, want one Pane id per resolved Window", stdout)
		}
		if got := len(store.registry.Agents) - before; got != 2 {
			t.Fatalf("interactive-only fan-out created %d Agents, want one per resolved Window", got)
		}
		if len(native.creates) != 0 || len(native.resumes) != 0 || len(panes.bound) != 0 {
			t.Fatalf("interactive-only fan-out reached the native lane: %+v / %+v / %+v", native.creates, native.resumes, panes.bound)
		}
		if len(legacy.plans) != 1 || len(legacy.bound) != 2 || len(splitWindowCalls(tmux)) != 2 {
			t.Fatalf("interactive-only fan-out plans=%+v bound=%+v splits=%v", legacy.plans, legacy.bound, splitWindowCalls(tmux))
		}
	})

	t.Run("payload-free fan-out stays on the plain lane before provider mutation", func(t *testing.T) {
		create, store, tmux, legacy, native, _ := newInteractiveOnlyCodexCreate(t)
		before := len(store.registry.Agents)

		stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "-o", "pane-id")
		if err != nil || stderr != "" || len(strings.Fields(stdout)) != 2 {
			t.Fatalf("payload-free fan-out stdout=%q stderr=%q err=%v", stdout, stderr, err)
		}
		if len(store.registry.Agents)-before != 2 || native.currentCalls != 0 || native.resolveCalls != 0 ||
			len(native.creates) != 0 || len(native.resumes) != 0 || len(legacy.bound) != 2 || len(splitWindowCalls(tmux)) != 2 {
			t.Fatalf("payload-free fan-out lane drifted: agents=%d current=%d resolve=%d native=%+v/%+v bound=%+v splits=%v",
				len(store.registry.Agents)-before, native.currentCalls, native.resolveCalls,
				native.creates, native.resumes, legacy.bound, splitWindowCalls(tmux))
		}
	})
}

// TestPayloadFreeCodexCreateUsesSafePlainFallbackAndInteractiveOnlyEquivalentLane
// is the functional C-2 owner for both public spellings. The default route must
// choose the existing plain launch before Current/Resolve/thread-start, while
// --interactive-only retains the same launch bytes and stdout.
func TestPayloadFreeCodexCreateUsesSafePlainFallbackAndInteractiveOnlyEquivalentLane(t *testing.T) {
	type receipt struct {
		stdout      string
		launch      []string
		declaration string
	}
	run := func(t *testing.T, args ...string) receipt {
		t.Helper()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, legacy := newTestAgentCreateCommand(t, store, tmux)
		create.agents = &productionBindingAgentLauncher{fakeAgentLauncher: legacy, binder: testAICommand(t.TempDir())}
		native := &fakeNativeThreadController{
			currentErr: errors.New("Current must not be consulted"),
			createErr:  errors.New("thread/start must not be called"),
		}
		panes := &fakeNativePaneLauncher{}
		create.codexNative = native
		create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
		stdout, stderr, err := runRoute(t, create, args...)
		if err != nil || stderr != "" {
			t.Fatalf("payload-free create %v stdout=%q stderr=%q err=%v", args, stdout, stderr, err)
		}
		if native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 {
			t.Fatalf("payload-free route consulted provider authority: current=%d resolve=%d creates=%+v resumes=%+v",
				native.currentCalls, native.resolveCalls, native.creates, native.resumes)
		}
		if len(panes.plans) != 0 || len(panes.bound) != 0 || len(panes.lifecycle) != 0 {
			t.Fatalf("payload-free route acquired native Pane state: plans=%+v bound=%+v lifecycle=%+v", panes.plans, panes.bound, panes.lifecycle)
		}
		calls := splitWindowCalls(tmux)
		if len(calls) != 1 || len(legacy.plans) != 1 || len(legacy.plans[0].payload) != 0 ||
			len(legacy.bound) != 0 || len(legacy.activationPanes) != 0 {
			t.Fatalf("payload-free plain lane: plans=%+v bound=%+v activation=%v splits=%v",
				legacy.plans, legacy.bound, legacy.activationPanes, calls)
		}
		launch := tmuxCommandArgv(calls[0])
		joined := strings.Join(launch, " ")
		if !strings.Contains(joined, "exec codex") || strings.Contains(joined, "--remote") || strings.Contains(joined, "resume") {
			t.Fatalf("payload-free launch is not the plain Codex argv: %v", launch)
		}
		agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
		pane, ok := store.registry.Pane(agent.Status.PaneRef)
		if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.SessionRef != nil ||
			agent.Status.Activation.State != coremetadata.ActivationNotRequested || agent.Status.Activation.Source != "" ||
			pane.Status.Activation.Codex != nil || pane.Status.Activation.RuntimeID == "" {
			t.Fatalf("payload-free Agent/Pane is not one Running plain lane: agent=%#v pane=%#v", agent.Status, pane.Status)
		}
		_, _, livePane := tmux.pane(pane.Status.Activation.RuntimeID)
		if livePane == nil || livePane.opts[aiPaneCodexAuthorityOption] != codexAuthorityHook ||
			livePane.opts[aiPaneCodexReasonOption] != codexNativeUnexplainedReason {
			t.Fatalf("payload-free authority/source signal is missing: pane=%#v", livePane)
		}
		return receipt{stdout: stdout, launch: launch, declaration: livePane.opts[aiPaneCodexDeclaredOption]}
	}

	canonical := run(t, "agent", "--provider", "codex", "--project", "alpha", "--window", "main")
	shortcut := run(t, "codex", "--project", "alpha", "--window", "main")
	interactive := run(t, "agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "main")
	if canonical.stdout != "agent/agent-test-1 created\n" || shortcut.stdout != canonical.stdout || interactive.stdout != canonical.stdout {
		t.Fatalf("payload-free stdout drifted: canonical=%q shortcut=%q interactive-only=%q", canonical.stdout, shortcut.stdout, interactive.stdout)
	}
	if !slices.Equal(canonical.launch, shortcut.launch) || !slices.Equal(canonical.launch, interactive.launch) {
		t.Fatalf("payload-free plain argv drifted: canonical=%v shortcut=%v interactive-only=%v",
			canonical.launch, shortcut.launch, interactive.launch)
	}
	if canonical.declaration != codexNativeDeclaredPayloadFreeFallback || shortcut.declaration != codexNativeDeclaredPayloadFreeFallback ||
		interactive.declaration != codexNativeDeclaredInteractiveOnly {
		t.Fatalf("diagnostic declarations = canonical:%q shortcut:%q interactive-only:%q",
			canonical.declaration, shortcut.declaration, interactive.declaration)
	}
}

// TestCodexCreatePayloadCardinalityInteractiveOnlyAndReadinessOutcomeTable is
// the closed pre-provider decision table. Readiness is deliberately irrelevant
// to both plain lanes because neither may ask the controller for it.
func TestCodexCreatePayloadCardinalityInteractiveOnlyAndReadinessOutcomeTable(t *testing.T) {
	for _, readiness := range []string{"ready", "unavailable", "indeterminate"} {
		for payloadCount := 0; payloadCount <= 2; payloadCount++ {
			for _, interactiveOnly := range []bool{false, true} {
				name := readiness + "/payload-" + string(rune('0'+payloadCount))
				if interactiveOnly {
					name += "/interactive-only"
				} else {
					name += "/default"
				}
				t.Run(name, func(t *testing.T) {
					create, store, _, legacy, native, _ := newInteractiveOnlyCodexCreate(t)
					switch readiness {
					case "unavailable":
						native.currentErr = errFakeNativeUnavailable
						native.fallback = true
					case "indeterminate":
						native.createErr = errors.New("provider identity became indeterminate")
					}
					args := []string{"agent", "--provider", "codex", "--project", "alpha", "--window", "main"}
					if interactiveOnly {
						args = append(args, interactiveOnlyFlag)
					}
					for i := 0; i < payloadCount; i++ {
						if i == 0 {
							args = append(args, "--")
						}
						args = append(args, "payload")
					}
					beforeAgents, beforePanes := len(store.registry.Agents), len(store.registry.Panes)
					stdout, _, err := runRoute(t, create, args...)
					nativeRequired := !interactiveOnly && payloadCount > 0
					success := !nativeRequired || (payloadCount == 1 && readiness == "ready")
					if success != (err == nil) {
						t.Fatalf("success=%t stdout=%q err=%v", success, stdout, err)
					}
					if success {
						if len(store.registry.Agents) != beforeAgents+1 || len(store.registry.Panes) != beforePanes+1 {
							t.Fatalf("successful cell cardinality agents=%d panes=%d, want 1/1",
								len(store.registry.Agents)-beforeAgents, len(store.registry.Panes)-beforePanes)
						}
					} else if stdout != "" || len(store.registry.Agents) != beforeAgents || len(store.registry.Panes) != beforePanes {
						t.Fatalf("refused cell mutated identity: stdout=%q agents=%d panes=%d",
							stdout, len(store.registry.Agents)-beforeAgents, len(store.registry.Panes)-beforePanes)
					}
					if !nativeRequired {
						if native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 ||
							len(legacy.plans) != 1 {
							t.Fatalf("plain cell consulted provider: current=%d resolve=%d create=%d resume=%d plans=%d",
								native.currentCalls, native.resolveCalls, len(native.creates), len(native.resumes), len(legacy.plans))
						}
						return
					}
					if payloadCount == 2 {
						if native.currentCalls != 0 || len(native.creates) != 0 {
							t.Fatalf("ambiguous payload reached provider: current=%d creates=%d", native.currentCalls, len(native.creates))
						}
						return
					}
					wantCreates := 1
					if readiness == "unavailable" {
						wantCreates = 0
					}
					if native.currentCalls != 1 || len(native.creates) != wantCreates {
						t.Fatalf("prompted native cell current=%d creates=%d, want 1/%d", native.currentCalls, len(native.creates), wantCreates)
					}
				})
			}
		}
	}
}

// TestPayloadFreeCodexPlainLaunchFailureRollsBackWithoutProviderMutation pins
// the transaction boundary for both the CLI and the AI picker default intent.
func TestPayloadFreeCodexPlainLaunchFailureRollsBackWithoutProviderMutation(t *testing.T) {
	t.Run("canonical CLI", func(t *testing.T) {
		create, store, tmux, legacy, native, panes := newInteractiveOnlyCodexCreate(t)
		tmux.fail = []string{"split-window"}
		beforeRegistry, beforeTmux := store.snapshot(), tmux.state()
		stdout, _, err := runRoute(t, create,
			"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "pane-id")
		if err == nil || stdout != "" {
			t.Fatalf("failed create stdout=%q err=%v", stdout, err)
		}
		if store.snapshot() != beforeRegistry || store.writes != 0 || tmux.state() != beforeTmux ||
			native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 ||
			len(legacy.bound) != 0 || len(panes.bound) != 0 {
			t.Fatalf("failed payload-free launch escaped rollback: writes=%d current=%d resolve=%d create=%d resume=%d plain=%d native=%d",
				store.writes, native.currentCalls, native.resolveCalls, len(native.creates), len(native.resumes), len(legacy.bound), len(panes.bound))
		}
	})

	t.Run("post-split binder failure removes the created Pane", func(t *testing.T) {
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, plain := newTestAgentCreateCommand(t, store, tmux)
		create.agents = &productionBindingAgentLauncher{fakeAgentLauncher: plain, binder: testAICommand(t.TempDir())}
		native := &fakeNativeThreadController{currentErr: errors.New("Current must not be consulted"), createErr: errors.New("thread/start must not be called")}
		create.codexNative = native
		create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
		tmux.fail = []string{"set-option", aiPaneCodexDeclaredOption}
		beforeRegistry, beforeTmux := store.snapshot(), tmux.state()
		stdout, _, err := runRoute(t, create,
			"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "pane-id")
		if err == nil || stdout != "" {
			t.Fatalf("failed post-split bind stdout=%q err=%v", stdout, err)
		}
		if store.snapshot() != beforeRegistry || store.writes != 0 || tmux.state() != beforeTmux ||
			!tmux.argvContains("split-window") || !tmux.argvContains("kill-pane") ||
			native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 {
			t.Fatalf("post-split rollback drifted: writes=%d split=%t kill=%t current=%d resolve=%d create=%d resume=%d",
				store.writes, tmux.argvContains("split-window"), tmux.argvContains("kill-pane"), native.currentCalls,
				native.resolveCalls, len(native.creates), len(native.resumes))
		}
	})

	t.Run("AI picker saved default", func(t *testing.T) {
		fx := canonicalFixture(t, false)
		native := &fakeNativeThreadController{currentErr: errors.New("Current must not be consulted"), createErr: errors.New("thread/start must not be called")}
		plain := fx.create.agents.(*fakeAgentLauncher)
		panes := &fakeNativePaneLauncher{}
		fx.create.codexNative = native
		fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
		fx.tmux.fail = []string{"split-window"}
		beforeRegistry, beforeTmux := fx.store.snapshot(), fx.tmux.state()
		err := fx.create.createFromIntent(agentPaneIntent{
			producer: canonicalProducerSavedDefault, provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID,
		}, ioDiscard{}, ioDiscard{})
		if err == nil {
			t.Fatal("failed picker create reported success")
		}
		if fx.store.snapshot() != beforeRegistry || fx.store.writes != 0 || fx.tmux.state() != beforeTmux ||
			native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 ||
			len(plain.bound) != 0 || len(panes.bound) != 0 {
			t.Fatalf("failed picker fallback escaped rollback: writes=%d current=%d resolve=%d create=%d resume=%d plain=%d native=%d",
				fx.store.writes, native.currentCalls, native.resolveCalls, len(native.creates), len(native.resumes), len(plain.bound), len(panes.bound))
		}
	})
}

// TestClaudeAndAntigravityLifecycleAndHookContractAreUnchangedByTheNativeGate
// is the negative half of the Phase: the native-required gate is Codex-only.
//
// Neither other provider consults the native controller, neither loses its
// fan-out, and both keep the exact hook activation source and Pane-binding
// contract they had, whatever the Codex app-server endpoint is doing.
func TestClaudeAndAntigravityLifecycleAndHookContractAreUnchangedByTheNativeGate(t *testing.T) {
	for _, provider := range []string{"claude", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			create, store, tmux, legacy, native, panes := newInteractiveOnlyCodexCreate(t)
			// The Codex endpoint is fully available; nothing about that may reach
			// another provider's route.
			agentsBefore := len(store.registry.Agents)

			stdout, stderr, err := runRoute(t, create, "agent", "--provider", provider, "--project", "alpha", "-o", "pane-id", "--", "fan-out prompt")
			if err != nil || stderr != "" || len(strings.Fields(stdout)) != 2 {
				t.Fatalf("%s fan-out stdout=%q stderr=%q err=%v", provider, stdout, stderr, err)
			}
			if got := len(store.registry.Agents) - agentsBefore; got != 2 {
				t.Fatalf("%s fan-out created %d Agents, want one per resolved Window", provider, got)
			}
			if len(native.creates) != 0 || len(native.resumes) != 0 || len(panes.plans) != 0 || len(panes.bound) != 0 {
				t.Fatalf("%s reached the Codex native lane: %+v %+v %+v %+v", provider, native.creates, native.resumes, panes.plans, panes.bound)
			}
			if len(legacy.plans) != 1 || !slices.Equal(legacy.plans[0].payload, []string{"fan-out prompt"}) ||
				len(legacy.bound) != 2 || len(legacy.activationPanes) != 2 || len(splitWindowCalls(tmux)) != 2 {
				t.Fatalf("%s plain lane = plans:%+v bound:%+v activation:%v splits:%v",
					provider, legacy.plans, legacy.bound, legacy.activationPanes, splitWindowCalls(tmux))
			}
			for _, agent := range store.registry.Agents {
				if agent.Spec.Provider != provider {
					continue
				}
				if agent.Status.SessionRef != nil {
					t.Fatalf("%s gained a native sessionRef: %#v", provider, agent.Status.SessionRef)
				}
				if agent.Status.Activation.Source != string(coremetadata.InteractionSourceProviderHook) ||
					agent.Status.Activation.State != coremetadata.ActivationAcknowledged {
					t.Fatalf("%s hook activation contract changed: %#v", provider, agent.Status.Activation)
				}
			}
		})
	}
}
