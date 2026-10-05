package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessRelaunchPromptGrammar(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		prompt []string
	}{
		{[]string{"uid:agent-test", "--model", "haiku", "--", "literal", "--model", "payload"}, []string{"literal", "--model", "payload"}},
		{[]string{"--model", "haiku", "--", "uid:agent-test"}, nil},
	} {
		request, err := parseAgentRelaunchArgs(tc.args, io.Discard)
		if err != nil || request.agentRef != "uid:agent-test" || !reflect.DeepEqual(request.prompt, tc.prompt) {
			t.Fatalf("%q: %+v %v", tc.args, request, err)
		}
	}
}

// Early refusals must not reach the Registry writer or either runtime.
func TestProcessClaudeRelaunchEarlyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flags     []string
		replyOnly bool
		want      string
	}{
		{name: "reply-only", flags: []string{"--model", "haiku", "--yes", "--", "task"}, replyOnly: true, want: replyOnlyReasonLaunchFixed},
		{name: "named socket", flags: []string{"--socket", "isolated", "--dry-run"}, want: "process agents do not use a tmux socket"},
		{name: "socket path", flags: []string{"--socket-path", "/tmp/process-relaunch-test.sock", "--dry-run"}, want: "process agents do not use a tmux socket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := New()
			reg, _, paneUID := processInventoryFixture(t)
			pane, _ := reg.Pane(paneUID)
			pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
			pane.Status.Activation = coremetadata.PaneActivation{}
			agent, _ := reg.Agent(pane.Metadata.OwnerUID())
			if tc.replyOnly {
				if agent.Metadata.Annotations == nil {
					agent.Metadata.Annotations = make(map[string]string)
				}
				agent.Metadata.Annotations[coremetadata.AnnotationAgentDialogueReplyOnly] = coremetadata.DialogueReplyOnlyOn
			}
			before := reg.Clone()
			app.agent.loadRegistry = func() (coremetadata.Registry, error) { return reg, nil }
			var stdout, stderr bytes.Buffer
			err := app.agent.runRelaunch(append([]string{"uid:" + agent.Metadata.UID}, tc.flags...), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tc.want) || stdout.Len() != 0 || !reflect.DeepEqual(reg, before) {
				t.Fatalf("err=%v stdout=%q Registry unchanged=%v", err, stdout.String(), reflect.DeepEqual(reg, before))
			}
		})
	}
}

func startProcessRelaunchCLI(t *testing.T, ctx context.Context, f processCreateCLI, ref string, flags ...string) (*processResumeCLIInvocation, agentRelaunchResult) {
	t.Helper()
	args := append([]string{"agent", "relaunch", ref, "-o", "json"}, flags...)
	run := &processResumeCLIInvocation{cmd: exec.CommandContext(ctx, f.binary, args...), stderr: &bytes.Buffer{}, ref: ref}
	run.cmd.Stderr = run.stderr
	var err error
	run.input, err = run.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := run.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = run.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = run.input.Close()
		if !run.done {
			_ = run.cmd.Process.Kill()
			_ = run.cmd.Wait()
		}
	})
	line, err := bufio.NewReader(out).ReadBytes('\n')
	if err != nil {
		t.Fatalf("relaunch result: %v %s", err, run.stderr.String())
	}
	var result agentRelaunchResult
	if err = json.Unmarshal(line, &result); err != nil {
		t.Fatalf("result %s: %v", line, err)
	}
	return run, result
}

func TestProcessClaudeRelaunchActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := processResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--effort", "low", "--", "question"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return len(r.Pending) > 0 })
	before, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	wireBefore, _ := os.ReadFile(f.trace)
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--model", "new-model", "--", "unconfirmed"}, relaunchReasonAgentBusy},
		{[]string{"--model", "new-model", "--yes"}, "requires -- <prompt>"},
		{[]string{"--profile", "missing", "--yes", "--", "task"}, "profile-not-found"},
	} {
		out, err := exec.CommandContext(ctx, f.binary, append([]string{"agent", "relaunch", first.ref}, tc.flags...)...).CombinedOutput()
		if err == nil || !bytes.Contains(out, []byte(tc.want)) {
			t.Fatalf("refusal %q: %v %s", tc.flags, err, out)
		}
		after, err := f.store.LoadReadOnly()
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("refusal changed Registry", err)
		}
		wireAfter, _ := os.ReadFile(f.trace)
		if !bytes.Equal(wireBefore, wireAfter) {
			t.Fatal("refusal wrote provider frame")
		}
	}
	dryArgs := []string{"agent", "relaunch", first.ref, "--model", "new-model", "--effort", "high", "--dry-run", "-o", "json"}
	dryRaw, err := exec.CommandContext(ctx, f.binary, dryArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run %v %s", err, dryRaw)
	}
	var dry agentRelaunchResult
	if json.Unmarshal(dryRaw, &dry) != nil || !dry.ConfirmationRequired || dry.Outcome != personaOutcomeWouldRestart {
		t.Fatalf("preview %s", dryRaw)
	}
	after, _ := f.store.LoadReadOnly()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("dry-run wrote Registry")
	}
	second, result := startProcessRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model", "--effort", "high", "--yes", "--", "next task")
	if result.AgentUID != strings.TrimPrefix(first.ref, "uid:") || result.NewPaneUID != old.Binding.PaneUID || result.Outcome != personaOutcomeRestarted || !reflect.DeepEqual(result.RelaunchReasons, []string{"model-changed", "effort-changed"}) {
		t.Fatalf("result %+v", result)
	}
	current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding.Generation != old.Binding.Generation && r.TurnID == ""
	})
	if current.SessionID != old.SessionID || current.History == nil || current.History.Binding != old.Binding || len(current.History.Expired) != len(old.Pending) {
		t.Fatalf("history %+v", current)
	}
	// Old caller finished only after its exact child Wait and lease cleanup.
	err = first.cmd.Wait()
	t.Logf("old foreground owner: exit=%v stderr=%q", err, first.stderr.String())
	if err != nil && !strings.Contains(first.stderr.String(), "process control closed") {
		t.Fatalf("old owner Wait: %v %s", err, first.stderr.String())
	}
	first.done = true
	after, err = f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := after.Agent(result.AgentUID)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentModel] != "new-model" || agent.Metadata.Annotations[coremetadata.AnnotationAgentModelSource] != coremetadata.SettingSourceRelaunch || agent.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != "high" {
		t.Fatalf("settings %+v", agent.Metadata.Annotations)
	}
	if _, _, ok := after.CurrentProcessActivation(old.Binding); ok {
		t.Fatal("old generation still current")
	}
	currentPane, _ := after.Pane(current.Binding.PaneUID)
	socket := processHostSocket(aiModeClaude, f.store.Path(), current.Binding.PaneUID, current.Binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := callProcessForeground(ctx, socket, identity, currentPane.Status.Activation.Process.HostProcess, claudeProcessCheck{Foreground: &processForegroundRequest{Authority: processhost.Authority{Binding: processSchemaBinding(old.Binding), Connection: old.ConnectionID, Session: old.SessionID}, Action: "respond", Token: processForegroundToken{Request: processhost.Request{ID: old.Pending[0].ID, Kind: old.Pending[0].Kind, Connection: old.ConnectionID, Session: old.SessionID, Turn: old.TurnID}}, Response: processhost.Response{Allow: true}}})
	if err == nil && stale.Accepted {
		t.Fatal("old generation response reached new child")
	}

	// A prompt alone explicitly asks for another foreground generation.
	third, promptOnly := startProcessRelaunchCLI(t, ctx, f, first.ref, "--yes", "--", "prompt-only task")
	if promptOnly.Outcome != personaOutcomeRestarted || len(promptOnly.RelaunchReasons) != 0 {
		t.Fatalf("prompt-only relaunch %+v", promptOnly)
	}
	last := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding.Generation != current.Binding.Generation && r.TurnID == ""
	})
	if last.SessionID != old.SessionID || last.Binding.PaneUID != old.Binding.PaneUID {
		t.Fatal("prompt-only relaunch changed conversation or Pane")
	}
	if err := second.cmd.Wait(); err != nil && (!strings.Contains(second.stderr.String(), "process control closed") || !strings.Contains(second.stderr.String(), "is retired")) {
		t.Fatalf("replaced relaunch owner: %v %s", err, second.stderr.String())
	}
	if strings.Contains(second.stderr.String(), "delete agent") {
		t.Fatal("replaced relaunch owner gave destructive cleanup advice")
	}
	second.done = true
	third.shutdown(t)
	wire, _ := os.ReadFile(f.trace)
	if strings.Count(string(wire), "next task") != 1 || strings.Count(string(wire), "prompt-only task") != 1 || bytes.Contains(wire, []byte("unconfirmed")) {
		t.Fatalf("wire %s", wire)
	}
	argv, _ := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
	if !bytes.Contains(argv, []byte(`"--resume", "process-session"`)) || !bytes.Contains(argv, []byte(`"--model", "new-model"`)) {
		t.Fatalf("argv %s", argv)
	}
}

func TestProcessClaudeRelaunchFailedStartRestoresRecipeActualCLI(t *testing.T) {
	f := processResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
	first.shutdown(t)
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
	// An executable that fails after spawn must be reaped before restoring settings.
	if err = os.WriteFile(filepath.Join(f.root, "claude"), []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--model", "rejected-model", "--", "retry task").CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("recover with:")) || !bytes.Contains(output, []byte("retry task")) {
		t.Fatalf("failure %v %s", err, output)
	}
	after, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := after.Agent(previous.Metadata.UID)
	if agent.Status.Phase != coremetadata.PhaseOffline || !reflect.DeepEqual(previous.Metadata.Annotations, agent.Metadata.Annotations) {
		t.Fatalf("recipe not restored %+v: %s", agent, output)
	}
	pane, _ := processResumePane(after, agent.Metadata.UID)
	if pane.Status.ProcessSession.SessionID != old.SessionID || !pane.Status.Activation.IsZero() {
		t.Fatal("failed writer/conversation leak")
	}
}

func TestProcessClaudeRelaunchSelfTargetActualCLI(t *testing.T) {
	f := processResumeCLIFixture(t, aiModeClaude)
	path := filepath.Join(f.root, "provider.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "elif prompt=='register-again':", `elif prompt=='self-relaunch':
   b=json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])
   p=subprocess.run([os.environ['PMX_TEST_PROCESS_BINARY'],'agent','relaunch','uid:'+b['Agent'],'--model','new-model','--yes','--','unsafe'],capture_output=True)
   open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'self-refusal'),'wb').write(p.stdout+p.stderr)
   emit({'type':'result','subtype':'success','session_id':'process-session'})
  elif prompt=='register-again':`, 1)
	if err = os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "self-relaunch"))
	for {
		text, _ := os.ReadFile(filepath.Join(f.root, "self-refusal"))
		if bytes.Contains(text, []byte(relaunchReasonSelfTarget)) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("self refusal %s", text)
		case <-time.After(10 * time.Millisecond):
		}
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentModel] == "new-model" {
		t.Fatal("self-target recorded settings")
	}
	first.shutdown(t)
	argv, _ := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
	if len(bytes.Split(bytes.TrimSpace(argv), []byte("\n"))) != 1 {
		t.Fatal("self-target started a second child")
	}
}

func TestProcessClaudeRelaunchUnknownOwnerDoesNotStartChildActualCLI(t *testing.T) {
	f := processResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.cmd.Wait()
	first.done = true
	before, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--model", "new-model", "--yes", "--", "unsafe").CombinedOutput()
	if err == nil {
		t.Fatalf("unreaped old owner accepted: %s", out)
	}
	after, readErr := f.store.LoadReadOnly()
	if readErr != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("unknown owner mutated Registry: %v", readErr)
	}
	pane, _ := processResumePane(before, strings.TrimPrefix(first.ref, "uid:"))
	child := pane.Status.Activation.Process.Child
	for {
		identity, _, e := localipc.Process(child.PID)
		if e != nil || identity != child {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("orphan provider after owner death")
		case <-time.After(10 * time.Millisecond):
		}
	}

	argv, _ := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
	if len(bytes.Split(bytes.TrimSpace(argv), []byte("\n"))) != 1 {
		t.Fatal("unknown owner started another child")
	}
}

func TestProcessClaudeRelaunchDigestChangesActualCLI(t *testing.T) {
	f := processResumeCLIFixture(t, aiModeClaude)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	writeAgentGuidance(t, paths, []byte("PROCESS_GUIDANCE_V1"))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
	// SessionStart reserves the id before stream init. Wait for the durable
	// ref and the initialized, completed first turn before comparing a
	// read-only preview against the whole Registry.
	waitCodexCreate(t, ctx, func() bool {
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			return false
		}
		agent, found := reg.Agent(old.Binding.AgentUID)
		pane, present := reg.Pane(old.Binding.PaneUID)
		return found && present && agent.Status.SessionRef != nil &&
			agent.Status.SessionRef.Claude != nil && agent.Status.SessionRef.Claude.SessionID == old.SessionID &&
			agent.Status.Activation.State == coremetadata.ActivationAcknowledged &&
			pane.Status.ProcessSession != nil && pane.Status.ProcessSession.SessionID == old.SessionID &&
			pane.Status.ProcessSession.TurnID == ""
	})
	before, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	oldAgent, _ := before.Agent(strings.TrimPrefix(first.ref, "uid:"))
	writeAgentGuidance(t, paths, []byte("PROCESS_GUIDANCE_V2"))
	writeLinkRules(t, paths, strings.TrimPrefix(f.project, "uid:"), linkRulesAlpha)
	preview, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--dry-run", "-o", "json").CombinedOutput()
	if err != nil {
		t.Fatalf("preview %v %s", err, preview)
	}
	var dry agentRelaunchResult
	if err := json.Unmarshal(preview, &dry); err != nil {
		t.Fatal(err)
	}
	want := []string{agentsettings.ReasonGuidanceChanged, agentsettings.ReasonLinkRulesChanged}
	if dry.Outcome != personaOutcomeWouldRestart || !reflect.DeepEqual(dry.RelaunchReasons, want) {
		t.Fatalf("digest preview %+v", dry)
	}
	afterPreview, _ := f.store.LoadReadOnly()
	if !reflect.DeepEqual(before, afterPreview) {
		beforeJSON, _ := json.Marshal(before)
		afterJSON, _ := json.Marshal(afterPreview)
		t.Fatalf("digest preview changed Registry: before=%s after=%s", beforeJSON, afterJSON)
	}
	second, result := startProcessRelaunchCLI(t, ctx, f, first.ref, "--yes", "--", "digest task")
	current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding.Generation != old.Binding.Generation && r.TurnID == ""
	})
	if current.SessionID != old.SessionID || !reflect.DeepEqual(result.RelaunchReasons, want) {
		t.Fatalf("digest relaunch %+v", result)
	}
	_ = first.cmd.Wait()
	first.done = true
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(oldAgent.Metadata.UID)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest] == oldAgent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest] || agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest] == "" || agent.Metadata.Annotations[coremetadata.AnnotationAgentSystemPromptSnapshot] != coremetadata.SystemPromptSnapshotOff {
		t.Fatalf("digest recipe %+v", agent.Metadata.Annotations)
	}
	second.shutdown(t)
	argv, _ := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
	if !bytes.Contains(argv, []byte(`"--system-prompt-snapshot", "off"`)) {
		t.Fatalf("snapshot argv %s", argv)
	}
}

func TestProcessClaudeRelaunchFailureRecoveryRequiresOffline(t *testing.T) {
	for _, phase := range []coremetadata.AgentPhase{coremetadata.PhasePending, coremetadata.PhaseRunning, coremetadata.PhaseOffline} {
		t.Run(string(phase), func(t *testing.T) {
			app := New()
			reg, _, paneUID := processInventoryFixture(t)
			pane, _ := reg.Pane(paneUID)
			agent, _ := reg.Agent(pane.Metadata.OwnerUID())
			agent.Status.Phase = phase
			app.agent.loadRegistry = func() (coremetadata.Registry, error) { return reg, nil }
			message := app.agent.processRelaunchFailureRecovery(reg, *agent, agentRelaunchRequest{model: "haiku", prompt: []string{"retry task"}})
			if !strings.Contains(message, "recover with:") || !strings.Contains(message, "retry task") {
				t.Fatal(message)
			}
			if phase != coremetadata.PhaseOffline {
				if !strings.Contains(message, "inspect with: projmux describe agent uid:"+agent.Metadata.UID) || !strings.Contains(message, "only then recover with:") || !strings.Contains(message, "exact Wait") {
					t.Fatal(message)
				}
			} else if strings.Contains(message, "inspect with:") {
				t.Fatal(message)
			}
		})
	}
}
