package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Prepare leaves the ownerless frozen recipe whose proof starts the first input
// through the public ResumeAgent; omitting it keeps today's waiting claim.
func TestSDKRelaunchPreparedProofStartsFirstInput(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	t.Run("default keeps the waiting claim", func(t *testing.T) {
		f := hostLostCLIProviderFixture(t, aiModeClaude)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		ref, uid, _ := liveClaudeForRelaunch(t, ctx, f)
		result, err := sdkHostLostClient(t, f).RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true})
		if err != nil || !result.ClaimWaiting || result.Outcome != "restarted" || result.Phase != "Offline" || result.Preparation != nil || result.PromptRequired || result.RelaunchCommand != "" || result.AgentUID != uid {
			t.Fatalf("default %+v %v", result, err)
		}
	})
	t.Run("prepare", func(t *testing.T) {
		f := hostLostCLIProviderFixture(t, aiModeClaude)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ref, uid, old := liveClaudeForRelaunch(t, ctx, f)
		c := sdkHostLostClient(t, f)
		result, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, Prepare: true})
		if err != nil {
			t.Fatal(err)
		}
		proof := result.Preparation
		if proof == nil || result.ClaimWaiting || result.Outcome != "restarted" || result.Phase != "Offline" || result.PromptRequired || result.RelaunchCommand != "" {
			t.Fatalf("prepared result %+v", result)
		}
		after := mustRegistry(t, f)
		agent, _ := after.Agent(uid)
		pane, _ := after.Pane(old.Binding.PaneUID)
		if proof.AgentUID != uid || proof.PaneUID != old.Binding.PaneUID || proof.Conversation != processRelaunchConversation(&old) || proof.LaunchDigest == "" || proof.HostLost || proof.Binding != core.ProcessBinding(pane.Status.ProcessSession.Binding) {
			t.Fatalf("proof %+v", *proof)
		}
		if agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() {
			t.Fatal("prepared recipe has an owner")
		}
		claim, err := mutationruntime.ReadDeferredClaim(mutationruntime.DeferredClaimPath(f.store.Path(), uid))
		if err != nil || claim.Agent != "" {
			t.Fatalf("prepared recipe kept a claim %+v %v", claim, err)
		}
		valid, err := c.ValidateAgentResumePreparation(ctx, core.ValidateAgentResumePreparationRequest{Ref: ref, Preparation: *proof})
		if err != nil || valid != *proof {
			t.Fatalf("validate %+v %v", valid, err)
		}
		resumed, err := c.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: ref, Detach: true, Prompt: "first input", Binding: valid.Binding, Preparation: &valid})
		if err != nil || resumed.Binding.Generation == valid.Binding.Generation {
			t.Fatalf("first input %+v %v", resumed, err)
		}
		next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
			return r.Binding.Generation != valid.Binding.Generation && r.ConnectionID != ""
		})
		if next.Binding.AgentUID != uid || processRelaunchConversation(&next) != processRelaunchConversation(&old) {
			t.Fatal("identity or conversation replaced")
		}
		detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
		claim, err = mutationruntime.ReadDeferredClaim(mutationruntime.DeferredClaimPath(f.store.Path(), uid))
		if err != nil || claim.Agent != "" {
			t.Fatalf("first input left a claim %+v %v", claim, err)
		}
	})
}

// A request that does not fit Prepare is refused before any effect, DryRun
// included; a dry run of the right shape plans only and carries no proof.
func TestSDKRelaunchPrepareShapeAndDryRun(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := hostLostCLIProviderFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	ref, uid, _ := liveClaudeForRelaunch(t, ctx, f)
	c := sdkHostLostClient(t, f)
	before := mustRegistry(t, f)
	for name, req := range map[string]core.RelaunchAgentRequest{
		"prompt":    {Ref: ref, Detach: true, Confirm: true, Prompt: "continue", Prepare: true},
		"no detach": {Ref: ref, Confirm: true, Prepare: true},
		"tmux":      {Ref: ref, Host: "tmux", Detach: true, Confirm: true, Prepare: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.RelaunchAgent(ctx, req)
			requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, uid)
			if !reflect.DeepEqual(before, mustRegistry(t, f)) {
				t.Fatal("refusal wrote the Registry")
			}
		})
	}
	planned, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, DryRun: true, Prepare: true})
	if err != nil || !planned.DryRun || planned.Preparation != nil || planned.ClaimWaiting || planned.AgentUID != uid {
		t.Fatalf("dry run %+v %v", planned, err)
	}
	if !reflect.DeepEqual(before, mustRegistry(t, f)) {
		t.Fatal("dry run wrote the Registry")
	}
}

// The Codex row of the Prepare shape needs a Codex fixture of its own.
func TestSDKRelaunchPrepareRefusesCodex(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := hostLostCLIProviderFixture(t, aiModeCodex)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	defer first.shutdown(t)
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	uid := strings.TrimPrefix(first.ref, "uid:")
	before := mustRegistry(t, f)
	c := sdkHostLostClient(t, f)
	for _, dry := range []bool{true, false} {
		_, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: first.ref, Detach: true, Confirm: true, DryRun: dry, Prepare: true})
		requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, uid)
		if !reflect.DeepEqual(before, mustRegistry(t, f)) {
			t.Fatalf("dry=%v refusal wrote the Registry", dry)
		}
	}
}

// The SDK result carries the CLI's own answers for a lost-host Claude, and the
// refusal of the execution carries them with the error.
func TestSDKRelaunchLostHostPromptAnswerMatchesCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := hostLostCLIProviderFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ref, before, old := recordedLostHostCLI(t, ctx, f, aiModeClaude)
	cleanupDetachedResume(t, f, ref)
	agent := old.Binding.AgentUID
	const command = "projmux agent relaunch uid:%s --detach --yes -- <prompt>"
	want := strings.Replace(command, "%s", agent, 1)
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--detach", "--dry-run", "-o", "json").CombinedOutput()
	if err != nil {
		t.Fatalf("cli %v %s", err, out)
	}
	var cli agentRelaunchResult
	if err = json.Unmarshal(out, &cli); err != nil || !cli.PromptRequired || cli.RelaunchCommand != want {
		t.Fatalf("cli %+v %v", cli, err)
	}
	c := sdkHostLostClient(t, f)
	base := core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, AllowHostLost: true}
	preview := base
	preview.DryRun = true
	planned, err := c.RelaunchAgent(ctx, preview)
	if err != nil || !planned.DryRun || !planned.PromptRequired || planned.RelaunchCommand != want || planned.Outcome != cli.Outcome || planned.Outcome != "would-resume" || planned.AgentUID != agent {
		t.Fatalf("sdk dry run %+v %v", planned, err)
	}
	refused, err := c.RelaunchAgent(ctx, base)
	requireSDKCode(t, err, projmux.CodeRelaunchRequestInvalid, agent)
	if !refused.PromptRequired || refused.RelaunchCommand != want {
		t.Fatalf("refused result %+v", refused)
	}
	if !reflect.DeepEqual(before, mustRegistry(t, f)) {
		t.Fatal("refusal wrote the Registry")
	}
	given := base
	given.Prompt = "continue"
	resumed, err := c.RelaunchAgent(ctx, given)
	if err != nil || resumed.Outcome != "resumed" || resumed.PromptRequired || resumed.RelaunchCommand != "" {
		t.Fatalf("with prompt %+v %v", resumed, err)
	}
	detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
}
