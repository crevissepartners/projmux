package app

import (
	"bytes"
	"context"
	"errors"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func refusalTextParity(t *testing.T, cli, sdk error) {
	t.Helper()
	t.Logf("CLI: %v\nSDK: %v", cli, sdk)
	if (cli == nil) != (sdk == nil) || cli != nil && cli.Error() != sdk.Error() {
		t.Errorf("CLI/SDK refusal differs: CLI=%v SDK=%v", cli, sdk)
	}
}

func TestSDKRefusalTextApprovalProvider(t *testing.T) {
	for _, route := range []string{"list", "answer"} {
		t.Run(route, func(t *testing.T) {
			reg := resourceFixtureRegistry(t)
			a := &reg.Agents[0]
			a.Spec.Provider = "gemini"
			client := approvalSDKClient(t, reg, t.TempDir(), config.AgentApprovalAnsweringClaude, nil)
			cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }}
			args := []string{route, "uid:" + a.Metadata.UID}
			var sdkErr error
			if route == "answer" {
				args = append(args, "7", "--allow")
				_, sdkErr = client.AnswerApproval(t.Context(), projmux.AnswerApprovalRequest{Agent: args[1], RequestID: "7", Allow: true})
			} else {
				_, sdkErr = client.ListApprovals(t.Context(), args[1])
			}
			cliErr := cmd.runPermissionApproval(args, &bytes.Buffer{}, &bytes.Buffer{})
			refusalTextParity(t, cliErr, sdkErr)
			var refusal *projmux.Refusal
			if !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodePermissionProviderUnsupported {
				t.Fatalf("provider Code changed: %v", sdkErr)
			}
		})
	}
}

func TestSDKRefusalTextNotificationStorage(t *testing.T) {
	for _, route := range []string{"list", "ack", "all"} {
		t.Run(route, func(t *testing.T) {
			client, _, paths := settingsClient(t, t.TempDir())
			queue := notify.NewDefaultStore(paths)
			if err := os.MkdirAll(queue.Path(), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := newCmd(queue)
			var cliErr, sdkErr error
			switch route {
			case "list":
				cliErr = cmd.runList([]string{"--json"}, &bytes.Buffer{}, &bytes.Buffer{})
				_, sdkErr = client.ListNotifications(t.Context(), core.NotificationFilter{})
			case "ack":
				cliErr = cmd.runAck("notification ack", []string{"missing"}, &bytes.Buffer{}, &bytes.Buffer{})
				_, sdkErr = client.AckNotifications(t.Context(), core.NotificationAck{ID: "missing"})
			case "all":
				cliErr = cmd.runAck("notification ack", []string{"--all"}, &bytes.Buffer{}, &bytes.Buffer{})
				_, sdkErr = client.AckNotifications(t.Context(), core.NotificationAck{All: true})
			}
			refusalTextParity(t, cliErr, sdkErr)
		})
	}
}

func TestSDKRefusalTextCreate(t *testing.T) {
	reg, _ := sdkResourceFixture(t)
	for i := range reg.Projects {
		reg.Projects[i].Spec.Root = t.TempDir()
		reg.Projects[i].Status.Conditions = nil
	}
	state, _, _ := renameState(t, reg)
	home := t.TempDir()
	conf := filepath.Join(home, ".config", "projmux")
	if err := os.MkdirAll(conf, 0700); err != nil {
		t.Fatal(err)
	}
	socket := refusalTextSocket(t)
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: conf, HomeDir: home, ProjmuxExecutable: "/bin/sh", SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"window", "pane", "agent"} {
		t.Run(kind, func(t *testing.T) {
			args := []string{kind, "--project", "alpha", "--name", "bad/name"}
			var sdkErr error
			switch kind {
			case "window":
				_, sdkErr = client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "alpha", Name: "bad/name"})
			case "pane":
				args = append(args, "--window", "main")
				_, sdkErr = client.CreatePane(context.Background(), core.CreatePaneRequest{Project: "alpha", Window: "main", Name: "bad/name"})
			case "agent":
				args = append(args, "--window", "main", "--provider", "claude")
				_, sdkErr = client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "alpha", Window: "main", Provider: "claude", Name: "bad/name"})
			}
			cmd := sdkAgentCLI(t, state, conf, socket, "/bin/sh", home)
			cmd.agents = newFakeAgentLauncher()
			cliErr := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
			refusalTextParity(t, cliErr, sdkErr)
		})
	}
	for _, req := range []core.CreateAgentRequest{
		{Project: "alpha", Window: "main", Provider: "claude", Effort: "bad"},
		{Project: "alpha", Window: "main", Provider: "claude", Model: "bad/model"},
		{Project: "alpha", Window: "main", Provider: "antigravity", Model: "model"},
		{Project: "alpha", Window: "main", Provider: "antigravity", Instructions: "reviewer"},
		{Project: "alpha", Window: "main", Provider: "claude", Profile: "missing"},
		{Project: "alpha", Window: "main", Provider: "claude", InteractiveOnly: true},
	} {
		t.Run(req.Provider+req.Model+req.Effort+req.Instructions+req.Profile, func(t *testing.T) {
			cmd := sdkAgentCLI(t, state, conf, socket, "/bin/sh", home)
			cmd.lookupEnv = func(string) string { return "" }
			cmd.agents.(*aiCommand).lookupEnv = cmd.lookupEnv
			args := []string{"agent", "--project", "alpha", "--window", "main", "--provider", req.Provider}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.Effort != "" {
				args = append(args, "--effort", req.Effort)
			}
			if req.Instructions != "" {
				args = append(args, "--instructions", req.Instructions)
			}
			if req.Profile != "" {
				args = append(args, "--profile", req.Profile)
			}
			if req.InteractiveOnly {
				args = append(args, "--interactive-only")
			}
			cliErr := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
			_, sdkErr := client.CreateAgent(t.Context(), req)
			refusalTextParity(t, cliErr, sdkErr)
		})
	}
}

func TestSDKRefusalTextControlSessionRename(t *testing.T) {
	fake := newFakeResourceStore(t)
	seed := seedControlOwnedGraph(t, fake, nil, "sdk-control")
	mut := store.DefaultMutator()
	agent, err := mut.CreateAgent(&fake.registry, seed.window.Metadata.UID, metadata.CreateAgentOptions{Name: "worker", Provider: "claude", OperationID: "refusal-text"})
	if err != nil {
		t.Fatal(err)
	}
	agentPane, err := mut.AttachAgentPane(&fake.registry, agent.Metadata.UID, metadata.BootstrapPane{Name: "worker-pane"}, "refusal-text-pane")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []core.RenameRequest{
		{Kind: core.KindWindow, Ref: "uid:" + seed.window.Metadata.UID, Name: "new-window"},
		{Kind: core.KindPane, Ref: "uid:" + seed.pane.Metadata.UID, Name: "new-pane"},
		{Kind: core.KindAgent, Ref: "uid:" + agent.Metadata.UID, Name: "new-agent"},
		{Kind: core.KindPane, Ref: "uid:" + agentPane.Metadata.UID, Name: "new-agent-pane"},
	} {
		t.Run(string(req.Kind), func(t *testing.T) {
			_, _, client := renameState(t, fake.registry)
			_, cliStore, _ := renameState(t, fake.registry)
			_, _, cliErr := renameCLI(t, cliStore, req)
			got, sdkErr := client.Rename(t.Context(), req)
			refusalTextParity(t, cliErr, sdkErr)
			if cliErr == nil && !got.Committed {
				t.Errorf("rename not committed")
			}
		})
	}
}

// The only runtime used by this table is this exact private socket. Retire its
// one session, rather than issuing a server-wide stop command.
func refusalTextSocket(t *testing.T) string {
	t.Helper()
	requireRealTmux(t)
	socket := filepath.Join(t.TempDir(), "s")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("tmux", append([]string{"-S", socket}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("private tmux: %v %s", err, out)
		}
	}
	run("-f", "/dev/null", "new-session", "-d", "-s", "control", "/bin/sh")
	t.Cleanup(func() { run("kill-session", "-t", "=control") })
	run("set-option", "-g", tmuxopts.AppGlobal, "1")
	run("set-option", "-g", runtimeMutationSocketNameOption, "projmux")
	return socket
}
