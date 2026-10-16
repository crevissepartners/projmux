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
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// These fixtures run one current CLI image. They do not prove old/new binary
// interoperability, model consumption, or the instructions sent to a real model.
func TestProcessCodexHostUpgradeCurrentConfigurationCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, tc := range []struct {
		name, instructions, status, reason string
	}{
		{"preserved", "legacy", "preserved", "codex-instructions-immutable"},
		{"replacement-not-applied", "replacement", "not-applied", "codex-instructions-immutable"},
		{"unavailable-not-applied", "missing", "not-applied", "instructions-unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := processCodexRelaunchFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			personas := persona.NewDefaultStore(paths)
			for _, name := range []string{"legacy", "replacement"} {
				if _, err := personas.Write(name, []byte("fixture instruction body: "+name)); err != nil {
					t.Fatal(err)
				}
			}
			profiles := profile.NewDefaultStore(paths)
			beforeDigest := writeCodexProfile(t, profiles, "upgrade", "provider = \"codex\"\nmodel = \"stub-model\"\neffort = \"low\"\ninstructions = \"legacy\"\n")
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "upgrade", "--", "finished work"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			before := mustRegistry(t, f)
			a, _ := before.Agent(strings.TrimPrefix(first.ref, "uid:"))
			p, _ := before.Pane(a.Status.PaneRef)
			old := *p.Status.ProcessSession.Clone()
			oldActivation := *p.Status.Activation.Process
			if a.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != beforeDigest {
				t.Fatal("initial profile was not recorded")
			}
			cleanupDetachedResume(t, f, first.ref)
			// Explicit maintenance uses public Stop, not stdin EOF or host-lost
			// reconciliation, as the provider Wait/retirement barrier.
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
			first.shutdown(t)
			assertCodexUpgradeStopped(t, ctx, f, old, oldActivation)
			afterDigest := writeCodexProfile(t, profiles, "upgrade", "provider = \"codex\"\nmodel = \"current-model\"\neffort = \"high\"\ninstructions = \""+tc.instructions+"\"\n")
			if afterDigest == beforeDigest {
				t.Fatal("current profile did not change")
			}
			cmd := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--detach", "-o", "json")
			var notices bytes.Buffer
			cmd.Stderr = &notices
			out, err := cmd.Output()
			if err != nil || !strings.Contains(notices.String(), "foreground=detached") {
				t.Fatalf("detached resume handshake: %v %s %s", err, out, &notices)
			}
			var receipt struct {
				Items               []json.RawMessage         `json:"items"`
				SettingsApplication *agentSettingsApplication `json:"settingsApplication"`
			}
			if err := json.Unmarshal(out, &receipt); err != nil || len(receipt.Items) != 1 || receipt.SettingsApplication == nil {
				t.Fatalf("resume JSON: %v %s", err, out)
			}
			profileEntry := lifecycleEntry(t, receipt.SettingsApplication, "profile")
			if profileEntry.Status != "recorded" || profileEntry.Value == nil || *profileEntry.Value != "upgrade" {
				t.Fatalf("profile preparation: %+v", profileEntry)
			}
			for setting, want := range map[string]string{"model": "current-model", "effort": "high"} {
				e := lifecycleEntry(t, receipt.SettingsApplication, setting)
				if e.Status != "delivered" || e.Reason != "thread-settings-ready" || e.Value == nil || *e.Value != want {
					t.Fatalf("%s preparation: %+v", setting, e)
				}
			}
			e := lifecycleEntry(t, receipt.SettingsApplication, "instructions")
			if e.Status != tc.status || e.Reason != tc.reason {
				t.Fatalf("legacy instructions: %+v", e)
			}
			if tc.instructions == "replacement" && (e.Value == nil || *e.Value != "legacy" || e.RequestedValue == nil || *e.RequestedValue != "replacement") {
				t.Fatalf("lost immutable/current distinction: %+v", e)
			}
			for _, setting := range []string{"agent-guidance", "project-guidance", "link-rules"} {
				e := lifecycleEntry(t, receipt.SettingsApplication, setting)
				if e.Status != "preserved" || e.Reason != "codex-instructions-immutable" || e.Value != nil {
					t.Fatalf("invented instruction application: %+v", e)
				}
			}
			next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			after := mustRegistry(t, f)
			nextAgent, _ := after.Agent(old.Binding.AgentUID)
			nextPane, _ := after.Pane(old.Binding.PaneUID)
			if len(after.Agents) != len(before.Agents) || len(after.Panes) != len(before.Panes) || nextAgent.Status.PaneRef != old.Binding.PaneUID || nextPane.Metadata.OwnerUID() != old.Binding.AgentUID || next.ThreadID != old.ThreadID || next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || next.History == nil || next.History.Binding != old.Binding {
				t.Fatal("resume replaced identity or conversation")
			}
			activation := *nextPane.Status.Activation.Process
			view, ok := observeProcessHostOwner(after, f.store.Path(), nextPane.Clone())
			if !ok || view.OwnerMode != processHostOwnerDetached || view.State != "ready" || activation.Binding != next.Binding || activation.HostProcess == oldActivation.HostProcess || activation.Child == oldActivation.Child || next.Binding.HostInstanceID == old.Binding.HostInstanceID || next.Binding.OperationID == old.Binding.OperationID {
				t.Fatalf("new detached owner handshake/birth: %+v %+v", activation, view)
			}
			if nextAgent.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != afterDigest || nextAgent.Metadata.Annotations[coremetadata.AnnotationAgentModel] != "current-model" || nextAgent.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != "high" {
				t.Fatal("current profile preparation not committed")
			}
			// A live competing writer is refused before another provider request.
			wireBefore, err := os.ReadFile(f.trace)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--detach").CombinedOutput()
			wireAfter, readErr := os.ReadFile(f.trace)
			if err == nil || !bytes.Contains(duplicate, []byte("process-resume-owned")) || readErr != nil || !bytes.Equal(wireBefore, wireAfter) || !reflect.DeepEqual(after, mustRegistry(t, f)) {
				t.Fatalf("live writer refusal changed Registry/wire: %v %v %s", err, readErr, duplicate)
			}
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
			assertCodexUpgradeStopped(t, ctx, f, next, activation)
			frames := codexUpgradeWire(t, f)
			if len(frames["thread/start"]) != 1 || len(frames["thread/resume"]) != 2 || len(frames["thread/settings/update"]) != 1 || len(frames["turn/start"]) != 1 {
				t.Fatalf("unexpected conversation/settings/input ledger: %+v", frames)
			}
			initialInstructions, _ := frames["thread/start"][0]["developerInstructions"].(string)
			if !strings.Contains(initialInstructions, "fixture instruction body: legacy") || strings.Contains(initialInstructions, "fixture instruction body: replacement") {
				t.Fatal("fixture did not establish the legacy developer instructions")
			}
			update := frames["thread/settings/update"][0]
			if update["model"] != "current-model" || update["effort"] != "high" || update["threadId"] != old.ThreadID {
				t.Fatalf("current settings wire: %+v", update)
			}
			for _, params := range frames["thread/resume"] {
				if params["threadId"] != old.ThreadID {
					t.Fatalf("wrong resumed conversation: %+v", params)
				}
			}
			for _, method := range []string{"thread/resume", "thread/settings/update"} {
				for _, params := range frames[method] {
					for _, key := range []string{"developerInstructions", "baseInstructions"} {
						if value, present := params[key]; present && value != nil && value != "" {
							t.Fatalf("legacy instructions rewritten on %s: %+v", method, params)
						}
					}
				}
			}
		})
	}
}

func TestProcessCodexHostUpgradeResumeFailureCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := processCodexRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "finished work"))
	awaitDetachedInitialConversation(t, ctx, f, first.ref)
	before := mustRegistry(t, f)
	a, _ := before.Agent(strings.TrimPrefix(first.ref, "uid:"))
	p, _ := before.Pane(a.Status.PaneRef)
	old := *p.Status.ProcessSession.Clone()
	activation := *p.Status.Activation.Process
	cleanupDetachedResume(t, f, first.ref)
	detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
	first.shutdown(t)
	assertCodexUpgradeStopped(t, ctx, f, old, activation)
	stopped := mustRegistry(t, f)
	oldPane, _ := stopped.Pane(old.Binding.PaneUID)
	oldWait := *oldPane.Status.LastTermination
	path := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const needle = "elif method=='thread/resume':\n  reply({'thread':{'id':'process-thread'},'model':model,'reasoningEffort':effort,'sandbox':sandbox,'approvalPolicy':approval})"
	if strings.Count(string(raw), needle) != 1 {
		t.Fatal("provider resume failure fixture anchor changed")
	}
	script := strings.Replace(string(raw), needle, "elif method=='thread/resume':emit({'id':n['id'],'error':{'code':-32600,'message':'thread has an active writer'}})", 1)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	// One attempt only. A provider refusal never authorizes replay or fallback.
	out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--detach").CombinedOutput()
	if err == nil {
		t.Fatalf("provider refusal accepted: %s", out)
	}
	for _, text := range []string{`"method":"thread/resume"`, `"rpc_code":-32600`, `"cause":"rpc-refused"`, "recorded conversation preserved", "agent " + first.ref, "pane uid:" + old.Binding.PaneUID, "retry: projmux agent resume " + first.ref} {
		if !bytes.Contains(out, []byte(text)) {
			t.Fatalf("missing failure/recovery evidence %q: %s", text, out)
		}
	}
	if bytes.Contains(out, []byte("thread has an active writer")) {
		t.Fatalf("arbitrary provider error text leaked: %s", out)
	}
	// This injected refusal proves the failure path, not an external writer's
	// actual existence. Only the safe method/cause/code projection is public.
	if bytes.Contains(out, []byte("delete agent")) {
		t.Fatalf("destructive recovery advice: %s", out)
	}
	after := mustRegistry(t, f)
	failedAgent, _ := after.Agent(old.Binding.AgentUID)
	failedPane, _ := after.Pane(old.Binding.PaneUID)
	failed := failedPane.Status.ProcessSession
	if len(after.Agents) != len(stopped.Agents) || len(after.Panes) != len(stopped.Panes) || failedAgent.Status.Phase != coremetadata.PhaseOffline || failedAgent.Status.PaneRef != old.Binding.PaneUID || failedPane.Metadata.OwnerUID() != old.Binding.AgentUID || failed.ThreadID != old.ThreadID || failed.Binding.Generation == old.Binding.Generation || !failedPane.Status.Activation.IsZero() || !reflect.DeepEqual(failedAgent.Metadata.Annotations, a.Metadata.Annotations) {
		t.Fatalf("failed resume lost Offline identity/conversation/recipe: %+v %+v", failedAgent, failedPane)
	}
	// Save the old normal Wait separately; failure cleanup records its own Wait.
	if !coremetadata.MatchesProcessWait(old.Binding, &oldWait) || !coremetadata.MatchesProcessWait(failed.Binding, failedPane.Status.LastTermination) || !coremetadata.SameProcessWait(failedPane.Status.LastTermination, failedAgent.Status.LastTermination) {
		t.Fatalf("old/new actual Wait evidence missing: old=%+v failed=%+v", oldWait, failedPane.Status.LastTermination)
	}
	retired, err := mutationruntime.ReadProcessRetirement(f.store.Path(), failed.Binding, failedPane.Status.LastTermination)
	if err != nil {
		t.Fatal("failed generation retirement ledger", err)
	}
	processCLIUntil(t, ctx, func() bool { return processActivationAbsent(retired, localipc.Process) })
	frames := codexUpgradeWire(t, f)
	if len(frames["thread/start"]) != 1 || len(frames["thread/resume"]) != 1 || len(frames["turn/start"]) != 1 || len(frames["thread/settings/update"]) != 0 || frames["thread/resume"][0]["threadId"] != old.ThreadID {
		t.Fatalf("failed resume created/replayed input: %+v", frames)
	}
	t.Logf("provider refusal=%s old Wait=%+v failed binding=%+v actual Wait=%+v retirement=%+v", out, oldWait, failed.Binding, failedPane.Status.LastTermination, retired)
}

func assertCodexUpgradeStopped(t *testing.T, ctx context.Context, f processCreateCLI, record coremetadata.ProcessSessionRecord, activation coremetadata.ProcessActivation) {
	t.Helper()
	awaitProcessResumeRecord(t, ctx, f, "uid:"+record.Binding.AgentUID, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding == record.Binding && r.ResumeState == coremetadata.ProcessResumable
	})
	reg := mustRegistry(t, f)
	a, _ := reg.Agent(record.Binding.AgentUID)
	p, _ := reg.Pane(record.Binding.PaneUID)
	if a.Status.Phase != coremetadata.PhaseOffline || a.Status.PaneRef != p.Metadata.UID || p.Status.ProcessSession.ThreadID != record.ThreadID || !coremetadata.MatchesProcessWait(record.Binding, p.Status.LastTermination) || !coremetadata.SameProcessWait(p.Status.LastTermination, a.Status.LastTermination) {
		t.Fatalf("Stop lacks Offline/same conversation/exact actual Wait: %+v %+v", a, p)
	}
	processCLIUntil(t, ctx, func() bool { return processActivationAbsent(activation, localipc.Process) })
	t.Logf("actual Wait and host/child retirement: binding=%+v activation=%+v receipt=%+v", record.Binding, activation, p.Status.LastTermination)
}

func codexUpgradeWire(t *testing.T, f processCreateCLI) map[string][]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	frames := map[string][]map[string]any{}
	for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
		var frame struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatal(err)
		}
		frames[frame.Method] = append(frames[frame.Method], frame.Params)
	}
	return frames
}
