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
	expectedProject := strings.TrimSpace(os.Getenv("PROJMUX_RESOURCE_EXPECT_PROJECT_ROOT"))
	fallbackViews := 0
	if expectedProject != "" {
		resolved := resources.ResolveProjectAnchors(inventory, []string{expectedProject})
		for i := range resolved {
			if strings.TrimSpace(inventory[i].ProjectAnchor) == "" && resolved[i].ProjectAnchor == expectedProject {
				fallbackViews++
			}
		}
		if fallbackViews == 0 {
			t.Fatalf("no blank-anchor pane current path resolved to expected project %q", expectedProject)
		}
		inventory = resolved
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
	expectedProjectPanes := 0
	if expectedProject != "" {
		for _, project := range snapshot.Projects {
			if project.Key == expectedProject {
				expectedProjectPanes = project.PaneCount
				break
			}
		}
		if expectedProjectPanes == 0 {
			t.Fatalf("expected project %q has no attributed pane bucket: fallback_views=%d", expectedProject, fallbackViews)
		}
	}
	t.Logf("sanitized resource smoke: panes=%d pane_pid_eq_sid=%d missing_pids=%d attributed_processes=%d fallback_views=%d expected_project_panes=%d expected_project=%q escaped_boundary=%d sampled=%d skipped=%d race=%d permission=%d status=%s",
		len(inventory), paneSIDMatches, missingPanePIDs, attributedProcesses,
		fallbackViews, expectedProjectPanes, expectedProject,
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
	smokeRoot := t.TempDir()
	socket := "projmux-resource-setsid-" + filepath.Base(smokeRoot)
	ctx := context.Background()
	runner := resourceSmokeRunner{socket: socket, tmuxTmpDir: smokeRoot, configFile: "/dev/null"}
	if output, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", "resource-smoke", "sh", "-c", "setsid sleep 20 & wait"); err != nil {
		t.Fatalf("start transient tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { cleanupIsolatedResourceSmoke(t, runner, smokeRoot) })
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

// TestResourceProjectFallbackTransientSmoke creates an isolated tmux server
// whose pane has no project option, then proves that the typed current path is
// resolved in memory without writing any tmux metadata.
func TestResourceProjectFallbackTransientSmoke(t *testing.T) {
	if os.Getenv("PROJMUX_RESOURCE_PROJECT_FALLBACK_SMOKE") != "1" {
		t.Skip("set PROJMUX_RESOURCE_PROJECT_FALLBACK_SMOKE=1 to run isolated real-tmux project fallback smoke")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}

	smokeRoot := t.TempDir()
	currentPath, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Clean(filepath.Join(currentPath, "..", "..", ".."))
	socket := "projmux-resource-fallback-" + filepath.Base(smokeRoot)
	runner := resourceSmokeRunner{socket: socket, tmuxTmpDir: smokeRoot, configFile: "/dev/null"}
	ctx := context.Background()
	if output, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", "resource-fallback-smoke", "-c", currentPath, "/usr/bin/sleep 20"); err != nil {
		t.Fatalf("start fallback tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { cleanupIsolatedResourceSmoke(t, runner, smokeRoot) })

	client := NewClient(runner, WithSocketName(socket))
	inventory, err := client.ListResourcePanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 1 || inventory[0].CurrentPath != currentPath || inventory[0].ProjectAnchor != "" {
		t.Fatalf("isolated inventory = %#v, want one blank-anchor pane at configured cwd", inventory)
	}
	resolved := resources.ResolveProjectAnchors(inventory, []string{projectRoot})
	if resolved[0].ProjectAnchor != projectRoot {
		t.Fatalf("resolved project anchor = %q, want %q", resolved[0].ProjectAnchor, projectRoot)
	}

	current := (procfsresources.Collector{}).Sample(ctx)
	snapshot := resources.BuildSnapshot(resolved, nil, current)
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].Key != projectRoot || snapshot.Projects[0].PaneCount != 1 {
		t.Fatalf("isolated project buckets = %#v, want one fallback-attributed pane", snapshot.Projects)
	}
	after, err := client.ListResourcePanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ProjectAnchor != "" {
		t.Fatalf("fallback mutated tmux project metadata: %#v", after)
	}
	t.Logf("sanitized fallback smoke: panes=%d fallback_project_panes=%d project=%q", len(after), snapshot.Projects[0].PaneCount, projectRoot)
}

type resourceSmokeRunner struct {
	socket     string
	tmuxTmpDir string
	configFile string
}

func (r resourceSmokeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	prefix := []string{"-L", r.socket}
	if r.configFile != "" {
		prefix = append(prefix, "-f", r.configFile)
	}
	args = append(prefix, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = resourceSmokeEnvironment(r.tmuxTmpDir)
	return cmd.CombinedOutput()
}

func resourceSmokeEnvironment(tmuxTmpDir string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "TMUX" || name == "TMUX_PANE" || tmuxTmpDir != "" && name == "TMUX_TMPDIR" {
			continue
		}
		env = append(env, entry)
	}
	if tmuxTmpDir != "" {
		env = append(env, "TMUX_TMPDIR="+tmuxTmpDir)
	}
	return env
}

func cleanupIsolatedResourceSmoke(t *testing.T, runner resourceSmokeRunner, smokeRoot string) {
	t.Helper()
	output, err := runner.Run(context.Background(), "tmux", "display-message", "-p", "#{socket_path}")
	if err != nil {
		t.Errorf("query isolated tmux socket before cleanup: %v: %s", err, output)
		return
	}
	socketPath := strings.TrimSpace(string(output))
	rel, err := filepath.Rel(smokeRoot, socketPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Errorf("refuse cleanup outside smoke root %q: socket=%q", smokeRoot, socketPath)
		return
	}
	if output, err := runner.Run(context.Background(), "tmux", "kill-server"); err != nil {
		t.Errorf("kill isolated tmux server %q: %v: %s", socketPath, err, output)
	}
}
