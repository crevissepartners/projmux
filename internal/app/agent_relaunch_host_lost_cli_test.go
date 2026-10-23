package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Retire only our fixture processes, then restore their captured Running record.
// This models a lost host without killing a real provider or manufacturing Wait.
func recordedLostHostCLI(t *testing.T, ctx context.Context, f processCreateCLI, provider string) (string, coremetadata.Registry, coremetadata.ProcessSessionRecord) {
	t.Helper()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	before := mustRegistry(t, f)
	p, _ := processResumePane(before, strings.TrimPrefix(first.ref, "uid:"))
	old := *p.Status.ProcessSession.Clone()
	first.shutdown(t)
	if !processActivationAbsent(*p.Status.Activation.Process, localipc.Process) {
		t.Fatal("fixture identities still present")
	}
	p.Status.ProcessSession.ResumeState = coremetadata.ProcessResumeUnknown
	p.Status.LastTermination = nil
	a, _ := before.Agent(old.Binding.AgentUID)
	a.Status.LastTermination = nil
	_, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error { *reg = before.Clone(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return first.ref, mustRegistry(t, f), old
}

func TestHostLostExplicitRelaunchActualCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, mode := range []string{"foreground", "detach"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				f := hostLostCLIProviderFixture(t, provider)
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
				cleanupDetachedResume(t, f, ref)
				flags := []string{"--model", "new-model", "--effort", "high"}
				if mode != "foreground" {
					flags = append(flags, "--detach")
				}
				if provider == aiModeClaude {
					flags = append(flags, "--", "continue")
				}
				var run *processResumeCLIInvocation
				if mode == "foreground" {
					run, _ = startProcessRelaunchCLI(t, ctx, f, ref, flags...)
				} else {
					out := detachedResumeCLI(t, ctx, f, append([]string{"agent", "relaunch", ref}, flags...)...)
					if !bytes.Contains(out, []byte(processRelaunchHostLostNotice)) {
						t.Fatal("missing lost-host warning", string(out))
					}
				}
				next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
				})
				if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || processRelaunchConversation(&next) != processRelaunchConversation(&old) {
					t.Fatal("identity/conversation replaced")
				}
				after := mustRegistry(t, f)
				a, _ := after.Agent(old.Binding.AgentUID)
				previous, _ := before.Agent(old.Binding.AgentUID)
				p, _ := after.Pane(old.Binding.PaneUID)
				if a.Metadata.Annotations[coremetadata.AnnotationAgentModel] != "new-model" || a.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != "high" || !a.Status.SessionRef.SameConversation(previous.Status.SessionRef) {
					t.Fatal("new recipe/conversation mismatch")
				}
				if p.Status.LastTermination == nil || p.Status.LastTermination.Source != coremetadata.TerminationSourceReconcile || p.Status.LastTermination.ExitCode != nil || coremetadata.MatchesProcessWait(old.Binding, p.Status.LastTermination) {
					t.Fatal("invented old Wait")
				}
				if run != nil {
					run.shutdown(t)
					if !bytes.Contains(run.stderr.Bytes(), []byte(processRelaunchHostLostNotice)) {
						t.Fatal("missing lost-host warning")
					}
				} else {
					detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
				}
				final := mustRegistry(t, f)
				p, _ = final.Pane(old.Binding.PaneUID)
				if !p.Status.Activation.IsZero() || !coremetadata.MatchesProcessWait(next.Binding, p.Status.LastTermination) {
					t.Fatal("new provider not reaped")
				}
			})
		}
	}
}

func TestHostLostExplicitRelaunchFailureActualCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, mode := range []string{"foreground", "detach"} {
			cases := []struct {
				name                string
				changed, afterBirth bool
			}{{name: "unchanged"}, {name: "changed", changed: true}}
			if provider == aiModeClaude && mode == "detach" {
				cases = append(cases, struct {
					name                string
					changed, afterBirth bool
				}{name: "changed-after-birth", changed: true, afterBirth: true})
			}
			for _, scenario := range cases {
				t.Run(provider+"/"+mode+"/"+scenario.name, func(t *testing.T) {
					changed := scenario.changed
					f := hostLostCLIProviderFixture(t, provider)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
					marker := filepath.Join(f.root, "failure-birth.json")
					stub := "#!/bin/sh\nexit 7\n"
					if scenario.afterBirth {
						stub = fmt.Sprintf(`#!/usr/bin/env python3
import json, os, sys, time
registry, marker, pane_uid, old_generation = %q, %q, %q, %q
deadline = time.monotonic() + 5
while time.monotonic() < deadline:
    with open(registry) as source:
        reg = json.load(source)
    pane = next(p for p in reg["panes"] if p["metadata"]["uid"] == pane_uid)
    activation = pane["status"].get("activation", {}).get("process", {})
    if activation.get("child", {}).get("pid") == os.getpid() and activation.get("binding", {}).get("generation") != old_generation:
        with open(marker, "w") as evidence:
            json.dump(activation, evidence)
        sys.exit(7)
    time.sleep(0.001)
sys.exit(99)
`, f.store.Path(), marker, old.Binding.PaneUID, old.Binding.Generation)
					}
					if err := os.WriteFile(filepath.Join(f.root, provider), []byte(stub), 0700); err != nil {
						t.Fatal(err)
					}
					args := []string{"agent", "relaunch", ref}
					if mode == "detach" {
						args = append(args, "--detach")
					}
					if changed {
						args = append(args, "--model", "rejected-model")
					}
					if provider == aiModeClaude {
						args = append(args, "--", "continue")
					}
					out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
					if err == nil || !bytes.Contains(out, []byte("recover with:")) {
						diagnostic, loadErr := f.store.LoadReadOnly()
						beforeJSON, beforeErr := json.Marshal(before)
						afterJSON, afterErr := json.Marshal(diagnostic)
						t.Fatalf("failure %v output=%s\nbefore=%s (marshal=%v)\nafter=%s (load=%v marshal=%v)", err, out, beforeJSON, beforeErr, afterJSON, loadErr, afterErr)
					}
					recovery := "agent resume uid:"
					if changed {
						recovery = "agent relaunch uid:"
					}
					if !bytes.Contains(out, []byte(recovery)) || provider == aiModeClaude && !bytes.Contains(out, []byte("--")) {
						t.Fatal(string(out))
					}
					var birth coremetadata.ProcessActivation
					if scenario.afterBirth {
						evidence, readErr := os.ReadFile(marker)
						if readErr != nil || json.Unmarshal(evidence, &birth) != nil || birth.Child.PID <= 0 || birth.Binding.Generation == old.Binding.Generation || birth.Binding.AgentUID != old.Binding.AgentUID {
							t.Fatalf("controlled birth not observed: %v %s; output=%s", readErr, evidence, out)
						}
					}
					after := mustRegistry(t, f)
					a, _ := after.Agent(old.Binding.AgentUID)
					previous, _ := before.Agent(old.Binding.AgentUID)
					p, _ := after.Pane(old.Binding.PaneUID)
					if !reflect.DeepEqual(a.Metadata.Annotations, previous.Metadata.Annotations) || !reflect.DeepEqual(a.Spec, previous.Spec) || processRelaunchConversation(p.Status.ProcessSession) != processRelaunchConversation(&old) {
						beforeJSON, beforeErr := json.Marshal(before)
						afterJSON, afterErr := json.Marshal(after)
						t.Fatalf("recipe/conversation not restored; error=%v output=%s\nbefore=%s (marshal=%v)\nafter=%s (marshal=%v)", err, out, beforeJSON, beforeErr, afterJSON, afterErr)
					}
					if scenario.afterBirth {
						if !coremetadata.MatchesProcessWait(birth.Binding, p.Status.LastTermination) || p.Status.LastTermination.ExitCode == nil || *p.Status.LastTermination.ExitCode != 7 || !processActivationAbsent(birth, localipc.Process) {
							snapshot, _ := json.Marshal(p)
							t.Fatalf("controlled birth/exit retirement not confirmed: %s; output=%s", snapshot, out)
						}
						observed, _ := json.Marshal(struct {
							Birth coremetadata.ProcessActivation
							Pane  *coremetadata.Pane
							Agent *coremetadata.Agent
						}{birth, p, a})
						t.Logf("controlled birth -> exit7 -> exact Wait/identity absence -> recipe restored: %s", observed)
					}
					original, _ := before.Pane(old.Binding.PaneUID)
					if p.Status.Activation.Process != nil && !reflect.DeepEqual(p.Status.Activation.Process, original.Status.Activation.Process) && !processActivationAbsent(*p.Status.Activation.Process, localipc.Process) {
						t.Fatal("failed child remains")
					}
					if p.Status.LastTermination != nil && p.Status.LastTermination.Generation == old.Binding.Generation && coremetadata.MatchesProcessWait(old.Binding, p.Status.LastTermination) {
						t.Fatal("invented old Wait")
					}
				})
			}
		}
	}
}

func hostLostCLIProviderFixture(t *testing.T, provider string) processCreateCLI {
	t.Helper()
	if provider == aiModeCodex {
		return processCodexRelaunchFixture(t)
	}
	return deferredResumeCLIFixture(t, provider)
}

func TestHostLostExplicitRelaunchClaudeRequiresPromptCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, mode := range []string{"foreground", "detach"} {
		for _, prompt := range []string{"", "   "} {
			t.Run(mode+"/"+map[bool]string{true: "omitted", false: "whitespace"}[prompt == ""], func(t *testing.T) {
				f := hostLostCLIProviderFixture(t, aiModeClaude)
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				ref, before, old := recordedLostHostCLI(t, ctx, f, aiModeClaude)
				traceBefore, err := os.ReadFile(f.trace)
				if err != nil {
					t.Fatal(err)
				}
				calls := len(deferredArgv(t, f))
				args := []string{"agent", "relaunch", ref, "--model", "new-model"}
				if mode == "detach" {
					args = append(args, "--detach")
				}
				if prompt != "" {
					args = append(args, "--", prompt)
				}
				recovery := "projmux agent relaunch uid:" + old.Binding.AgentUID + " --detach --yes -- <prompt>"
				for _, output := range []string{"text", "json"} {
					previewArgs := []string{"agent", "relaunch", ref, "--model", "new-model", "--dry-run"}
					if output == "json" {
						previewArgs = append(previewArgs, "-o", "json")
					}
					if mode == "detach" {
						previewArgs = append(previewArgs, "--detach")
					}
					if prompt != "" {
						previewArgs = append(previewArgs, "--", prompt)
					}
					out, err := exec.CommandContext(ctx, f.binary, previewArgs...).CombinedOutput()
					if err != nil {
						t.Fatalf("preview %s: %v %s", output, err, out)
					}
					if output == "json" {
						var result agentRelaunchResult
						if json.Unmarshal(out, &result) != nil || !result.DryRun || !result.PromptRequired || result.RelaunchCommand != recovery || result.AgentUID != old.Binding.AgentUID || result.NewModel != "new-model" || result.ConfirmationRequired {
							t.Fatalf("preview JSON %s", out)
						}
					} else if !bytes.Contains(out, []byte(recovery)) || !bytes.Contains(out, []byte("execution requires a nonempty first prompt")) || !bytes.Contains(out, []byte("nothing was changed")) {
						t.Fatalf("preview text %s", out)
					}
					traceAfter, readErr := os.ReadFile(f.trace)
					if readErr != nil || !reflect.DeepEqual(before, mustRegistry(t, f)) || !bytes.Equal(traceBefore, traceAfter) || len(deferredArgv(t, f)) != calls {
						t.Fatal("preview produced effects", readErr)
					}
				}
				out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
				if err == nil || !bytes.Contains(out, []byte(recovery)) || !bytes.Contains(out, []byte("nothing was changed")) {
					t.Fatalf("prompt refusal %v %s", err, out)
				}
				after := mustRegistry(t, f)
				traceAfter, err := os.ReadFile(f.trace)
				if err != nil || !reflect.DeepEqual(before, after) || !bytes.Equal(traceBefore, traceAfter) || len(deferredArgv(t, f)) != calls {
					t.Fatal("prompt refusal produced effects", err)
				}
				c := deferredFixtureCommand(f)
				for _, path := range []string{c.deferredClaimPath(old.Binding.AgentUID), c.deferredStatePath("deferred-launches", old.Binding.AgentUID)} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("prompt refusal created claim/recipe", err)
					}
				}
			})
		}
	}
}
