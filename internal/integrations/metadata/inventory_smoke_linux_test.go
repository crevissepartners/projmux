//go:build linux

package metadata

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// TestResolvedResourceGraphRealTmuxSmoke observes a real tmux server the test
// creates itself, so the format strings, the option spellings, and the
// containment join are proven against tmux rather than against a fixture.
//
// It is opt-in because it spawns a tmux server, which a unit run must never do.
// It never touches the caller's tmux: the inherited TMUX/TMUX_PANE are stripped
// from every invocation, TMUX_TMPDIR and the socket name are unique to this test,
// and cleanup kills only the exact #{socket_path} it has confirmed lives inside
// its own temporary root.
//
//	PROJMUX_RESOURCEGRAPH_TMUX_SMOKE=1 go test ./internal/integrations/metadata/ \
//	  -run TestResolvedResourceGraphRealTmuxSmoke -count=1 -v
func TestResolvedResourceGraphRealTmuxSmoke(t *testing.T) {
	if strings.TrimSpace(os.Getenv("PROJMUX_RESOURCEGRAPH_TMUX_SMOKE")) == "" {
		t.Skip("set PROJMUX_RESOURCEGRAPH_TMUX_SMOKE=1 to run the isolated real-tmux smoke")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux is not installed: %v", err)
	}
	root := t.TempDir()
	tmpdir := filepath.Join(root, "tmux")
	if err := os.MkdirAll(tmpdir, 0o700); err != nil {
		t.Fatalf("create isolated TMUX_TMPDIR: %v", err)
	}
	primary := fmt.Sprintf("pmxrg-%d-a", os.Getpid())
	sibling := fmt.Sprintf("pmxrg-%d-b", os.Getpid())

	runner := &smokeRunner{tmpdir: tmpdir}
	ctx := context.Background()
	run := func(socket string, args ...string) string {
		t.Helper()
		out, err := runner.Run(ctx, "tmux", append([]string{"-L", socket}, args...)...)
		if err != nil {
			t.Fatalf("tmux -L %s %v: %v (%s)", socket, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	killExact := func(socket string) {
		t.Helper()
		path, err := runner.Run(ctx, "tmux", "-L", socket, "display-message", "-p", "#{socket_path}")
		if err != nil {
			t.Logf("cleanup: socket %s already gone: %v", socket, err)
			return
		}
		socketPath := strings.TrimSpace(string(path))
		// Never kill a server whose socket is not provably inside this test's own
		// root: a bare kill-server or a TMUX_TMPDIR-only assumption would be able
		// to take down the operator's sessions.
		if socketPath == "" || !strings.HasPrefix(socketPath, root+string(os.PathSeparator)) {
			t.Errorf("refusing to kill socket %q: outside the smoke root %q", socketPath, root)
			return
		}
		if _, err := runner.Run(ctx, "tmux", "-S", socketPath, "kill-server"); err != nil {
			t.Logf("cleanup: kill %s: %v", socketPath, err)
			return
		}
		t.Logf("cleanup: killed exact socket %s", socketPath)
	}
	t.Cleanup(func() { killExact(primary) })
	t.Cleanup(func() { killExact(sibling) })

	// The primary server: one managed Project session with a managed Window and
	// Pane, a control Home, an ephemeral scratch session, and a plain session.
	created := run(primary, "new-session", "-d", "-s", "alpha", "-c", root, "-P", "-F", "#{window_id} #{pane_id}")
	fields := strings.Fields(created)
	if len(fields) != 2 {
		t.Fatalf("new-session reported %q, want a window id and a pane id", created)
	}
	windowID, paneID := fields[0], fields[1]
	run(primary, "set-option", "-g", "@projmux_app", "1")
	run(primary, "set-option", "-t", "alpha", "-q", "@projmux_project_uid", "project-alpha")
	run(primary, "set-option", "-t", "alpha", "-q", "@projmux_project_name", "alpha")
	run(primary, "set-option", "-t", "alpha", "-q", "@projmux_project_path", root)
	run(primary, "set-option", "-w", "-t", windowID, "-q", "@projmux_window_uid", "win-alpha-1")
	run(primary, "set-option", "-p", "-t", paneID, "-q", "@projmux_pane_uid", "pane-alpha-1")
	run(primary, "new-session", "-d", "-s", "home", "-c", root)
	run(primary, "set-option", "-t", "home", "-q", "@projmux_session_role", "control")
	run(primary, "new-session", "-d", "-s", "scratch", "-c", root)
	run(primary, "set-option", "-t", "scratch", "-q", "@projmux_ephemeral", "1")
	run(primary, "new-session", "-d", "-s", "plain", "-c", root)

	// The sibling server mirrors the same uids. Nothing in the observation may
	// read it, and nothing in the graph may bind to it.
	siblingCreated := run(sibling, "new-session", "-d", "-s", "alpha", "-c", root, "-P", "-F", "#{window_id} #{pane_id}")
	siblingFields := strings.Fields(siblingCreated)
	if len(siblingFields) != 2 {
		t.Fatalf("sibling new-session reported %q", siblingCreated)
	}
	run(sibling, "set-option", "-t", "alpha", "-q", "@projmux_project_uid", "project-alpha")
	run(sibling, "set-option", "-w", "-t", siblingFields[0], "-q", "@projmux_window_uid", "win-alpha-1")
	run(sibling, "set-option", "-p", "-t", siblingFields[1], "-q", "@projmux_pane_uid", "pane-alpha-1")
	siblingSessionsBefore := run(sibling, "list-sessions", "-F", "#{session_name}")

	transport := resourcegraph.Transport{
		Kind: resourcegraph.TransportSocketName, Value: primary,
		Source: resourcegraph.TransportSourceSocketName,
	}
	runner.record = true
	observer := NewInventoryObserver(runner, transport)
	observed := observer.Observe(ctx)
	observer.Observe(ctx)
	observer.Observe(ctx)
	runner.record = false

	if len(runner.observed) != 4 {
		t.Fatalf("observation issued %d tmux calls, want the fixed budget of 4: %v", len(runner.observed), runner.observed)
	}
	for _, call := range runner.observed {
		joined := strings.Join(call, " ")
		if call[0] != "-L" || call[1] != primary {
			t.Fatalf("observation call %q escaped the exact transport", joined)
		}
		if strings.Contains(joined, sibling) {
			t.Fatalf("observation call %q read the sibling socket", joined)
		}
		for _, arg := range call {
			if arg == "set-option" || arg == "kill-server" || arg == "kill-session" || arg == "new-session" {
				t.Fatalf("observation call %q wrote to tmux", joined)
			}
		}
	}
	if observed.HostMode != resourcegraph.HostModeAppOwned {
		t.Fatalf("host mode = %q, want app-owned", observed.HostMode)
	}
	if len(observed.Unavailable) != 0 {
		t.Fatalf("real server reported %+v", observed.Unavailable)
	}
	if len(observed.Sessions) != 4 {
		t.Fatalf("observed %d sessions, want 4: %+v", len(observed.Sessions), observed.Sessions)
	}

	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: "project-alpha", Name: "alpha"},
		Spec:     coremetadata.ProjectSpec{Root: root},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: "win-alpha-1", Name: "editor",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "project-alpha"}},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: "pane-alpha-1", Name: "shell",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-1"}},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}}
	graph := resourcegraph.Resolve(registry, observed)

	if len(graph.Conflicts) != 0 {
		t.Fatalf("real observation produced conflicts: %+v", graph.Conflicts)
	}
	for _, want := range []struct {
		uid    string
		status resourcegraph.Status
		id     string
	}{
		{uid: "project-alpha", status: resourcegraph.StatusLive},
		{uid: "win-alpha-1", status: resourcegraph.StatusLive, id: windowID},
		{uid: "pane-alpha-1", status: resourcegraph.StatusLive, id: paneID},
	} {
		var status resourcegraph.Status
		var ref *resourcegraph.RuntimeRef
		switch want.uid {
		case "project-alpha":
			status, ref = graph.Projects[0].Status, graph.Projects[0].Runtime
		case "win-alpha-1":
			status, ref = graph.Windows[0].Status, graph.Windows[0].Runtime
		default:
			status, ref = graph.Panes[0].Status, graph.Panes[0].Runtime
		}
		if status != want.status {
			t.Fatalf("%s status = %q, want %q", want.uid, status, want.status)
		}
		if ref == nil {
			t.Fatalf("%s bound no runtime handle", want.uid)
		}
		if want.id != "" && ref.ID != want.id {
			t.Fatalf("%s bound %s, want the exact tmux id %s", want.uid, ref.ID, want.id)
		}
	}

	classes := map[string]resourcegraph.Class{}
	for _, node := range graph.Runtime {
		if node.Ref.Kind == resourcegraph.ObjectSession {
			classes[node.Ref.Name] = node.Class
		}
	}
	wantClasses := map[string]resourcegraph.Class{
		"alpha":   resourcegraph.ClassManaged,
		"home":    resourcegraph.ClassControl,
		"scratch": resourcegraph.ClassEphemeral,
		"plain":   resourcegraph.ClassUnattributed,
	}
	for name, want := range wantClasses {
		if classes[name] != want {
			t.Fatalf("session %s classified %q, want %q (all: %+v)", name, classes[name], want, classes)
		}
	}
	t.Logf("real tmux classes: %+v", classes)

	if after := run(sibling, "list-sessions", "-F", "#{session_name}"); after != siblingSessionsBefore {
		t.Fatalf("sibling sessions changed from %q to %q", siblingSessionsBefore, after)
	}
	if got := run(primary, "list-sessions", "-F", "#{session_name}"); len(strings.Fields(got)) != 4 {
		t.Fatalf("observation changed the primary server's sessions: %q", got)
	}
}

// smokeRunner executes real tmux with the caller's client environment stripped.
// The two inherited variables are removed on every call, not once at setup, so no
// invocation can accidentally address the operator's server.
type smokeRunner struct {
	tmpdir   string
	record   bool
	observed [][]string
}

func (r *smokeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.record {
		r.observed = append(r.observed, append([]string(nil), args...))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_PANE=") ||
			strings.HasPrefix(entry, "TMUX_TMPDIR=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "TMUX_TMPDIR="+r.tmpdir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
