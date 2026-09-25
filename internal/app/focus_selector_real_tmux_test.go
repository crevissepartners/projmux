package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestFocusPaneUIDSelectsThePaneOnAnIsolatedTmux drives `focus pane uid:...`
// against an isolated real tmux server with one attached control-mode client.
// The Registry fixture is the only source of the session, `@N`, `%N`, and
// socket path; the inherited environment names no tmux at all, so the move can
// only have reached the server the Project records. The target Pane is neither
// in the current Window nor the active Pane before the call.
func TestFocusPaneUIDSelectsThePaneOnAnIsolatedTmux(t *testing.T) {
	requireRealTmux(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "pfu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	socket := filepath.Join(root, "focus-uid.sock")
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const sessionName = "focus-uid"
	created, err := tmux("new-session", "-d", "-s", sessionName, "-n", "main", "-c", root,
		"-P", "-F", "#{window_id}\t#{pane_id}\t#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	fields := strings.Split(created, "\t")
	var windowID, paneID, serverPID string
	if len(fields) == 4 {
		windowID, paneID, serverPID = fields[0], fields[1], fields[2]
	}
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))
	if len(fields) != 4 || exactTmuxHandle(windowID, "@") == "" || exactTmuxHandle(paneID, "%") == "" || fields[3] != socket {
		t.Fatalf("isolated tmux receipt = %q, want window/pane/pid on %s", created, socket)
	}
	// A second Pane in the target Window becomes active, and a second Window
	// becomes current, so the focus has something to move in both dimensions.
	if out, err := tmux("split-window", "-t", paneID, "tail", "-f", "/dev/null"); err != nil {
		t.Fatalf("split: %v: %s", err, out)
	}
	if out, err := tmux("new-window", "-t", sessionName, "-n", "other", "tail", "-f", "/dev/null"); err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	current := func(t *testing.T) (string, string) {
		t.Helper()
		out, err := tmux("display-message", "-p", "-t", sessionName, "#{window_id}\t#{pane_id}")
		if err != nil {
			t.Fatalf("read current pane: %v: %s", err, out)
		}
		window, pane, _ := strings.Cut(out, "\t")
		return window, pane
	}
	if window, pane := current(t); window == windowID || pane == paneID {
		t.Fatalf("precondition: current = %s %s, want neither %s nor %s", window, pane, windowID, paneID)
	}
	activeInTarget, err := tmux("display-message", "-p", "-t", sessionName+":"+windowID, "#{pane_id}")
	if err != nil || activeInTarget == paneID {
		t.Fatalf("precondition: active pane in %s = %q (%v), want not %s", windowID, activeInTarget, err, paneID)
	}

	// One attached client: a control-mode client whose stdin stays open.
	client := exec.CommandContext(ctx, "tmux", "-S", socket, "-f", "/dev/null", "-C", "attach-session", "-t", sessionName)
	client.Env = environment
	stdin, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatalf("attach control client: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = client.Process.Kill()
		_ = client.Wait()
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if out, err := tmux("list-clients", "-F", "#{client_name}"); err == nil && out != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control client never attached")
		}
	}

	const projectUID, windowUID, paneUID = "proj-focus-uid", "win-focus-uid", "pane-focus-uid"
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		Kind:     coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: "focus-uid"},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: sessionName, Live: true, SocketPath: socket}},
	}}
	registry.Windows = []coremetadata.Window{{
		Kind:     coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "main", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: projectUID}},
		Status:   coremetadata.WindowStatus{RuntimeID: windowID},
	}}
	pane := coremetadata.Pane{
		Kind:     coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "shell", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID}},
	}
	pane.Status.Activation.RuntimeID = paneID
	registry.Panes = []coremetadata.Pane{pane}

	cmd := &focusCommand{
		runner:       shellTmuxExecRunner{env: func() []string { return environment }},
		lookupEnv:    func(string) string { return "" },
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"pane", "uid:" + paneUID, "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("focus pane uid: %v (stdout=%s stderr=%s)", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"dispatch":"switch-client"`) || !strings.Contains(stdout.String(), `"ok":true`) {
		t.Fatalf("focus result = %s, want an ok switch-client dispatch", stdout.String())
	}
	if window, pane := current(t); window != windowID || pane != paneID {
		t.Fatalf("after focus current = %s %s, want %s %s", window, pane, windowID, paneID)
	}
}
