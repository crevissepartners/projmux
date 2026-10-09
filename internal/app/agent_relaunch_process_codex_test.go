package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessCodexRelaunchActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := processCodexRelaunchFixture(t)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persona.NewDefaultStore(paths).Write("different", []byte("different instructions")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "controls"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return len(r.Pending) == 2 })
	before, _ := f.store.LoadReadOnly()
	wireBefore, _ := os.ReadFile(f.trace)
	for _, tc := range []struct {
		flags []string
		token string
	}{
		{[]string{"--model", "new-model"}, relaunchReasonAgentBusy},
		{[]string{"--instructions", "different", "--yes"}, "codex-instructions-immutable"},
		{[]string{"--profile", "missing", "--yes"}, "profile-not-found"},
	} {
		out, err := exec.CommandContext(ctx, f.binary, append([]string{"agent", "relaunch", first.ref}, tc.flags...)...).CombinedOutput()
		if err == nil || !bytes.Contains(out, []byte(tc.token)) {
			t.Fatalf("refusal %q: %v %s", tc.flags, err, out)
		}
		after, err := f.store.LoadReadOnly()
		wire, _ := os.ReadFile(f.trace)
		if err != nil || !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wire) {
			t.Fatal("refusal mutated Registry or provider", err)
		}
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--model", "new-model", "--effort", "high", "--dry-run", "-o", "json").CombinedOutput()
	var preview agentRelaunchResult
	if err != nil || json.Unmarshal(out, &preview) != nil || preview.Outcome != personaOutcomeWouldRestart || !preview.ConfirmationRequired {
		t.Fatalf("preview %v %s", err, out)
	}
	after, _ := f.store.LoadReadOnly()
	wire, _ := os.ReadFile(f.trace)
	if !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wire) {
		t.Fatal("dry-run mutated Registry or provider")
	}

	second, result := startProcessRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model", "--effort", "high", "--yes")
	current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding.Generation != old.Binding.Generation && r.ThreadID != ""
	})
	assertProcessCLIForeground(t, f, current)
	if result.AgentUID != old.Binding.AgentUID || result.NewPaneUID != old.Binding.PaneUID || result.Outcome != personaOutcomeRestarted || !reflect.DeepEqual(result.RelaunchReasons, []string{"model-changed", "effort-changed"}) {
		t.Fatalf("result %+v", result)
	}
	if current.ThreadID != old.ThreadID || current.History == nil || current.History.Binding != old.Binding || len(current.History.Expired) != len(old.Pending) {
		t.Fatalf("history %+v", current)
	}
	err = first.cmd.Wait()
	first.done = true
	if err != nil && !strings.Contains(first.stderr.String(), "process control closed") {
		t.Fatalf("old owner Wait: %v %s", err, first.stderr.String())
	}
	if strings.Contains(first.stderr.String(), "delete agent") {
		t.Fatal("old owner gave destructive cleanup advice")
	}
	after, _ = f.store.LoadReadOnly()
	agent, _ := after.Agent(result.AgentUID)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentModel] != "new-model" || agent.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != "high" || agent.Metadata.Annotations[coremetadata.AnnotationAgentModelSource] != coremetadata.SettingSourceRelaunch {
		t.Fatalf("annotations %+v", agent.Metadata.Annotations)
	}
	if _, _, live := after.CurrentProcessActivation(old.Binding); live {
		t.Fatal("old writer is still current")
	}
	pane, _ := after.Pane(current.Binding.PaneUID)
	socket := processHostSocket(aiModeCodex, f.store.Path(), current.Binding.PaneUID, current.Binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	stale := processForegroundRequest{Authority: processhost.Authority{Binding: processSchemaBinding(old.Binding), Connection: old.ConnectionID, Session: old.ThreadID}, Action: "respond", Token: processForegroundToken{Request: processhost.Request{ID: old.Pending[0].ID, Kind: old.Pending[0].Kind, Connection: old.ConnectionID, Session: old.ThreadID, Turn: old.TurnID}}, Response: processhost.Response{Allow: true}}
	wireBefore, _ = os.ReadFile(f.trace)
	reply, err := callProcessForeground(ctx, socket, identity, pane.Status.Activation.Process.HostProcess, codexProcessExchange{Foreground: &stale})
	if err == nil && reply.Accepted {
		t.Fatal("old control accepted by new writer")
	}
	wire, _ = os.ReadFile(f.trace)
	if !bytes.Equal(wireBefore, wire) {
		t.Fatal("old control wrote to new provider")
	}
	if strings.Count(string(wire), "thread/start") != 1 || strings.Count(string(wire), "thread/resume") != 2 || strings.Count(string(wire), "turn/start") != 1 || strings.Count(string(wire), "thread/settings/update") != 1 || !bytes.Contains(wire, []byte("new-model")) || !bytes.Contains(wire, []byte("high")) {
		t.Fatalf("wire %s", wire)
	}
	third, promptOnly := startProcessRelaunchCLI(t, ctx, f, first.ref, "--yes", "--", "new task")
	if promptOnly.Outcome != personaOutcomeRestarted || len(promptOnly.RelaunchReasons) != 0 {
		t.Fatalf("prompt result %+v", promptOnly)
	}
	if err := second.cmd.Wait(); err != nil && !strings.Contains(second.stderr.String(), "is retired") {
		t.Fatalf("replaced owner %v %s", err, second.stderr.String())
	}
	second.done = true
	third.shutdown(t)
	wire, _ = os.ReadFile(f.trace)
	if strings.Count(string(wire), "new task") != 1 || strings.Count(string(wire), "thread/start") != 1 {
		t.Fatalf("prompt replay/new conversation: %s", wire)
	}
}

func TestProcessCodexRelaunchPermissionsKeptActualCLI(t *testing.T) {
	f := processCodexRelaunchFixture(t)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	writeCodexProfile(t, profile.NewDefaultStore(paths), "guard", "model = \"stub-model\"\neffort = \"low\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"on-request\"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "guard", "--", "first task"))
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ThreadID != "" && r.TurnID == "" })
	before, _ := f.store.LoadReadOnly()
	wireBefore, _ := os.ReadFile(f.trace)
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--profile", "none", "--yes").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte(relaunchReasonCodexPermissionsKept)) {
		t.Fatalf("permissions kept refusal: %v %s", err, out)
	}
	after, _ := f.store.LoadReadOnly()
	wire, _ := os.ReadFile(f.trace)
	if !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wire) {
		t.Fatal("permissions refusal wrote Registry/provider")
	}
	first.shutdown(t)
}

func TestProcessCodexRelaunchUnretiredWriterActualCLI(t *testing.T) {
	f := processCodexRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--", "hold"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.TurnID != "" })
	before, _ := f.store.LoadReadOnly()
	wireBefore, _ := os.ReadFile(f.trace)
	// A missing owner is not an actual child Wait. No second writer is allowed.
	_ = first.cmd.Process.Signal(syscall.SIGKILL)
	_ = first.cmd.Wait()
	first.done = true
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--model", "new-model", "--yes").CombinedOutput()
	if err == nil {
		t.Fatalf("unretired owner accepted: %s", out)
	}
	after, _ := f.store.LoadReadOnly()
	wire, _ := os.ReadFile(f.trace)
	if !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wire) {
		t.Fatal("unretired writer mutated Registry/provider")
	}
	if strings.Count(string(wire), "initialize") != 2 || bytes.Contains(wire, []byte("thread/resume")) {
		t.Fatalf("new writer started: %s", wire)
	}
	pane, _ := before.Pane(old.Binding.PaneUID)
	child := pane.Status.Activation.Process.Child
	waitCodexCreate(t, ctx, func() bool { identity, _, err := localipc.Process(child.PID); return err != nil || identity != child })
}

func TestProcessCodexRelaunchActiveWriterFailureActualCLI(t *testing.T) {
	f := processCodexRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--", "first task"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ThreadID != "" && r.TurnID == "" })
	first.shutdown(t)
	before, _ := f.store.LoadReadOnly()
	previous, _ := before.Agent(old.Binding.AgentUID)
	path := filepath.Join(f.root, "codex-provider.py")
	raw, _ := os.ReadFile(path)
	source := strings.Replace(string(raw), "elif method=='thread/resume':\n  reply({'thread':{'id':'process-thread'},'model':model,'reasoningEffort':effort,'sandbox':sandbox,'approvalPolicy':approval})", "elif method=='thread/resume':emit({'id':n['id'],'error':{'code':-32600,'message':'thread has an active writer'}})", 1)
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--model", "rejected-model").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("active writer")) || !bytes.Contains(out, []byte("-32600")) || !bytes.Contains(out, []byte("recover with:")) || bytes.Contains(out, []byte("-- <prompt>")) {
		t.Fatalf("active writer refusal %v %s", err, out)
	}
	after, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := after.Agent(old.Binding.AgentUID)
	pane, _ := processResumePane(after, old.Binding.AgentUID)
	if agent.Status.Phase != coremetadata.PhaseOffline || !reflect.DeepEqual(agent.Metadata.Annotations, previous.Metadata.Annotations) || pane.Status.ProcessSession.ThreadID != old.ThreadID || !pane.Status.Activation.IsZero() {
		t.Fatalf("failed launch lost previous recipe/conversation: %+v %s", agent, out)
	}
	wire, _ := os.ReadFile(f.trace)
	if strings.Count(string(wire), "thread/start") != 1 || strings.Count(string(wire), "thread/resume") != 1 || strings.Count(string(wire), "turn/start") != 1 {
		t.Fatalf("failure created another conversation/turn: %s", wire)
	}
}

// This fixture models the existing ResumeThreadWithSettings barrier: a resume
// loads the saved settings, an update applies changed values, and a second
// resume echoes them. No no-op probe is sent to the old writer.
func processCodexRelaunchFixture(t *testing.T) processCreateCLI {
	f := processResumeCLIFixture(t, aiModeCodex)
	path := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "turn=0", "model='stub-model'\neffort='low'\nsandbox={'type':'readOnly'}\napproval='on-request'\nturn=0", 1)
	source = strings.Replace(source, "elif method in ('thread/start','thread/resume'):", `elif method=='thread/resume':
  reply({'thread':{'id':'process-thread'},'model':model,'reasoningEffort':effort,'sandbox':sandbox,'approvalPolicy':approval})
 elif method=='thread/settings/update':
  p=n['params'];model=p.get('model',model);effort=p.get('effort',effort);sandbox=p.get('sandboxPolicy',sandbox);approval=p.get('approvalPolicy',approval);reply({})
 elif method=='thread/start':`, 1)
	if err = os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}
