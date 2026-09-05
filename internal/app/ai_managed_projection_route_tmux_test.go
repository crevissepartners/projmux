package app

import (
	"bytes"
	"context"
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

// The optional installed binary covers the reachable hook interaction only.
// Internal topic/idle cases always compile this source; they are not installed
// CLI calls. Canonical agent status/topic uses a separate already-routed mirror.
func TestManagedProjectionRoutingRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	binary := os.Getenv("PROJMUX_TEST_MANAGED_ROUTE_BINARY")
	if binary != "" && !filepath.IsAbs(binary) {
		t.Fatal("installed smoke binary must be absolute")
	}
	// Darwin's inherited TMPDIR can exhaust the Unix socket path limit. Both
	// supported hosts provide /tmp; resolve its spelling before containment
	// checks because Darwin commonly exposes it through /private/tmp.
	root, err := os.MkdirTemp("/tmp", "pmx-managed-route-")
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
		if key == "HOME" || key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") || key == internalActivationPaneUIDEnv || key == internalActivationGenerationEnv {
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

	h := newSessionRefHarness(t, aiModeClaude)
	paneUID := h.paneUID
	registeredPane, _ := h.registry.Pane(paneUID)
	registeredPane.Status.Activation.RuntimeID = pane
	store := intmetadata.NewStore(intmetadata.PathFor(filepath.Join(root, "state", "projmux")))
	for i, socket := range sockets {
		tmux("-S", socket, "set-option", "-g", tmuxopts.AppGlobal, "1")
		tmux("-S", socket, "set-option", "-p", "-t", pane, tmuxopts.PaneUID, paneUID)
		t.Logf("owned server %d socket=%s pane=%s", i, socket, pane)
	}
	option := func(socket, key string) string {
		return tmux("-S", socket, "show-options", "-p", "-qv", "-t", pane, key)
	}
	keys := []string{aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption, aiPaneTopicOption, aiPaneTopicManualOption}
	for _, inherited := range []bool{false, true} {
		label := map[bool]string{false: "detached", true: "inherited"}[inherited]
		t.Run(label, func(t *testing.T) {
			target, opposite := sockets[0], sockets[1]
			extra := []string{}
			if inherited {
				target, opposite = sockets[1], sockets[0]
				extra = []string{"TMUX=" + target + ",42,0", "TMUX_PANE=" + pane}
			}
			for _, tc := range managedProjectionCases(pane) {
				t.Run(tc.name, func(t *testing.T) {
					for _, socket := range sockets {
						for _, key := range keys {
							tmux("-S", socket, "set-option", "-p", "-t", pane, key, "sentinel")
						}
					}
					a, _ := h.registry.Agent(h.agentUID)
					a.Status.Interaction = coremetadata.AgentInteraction{Kind: coremetadata.InteractionInProgress, Source: "manual", ObservedAt: sessionRefObservedAt.Add(-time.Minute)}
					if a.Metadata.Annotations == nil {
						a.Metadata.Annotations = map[string]string{}
					}
					a.Metadata.Annotations[coremetadata.AnnotationAgentTopic] = "old topic"
					if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = h.registry.Clone(); return nil }); err != nil {
						t.Fatal(err)
					}
					h.updates = 0
					cmd := testAICommand(root)
					cmdEnv := append(append([]string{}, env...), extra...)
					cmd.lookupEnv = func(name string) string {
						for _, val := range cmdEnv {
							if key, value, ok := strings.Cut(val, "="); ok && key == name {
								return value
							}
						}
						return ""
					}
					cmd.readFile = os.ReadFile
					cmd.loadRegistry = store.Load
					cmd.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
						reg, err := store.Update(fn)
						if err == nil {
							h.updates++
							*h.registry = reg.Clone()
						}
						return reg, err
					}
					// Existing managed attribution reads receive the known exact route.
					// Writes use real argv unchanged, making detached raw writes fail rather
					// than being silently repaired by the fixture's runner.
					cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
						if name == "tmux" && (len(args) == 0 || (args[0] != "-L" && args[0] != "-S")) {
							args = append([]string{"-S", target}, args...)
						}
						return run(extra, name, args...)
					}
					cmd.runCommand = func(_ context.Context, name string, args ...string) error {
						out, err := run(extra, name, args...)
						if err != nil {
							return fmt.Errorf("%w: %s", err, out)
						}
						return nil
					}
					cmd.notifyStore = &stubNotifyStore{}
					cmd.producer = &storeAttentionNotifyProducer{store: &stubNotifyStore{}, ttl: time.Minute}
					h.cmd = cmd
					var out, errOut bytes.Buffer
					var result error
					installed := binary != "" && tc.args[0] == "ingest"
					if installed {
						// The old attribution reader is unprefixed. A private default alias to
						// the opposite server supplies the same registered PaneUID; only the
						// shared write route may change the app target. No live default exists
						// inside this fixture root.
						defaultAlias := filepath.Join(filepath.Dir(sockets[0]), "default")
						if err := os.Symlink(opposite, defaultAlias); err != nil {
							t.Fatal(err)
						}
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						process := exec.CommandContext(ctx, binary, "internal", "agent-hook", "ingest", "claude-hook", "--pane", paneUID)
						process.Env = cmdEnv
						process.Stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","cwd":"/src/app"}`)
						process.Stdout, process.Stderr = &out, &errOut
						result = process.Run()
						cancel()
						if err := os.Remove(defaultAlias); err != nil {
							t.Fatal(err)
						}
						reg, err := store.Load()
						if err != nil {
							t.Fatal(err)
						}
						*h.registry = reg
					} else {
						result = runManagedProjectionCase(h, tc, &out, &errOut)
					}
					if !installed && h.updates != 1 {
						t.Errorf("managed caller committed %d Registry updates, want 1", h.updates)
					}
					assertManagedProjectionRegistry(t, h, tc)
					if result != nil || out.Len() != 0 || errOut.Len() != 0 {
						t.Errorf("managed caller result=%v stdout=%q stderr=%q", result, out.String(), errOut.String())
					}
					for key, want := range tc.want {
						if got := option(target, key); got != want {
							t.Errorf("managed target %s=%q, want %q", key, got, want)
						}
					}
					// Empty show-options is not enough: the option must actually be absent.
					options := tmux("-S", target, "show-options", "-p", "-t", pane)
					for key, want := range tc.want {
						if want == "" && strings.Contains(options, key+" ") {
							t.Errorf("managed target %s must be unset", key)
						}
					}
					for _, key := range keys {
						if got := option(opposite, key); got != "sentinel" {
							t.Errorf("opposite same-%s %s=%q, want sentinel", pane, key, got)
						}
					}
					t.Logf("%s target=%s opposite=%s installed-hook=%t registry interaction=%s topic=%q", tc.name, target, opposite, installed, h.agent(t).Status.Interaction.Kind, h.agent(t).Metadata.Annotations[coremetadata.AnnotationAgentTopic])
				})
			}
		})
	}

	if binary != "" {
		t.Run("installed_canonical_parity", func(t *testing.T) {
			target, opposite := sockets[1], sockets[0]
			extra := []string{"TMUX=" + target + ",42,0", "TMUX_PANE=" + pane}
			for _, tc := range managedProjectionCases(pane) {
				t.Run(tc.name, func(t *testing.T) {
					for _, socket := range sockets {
						for _, key := range keys {
							tmux("-S", socket, "set-option", "-p", "-t", pane, key, "sentinel")
						}
					}
					args := []string{"agent"}
					switch tc.name {
					case "interaction_hook_set":
						args = append(args, "status", "set", "in_progress")
					case "interaction_internal_idle":
						args = append(args, "status", "set", "idle")
					case "topic_set":
						args = append(args, "topic", "set", tc.topic)
					case "topic_clear":
						args = append(args, "topic", "clear")
					}
					args = append(args, "uid:"+h.agentUID)
					out, err := run(extra, binary, args...)
					if err != nil || len(out) != 0 {
						t.Fatalf("installed canonical %q result=%v output=%q", args, err, out)
					}
					reg, err := store.Load()
					if err != nil {
						t.Fatal(err)
					}
					a, ok := reg.Agent(h.agentUID)
					if !ok {
						t.Fatal("canonical Agent disappeared")
					}
					if strings.HasPrefix(tc.name, "interaction") {
						if a.Status.Interaction.Kind != tc.kind || a.Status.Interaction.Source != "manual" {
							t.Errorf("canonical Registry interaction=%+v", a.Status.Interaction)
						}
					} else if got := a.Metadata.Annotations[coremetadata.AnnotationAgentTopic]; got != tc.topic {
						t.Errorf("canonical Registry topic=%q, want %q", got, tc.topic)
					}
					options := tmux("-S", target, "show-options", "-p", "-t", pane)
					for key, want := range tc.want {
						if got := option(target, key); got != want {
							t.Errorf("canonical target %s=%q, want %q", key, got, want)
						}
						if want == "" && strings.Contains(options, key+" ") {
							t.Errorf("canonical target %s must be unset", key)
						}
					}
					for _, key := range keys {
						if got := option(opposite, key); got != "sentinel" {
							t.Errorf("canonical opposite %s=%q, want sentinel", key, got)
						}
					}
					t.Logf("installed canonical parity args=%q target=%s opposite=%s; separate mirror, not internal topic proof", args, target, opposite)
				})
			}
		})
	}
}
