package projmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// windowIntentFixture is one Project on a private tmux server whose provider
// and supervisor are inert shells: it proves the create transaction, not a
// provider birth.
type windowIntentFixture struct {
	c         *Client
	project   string
	workspace string
	state     string
	run       func(args ...string) string
}

func newWindowIntentFixture(t *testing.T) windowIntentFixture {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal("fixture requires tmux:", err)
	}
	root := t.TempDir()
	home, config, state := filepath.Join(root, "home"), filepath.Join(root, "config", "projmux"), filepath.Join(root, "state", "projmux")
	workspace, bin := filepath.Join(root, "workspace"), filepath.Join(home, "bin")
	// Unix socket paths must stay short even under CI's long TMPDIR.
	socketDir, err := os.MkdirTemp("/tmp", "pmx-intent-")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(workspace, "sub"), bin} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
	t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	for _, name := range []string{"projmux", "claude"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexec /bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(socketDir, "s")
	if info, err := os.Stat(socketDir); err != nil || !info.IsDir() {
		t.Fatal("fixture socket directory must already exist")
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := (tmux.ExecRunner{}).Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
		if err != nil {
			t.Fatalf("fixture tmux %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		if out, err := (tmux.ExecRunner{}).Run(context.Background(), "tmux", "-S", socket, "kill-server"); err != nil {
			t.Errorf("fixture socket cleanup: %v %s", err, out)
			return // Preserve the private root if server retirement is uncertain.
		}
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("fixture socket root cleanup: %v", err)
		}
	})
	run("-f", "/dev/null", "new-session", "-d", "-s", "control", "-n", "control", "-c", root, "/bin/sh")
	run("set-option", "-g", "@projmux_app", "1")
	run("set-option", "-g", "@projmux_socket_name", "projmux")
	c, err := NewClient(Options{HomeDir: home, ConfigDir: config, StateDir: state, SocketPath: socket, DefaultShell: "/bin/sh", ProjmuxExecutable: filepath.Join(bin, "projmux")})
	if err != nil {
		t.Fatal(err)
	}
	c.agentEnvironment = map[string]string{"PATH": bin}
	project, err := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: workspace, Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return windowIntentFixture{c: c, project: project.Project.Metadata.UID, workspace: workspace, state: state, run: run}
}

func (f windowIntentFixture) registry(t *testing.T) metadata.Registry {
	t.Helper()
	reg, err := store.NewStore(store.PathFor(f.state)).LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// tmuxWindows is every tmux Window on the fixture server with its Panes.
func (f windowIntentFixture) tmuxWindows(t *testing.T) string {
	t.Helper()
	return f.run("list-panes", "-a", "-F", "#{window_id} #{pane_id}")
}

func (f windowIntentFixture) intent(agent *core.WindowIntentAgent) core.WindowCreateIntent {
	return core.WindowCreateIntent{Project: "uid:" + f.project, Agent: agent}
}

// requireAgentOnlyWindow checks the shape `create window --provider` commits:
// the Window holds exactly its Agent's Pane, as anchor, and no default shell.
func requireAgentOnlyWindow(t *testing.T, reg metadata.Registry, result core.WindowIntentResult) metadata.Agent {
	t.Helper()
	if result.Agent == nil {
		t.Fatal("Agent intent returned no Agent")
	}
	window, ok := reg.Window(result.Window.Metadata.UID)
	if !ok {
		t.Fatal("committed Window missing from the Registry")
	}
	agent, ok := reg.Agent(result.Agent.Metadata.UID)
	if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.UID != window.Metadata.UID {
		t.Fatalf("Agent not owned by the new Window: %+v", agent)
	}
	if window.Spec.DefaultShellPaneRef != "" {
		t.Fatalf("Window kept a default shell %q", window.Spec.DefaultShellPaneRef)
	}
	anchor, ok := reg.WindowAnchor(window.Metadata.UID)
	if !ok || anchor.Metadata.UID != agent.Status.PaneRef {
		t.Fatalf("Window anchor is not the Agent Pane: %+v", anchor)
	}
	for _, pane := range reg.Panes {
		if _, in := reg.PaneInWindow(window.Metadata.UID, pane.Metadata.UID); in && pane.Metadata.UID != agent.Status.PaneRef {
			t.Fatalf("Window holds another Pane %s", pane.Metadata.UID)
		}
	}
	ops := []string{}
	for _, resource := range result.Receipt.AffectedUIDs {
		ops = append(ops, resource.Kind+"/"+resource.UID+"/"+string(resource.Action))
	}
	if string(result.Receipt.Operation) != "create.window" || len(ops) != 2 || ops[1] != "agent/"+agent.Metadata.UID+"/created" {
		t.Fatalf("receipt %s %v", result.Receipt.Operation, ops)
	}
	return agent.Clone()
}

// TestWindowIntentCLIParity holds the intent to the CLI's shapes: a shell
// intent is `create window`, an Agent intent is `create window --provider`
// (the Agent's Pane alone, no default shell), the Agent validator refuses as
// CreateAgent refuses, and a pane cwd follows the split rule and its fallback.
func TestWindowIntentCLIParity(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()

	t.Run("shell-absent-project", func(t *testing.T) {
		// The preflight runs the Project checks CreateWindow's transaction
		// runs, so both refuse with one Code and one message.
		before := f.registry(t)
		_, created := f.c.CreateWindow(ctx, core.CreateWindowRequest{Project: "uid:proj-absent"})
		_, previewed := f.c.PreflightWindowIntent(ctx, core.WindowCreateIntent{Project: "uid:proj-absent"})
		var want, got *Refusal
		if !errors.As(created, &want) || !errors.As(previewed, &got) || want.Code != CodeResourceNotFound || got.Code != want.Code || got.Error() != want.Error() || !strings.Contains(got.Error(), "uid:proj-absent matched no projects") {
			t.Fatalf("CreateWindow %v, preflight %v", created, previewed)
		}
		if after := f.registry(t); len(after.Windows) != len(before.Windows) {
			t.Fatal("refused shell create changed the Registry")
		}
	})
	t.Run("shell", func(t *testing.T) {
		preview, err := f.c.PreflightWindowIntent(ctx, f.intent(nil))
		if err != nil || !preview.Shell || preview.Host != "tmux" {
			t.Fatalf("preview %+v %v", preview, err)
		}
		result, err := f.c.CreateWindowFromIntent(ctx, core.WindowCreateIntent{Project: "uid:" + f.project, Name: "plain"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Agent != nil || result.Window.Metadata.Name != "plain" || string(result.Receipt.Operation) != "create.window" || result.PaneID == "" {
			t.Fatalf("shell result %+v", result)
		}
		reg := f.registry(t)
		if shell, ok := reg.WindowDefaultShell(result.Window.Metadata.UID); !ok || shell.Spec.Role != metadata.PaneRoleShell {
			t.Fatal("shell Window has no default shell")
		}
	})

	t.Run("new", func(t *testing.T) {
		agent := &core.WindowIntentAgent{Provider: "claude", Labels: []string{"role=worker"}}
		preview, err := f.c.PreflightWindowIntent(ctx, f.intent(agent))
		if err != nil || preview.Shell || preview.Provider != "claude" || preview.CwdFrom != "project" {
			t.Fatalf("preview %+v %v", preview, err)
		}
		intent := f.intent(agent)
		intent.Name, intent.Labels = "work", []string{"team=core"}
		result, err := f.c.CreateWindowFromIntent(ctx, intent)
		if err != nil {
			t.Fatal(err)
		}
		reg := f.registry(t)
		stored := requireAgentOnlyWindow(t, reg, result)
		if result.Window.Metadata.Name != "work" || result.Window.Metadata.Labels["team"] != "core" || stored.Metadata.Labels["role"] != "worker" || stored.Metadata.Labels["team"] != "" {
			t.Fatalf("names/labels: window %+v agent %+v", result.Window.Metadata, stored.Metadata)
		}
		if stored.Spec.Workspace.CWD != f.workspace {
			t.Fatalf("workspace %q", stored.Spec.Workspace.CWD)
		}
		if panes := f.run("list-panes", "-t", result.PaneID, "-F", "#{pane_id}"); panes != result.PaneID {
			t.Fatalf("tmux Window panes %q, want only %s", panes, result.PaneID)
		}
	})

	t.Run("resume", func(t *testing.T) {
		// The resume picker's create: the Agent's Pane alone in a new Window,
		// in the conversation's directory, with the conversation recorded as
		// the Agent's session.
		f.useResumeCatalog(nil)
		defer func() { f.c.resumeSources = nil }()
		id := "11111111-2222-4333-8444-000000000020"
		cwd := filepath.Join(f.workspace, "sub")
		f.writeClaudeResumeSession(t, id, cwd, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
		agent := &core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "claude", Conversation: id, Source: aisessions.SourceClaudeTranscript, Cwd: cwd}}
		if preview, err := f.c.PreflightWindowIntent(ctx, f.intent(agent)); err != nil || preview.Shell || preview.Provider != "claude" {
			t.Fatalf("preview %+v %v", preview, err)
		}
		result, err := f.c.CreateWindowFromIntent(ctx, f.intent(agent))
		if err != nil {
			t.Fatal(err)
		}
		stored := requireAgentOnlyWindow(t, f.registry(t), result)
		if ref := stored.Status.SessionRef; ref == nil || ref.Claude == nil || ref.Claude.SessionID != id || stored.Spec.Workspace.CWD != cwd {
			t.Fatalf("resumed Agent workspace %q session %+v", stored.Spec.Workspace.CWD, ref)
		}
		for _, notice := range result.Notices {
			if strings.Contains(notice, "session history not recorded") {
				t.Fatalf("history notice on a recorded create: %q", result.Notices)
			}
		}
	})

	t.Run("refusals", func(t *testing.T) {
		for _, body := range []string{"provider = \"codex\"\n"} {
			if _, err := f.c.WriteProfile(ctx, "coder", []byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		before, tmuxBefore := f.registry(t), f.tmuxWindows(t)
		for name, agent := range map[string]core.WindowIntentAgent{
			"profile-provider-mismatch": {Provider: "claude", Profile: "coder"},
			"profile-missing":           {Provider: "claude", Profile: "absent"},
			"unknown-provider":          {Provider: "nonesuch"},
			"instructions-missing":      {Provider: "claude", Instructions: "absent"},
			"creator-shape":             {Provider: "claude", Creator: "agent-x"},
			"creator-missing":           {Provider: "claude", Creator: "uid:agent-absent"},
		} {
			t.Run(name, func(t *testing.T) {
				_, want := f.c.PreflightCreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Provider: agent.Provider, Profile: agent.Profile, Instructions: agent.Instructions, Creator: agent.Creator})
				var wantRefusal *Refusal
				if !errors.As(want, &wantRefusal) {
					t.Fatalf("CreateAgent preflight did not refuse: %v", want)
				}
				_, gotPreflight := f.c.PreflightWindowIntent(ctx, f.intent(&agent))
				_, gotCreate := f.c.CreateWindowFromIntent(ctx, f.intent(&agent))
				for _, got := range []error{gotPreflight, gotCreate} {
					var refusal *Refusal
					if !errors.As(got, &refusal) || refusal.Code != wantRefusal.Code || refusal.Error() != wantRefusal.Error() {
						t.Fatalf("intent refusal %v, CreateAgent %s: %v", got, wantRefusal.Code, want)
					}
				}
			})
		}
		after := f.registry(t)
		if len(after.Windows) != len(before.Windows) || len(after.Agents) != len(before.Agents) || f.tmuxWindows(t) != tmuxBefore {
			t.Fatal("a refused intent changed the Registry or tmux")
		}
	})

	t.Run("cwd", func(t *testing.T) {
		source, err := f.c.CreateWindowFromIntent(ctx, core.WindowCreateIntent{Project: "uid:" + f.project, Name: "source"})
		if err != nil {
			t.Fatal(err)
		}
		shell, _ := f.registry(t).WindowDefaultShell(source.Window.Metadata.UID)
		outside := t.TempDir()
		for _, tc := range []struct {
			dir, want string
			notice    bool
		}{{filepath.Join(f.workspace, "sub"), filepath.Join(f.workspace, "sub"), false}, {outside, f.workspace, true}} {
			f.run("send-keys", "-t", source.PaneID, "cd "+tc.dir, "Enter")
			deadline := time.Now().Add(5 * time.Second)
			for f.run("display-message", "-p", "-t", source.PaneID, "#{pane_current_path}") != tc.dir && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			agent := &core.WindowIntentAgent{Provider: "claude", CwdFrom: "pane", CwdPane: "uid:" + shell.Metadata.UID}
			if preview, err := f.c.PreflightWindowIntent(ctx, f.intent(agent)); err != nil || preview.CwdFrom != "pane" {
				t.Fatalf("preview %+v %v", preview, err)
			}
			result, err := f.c.CreateWindowFromIntent(ctx, f.intent(agent))
			if err != nil {
				t.Fatal(err)
			}
			stored := requireAgentOnlyWindow(t, f.registry(t), result)
			if stored.Spec.Workspace.CWD != tc.want {
				t.Fatalf("cwd %q, want %q", stored.Spec.Workspace.CWD, tc.want)
			}
			fallback := strings.Contains(strings.Join(result.Notices, "\n"), "split started in Project root")
			if fallback != tc.notice {
				t.Fatalf("notices %q", result.Notices)
			}
		}
		for _, agent := range []core.WindowIntentAgent{{Provider: "claude", CwdFrom: "pane"}, {Provider: "claude", CwdPane: "uid:" + shell.Metadata.UID}, {Provider: "claude", CwdFrom: "pane", CwdPane: "uid:pane-absent"}} {
			var refusal *Refusal
			if _, err := f.c.PreflightWindowIntent(ctx, f.intent(&agent)); !errors.As(err, &refusal) || refusal.Code != CodeCreateRequestInvalid {
				t.Fatalf("cwd shape %+v: %v", agent, err)
			}
		}
	})
}

// TestWindowIntentCreatorProvenance records only what the intent states: a
// declared Agent with the explicit basis, an in-process operator in its place
// (the declaration reported as not recorded), and nothing for neither.
func TestWindowIntentCreatorProvenance(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	declared, err := f.c.CreateWindowFromIntent(ctx, f.intent(&core.WindowIntentAgent{Provider: "claude"}))
	if err != nil {
		t.Fatal(err)
	}
	creator := declared.Agent.Metadata.UID
	for _, tc := range []struct {
		name   string
		agent  core.WindowIntentAgent
		want   map[string]string
		notice string
	}{
		{"none", core.WindowIntentAgent{Provider: "claude"}, map[string]string{}, ""},
		{"declared", core.WindowIntentAgent{Provider: "claude", Creator: "uid:" + creator}, metadata.ExplicitCreatorAnnotations(creator), ""},
		{"operator", core.WindowIntentAgent{Provider: "claude", Operator: core.NewOperatorCreator("web")}, metadata.OperatorCreatorAnnotations("web"), ""},
		{"operator-over-declared", core.WindowIntentAgent{Provider: "claude", Creator: "uid:" + creator, Operator: core.NewOperatorCreator("web")}, metadata.OperatorCreatorAnnotations("web"), mutationruntime.CreatorDeclinedNotice(mutationruntime.CreatorDeclinedOperator, creator)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := f.c.CreateWindowFromIntent(ctx, f.intent(&tc.agent))
			if err != nil {
				t.Fatal(err)
			}
			stored := requireAgentOnlyWindow(t, f.registry(t), result)
			for _, key := range []string{metadata.AnnotationCreatorAgent, metadata.AnnotationCreatorPane, metadata.AnnotationCreatorClient, metadata.AnnotationCreatorBasis} {
				if stored.Metadata.Annotations[key] != tc.want[key] {
					t.Fatalf("%s = %q, want %q", key, stored.Metadata.Annotations[key], tc.want[key])
				}
			}
			if tc.notice != "" && !strings.Contains(strings.Join(result.Notices, "\n"), tc.notice) {
				t.Fatalf("notices %q, want %q", result.Notices, tc.notice)
			}
		})
	}
	var refusal *Refusal
	if _, err := f.c.PreflightWindowIntent(ctx, f.intent(&core.WindowIntentAgent{Provider: "claude", Operator: core.NewOperatorCreator("Not Valid")})); !errors.As(err, &refusal) || refusal.Code != CodeCreateCreatorInvalid {
		t.Fatalf("invalid operator: %v", err)
	}
}

// TestWindowIntentRollbackAndActivationUnknown: a failure inside the
// transaction leaves neither the Window nor the Agent, in the Registry or in
// tmux; an unconfirmed activation keeps both and returns them with the error.
func TestWindowIntentRollbackAndActivationUnknown(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	// Bring the Project's session up so the rollback is measured against a
	// live server, not a session the failed create started.
	if _, err := f.c.CreateWindowFromIntent(ctx, f.intent(nil)); err != nil {
		t.Fatal(err)
	}

	t.Run("rollback", func(t *testing.T) {
		before, tmuxBefore := f.registry(t), f.tmuxWindows(t)
		bin := f.c.agentEnvironment["PATH"]
		f.c.agentEnvironment = map[string]string{"PATH": t.TempDir()}
		defer func() { f.c.agentEnvironment = map[string]string{"PATH": bin} }()
		// The provider binary is found only inside the transaction, after the
		// Window and its shell exist.
		result, err := f.c.CreateWindowFromIntent(ctx, f.intent(&core.WindowIntentAgent{Provider: "claude"}))
		if err == nil || result.Agent != nil || result.Window.Metadata.UID != "" {
			t.Fatalf("missing provider committed: %+v %v", result, err)
		}
		after := f.registry(t)
		if len(after.Windows) != len(before.Windows) || len(after.Panes) != len(before.Panes) || len(after.Agents) != len(before.Agents) {
			t.Fatalf("Registry kept a rolled-back create: windows %d->%d panes %d->%d", len(before.Windows), len(after.Windows), len(before.Panes), len(after.Panes))
		}
		if got := f.tmuxWindows(t); got != tmuxBefore {
			t.Fatalf("tmux kept a rolled-back create:\n%s\nwant\n%s", got, tmuxBefore)
		}
	})

	t.Run("activation-unknown", func(t *testing.T) {
		f.c.activationAwait = func(context.Context, mutationruntime.TmuxCommandRunner, string, time.Duration, time.Duration) (bool, string, error) {
			return false, "", nil
		}
		defer func() { f.c.activationAwait = nil }()
		result, err := f.c.CreateWindowFromIntent(ctx, f.intent(&core.WindowIntentAgent{Provider: "claude", Payload: []string{"hello"}}))
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeCreateActivationUnconfirmed {
			t.Fatalf("want activation-unconfirmed, got %v", err)
		}
		reg := f.registry(t)
		stored := requireAgentOnlyWindow(t, reg, result)
		if result.PaneID == "" || len(result.Projection.Items) != 2 {
			t.Fatalf("committed resources not returned: %+v", result)
		}
		if stored.Status.Activation.State != metadata.ActivationUnconfirmed || !strings.Contains(err.Error(), stored.Metadata.UID) {
			t.Fatalf("activation %+v, error %v", stored.Status.Activation, err)
		}
	})

	t.Run("process-shape", func(t *testing.T) {
		before := f.registry(t)
		for _, intent := range []core.WindowCreateIntent{
			{Project: "uid:" + f.project, Host: "process", Detach: true},
			{Project: "uid:" + f.project, Host: "process", Agent: &core.WindowIntentAgent{Provider: "claude"}},
			{Project: "uid:" + f.project, Host: "process", Detach: true, Agent: &core.WindowIntentAgent{Provider: "claude", CwdFrom: "pane", CwdPane: "uid:pane-x"}},
			{Project: "uid:" + f.project, Detach: true, Agent: &core.WindowIntentAgent{Provider: "claude"}},
			{Project: "uid:" + f.project, Host: "process", Detach: true, Agent: &core.WindowIntentAgent{Provider: "claude", Resume: &core.ResumeSelection{Provider: "claude", Conversation: "c", Source: "s"}}},
		} {
			var refusal *Refusal
			if _, err := f.c.CreateWindowFromIntent(ctx, intent); !errors.As(err, &refusal) || refusal.Code != CodeCreateRequestInvalid {
				t.Fatalf("%+v: %v", intent, err)
			}
		}
		if after := f.registry(t); len(after.Windows) != len(before.Windows) {
			t.Fatal("refused process intent changed the Registry")
		}
	})
}

// TestWindowIntentProcessReceipt pins the receipt a process intent returns to
// the tmux route's create.window shape with its Agent row, from the rows the
// process creator's commit names.
func TestWindowIntentProcessReceipt(t *testing.T) {
	window := mutationruntime.ShellCreateResource{Kind: metadata.KindWindow, UID: "win-a", Name: "work"}
	var got core.OperationReceipt
	if err := unregisterCopy(windowIntentReceipt(window, "agent-a", "agent-1", &receipt.ReceiptProfile{Name: "coder"}), &got); err != nil {
		t.Fatal(err)
	}
	ops := []string{}
	for _, resource := range got.AffectedUIDs {
		ops = append(ops, resource.Kind+"/"+resource.UID+"/"+string(resource.Action))
	}
	if string(got.Operation) != "create.window" || strings.Join(ops, " ") != "Window/win-a/created agent/agent-a/created" || strings.Join(got.SelectedWindowUIDs, " ") != "win-a" || got.Profile == nil || got.Profile.Name != "coder" {
		t.Fatalf("receipt %s %v %v %+v", got.Operation, ops, got.SelectedWindowUIDs, got.Profile)
	}
}
