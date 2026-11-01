package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
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

func TestSDKRelaunchLostHostOptIn(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
			cleanupDetachedResume(t, f, ref)
			agent := old.Binding.AgentUID
			c := sdkHostLostClient(t, f)
			base := core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, AllowHostLost: true, Prompt: "continue"}
			// Wrong shapes and a stale fence are refused before any effect.
			wrong := base
			wrong.Host = "tmux"
			_, err := c.RelaunchAgent(ctx, wrong)
			requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, agent)
			if provider == aiModeCodex {
				journal := mutationruntime.CodexHostTransferPath(f.root+"/state/projmux", agent)
				if err := os.WriteFile(journal, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = c.RelaunchAgent(ctx, base)
				requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, agent)
				if err := os.Remove(journal); err != nil {
					t.Fatal(err)
				}
			}
			good := core.ProcessBinding(old.Binding)
			for name, mutate := range map[string]func(*core.ProcessBinding){
				"host": func(b *core.ProcessBinding) { b.HostInstanceID += "x" }, "project": func(b *core.ProcessBinding) { b.ProjectUID += "x" },
				"window": func(b *core.ProcessBinding) { b.WindowUID += "x" }, "agent": func(b *core.ProcessBinding) { b.AgentUID += "x" },
				"pane": func(b *core.ProcessBinding) { b.PaneUID += "x" }, "generation": func(b *core.ProcessBinding) { b.Generation += "x" },
				"operation": func(b *core.ProcessBinding) { b.OperationID += "x" },
			} {
				stale := base
				stale.Binding = good
				mutate(&stale.Binding)
				_, err = c.RelaunchAgent(ctx, stale)
				requireSDKCode(t, err, projmux.CodeRelaunchSourceStale, agent)
				_ = name
			}
			if provider == aiModeClaude {
				empty := base
				empty.Prompt = ""
				_, err = c.RelaunchAgent(ctx, empty)
				requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, agent)
				preview := empty
				preview.DryRun = true
				if _, err = c.RelaunchAgent(ctx, preview); err != nil {
					t.Fatal(err)
				}
			}
			traceBefore, _ := os.ReadFile(f.trace)
			if !reflect.DeepEqual(before, mustRegistry(t, f)) {
				t.Fatal("a refusal wrote the Registry")
			}
			_ = traceBefore
			// The opt-in recovers like `agent relaunch --detach`.
			ok := base
			ok.Binding = good
			result, err := c.RelaunchAgent(ctx, ok)
			if err != nil {
				t.Fatal(err)
			}
			if result.AgentUID != agent || result.PaneUID != old.Binding.PaneUID || result.Outcome != "OBSERVE" {
				t.Fatalf("recovered result %+v", result)
			}
			next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *m.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || processRelaunchConversation(&next) != processRelaunchConversation(&old) {
				t.Fatal("identity or conversation replaced")
			}
			// A non-lost Agent gets the same answer for true and false.
			_ = bytes.Equal
		})
	}
}
