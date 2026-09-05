package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The optional binary is used only by the post-install smoke. With it, the
// same detached/inherited hook assertions execute the installed entrypoint;
// normal test runs exercise the real ingest command with real tmux subprocesses.
func TestSharedPaneRoutingRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	binary := os.Getenv("PROJMUX_TEST_PANE_ROUTE_BINARY")
	if binary != "" && !filepath.IsAbs(binary) {
		t.Fatal("installed smoke binary must be absolute")
	}
	// Darwin's inherited TMPDIR can exhaust the Unix socket path limit. Both
	// supported hosts provide /tmp; resolve its spelling before containment
	// checks because Darwin commonly exposes it through /private/tmp.
	root, err := os.MkdirTemp("/tmp", "pmx-pane-route-")
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	root = resolvedRoot
	cleanupVerified := true
	t.Cleanup(func() {
		if cleanupVerified {
			_ = os.RemoveAll(root)
		}
	})
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "HOME" || key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "HOME="+root, "TMUX_TMPDIR="+root,
		"XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"))
	if err := os.Mkdir(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(extra []string, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		process := exec.CommandContext(ctx, name, args...)
		process.Env = append(append([]string{}, env...), extra...)
		return process.CombinedOutput()
	}
	tmux := func(args ...string) string {
		t.Helper()
		out, err := run(nil, "tmux", args...)
		if err != nil {
			t.Fatalf("isolated tmux %q: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	otherName := fmt.Sprintf("other-%d", os.Getpid())
	sockets := []string{}
	panes := []string{}
	for _, name := range []string{fmt.Sprintf("app-%d", os.Getpid()), otherName} {
		pane := tmux("-L", name, "-f", "/dev/null", "new-session", "-d", "-s", "fixture", "-P", "-F", "#{pane_id}", "sleep 600")
		socket := tmux("-L", name, "display-message", "-p", "-t", pane, "#{socket_path}")
		if !strings.HasPrefix(socket, root+string(os.PathSeparator)) {
			t.Fatalf("unsafe smoke socket %q", socket)
		}
		sockets = append(sockets, socket)
		panes = append(panes, pane)
		t.Cleanup(func() {
			// Re-observe exactly the owned route before exact-path cleanup.
			out, queryErr := run(nil, "tmux", "-S", socket, "display-message", "-p", "#{socket_path}")
			if queryErr != nil {
				cleanupVerified = false
				t.Errorf("cleanup route cannot be verified: %v", queryErr)
				return
			}
			if strings.TrimSpace(string(out)) != socket || !strings.HasPrefix(socket, root+string(os.PathSeparator)) {
				cleanupVerified = false
				t.Errorf("cleanup route drifted: %q", out)
				return
			}
			if out, err := run(nil, "tmux", "-S", socket, "kill-server"); err != nil {
				cleanupVerified = false
				t.Errorf("cleanup: %v: %s", err, out)
			}
		})
	}
	// Detached production lookup keeps its fixed logical name. The servers
	// themselves are created with unique names; this private alias names only
	// the exact socket observed above, inside this test's dedicated root.
	alias := filepath.Join(filepath.Dir(sockets[0]), defaultAppSocket)
	if err := os.Symlink(sockets[0], alias); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(alias); err != nil || resolved != sockets[0] {
		t.Fatalf("app alias failed containment: %q %v", resolved, err)
	}
	if panes[0] != panes[1] {
		t.Fatalf("collision fixture needs identical pane ids, got %v", panes)
	}
	pane := panes[0]
	registry := coremetadata.Registry{APIVersion: coremetadata.APIVersion, SchemaVersion: coremetadata.SchemaVersion}
	mutator := coremetadata.Mutator{NewUID: sequentialTestUID(), DirExists: func(path string) (bool, error) { info, err := os.Stat(path); return err == nil && info.IsDir(), err }}
	registered, err := mutator.RegisterProject(&registry, coremetadata.RegisterProjectOptions{Root: root, OperationID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	paneUID := registered.Panes[0].Metadata.UID
	if _, err := mutator.RecordPaneActivation(&registry, paneUID, coremetadata.PaneActivationOptions{Generation: "fixture-generation", RuntimeID: pane, OperationID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	store := intmetadata.NewStore(intmetadata.PathFor(filepath.Join(root, "state", "projmux")))
	if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	for i, socket := range sockets {
		tmux("-S", socket, "set-option", "-g", tmuxopts.AppGlobal, "1")
		tmux("-S", socket, "set-option", "-p", "-t", pane, tmuxopts.PaneUID, paneUID)
		t.Logf("owned server %d socket=%s pane=%s", i, socket, pane)
	}
	option := func(socket, key string) string {
		return tmux("-S", socket, "show-options", "-p", "-qv", "-t", pane, key)
	}
	keys := []string{aiPaneStateOption, aiPaneHookActiveOption, attentionAckOption, attentionFocusArmedOption, aiPaneTopicOption, aiPaneResumeIDOption, aiPaneResumeSourceOption}
	reset := func() {
		for _, socket := range sockets {
			for _, key := range keys {
				tmux("-S", socket, "set-option", "-p", "-t", pane, key, "sentinel")
			}
		}
	}
	for _, inherited := range []bool{false, true} {
		label := map[bool]string{false: "detached", true: "inherited"}[inherited]
		t.Run(label, func(t *testing.T) {
			reset()
			target, opposite := sockets[0], sockets[1]
			extra := []string{}
			if inherited {
				target, opposite = sockets[1], sockets[0]
				extra = []string{"TMUX=" + target + ",42,0", "TMUX_PANE=" + pane}
			}
			cmdEnv := append(append([]string{}, env...), extra...)
			command := func() *aiCommand {
				cmd := testAICommand(root)
				cmd.lookupEnv = func(name string) string {
					for _, value := range cmdEnv {
						if key, val, ok := strings.Cut(value, "="); ok && key == name {
							return val
						}
					}
					return ""
				}
				cmd.readFile = os.ReadFile
				cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
				cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) { return run(extra, name, args...) }
				cmd.runCommand = func(_ context.Context, name string, args ...string) error {
					_, err := run(extra, name, args...)
					return err
				}
				cmd.notifyStore = &stubNotifyStore{}
				cmd.producer = &storeAttentionNotifyProducer{store: &stubNotifyStore{}, ttl: time.Minute}
				return cmd
			}
			// Direct option set/clear/record checks the entire shared boundary.
			cmd := command()
			if err := cmd.setAIPaneOption(pane, aiPaneTopicOption, "routed-topic"); err != nil {
				t.Fatal(err)
			}
			if err := cmd.clearAIPaneOption(pane, attentionAckOption); err != nil {
				t.Fatal(err)
			}
			cmd.recordAIPaneOption(pane, aiPaneHookActiveOption, "1")
			if option(target, aiPaneTopicOption) != "routed-topic" || option(target, attentionAckOption) != "" || option(target, aiPaneHookActiveOption) != "1" {
				t.Fatal("shared options missed target")
			}
			for _, key := range keys {
				if option(opposite, key) != "sentinel" {
					t.Fatalf("shared helper changed other server %s", key)
				}
			}
			for _, event := range []string{"PreToolUse", "UserPromptSubmit"} {
				for _, key := range []string{aiPaneStateOption, aiPaneHookActiveOption, attentionAckOption, attentionFocusArmedOption, aiPaneResumeIDOption, aiPaneResumeSourceOption} {
					tmux("-S", target, "set-option", "-p", "-t", pane, key, "sentinel")
				}
				payload := `{"hook_event_name":"` + event + `","session_id":"fixture-session","cwd":"` + root + `"}`
				if binary != "" {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					process := exec.CommandContext(ctx, binary, "internal", "agent-hook", "ingest", "codex-hook", "--pane", paneUID)
					process.Env = cmdEnv
					process.Stdin = strings.NewReader(payload)
					out, err := process.CombinedOutput()
					cancel()
					if err != nil {
						t.Fatalf("installed hook %s: %v: %s", event, err, out)
					}
				} else {
					cmd := command()
					cmd.stdin = strings.NewReader(payload)
					if err := cmd.Run([]string{"ingest", "codex-hook", "--pane", paneUID}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
						t.Fatal(err)
					}
				}
				if option(target, aiPaneHookActiveOption) != "1" || option(target, aiPaneResumeIDOption) != "fixture-session" || option(target, aiPaneResumeSourceOption) != "hook" {
					t.Fatalf("%s hook marker/resume writes missed target", event)
				}
				data, err := os.ReadFile(filepath.Join(root, "state", "projmux", aiIngestLogName))
				if err != nil {
					t.Fatal(err)
				}
				lines := nonEmptyLines(string(data))
				var record aiIngestLogEntry
				if len(lines) == 0 {
					t.Fatal("hook wrote no ingest record")
				}
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
					t.Fatal(err)
				}
				wantResult := "quiet"
				if event == "UserPromptSubmit" {
					wantResult = "state"
				}
				if record.Event != event || record.Result != wantResult {
					t.Fatalf("%s hook record=%+v", event, record)
				}
				if event == "PreToolUse" && option(target, aiPaneStateOption) != "sentinel" {
					t.Fatal("quiet marker event changed ordinary state")
				}
				for _, key := range keys {
					if option(opposite, key) != "sentinel" {
						t.Fatalf("%s hook changed opposite server %s", event, key)
					}
				}
			}
			if option(target, aiPaneStateOption) != "thinking" || option(target, aiPaneHookActiveOption) != "1" || option(target, attentionFocusArmedOption) != "" {
				t.Fatal("hook state/marker/clear missed target")
			}
			for _, key := range keys {
				if option(opposite, key) != "sentinel" {
					t.Fatalf("hook changed opposite server %s", key)
				}
			}

			// A missing exact inherited server cannot fall back to app-owned.
			missing := command()
			baseEnv := missing.lookupEnv
			missing.lookupEnv = func(name string) string {
				if name == "TMUX" {
					return filepath.Join(root, "missing") + ",1,0"
				}
				return baseEnv(name)
			}
			if err := missing.setAIPaneOption(pane, aiPaneTopicOption, "wrong"); !errors.Is(err, errAIPaneWriteUnavailable) {
				t.Fatalf("missing-route error=%v", err)
			}
			if option(target, aiPaneTopicOption) != "routed-topic" || option(opposite, aiPaneTopicOption) != "sentinel" {
				t.Fatal("missing route fell back")
			}
			t.Logf("%s: target state=thinking marker=1 cleared focus; opposite identical-%s sentinels unchanged; missing route refused; binary=%s", label, pane, binary)
		})
	}
}
