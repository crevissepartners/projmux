package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"

	sdk "github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The copied product talks to a real fixture provider and its foreground host.
// No tmux Session exists; Project stop must still retire that host through Wait.
func TestProjectStopHeadlessActualCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := newProcessCreateCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	run := startResumeCLIInvocation(t, ctx, f, f.args("--", "initial task"))
	record := awaitProcessResumeRecord(t, ctx, f, run.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" })
	// SessionStart reserves an id before stream init. Observe the established
	// session on this binding before asking Stop to preserve its conversation.
	awaitProcessResumeRecord(t, ctx, f, run.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		if r.Binding != record.Binding || r.SessionID != record.SessionID {
			return false
		}
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		agent, ok := reg.Agent(record.Binding.AgentUID)
		pane, paneOK := reg.Pane(record.Binding.PaneUID)
		return ok && paneOK && agent.Status.PaneRef == record.Binding.PaneUID &&
			pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == record.Binding &&
			agent.Status.SessionRef != nil && agent.Status.SessionRef.Claude != nil &&
			agent.Status.SessionRef.Claude.SessionID == record.SessionID
	})
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(strings.TrimPrefix(run.ref, "uid:"))
	pane, _ := reg.Pane(agent.Status.PaneRef)
	birth := pane.Status.Activation.Process.Child
	// Protected means undeletable, not un-stoppable.
	_, _, err = f.store.UpdateConvergent(func(r *coremetadata.Registry) error {
		a, _ := r.Agent(agent.Metadata.UID)
		if a.Metadata.Labels == nil {
			a.Metadata.Labels = map[string]string{}
		}
		a.Metadata.Labels[coremetadata.LabelProtected] = "true"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "stop", "project", "uid:"+f.project, "-o", "receipt").CombinedOutput()
	if err != nil || !strings.Contains(string(out), agent.Metadata.UID) {
		t.Fatalf("stop: %v %s", err, out)
	}
	var receipt cli.OperationReceipt
	if err := json.Unmarshal(out, &receipt); err != nil {
		t.Fatalf("decode receipt: %v %s", err, out)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	if receipt.Effects.Runtime != cli.RuntimeAgentsStopped || len(receipt.AffectedUIDs) != 2 {
		t.Fatalf("process-only runtime/affected rows: %s", out)
	}
	actions := map[string]cli.ReceiptAction{}
	for _, row := range receipt.AffectedUIDs {
		actions[row.UID] = row.Action
	}
	if actions[f.project] != cli.ActionUnchanged || actions[agent.Metadata.UID] != cli.ActionStopped {
		t.Fatalf("process-only Project/Agent actions: %s", out)
	}
	err = run.cmd.Wait()
	run.done = true
	if err != nil {
		t.Fatalf("owner Wait: %v %s", err, run.stderr.String())
	}
	reg, err = f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ = reg.Agent(agent.Metadata.UID)
	pane, _ = reg.Pane(pane.Metadata.UID)
	if agent.Status.Phase != coremetadata.PhaseOffline || pane.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable || pane.Status.ProcessSession.SessionID != record.SessionID || agent.Status.SessionRef == nil || agent.Status.SessionRef.Claude == nil || agent.Status.SessionRef.Claude.SessionID != record.SessionID || !coremetadata.MatchesProcessWait(record.Binding, pane.Status.LastTermination) {
		t.Fatalf("phase=%s record=%+v evidence=%+v", agent.Status.Phase, pane.Status.ProcessSession, pane.Status.LastTermination)
	}
	if !pane.Status.ProcessSession.StopRequested {
		t.Fatal("project stop lost explicit stopRequested")
	}
	actual, _, identityErr := newAgentCommand().readProcessIdentity()(birth.PID)
	if identityErr == nil && actual == birth {
		t.Fatal("provider child still alive")
	}
	out, err = exec.CommandContext(ctx, f.binary, "stop", "project", "uid:"+f.project).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "nothing was changed") {
		t.Fatalf("empty stop: %v %s", err, out)
	}
}

// Reuses the fixed protocol fixtures; no real provider or shared installation.
func TestStopRequestedAgentActualCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial task"))
			awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
			out, err := exec.CommandContext(ctx, f.binary, "agent", "stop", first.ref).CombinedOutput()
			if err != nil {
				t.Fatalf("stop: %v %s", err, out)
			}
			if err = first.cmd.Wait(); err != nil {
				t.Fatalf("owner Wait: %v %s", err, first.stderr.String())
			}
			first.done = true
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			if !old.StopRequested {
				t.Fatal("explicit stop did not mark retired session")
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			candidates := listResumableProcessAgents(reg, processResumeFilter{})
			found := false
			for _, candidate := range candidates {
				if candidate.Record.Binding == old.Binding {
					found = candidate.Record.StopRequested && candidate.Pane.Status.ProcessSession.StopRequested
				}
			}
			if !found {
				t.Fatal("marked resume candidate excluded or stripped")
			}
			out, err = exec.CommandContext(ctx, f.binary, "describe", "pane", "uid:"+old.Binding.PaneUID, "-o", "json").CombinedOutput()
			var described core.Pane
			if err != nil || json.Unmarshal(out, &described) != nil || described.Status.ProcessSession == nil || !described.Status.ProcessSession.StopRequested {
				t.Fatalf("describe JSON: %v %s", err, out)
			}
			state := filepath.Dir(filepath.Dir(f.store.Path()))
			client, err := sdk.NewClient(sdk.Options{ConfigDir: filepath.Join(state, "config"), StateDir: state, HomeDir: state})
			if err != nil {
				t.Fatal(err)
			}
			resource, err := client.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + old.Binding.PaneUID})
			if err != nil {
				t.Fatal(err)
			}
			projected, ok := resource.(core.Pane)
			if !ok || !reflect.DeepEqual(projected.Status.ProcessSession, described.Status.ProcessSession) {
				t.Fatal("SDK / describe parity mismatch")
			}
			second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "explicit resume"})
			current := awaitProcessResumeRecord(t, ctx, f, second.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if current.StopRequested {
				t.Fatal("new generation inherited stopRequested")
			}
			second.shutdown(t)
			retired := awaitProcessResumeRecord(t, ctx, f, second.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			out, err = exec.CommandContext(ctx, f.binary, "agent", "stop", second.ref).CombinedOutput()
			if err != nil {
				t.Fatalf("already offline stop: %v %s", err, out)
			}
			latest := awaitProcessResumeRecord(t, ctx, f, second.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.Binding == retired.Binding })
			if latest.StopRequested {
				t.Fatal("EOF/internal retirement or no-op Stop acquired intent")
			}
		})
	}
}

func TestStopRequestedCandidateProjection(t *testing.T) {
	reg := processResumeQueryFixture(t)
	candidates := listResumableProcessAgents(reg, processResumeFilter{})
	if len(candidates) != 1 {
		t.Fatal("fixture candidate missing")
	}
	pane, _ := reg.Pane(candidates[0].Record.Binding.PaneUID)
	pane.Status.ProcessSession.StopRequested = true
	before := reg.Clone()
	candidates = listResumableProcessAgents(reg, processResumeFilter{})
	if len(candidates) != 1 || !candidates[0].Record.StopRequested || !candidates[0].Pane.Status.ProcessSession.StopRequested {
		t.Fatal("candidate intent excluded or stripped")
	}
	if err := processResumeRefusal(reg, candidates[0].Agent.Metadata.UID, false); err != nil {
		t.Fatal("stop mark became a resume refusal:", err)
	}
	if !reflect.DeepEqual(before, reg) {
		t.Fatal("projection mutated registry")
	}
}
