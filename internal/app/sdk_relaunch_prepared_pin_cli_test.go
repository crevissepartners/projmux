package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// liveClaudeForRelaunch starts one foreground Claude process Agent the SDK can
// relaunch on its own host, as the claim fixtures of the CLI tests do.
func liveClaudeForRelaunch(t *testing.T, ctx context.Context, f processCreateCLI) (string, string, coremetadata.ProcessSessionRecord) {
	t.Helper()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeClaude, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	// A relaunch retires this foreground owner by control-stop, so its exit
	// status says nothing here: close its input and reap it without asserting.
	t.Cleanup(func() {
		_ = first.input.Close()
		_ = first.cmd.Wait()
		first.done = true
	})
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ConnectionID != "" })
	cleanupDetachedResume(t, f, first.ref)
	return first.ref, old.Binding.AgentUID, old
}

// This file names no field added with Prepare, so it also runs against the base
// commit to observe today's literals before the change.
func TestSDKRelaunchPreparedBasePin(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	t.Run("waiting claim without Prepare", func(t *testing.T) {
		f := hostLostCLIProviderFixture(t, aiModeClaude)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		ref, uid, _ := liveClaudeForRelaunch(t, ctx, f)
		result, err := sdkHostLostClient(t, f).RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true})
		t.Logf("PIN default claim: ClaimWaiting=%v Outcome=%q Phase=%q AgentUID==uid:%v err=%v", result.ClaimWaiting, result.Outcome, result.Phase, result.AgentUID == uid, err)
		if err != nil || !result.ClaimWaiting || result.Outcome != "restarted" || result.Phase != "Offline" || result.AgentUID != uid {
			t.Fatalf("default %+v %v", result, err)
		}
	})
	t.Run("lost-host dry run answers", func(t *testing.T) {
		f := hostLostCLIProviderFixture(t, aiModeClaude)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		ref, _, old := recordedLostHostCLI(t, ctx, f, aiModeClaude)
		out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--detach", "--dry-run", "-o", "json").CombinedOutput()
		if err != nil {
			t.Fatalf("cli %v %s", err, out)
		}
		var cli agentRelaunchResult
		if err = json.Unmarshal(out, &cli); err != nil {
			t.Fatal(err)
		}
		t.Logf("PIN lost-host dry run: promptRequired=%v relaunchCommand=%q outcome=%q agent=%s", cli.PromptRequired, cli.RelaunchCommand, cli.Outcome, old.Binding.AgentUID)
		if !cli.PromptRequired || cli.RelaunchCommand != "projmux agent relaunch uid:"+old.Binding.AgentUID+" --detach --yes -- <prompt>" || cli.Outcome != "would-resume" {
			t.Fatalf("lost-host dry run %+v", cli)
		}
	})
}
