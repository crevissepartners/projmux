package app

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

// retiredRefsDefaultGeneration is the default daemon endpoint generation the
// retired-reference fixtures resolve to; retiredRefsOldGeneration is the
// generation a retired private host (or an older daemon) minted.
const (
	retiredRefsDefaultGeneration = "codex-0.154.0"
	retiredRefsOldVersion        = "0.152.1"
	retiredRefsOldGeneration     = "codex-" + retiredRefsOldVersion
	retiredRefsOtherDomain       = "other-codex-home-domain"
)

// retiredRefsForbidden are the operator texts no retired-reference output may
// carry: the private generation commands are deleted in the next task, and the
// handover-required refusal no longer exists.
var retiredRefsForbidden = []string{"app-server handover", "app-server upgrade", "handover-required"}

// retiredRefsNativeController keeps the production Current/Resolve switch and
// records the provider resume instead of dialing the shared endpoint.
type retiredRefsNativeController struct {
	defaultCodexNativeThreadController
	resumes []fakeNativeResume
}

func (c *retiredRefsNativeController) Resume(_ context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID string) (codexappserver.ThreadBinding, error) {
	c.resumes = append(c.resumes, fakeNativeResume{route: route, workspace: workspace, threadID: threadID})
	return codexappserver.ThreadBinding{ThreadID: threadID}, nil
}

func newRetiredRefsNativeController(t *testing.T, stateDir string) *retiredRefsNativeController {
	t.Helper()
	controller := newCodexNativeThreadController(stateDir)
	controller.current = func(context.Context) (codexNativeEndpointRoute, error) {
		return nativeTestDefaultRoute(retiredRefsDefaultGeneration), nil
	}
	controller.open = func(context.Context, codexNativeEndpointRoute, bool) (codexNativeThreadClient, error) {
		t.Error("retired reference resume opened a create client")
		return nil, errFakeNativeUnavailable
	}
	return &retiredRefsNativeController{defaultCodexNativeThreadController: controller}
}

// shortRetiredStateDir keeps the deterministic private socket path under the
// platform bound so a real listener can occupy it.
func shortRetiredStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if len(dir) <= 60 {
		return dir
	}
	dir, err := os.MkdirTemp("/tmp", "pxr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// listenRetiredGeneration occupies the socket a retired private host of
// endpoint would have listened on.
func listenRetiredGeneration(t *testing.T, stateDir string, endpoint coremetadata.CodexEndpointRef) string {
	t.Helper()
	version := strings.TrimPrefix(endpoint.EndpointGenerationID, "codex-")
	root, socketPath, err := managedCodexRuntimeLocation(stateDir, endpoint.StateDomainID, version)
	if err != nil {
		t.Fatalf("retired socket location: %v", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen retired generation: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return socketPath
}

func retiredRefsEndpoint(domain, generation string) coremetadata.CodexEndpointRef {
	return coremetadata.CodexEndpointRef{StateDomainID: domain, EndpointGenerationID: generation}
}

func retiredRefsLifecycle(state coremetadata.CodexGenerationState, endpoint coremetadata.CodexEndpointRef) *coremetadata.CodexGenerationLifecycleRef {
	lifecycle := &coremetadata.CodexGenerationLifecycleRef{State: state}
	if state == coremetadata.CodexGenerationDraining || state == coremetadata.CodexGenerationHandoverPending {
		lifecycle.Operation = &coremetadata.CodexGenerationOperationRef{ID: "upgrade-retired", Endpoint: endpoint}
	}
	return lifecycle
}

func retiredRefsSessionRef(endpoint coremetadata.CodexEndpointRef, state coremetadata.CodexGenerationState) *coremetadata.AgentSessionRef {
	ref := resumeFixtureRef(resourceFixtureClock)
	stored := endpoint
	ref.Codex.Endpoint = &stored
	ref.Codex.Lifecycle = retiredRefsLifecycle(state, endpoint)
	return ref
}

var retiredRefsStates = []coremetadata.CodexGenerationState{
	coremetadata.CodexGenerationCurrent, coremetadata.CodexGenerationDraining, coremetadata.CodexGenerationHandoverPending,
}

type retiredRefsCase struct {
	name     string
	endpoint coremetadata.CodexEndpointRef
	running  bool
	// refusal is empty for a switch.
	refusal string
}

func retiredRefsCases(state coremetadata.CodexGenerationState) []retiredRefsCase {
	cases := []retiredRefsCase{
		{name: "same-domain-old-generation", endpoint: retiredRefsEndpoint("test-domain", retiredRefsOldGeneration)},
		{name: "other-domain", endpoint: retiredRefsEndpoint(retiredRefsOtherDomain, retiredRefsOldGeneration), refusal: codexNativeReasonRetiredStateDomain},
		{name: "retired-socket-alive", endpoint: retiredRefsEndpoint("test-domain", retiredRefsOldGeneration), running: true, refusal: codexNativeReasonRetiredRunning},
	}
	if state != coremetadata.CodexGenerationCurrent {
		// A planned marker on the exact default generation also resumes.
		cases = append(cases, retiredRefsCase{name: "same-generation", endpoint: retiredRefsEndpoint("test-domain", retiredRefsDefaultGeneration)})
	}
	return cases
}

// retiredRefsTranscript collects every operator text these fixtures produce so
// one golden pins them and one scan proves none names a retired command.
type retiredRefsTranscript struct {
	buf      bytes.Buffer
	stateDir []string
}

func (r *retiredRefsTranscript) add(label, text string) {
	for _, dir := range r.stateDir {
		text = strings.ReplaceAll(text, dir, "<stateDir>")
	}
	r.buf.WriteString("== " + label + "\n" + text + "\n")
}

func (r *retiredRefsTranscript) check(t *testing.T, golden string) {
	t.Helper()
	for _, forbidden := range retiredRefsForbidden {
		if strings.Contains(r.buf.String(), forbidden) {
			t.Fatalf("retired reference output names %q:\n%s", forbidden, r.buf.String())
		}
	}
	path := filepath.Join("testdata", golden)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, r.buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.buf.Bytes(), want) {
		t.Fatalf("%s mismatch (UPDATE_GOLDEN=1 rewrites it)\n--- got ---\n%s\n--- want ---\n%s", golden, r.buf.Bytes(), want)
	}
}

// runRetiredRefsResumeMatrix drives `agent resume` for one lifecycle state and
// returns the refusal texts in case order.
func runRetiredRefsResumeMatrix(t *testing.T, state coremetadata.CodexGenerationState, transcript *retiredRefsTranscript) {
	t.Helper()
	for _, tc := range retiredRefsCases(state) {
		t.Run(string(state)+"/"+tc.name, func(t *testing.T) {
			stateDir := shortRetiredStateDir(t)
			store := newFakeResourceStore(t)
			setFixtureSessionRef(t, store, "agt-beta-codex", retiredRefsSessionRef(tc.endpoint, state))
			tmux := newFakeTmux()
			command, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
			native := newRetiredRefsNativeController(t, stateDir)
			panes := &fakeNativePaneLauncher{}
			command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
			command.rebind.create.codexNative = native
			if tc.running {
				transcript.stateDir = append(transcript.stateDir, stateDir)
				listenRetiredGeneration(t, stateDir, tc.endpoint)
			}
			before, paneCount := store.snapshot(), tmux.paneCount()

			stdout, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
			if tc.refusal != "" {
				if err == nil || stdout != "" || stderr != "" {
					t.Fatalf("stdout=%q stderr=%q err=%v, want %s refusal", stdout, stderr, err, tc.refusal)
				}
				for _, want := range []string{"(" + tc.refusal + ")", "`projmux agent resume uid:agt-beta-codex`"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal lacks %q: %v", want, err)
					}
				}
				if len(native.resumes) != 0 || len(panes.plans) != 0 || len(panes.bound) != 0 || len(legacy.plans) != 0 ||
					len(splitWindowCalls(tmux)) != 0 || store.transactions != 0 || store.writes != 0 ||
					store.snapshot() != before || tmux.paneCount() != paneCount {
					t.Fatalf("refused resume had effects: resumes=%+v plans=%+v legacy=%+v splits=%v tx=%d writes=%d",
						native.resumes, panes.plans, legacy.plans, splitWindowCalls(tmux), store.transactions, store.writes)
				}
				transcript.add("agent resume "+string(state)+" "+tc.name, err.Error())
				return
			}
			if err != nil || stdout != "agent/codex resumed\n" {
				t.Fatalf("switch resume stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}
			defaultEndpoint := nativeTestDefaultRoute(retiredRefsDefaultGeneration).Endpoint
			if len(native.resumes) != 1 || !native.resumes[0].route.Default || !native.resumes[0].route.Endpoint.Same(defaultEndpoint) ||
				native.resumes[0].threadID != resumeFixtureConversation {
				t.Fatalf("provider resumes = %+v, want exactly one on the default endpoint", native.resumes)
			}
			if len(panes.plans) != 2 || !panes.plans[1].route.Default || len(panes.bound) != 1 || len(splitWindowCalls(tmux)) != 1 ||
				tmux.paneCount() <= paneCount || len(panes.lifecycle) != 1 || !panes.lifecycle[0].NativeRoute.Default {
				t.Fatalf("switch resume tmux effects: plans=%+v bound=%+v splits=%v lifecycle=%+v", panes.plans, panes.bound, splitWindowCalls(tmux), panes.lifecycle)
			}
			after, _ := store.registry.Agent("agt-beta-codex")
			ref := after.Status.SessionRef
			if after.Status.Phase != coremetadata.PhaseRunning || ref == nil || ref.Codex == nil || ref.Codex.ThreadID != resumeFixtureConversation ||
				ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Same(defaultEndpoint) || ref.Codex.Lifecycle == nil ||
				ref.Codex.Lifecycle.State != coremetadata.CodexGenerationCurrent || ref.Codex.Lifecycle.Operation != nil || ref.Codex.HandoverResume != nil {
				t.Fatalf("switched Agent was not stored on the default endpoint: phase=%s ref=%+v", after.Status.Phase, ref.Codex)
			}
			pane, ok := store.registry.Pane(after.Status.PaneRef)
			if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != resumeFixtureConversation {
				t.Fatalf("switched Pane native binding = %+v", pane.Status.Activation)
			}
			if err := store.registry.Validate(); err != nil {
				t.Fatalf("switched Registry invalid: %v", err)
			}
		})
	}
}

// TestRetiredCodexEndpointRefResumeSwitchesOrRefuses is C-2 for a current
// lifecycle: a stored endpoint that is not the default daemon endpoint either
// resumes there (same Codex state domain) or refuses with the reason and the
// next command, with zero provider, Registry, and tmux effects.
func TestRetiredCodexEndpointRefResumeSwitchesOrRefuses(t *testing.T) {
	var transcript retiredRefsTranscript
	runRetiredRefsResumeMatrix(t, coremetadata.CodexGenerationCurrent, &transcript)
}

// TestRetiredCodexDrainingRefResumeSwitchesOrRefuses is the same matrix for
// the draining and handover-pending markers the retired pool left behind.
func TestRetiredCodexDrainingRefResumeSwitchesOrRefuses(t *testing.T) {
	var transcript retiredRefsTranscript
	for _, state := range retiredRefsStates[1:] {
		runRetiredRefsResumeMatrix(t, state, &transcript)
	}
}

// runRetiredRefsPickerRow drives the resume-picker create path for one row.
func runRetiredRefsPickerRow(t *testing.T, state coremetadata.CodexGenerationState, tc retiredRefsCase, transcript *retiredRefsTranscript) {
	t.Helper()
	fx := canonicalFixture(t, false)
	stateDir := shortRetiredStateDir(t)
	id := "019f0000-0000-7000-8000-0000000000a1"
	native := newRetiredRefsNativeController(t, stateDir)
	legacy := newFakeResumeLauncher()
	panes := &fakeNativePaneLauncher{}
	fx.create.codexNative = native
	fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	if tc.running {
		transcript.stateDir = append(transcript.stateDir, stateDir)
		listenRetiredGeneration(t, stateDir, tc.endpoint)
	}
	before, paneCount := fx.store.snapshot(), fx.tmux.paneCount()
	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer, anchorPaneID: fx.originID,
		resumeEndpoint: tc.endpoint, resumeGenerationState: state,
	}, ioDiscard{}, ioDiscard{})
	if tc.refusal != "" {
		if err == nil || !strings.Contains(err.Error(), "("+tc.refusal+")") || !strings.Contains(err.Error(), "projmux ") {
			t.Fatalf("picker row err=%v, want %s refusal with a next command", err, tc.refusal)
		}
		if len(native.resumes) != 0 || len(legacy.plans) != 0 || len(panes.plans) != 0 || len(splitWindowCalls(fx.tmux)) != 0 ||
			fx.store.transactions != 0 || fx.store.writes != 0 || fx.store.snapshot() != before || fx.tmux.paneCount() != paneCount {
			t.Fatalf("refused picker row had effects: resumes=%+v plans=%+v tx=%d writes=%d", native.resumes, panes.plans, fx.store.transactions, fx.store.writes)
		}
		transcript.add("picker row "+string(state)+" "+tc.name, err.Error())
		return
	}
	if err != nil {
		t.Fatalf("picker row switch: %v", err)
	}
	defaultEndpoint := nativeTestDefaultRoute(retiredRefsDefaultGeneration).Endpoint
	if len(native.resumes) != 1 || !native.resumes[0].route.Endpoint.Same(defaultEndpoint) || len(panes.bound) != 1 {
		t.Fatalf("picker row switch effects: resumes=%+v bound=%+v", native.resumes, panes.bound)
	}
	found := false
	for _, agent := range fx.store.registry.Agents {
		ref := agent.Status.SessionRef
		if ref == nil || ref.Codex == nil || ref.Codex.ThreadID != id {
			continue
		}
		found = true
		if ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Same(defaultEndpoint) || ref.Codex.Lifecycle == nil ||
			ref.Codex.Lifecycle.State != coremetadata.CodexGenerationCurrent {
			t.Fatalf("picker Agent stored %+v, want the default endpoint", ref.Codex)
		}
	}
	if !found {
		t.Fatal("picker row created no Agent for the resumed thread")
	}
}

// TestRetiredCodexPickerRowSwitchesOrRefuses covers the resume-picker create
// path for every lifecycle row state.
func TestRetiredCodexPickerRowSwitchesOrRefuses(t *testing.T) {
	var transcript retiredRefsTranscript
	for _, state := range retiredRefsStates {
		for _, tc := range retiredRefsCases(state) {
			t.Run(string(state)+"/"+tc.name, func(t *testing.T) { runRetiredRefsPickerRow(t, state, tc, &transcript) })
		}
	}
}

func retiredRefsDraining(endpoint coremetadata.CodexEndpointRef) coremetadata.CodexGenerationLifecycleRef {
	return *retiredRefsLifecycle(coremetadata.CodexGenerationDraining, endpoint)
}

// TestRetiredCodexRefTurnAndMessageOutcomes pins `agent turn` and the Codex
// `agent message send` push for old-reference Agents: a Running Agent keeps its
// live binding and succeeds; an Offline one refuses with the resume command; a
// Running one whose native connection is gone refuses with its Open Codex
// command. None of them touches the control transport when refusing.
func TestRetiredCodexRefTurnAndMessageOutcomes(t *testing.T) {
	collect := &retiredRefsTranscript{}
	runRetiredRefsTurnAndMessage(t, collect)
}

func runRetiredRefsTurnAndMessage(t *testing.T, transcript *retiredRefsTranscript) {
	t.Helper()
	offlineRef := retiredRefsSessionRef(retiredRefsEndpoint("test-domain", retiredRefsOldGeneration), coremetadata.CodexGenerationDraining)

	t.Run("turn running draining live binding", func(t *testing.T) {
		cmd, store, _ := exactControlCLICommand(t)
		if _, _, err := store.mutator().SetCodexGenerationLifecycle(&store.registry, "agt-alpha-codex", phase6CLIEndpoint(), retiredRefsDraining(phase6CLIEndpoint())); err != nil {
			t.Fatal(err)
		}
		calls := 0
		cmd.controlCall = func(_ context.Context, _ string, endpoint coremetadata.CodexEndpointRef, _ codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
			calls++
			if !endpoint.Same(phase6CLIEndpoint()) {
				t.Fatalf("live turn left its binding endpoint: %+v", endpoint)
			}
			return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-2"}, nil
		}
		stdout, _, err := runRoute(t, cmd, "turn", "start", "uid:agt-alpha-codex", "--", "hello")
		if err != nil || calls != 1 || !strings.Contains(stdout, "turn=\"turn-2\"") {
			t.Fatalf("live draining turn stdout=%q calls=%d err=%v", stdout, calls, err)
		}
	})
	t.Run("turn running connection gone", func(t *testing.T) {
		cmd, _, binding := exactControlCLICommand(t)
		binding.live.Authority, binding.live.Epoch, binding.live.Reason = "", "", "endpoint-unavailable"
		cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
			t.Fatal("refused turn reached the control transport")
			return agentControlResponse{}, nil
		}
		_, _, err := runRoute(t, cmd, "turn", "start", "uid:agt-alpha-codex", "--", "hello")
		if err == nil || !strings.Contains(err.Error(), "`projmux focus pane uid:pan-alpha-codex") {
			t.Fatalf("disconnected turn err=%v, want Open Codex command", err)
		}
		transcript.add("agent turn start running connection gone", err.Error())
	})
	for _, verb := range [][]string{{"turn", "start", "uid:agt-beta-codex", "--", "hello"}, {"turn", "interrupt", "uid:agt-beta-codex"}} {
		t.Run("offline "+strings.Join(verb[:2], " "), func(t *testing.T) {
			cmd, store, _ := exactControlCLICommand(t)
			setFixtureSessionRef(t, store, "agt-beta-codex", offlineRef.Clone())
			cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
				t.Fatal("offline turn reached the control transport")
				return agentControlResponse{}, nil
			}
			before := store.snapshot()
			_, _, err := runRoute(t, cmd, verb...)
			if err == nil || !strings.Contains(err.Error(), "next: `projmux agent resume uid:agt-beta-codex`") || store.snapshot() != before {
				t.Fatalf("offline %v err=%v", verb, err)
			}
			transcript.add("agent "+strings.Join(verb[:2], " ")+" offline", err.Error())
		})
	}
	t.Run("message running draining live binding", func(t *testing.T) {
		fixture := newCodexPushFixture(t)
		agent, _ := fixture.registry.Agent("agt-alpha-codex")
		agent.Status.SessionRef.Codex.Lifecycle = retiredRefsLifecycle(coremetadata.CodexGenerationDraining, *agent.Status.SessionRef.Codex.Endpoint)
		record := fixture.accept(t, "message-retired-live")
		updated, err := fixture.cmd.pushCodexCoordination(record, *agent, record.Envelope)
		if err != nil || updated.Delivery.State != coremessage.StateDelivered || fixture.calls[agentControlOpStart] != 1 {
			t.Fatalf("live draining push delivery=%+v err=%v calls=%v", updated.Delivery, err, fixture.calls)
		}
	})
	for _, send := range []struct {
		name string
		args []string
	}{
		{name: "agent message send offline", args: []string{"message", "send", "--source", "uid:agt-alpha-codex", "uid:agt-beta-codex", "--", "hello"}},
		{name: "agent message send offline reply", args: []string{"message", "send", "--source", "uid:agt-alpha-codex", "--reply-to", "message-original", "uid:agt-beta-codex", "--", "hello"}},
	} {
		t.Run(send.name, func(t *testing.T) {
			fixture := newCodexPushFixture(t)
			target, _ := fixture.registry.Agent("agt-beta-codex")
			target.Status.SessionRef = offlineRef.Clone()
			fixture.cmd.messageRoute = liveAgentMessageRouteResolver{}
			_, _, err := runRoute(t, fixture.cmd, send.args...)
			if err == nil || !strings.Contains(err.Error(), "target Agent is not eligible") ||
				!strings.HasSuffix(err.Error(), "; next: `projmux agent resume uid:agt-beta-codex`") || len(fixture.calls) != 0 {
				t.Fatalf("offline message send err=%v calls=%v", err, fixture.calls)
			}
			if _, found, getErr := fixture.store.Get("message-original"); getErr != nil || found {
				t.Fatalf("refused send stored a record: found=%t err=%v", found, getErr)
			}
			transcript.add(send.name, err.Error())
		})
	}
	t.Run("message push offline", func(t *testing.T) {
		fixture := newCodexPushFixture(t)
		target, _ := fixture.registry.Agent("agt-beta-codex")
		target.Status.SessionRef = offlineRef.Clone()
		record := fixture.accept(t, "message-retired-offline")
		updated, err := fixture.cmd.pushCodexCoordination(record, *target, record.Envelope)
		if err == nil || updated.Delivery.Reason != "codex-native-binding-unavailable" ||
			!strings.Contains(err.Error(), "next: `projmux agent resume uid:agt-beta-codex`") || len(fixture.calls) != 0 {
			t.Fatalf("offline push delivery=%+v err=%v calls=%v", updated.Delivery, err, fixture.calls)
		}
		transcript.add("agent message push offline", err.Error())
	})
}

// TestRetiredCodexRefDoctorReportsResumeOutcome shows the resume outcome and
// next command for each retired reference in the Doctor endpoint section.
func TestRetiredCodexRefDoctorReportsResumeOutcome(t *testing.T) {
	var transcript retiredRefsTranscript
	runRetiredRefsDoctor(t, &transcript)
}

func runRetiredRefsDoctor(t *testing.T, transcript *retiredRefsTranscript) {
	t.Helper()
	health := codexappserver.Health{EndpointReadiness: codexappserver.EndpointReady, RunningVersion: strings.TrimPrefix(retiredRefsDefaultGeneration, "codex-")}
	pool := &doctorCodexGenerationPool{Status: "absent"}
	for _, test := range []struct {
		name   string
		ref    *coremetadata.AgentSessionRef
		resume string
	}{
		{name: "same-domain-old-generation", ref: retiredRefsSessionRef(retiredRefsEndpoint("test-domain", retiredRefsOldGeneration), coremetadata.CodexGenerationCurrent), resume: doctorCodexRetiredResumeSwitches},
		{name: "draining-default-generation", ref: retiredRefsSessionRef(retiredRefsEndpoint("test-domain", retiredRefsDefaultGeneration), coremetadata.CodexGenerationDraining), resume: doctorCodexRetiredResumeSwitches},
		{name: "handover-pending-old-generation", ref: retiredRefsSessionRef(retiredRefsEndpoint("test-domain", retiredRefsOldGeneration), coremetadata.CodexGenerationHandoverPending), resume: doctorCodexRetiredResumeSwitches},
		{name: "other-domain", ref: retiredRefsSessionRef(retiredRefsEndpoint(retiredRefsOtherDomain, retiredRefsOldGeneration), coremetadata.CodexGenerationCurrent), resume: doctorCodexRetiredResumeRefused},
		{name: "exact-default", ref: retiredRefsSessionRef(retiredRefsEndpoint("test-domain", retiredRefsDefaultGeneration), coremetadata.CodexGenerationCurrent)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			setFixtureSessionRef(t, store, "agt-beta-codex", test.ref)
			report := diagnoseCodexEndpointMismatch(store.registry, nil, "test-domain", nil, pool, &health)
			var rows []doctorCodexRetiredRef
			if report != nil {
				rows = report.RetiredRefs
			}
			if test.resume == "" {
				if len(rows) != 0 {
					t.Fatalf("exact default ref reported as retired: %+v", rows)
				}
				return
			}
			if len(rows) != 1 || rows[0].AgentUID != "agt-beta-codex" || rows[0].Resume != test.resume ||
				!strings.Contains(rows[0].Next, "projmux agent resume uid:agt-beta-codex") {
				t.Fatalf("retired rows = %+v", rows)
			}
			var text bytes.Buffer
			writeDoctorCodexEndpointMismatchText(&text, report)
			var line string
			for candidate := range strings.SplitSeq(text.String(), "\n") {
				if strings.Contains(candidate, "Retired Codex ref") {
					line = candidate
				}
			}
			if line == "" {
				t.Fatalf("Doctor text lacks the retired reference line:\n%s", text.String())
			}
			transcript.add("doctor "+test.name, line)
		})
	}
}

// TestRetiredCodexRefOperatorTextGolden collects every refusal, badge, and
// Doctor text the retired-reference fixtures produce into one golden and
// proves none of them routes the operator to a deleted command.
func TestRetiredCodexRefOperatorTextGolden(t *testing.T) {
	var transcript retiredRefsTranscript
	for _, state := range retiredRefsStates {
		runRetiredRefsResumeMatrix(t, state, &transcript)
	}
	for _, state := range retiredRefsStates {
		for _, tc := range retiredRefsCases(state) {
			if tc.refusal == "" {
				continue
			}
			t.Run("picker/"+string(state)+"/"+tc.name, func(t *testing.T) { runRetiredRefsPickerRow(t, state, tc, &transcript) })
		}
	}
	runRetiredRefsTurnAndMessage(t, &transcript)
	for _, state := range []coremetadata.CodexGenerationState{
		coremetadata.CodexGenerationCurrent, coremetadata.CodexGenerationDraining, coremetadata.CodexGenerationHandoverPending,
		coremetadata.CodexGenerationRetired, coremetadata.CodexGenerationBlocked,
	} {
		badge := aiResumeGenerationStatus(aisessions.SessionMeta{Agent: aiModeCodex, Source: aisessions.SourceCodexAppServer, GenerationState: string(state)})
		if (state == coremetadata.CodexGenerationDraining || state == coremetadata.CodexGenerationHandoverPending) && badge != "" {
			t.Fatalf("%s picker row badge = %q, want none", state, badge)
		}
		transcript.add("picker badge "+string(state), badge)
	}
	runRetiredRefsDoctor(t, &transcript)
	transcript.check(t, "codex_retired_generation_refs.golden")
}

// retiredRefsJournalFixture writes a valid journal with a ready current and a
// draining private route for domain under stateDir and pins its mtime.
func retiredRefsJournalFixture(t *testing.T, stateDir, domain string) (*codexupgrade.Store, []byte, time.Time) {
	t.Helper()
	store := codexupgrade.NewStateStore(stateDir)
	fixture, _, current := codexPoolInventoryFixture(t)
	journal, exists, err := fixture.Load()
	if err != nil || !exists {
		t.Fatalf("pool fixture: exists=%t err=%v", exists, err)
	}
	journal.StateDomainID = domain
	for i := range journal.Routes {
		journal.Routes[i].Generation.Endpoint.StateDomainID = domain
		journal.Routes[i].Config.Endpoint.StateDomainID = domain
		journal.Routes[i].Proof.Endpoint.StateDomainID = domain
	}
	journal.CurrentGenerationID = current.Generation.Endpoint.EndpointGenerationID
	if _, err := store.Update(context.Background(), func(got *codexupgrade.Journal, _ bool) error {
		*got = journal
		return nil
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	if loaded, _, err := store.Load(); err != nil {
		t.Fatalf("journal fixture is not valid: %v", err)
	} else if _, ok := loaded.CurrentRoute(); !ok {
		t.Fatal("journal fixture has no ready current route")
	}
	pinned := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(store.Path(), pinned, pinned); err != nil {
		t.Fatal(err)
	}
	body, mod := snapshotCodexJournal(t, store)
	return store, body, mod
}

// sealJournalDir makes any read or Lstat of the journal fail with a
// permission error until the returned restore runs, so a route that consults
// the journal refuses instead of passing.
func sealJournalDir(t *testing.T, store *codexupgrade.Store) func() {
	t.Helper()
	dir := filepath.Dir(store.Path())
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(store.Path()); err == nil {
		_ = os.Chmod(dir, 0o700)
		t.Skip("journal directory permissions are not enforced for this user")
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(restore)
	return restore
}

// TestCodexJournalNeverDecidesNativeRoute is the journal half of C-1: with a
// valid rolling-upgrade journal naming a ready private current route, the
// production-wired controller creates, catalogs, resolves, guards a durable
// create, and recovers the broker only on the default daemon endpoint, and
// never reads, Lstats, or writes the journal.
func TestCodexJournalNeverDecidesNativeRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	codexHome := filepath.Join(home, "codex-home")
	bin := filepath.Join(home, "bin")
	for _, dir := range []string{codexHome, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", bin)
	domain, err := defaultCodexStateDomainID(os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := codexBrokerStateDomain(os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil || paths.StateDir != stateDir {
		t.Fatalf("App state dir %q (err %v) differs from broker state domain %q", paths.StateDir, err, stateDir)
	}
	journal, bodyBefore, modBefore := retiredRefsJournalFixture(t, stateDir, domain)
	journaled, _, _ := journal.Load()

	health := codexappserver.Decide(codexappserver.AvailabilityAvailable, codexappserver.ReasonNone, "0.154.0",
		codexappserver.EndpointStdioProxy, codexappserver.ConnectionReady, true)
	health.VersionRelation, health.ManagerOwnership = codexappserver.VersionCurrent, codexappserver.ManagerManaged
	health.NativeAction, health.RunningVersion = codexappserver.NativeActionReady, "0.154.0"
	controller := newCodexNativeThreadController(paths.StateDir)
	controller.probe = func(context.Context) codexappserver.Health { return health }
	var opened []codexNativeEndpointRoute
	events := []string{}
	controller.open = func(_ context.Context, route codexNativeEndpointRoute, _ bool) (codexNativeThreadClient, error) {
		opened = append(opened, route)
		return &orderedNativeThreadClient{events: &events}, nil
	}
	defaultEndpoint := coremetadata.CodexEndpointRef{StateDomainID: domain, EndpointGenerationID: "codex-0.154.0"}
	isDefault := func(route codexNativeEndpointRoute) bool {
		return route.Default && route.SocketPath == "" && route.Endpoint.Same(defaultEndpoint) && route.State == coremetadata.CodexGenerationCurrent
	}

	dirBefore := retiredRefsJournalDirState(t, journal)
	restore := sealJournalDir(t, journal)
	current, err := controller.Current(context.Background())
	if err != nil || !isDefault(current) {
		t.Fatalf("current = %+v, %v", current, err)
	}
	routes, err := controller.CatalogRoutes(context.Background())
	if err != nil || len(routes) != 1 || !isDefault(routes[0]) {
		t.Fatalf("catalog = %+v, %v", routes, err)
	}
	for _, route := range journaled.Routes {
		resolved, err := controller.Resolve(context.Background(), route.Generation.Endpoint)
		if err != nil || !isDefault(resolved) {
			t.Fatalf("resolve journaled %s = %+v, %v", route.Generation.Endpoint.EndpointGenerationID, resolved, err)
		}
	}
	workspace := coremetadata.AgentWorkspace{CWD: home}
	binding, err := controller.Create(context.Background(), current, workspace, "", "generation-1")
	if err != nil || binding.ThreadID != "thread-production-order" {
		t.Fatalf("create with durable guard = %+v, %v", binding, err)
	}
	if len(opened) != 2 || !isDefault(opened[0]) || !isDefault(opened[1]) {
		t.Fatalf("create/durable guard opened %+v, want the default endpoint twice", opened)
	}
	session, err := newCodexBrokerObserverSessionForRoute(brokerTestIdentity("journal-route"), home, nil, current)
	if err != nil {
		t.Fatalf("broker session: %v", err)
	}
	defer session.Close()
	if session.recoveryGuard != nil {
		t.Fatal("default broker recovery still carries a journal guard")
	}
	session.recoverRoute = controller.Current
	if err := session.refreshRoute(context.Background()); err != nil {
		t.Fatalf("default broker recovery refused: %v", err)
	}
	restore()

	bodyAfter, modAfter := snapshotCodexJournal(t, journal)
	if !reflect.DeepEqual(bodyBefore, bodyAfter) || !modAfter.Equal(modBefore) {
		t.Fatalf("journal changed: bytes-equal=%t mtime %s -> %s", reflect.DeepEqual(bodyBefore, bodyAfter), modBefore, modAfter)
	}
	if got := retiredRefsJournalDirState(t, journal); got != dirBefore {
		t.Fatalf("journal directory changed:\nbefore=%s\nafter=%s", dirBefore, got)
	}
}

// retiredRefsJournalDirState lists the journal directory entries with their
// sizes and mtimes, so a new lock or temp file is visible.
func retiredRefsJournalDirState(t *testing.T, store *codexupgrade.Store) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(entry.Name() + ":" + info.ModTime().UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatInt(info.Size(), 10) + ";")
	}
	return out.String()
}

// TestCodexConsumerProjectionIgnoresJournal proves the Registry-only consumer
// projections (control binding, control fence revalidation, live Pane
// decoration) are identical with and without a rolling-upgrade journal.
func TestCodexConsumerProjectionIgnoresJournal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	project := func(t *testing.T) []any {
		t.Helper()
		cmd, store, binding := exactControlCLICommand(t)
		if _, _, err := store.mutator().SetCodexGenerationLifecycle(&store.registry, "agt-alpha-codex", phase6CLIEndpoint(), retiredRefsDraining(phase6CLIEndpoint())); err != nil {
			t.Fatal(err)
		}
		registry := store.registry.Clone()
		agent, _ := registry.Agent("agt-alpha-codex")
		resolved, resolveErr := resolveExactAgentControlBinding(registry, *agent, binding.live, true, paths.StateDir)
		fenceErr := cmd.revalidateControlConsumerFence(resolved)
		row := livePaneRow{Pane: "%7", Agent: aiModeCodex, AIState: codexgeneration.LifecycleStateDraining, ReplyState: true}
		decorateGenerationLivePane(&row, registry)
		return []any{resolved, resolveErr, fenceErr, row}
	}
	without := project(t)
	journal, body, mod := retiredRefsJournalFixture(t, paths.StateDir, phase6CLIEndpoint().StateDomainID)
	with := project(t)
	if !reflect.DeepEqual(without, with) {
		t.Fatalf("consumer projections changed with a journal:\nwithout=%+v\nwith=%+v", without, with)
	}
	if without[1] != nil || without[2] != nil || without[0].(exactAgentControlBinding).Fence == "" || without[3].(livePaneRow).AuthorityFence == "" {
		t.Fatalf("projection fixture is not an exact live binding: %+v", without)
	}
	after, afterMod := snapshotCodexJournal(t, journal)
	if !bytes.Equal(body, after) || !afterMod.Equal(mod) {
		t.Fatal("consumer projection touched the journal")
	}
}
