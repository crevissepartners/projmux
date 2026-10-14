package app

import (
	"bytes"
	"context"
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
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
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
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, mode := range []string{"foreground", "detach"} {
			for _, changed := range []bool{false, true} {
				t.Run(provider+"/"+mode+"/"+map[bool]string{true: "changed", false: "unchanged"}[changed], func(t *testing.T) {
					f := hostLostCLIProviderFixture(t, provider)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
					if err := os.WriteFile(filepath.Join(f.root, provider), []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
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
						t.Fatalf("failure %v %s", err, out)
					}
					recovery := "agent resume uid:"
					if changed {
						recovery = "agent relaunch uid:"
					}
					if !bytes.Contains(out, []byte(recovery)) || provider == aiModeClaude && !bytes.Contains(out, []byte("--")) {
						t.Fatal(string(out))
					}
					after := mustRegistry(t, f)
					a, _ := after.Agent(old.Binding.AgentUID)
					previous, _ := before.Agent(old.Binding.AgentUID)
					p, _ := after.Pane(old.Binding.PaneUID)
					if !reflect.DeepEqual(a.Metadata.Annotations, previous.Metadata.Annotations) || !reflect.DeepEqual(a.Spec, previous.Spec) || processRelaunchConversation(p.Status.ProcessSession) != processRelaunchConversation(&old) {
						t.Fatal("recipe/conversation not restored")
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
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
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
				out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
				recovery := "projmux agent relaunch uid:" + old.Binding.AgentUID + " --detach --yes -- <prompt>"
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
