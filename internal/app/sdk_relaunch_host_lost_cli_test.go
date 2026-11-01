package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func requireSDKCode(t *testing.T, err error, want projmux.Code, agent string) {
	t.Helper()
	var refused *projmux.Refusal
	if !errors.As(err, &refused) || refused.Code != want || refused.AgentUID != agent {
		t.Fatalf("want %s for %s got %v", want, agent, err)
	}
}

// The same lost-host seed as TestHostLostExplicitRelaunchActualCLI, requested
// through the public SDK: refusals first (no effect), then the recovery.
func TestSDKRelaunchLostHostOptIn(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
			cleanupDetachedResume(t, f, ref)
			agent := old.Binding.AgentUID
			c := sdkHostLostClient(t, f)
			prompt := ""
			if provider == aiModeClaude {
				prompt = "continue"
			}
			base := core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, AllowHostLost: true, Prompt: prompt, Model: "new-model", Effort: "high"}
			unchanged := func(what string) {
				t.Helper()
				if !reflect.DeepEqual(before, mustRegistry(t, f)) {
					t.Fatalf("%s wrote the Registry", what)
				}
			}
			wrong := base
			wrong.Host = "tmux"
			_, err := c.RelaunchAgent(ctx, wrong)
			requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, agent)
			unchanged("host move")
			if provider == aiModeCodex {
				journal := mutationruntime.CodexHostTransferPath(f.root+"/state/projmux", agent)
				if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = c.RelaunchAgent(ctx, base)
				requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, agent)
				if err := os.Remove(journal); err != nil {
					t.Fatal(err)
				}
				unchanged("pending transfer")
			}
			good := core.ProcessBinding(old.Binding)
			for name, mutate := range map[string]func(*core.ProcessBinding){
				"host": func(b *core.ProcessBinding) { b.HostInstanceID += "x" }, "project": func(b *core.ProcessBinding) { b.ProjectUID += "x" },
				"window": func(b *core.ProcessBinding) { b.WindowUID += "x" }, "agent": func(b *core.ProcessBinding) { b.AgentUID += "x" },
				"pane": func(b *core.ProcessBinding) { b.PaneUID += "x" }, "generation": func(b *core.ProcessBinding) { b.Generation += "x" },
				"operation": func(b *core.ProcessBinding) { b.OperationID += "x" },
			} {
				t.Run("stale "+name, func(t *testing.T) {
					stale := base
					stale.Binding = good
					mutate(&stale.Binding)
					_, err := c.RelaunchAgent(ctx, stale)
					requireSDKCode(t, err, projmux.CodeRelaunchSourceStale, agent)
					unchanged("stale " + name)
				})
			}
			if provider == aiModeClaude {
				empty := base
				empty.Prompt = ""
				_, err = c.RelaunchAgent(ctx, empty)
				requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, agent)
				unchanged("empty prompt")
				preview := empty
				preview.DryRun = true
				if _, err = c.RelaunchAgent(ctx, preview); err != nil {
					t.Fatal(err)
				}
				unchanged("empty prompt dry run")
			}
			ok := base
			ok.Binding = good
			result, err := c.RelaunchAgent(ctx, ok)
			if err != nil {
				t.Fatal(err)
			}
			if result.AgentUID != agent || result.PaneUID != old.Binding.PaneUID || result.NewPaneUID != old.Binding.PaneUID || result.Outcome != "resumed" || result.DryRun {
				t.Fatalf("recovered result %+v", result)
			}
			next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *m.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || processRelaunchConversation(&next) != processRelaunchConversation(&old) {
				t.Fatal("identity or conversation replaced")
			}
			after := mustRegistry(t, f)
			a, _ := after.Agent(agent)
			previous, _ := before.Agent(agent)
			p, _ := after.Pane(old.Binding.PaneUID)
			if a.Metadata.Annotations[m.AnnotationAgentModel] != "new-model" || a.Metadata.Annotations[m.AnnotationAgentEffort] != "high" || !a.Status.SessionRef.SameConversation(previous.Status.SessionRef) {
				t.Fatal("new recipe or conversation mismatch")
			}
			if p.Status.LastTermination == nil || p.Status.LastTermination.Source != m.TerminationSourceReconcile || p.Status.LastTermination.ExitCode != nil || m.MatchesProcessWait(old.Binding, p.Status.LastTermination) {
				t.Fatal("invented old Wait")
			}
			detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
			final := mustRegistry(t, f)
			p, _ = final.Pane(old.Binding.PaneUID)
			if !p.Status.Activation.IsZero() || !m.MatchesProcessWait(next.Binding, p.Status.LastTermination) {
				t.Fatal("new provider not reaped")
			}
		})
	}
}

// A live owner is not a lost host: the opt-in changes nothing for it.
func TestSDKRelaunchAllowHostLostIgnoredForLiveOwner(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
			defer first.shutdown(t)
			c := sdkHostLostClient(t, f)
			type answer struct {
				Outcome string
				Code    projmux.Code
				Message string
			}
			ask := func(allow bool) answer {
				result, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: first.ref, Detach: true, DryRun: true, Confirm: true, Prompt: "continue", AllowHostLost: allow})
				var refused *projmux.Refusal
				if errors.As(err, &refused) {
					return answer{Code: refused.Code, Message: refused.Error()}
				}
				if err != nil {
					t.Fatal(err)
				}
				return answer{Outcome: result.Outcome}
			}
			off, on := ask(false), ask(true)
			if off != on || (off.Outcome == "" && off.Code == "") {
				t.Fatalf("live owner: false=%+v true=%+v", off, on)
			}
		})
	}
}

// A host-lost source that changes after planning is source-stale to the SDK;
// the CLI text is unchanged.
func TestEncodeDetachedRelaunchErrorClassifiesHostLostSourceChange(t *testing.T) {
	var out bytes.Buffer
	cause := deferredRefused("host-lost preparation source changed")
	_ = encodeDetachedRelaunchError(json.NewEncoder(&out), m.ProcessIdentity{}, processhost.Binding{}, nil, cause)
	var reply processDetachedReply
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ErrorCode != "relaunch-source-stale" || reply.Error != cause.Error() {
		t.Fatalf("reply %+v", reply)
	}
}
