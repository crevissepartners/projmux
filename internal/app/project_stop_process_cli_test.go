package app

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The copied product talks to a real fixture provider and its foreground host.
// No tmux Session exists; Project stop must still retire that host through Wait.
func TestProjectStopHeadlessActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
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
	actual, _, identityErr := newAgentCommand().readProcessIdentity()(birth.PID)
	if identityErr == nil && actual == birth {
		t.Fatal("provider child still alive")
	}
	out, err = exec.CommandContext(ctx, f.binary, "stop", "project", "uid:"+f.project).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "nothing was changed") {
		t.Fatalf("empty stop: %v %s", err, out)
	}
}
