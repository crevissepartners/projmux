//go:build linux

package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/resources"
	"github.com/crevissepartners/projmux/internal/integrations/procfsresources"
)

// TestResourceAttributionRealTmuxReadOnlySmoke is opt-in because it observes
// the caller's existing tmux server. It performs no tmux or process mutation
// and reports count-only evidence.
func TestResourceAttributionRealTmuxReadOnlySmoke(t *testing.T) {
	socket := strings.TrimSpace(os.Getenv("PROJMUX_RESOURCE_TMUX_SOCKET"))
	if socket == "" {
		t.Skip("set PROJMUX_RESOURCE_TMUX_SOCKET to an existing tmux -L socket")
	}
	ctx := context.Background()
	client := NewClient(resourceSmokeRunner{socket: socket}, WithSocketName(socket))
	inventory, err := client.ListResourcePanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) == 0 {
		t.Fatal("resource inventory is empty")
	}
	collector := procfsresources.Collector{}
	previous := collector.Sample(ctx)
	time.Sleep(125 * time.Millisecond)
	snapshot, current := collector.Collect(ctx, inventory, &previous)
	if !current.Available || snapshot.Status == "unavailable" || snapshot.Status == "warming" {
		t.Fatalf("snapshot status = %q reason=%q", snapshot.Status, snapshot.StatusReason)
	}
	processesByPID := make(map[int]int, len(current.Processes))
	for _, process := range current.Processes {
		processesByPID[process.Identity.PID] = process.SessionID
	}
	paneSIDMatches := 0
	missingPanePIDs := 0
	for _, pane := range inventory {
		sid, ok := processesByPID[pane.PanePID]
		if !ok {
			missingPanePIDs++
			continue
		}
		if sid == pane.PanePID {
			paneSIDMatches++
		}
	}
	attributedProcesses := 0
	for _, pane := range snapshot.Panes {
		attributedProcesses += pane.ProcessCount
	}
	t.Logf("sanitized resource smoke: panes=%d pane_pid_eq_sid=%d missing_pids=%d attributed_processes=%d escaped_boundary=%d sampled=%d skipped=%d race=%d permission=%d status=%s",
		len(inventory), paneSIDMatches, missingPanePIDs, attributedProcesses,
		snapshot.Diagnostics.EscapedProcessCount, current.Diagnostics.SampledProcesses,
		current.Diagnostics.SkippedProcesses, current.Diagnostics.RaceCount,
		current.Diagnostics.PermissionCount, snapshot.Status)
	if missingPanePIDs != 0 || paneSIDMatches != len(inventory) {
		t.Fatalf("pane PID/SID contract mismatch: panes=%d matches=%d missing=%d", len(inventory), paneSIDMatches, missingPanePIDs)
	}
	if attributedProcesses < len(inventory) {
		t.Fatalf("attributed process count %d is smaller than pane count %d", attributedProcesses, len(inventory))
	}
}

// TestResourceAttributionTransientSetsidSmoke uses a disposable real tmux
// server to create a positive setsid boundary. It never touches an existing
// server and kills the isolated server during cleanup.
func TestResourceAttributionTransientSetsidSmoke(t *testing.T) {
	if os.Getenv("PROJMUX_RESOURCE_TRANSIENT_SMOKE") != "1" {
		t.Skip("set PROJMUX_RESOURCE_TRANSIENT_SMOKE=1 to run isolated real-tmux setsid smoke")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	socket := "projmux-resource-smoke-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	runner := resourceSmokeRunner{socket: socket}
	defer func() { _, _ = runner.Run(context.Background(), "tmux", "kill-server") }()
	if output, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", "resource-smoke", "sh", "-c", "setsid sleep 20 & wait"); err != nil {
		t.Fatalf("start transient tmux: %v: %s", err, output)
	}
	client := NewClient(runner, WithSocketName(socket))
	collector := procfsresources.Collector{}

	deadline := time.Now().Add(3 * time.Second)
	for {
		inventory, err := client.ListResourcePanes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		current := collector.Sample(ctx)
		snapshot := resources.BuildSnapshot(inventory, nil, current)
		if snapshot.Diagnostics.EscapedProcessCount > 0 {
			attributed := 0
			for _, pane := range snapshot.Panes {
				attributed += pane.ProcessCount
			}
			t.Logf("sanitized transient setsid smoke: panes=%d attributed_processes=%d escaped_boundary=%d sampled=%d skipped=%d race=%d permission=%d",
				len(inventory), attributed, snapshot.Diagnostics.EscapedProcessCount,
				current.Diagnostics.SampledProcesses, current.Diagnostics.SkippedProcesses,
				current.Diagnostics.RaceCount, current.Diagnostics.PermissionCount)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("setsid escape not observed before deadline: diagnostics=%#v", snapshot.Diagnostics)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type resourceSmokeRunner struct {
	socket string
}

func (r resourceSmokeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	args = append([]string{"-L", r.socket}, args...)
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
