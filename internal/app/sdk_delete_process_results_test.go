package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestDeleteProcessResultSDKCLIParity(t *testing.T) {
	for _, mode := range []string{"unknown", "offline", "running"} {
		t.Run(mode, func(t *testing.T) {
			reg, uid, paneUID := processDeleteFixture(t, "codex")
			pane, _ := reg.Pane(paneUID)
			agent, _ := reg.Agent(uid)
			switch mode {
			case "unknown":
				pane.Status.Activation = metadata.PaneActivation{}
				pane.Status.ProcessSession = nil
				agent.Status.Phase = metadata.PhaseOffline
			case "offline":
				recordFixtureWait(t, reg, paneUID)
			case "running":
				// An owned inert child supplies real kernel birth evidence. The
				// running case only previews; no owner transport or provider runs.
				child := exec.Command("sleep", "30")
				if err := child.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
				birth, _, err := localipc.Process(child.Process.Pid)
				if err != nil {
					t.Fatal(err)
				}
				pane.Status.Activation.Process.HostProcess = birth
			}
			_, _, client := renameState(t, *reg)
			cliState, _, _ := renameState(t, *reg)
			req := core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + uid, DryRun: true}
			for _, commit := range []bool{false, true} {
				if commit && mode == "running" {
					continue
				}
				req.DryRun, req.Confirm = !commit, commit
				result, err := client.Delete(context.Background(), req)
				if err != nil || result.Committed != commit || len(result.Processes) != 1 {
					t.Fatalf("SDK: %+v %v", result, err)
				}
				stdout, stderr, err := sdkDeleteCLI(t, cliState, req)
				if err != nil || stderr != "" {
					t.Fatalf("CLI: %q %q %v", stdout, stderr, err)
				}
				p := result.Processes[0]
				window, _ := reg.Window(agent.Metadata.OwnerUID())
				if p.AgentUID != uid || p.WindowUID != window.Metadata.UID || p.RootKind != core.KindProject || p.RootUID != window.Metadata.OwnerUID() || p.Evidence == "" {
					t.Fatalf("incomplete process identity/evidence: %+v", p)
				}
				for _, field := range []string{
					"uid=" + p.AgentUID,
					"runtime=" + p.Runtime,
					"evidence=" + p.Evidence,
					"owner-window=" + p.WindowUID,
					"root=" + strings.ToLower(string(p.RootKind)) + "/" + p.RootUID,
				} {
					if !strings.Contains(stdout, field) {
						t.Fatalf("SDK field %q missing from CLI:\n%s", field, stdout)
					}
				}
				if mode == "running" {
					if p.Runtime != "running" || p.HostPID == 0 || !strings.Contains(stdout, fmt.Sprintf("host pid=%d", p.HostPID)) {
						t.Fatalf("running owner differs: %+v\n%s", p, stdout)
					}
				} else if p.Runtime != mode || p.HostPID != 0 {
					t.Fatalf("inactive result: %+v", p)
				}
			}
		})
	}
}
