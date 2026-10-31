package app

import (
	"context"
	"errors"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

var keptAgentLabels = map[string]string{"owner": "kept-by-test", "tier": "gold"}

// setKeptAgentLabels gives the Agent the labels a caller would have set, in the
// fixture's own Registry, so each later step can be compared against them.
func setKeptAgentLabels(t *testing.T, f processCreateCLI, uid string) {
	t.Helper()
	if _, _, err := f.store.UpdateConvergent(func(reg *m.Registry) error {
		agent, ok := reg.Agent(uid)
		if !ok {
			return errors.New("fixture Agent is missing")
		}
		agent.Metadata.Labels = maps.Clone(keptAgentLabels)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func requireKeptAgentLabels(t *testing.T, f processCreateCLI, uid, step string) {
	t.Helper()
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := reg.Agent(uid)
	if !ok {
		t.Fatalf("%s: Agent %s is gone", step, uid)
	}
	if !maps.Equal(agent.Metadata.Labels, keptAgentLabels) {
		t.Fatalf("%s: labels %v, want %v", step, agent.Metadata.Labels, keptAgentLabels)
	}
}

// ResumeAgent resumes the same Agent after its host was lost and leaves the
// labels a caller set exactly as they were.
func TestPublicResumeAgentKeepsLabels(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	f := hostLostCLIProviderFixture(t, "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	ref, before, old := recordedLostHostCLI(t, ctx, f, "claude")
	if _, _, err := f.store.UpdateConvergent(func(reg *m.Registry) error { *reg = before.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	uid := old.Binding.AgentUID
	setKeptAgentLabels(t, f, uid)
	cleanupDetachedResume(t, f, ref)
	client, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	binding := core.ProcessBinding{HostInstanceID: old.Binding.HostInstanceID, ProjectUID: old.Binding.ProjectUID, WindowUID: old.Binding.WindowUID, AgentUID: old.Binding.AgentUID, PaneUID: old.Binding.PaneUID, Generation: old.Binding.Generation, OperationID: old.Binding.OperationID}
	resumed, err := client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: ref, Detach: true, Binding: binding, Prompt: "labels kept"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Binding.AgentUID != uid || resumed.Binding.Generation == old.Binding.Generation {
		t.Fatalf("resume did not start a new generation of the same Agent: %+v", resumed.Binding)
	}
	requireKeptAgentLabels(t, f, uid, "after host-lost resume")
}

// RelaunchAgent moves an Agent between hosts and back without changing the
// labels a caller set.
func TestPublicRelaunchAgentKeepsLabels(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f, source, _, socket := hostMoveCLIFixture(t)
	uid := source.Metadata.UID
	setKeptAgentLabels(t, f, uid)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	ctx, done := context.WithTimeout(context.Background(), 60*time.Second)
	defer done()
	ref := "uid:" + uid
	cleanupDetachedResume(t, f, ref)
	moved, err := client.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Host: "process", Detach: true, Confirm: true, SocketPath: socket, Prompt: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Binding.AgentUID != uid || moved.Binding.Generation == "" {
		t.Fatalf("move to process: %+v", moved)
	}
	awaitProcessResumeRecord(t, ctx, f, ref, func(r *m.ProcessSessionRecord) bool { return r.ConnectionID != "" })
	requireKeptAgentLabels(t, f, uid, "after move to process")
	back, err := client.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Host: "tmux", SocketPath: socket, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if back.AgentUID != uid || back.TargetHost != "tmux" {
		t.Fatalf("move back to tmux: %+v", back)
	}
	requireKeptAgentLabels(t, f, uid, "after move back to tmux")
}
