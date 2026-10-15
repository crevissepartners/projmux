package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func sdkAgentCLI(t *testing.T, state, config, socket, binary, home string) *createCommand {
	t.Helper()
	storage := store.NewStore(store.PathFor(state))
	resource := &resourceStore{load: storage.LoadReadOnly, snapshot: storage.LoadReadOnly, update: storage.Update, mutator: store.DefaultMutator, stateDir: func() (string, error) { return state, nil }}
	c, err := virtualWindowCreator(tmux.ExecRunner{}, os.Getenv, resource, socket)
	if err != nil {
		t.Fatal(err)
	}
	c.shell = "/bin/sh"
	c.homeDir = func() (string, error) { return home, nil }
	c.lookupEnv = os.Getenv
	c.runtime.Executable = func() (string, error) { return binary, nil }
	c.runtime.ConfigPath = filepath.Join(config, "tmux.conf")
	ai := &aiCommand{lookupEnv: os.Getenv, homeDir: c.homeDir, readCommand: readExternalCommand, runCommand: runExternalCommand, executable: c.runtime.Executable, loadRegistry: storage.LoadReadOnly, updateRegistry: storage.Update}
	c.agents = ai
	c.resumes = ai
	return c
}
func normalizeAgentCreateJSON(t *testing.T, raw string, reg metadata.Registry, socket string) string {
	t.Helper()
	for i, a := range reg.Agents {
		raw = strings.ReplaceAll(raw, a.Metadata.UID, "AGENT-"+string(rune('A'+i)))
	}
	raw = regexp.MustCompile(`("pid"\s*:\s*)[0-9]+`).ReplaceAllString(raw, `${1}0`)
	raw = regexp.MustCompile(`("start"\s*:\s*)"[^"]*"`).ReplaceAllString(raw, `${1}"NORMALIZED"`)
	return normalizeCreateJSON(t, raw, reg, socket)
}

// Wait for the asynchronous supervisor registration on both sides before
// comparing Registry snapshots; a missing process identity remains a failure.
func waitSDKClaudeProcesses(t *testing.T, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		reg := projectRegistry(t, state)
		ready := true
		claudeCount := 0
		for _, agent := range reg.Agents {
			if agent.Spec.Provider != "claude" {
				continue
			}
			claudeCount++
			registered := false
			for _, pane := range reg.Panes {
				if pane.Metadata.UID == agent.Status.PaneRef && pane.Status.Activation.Claude != nil && pane.Status.Activation.Claude.Process.PID > 0 {
					registered = true
				}
			}
			ready = ready && registered
		}
		if ready && claudeCount > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Claude process registration did not finish: %s", projectRegistryBytes(t, state))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func sdkAgentHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex", "agy"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexec /bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home
}
func TestCreateAgentSDKCLIParity(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name, provider       string
		interactive, profile bool
	}{{"claude", "claude", false, false}, {"codex-empty", "codex", false, false}, {"codex-interactive", "codex", true, false}, {"antigravity", "antigravity", false, false}, {"claude-profile", "claude", false, true}, {"claude-creator", "claude", false, false}, {"claude-workspace", "claude", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var snapshots, receipts, projections, runtimes []string
			for _, sdk := range []bool{false, true} {
				state := filepath.Join(t.TempDir(), "projmux")
				config := filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				socket, run := sdkCreateSocket(t)
				run("set-option", "-w", "-t", "control:0", "automatic-rename", "off")
				run("rename-window", "-t", "control:0", "control")
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
					t.Fatal(err)
				}
				if _, err = client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"}); err != nil {
					t.Fatal(err)
				}
				req := core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: tc.provider, InteractiveOnly: tc.interactive, Name: "worker", Labels: []string{"team=one"}}
				args := []string{"agent", "--project", "sample", "--window", "extra", "--provider", tc.provider, "--name", "worker", "--label", "team=one"}
				if tc.interactive {
					args = append(args, "--interactive-only")
				}
				if tc.name == "claude-creator" {
					parent, e := client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Name: "parent"})
					if e != nil {
						t.Fatal(e)
					}
					req.Creator = "uid:" + parent.Agent.Metadata.UID
					args = append(args, "--creator", req.Creator)
				}
				if tc.name == "claude-workspace" {
					cwd, extra := filepath.Join(root, "nested"), filepath.Join(root, "extra-root")
					for _, dir := range []string{cwd, extra} {
						if e := os.MkdirAll(dir, 0700); e != nil {
							t.Fatal(e)
						}
					}
					reg := projectRegistry(t, state)
					for _, window := range reg.Windows {
						if window.Metadata.Name == "extra" {
							req.Pane = "uid:" + window.Spec.DefaultShellPaneRef
						}
					}
					req.Cwd = cwd
					req.AddDirs = []string{extra}
					req.Placement = "down"
					args = append(args, "--pane", req.Pane, "--cwd", cwd, "--add-dir", extra, "--placement", "down")
				}
				if tc.profile {
					if _, err = client.WriteInstructions(context.Background(), "reviewer", []byte("Read carefully.\n")); err != nil {
						t.Fatal(err)
					}
					if _, err = client.WriteProfile(context.Background(), "review", []byte("provider = \"claude\"\ninstructions = \"reviewer\"\nmodel = \"sonnet\"\neffort = \"high\"\n")); err != nil {
						t.Fatal(err)
					}
					req.Profile = "review"
					args = append(args, "--profile", "review")
				}
				var receipt, projection string
				if sdk {
					result, err := client.CreateAgent(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					receipt = sdkJSON(t, result.Receipt)
					projection = sdkJSON(t, result.Projection)
					if result.PaneID == "" || result.Agent.Metadata.Name != "worker" {
						t.Fatalf("missing result: %+v", result)
					}
				} else {
					c := sdkAgentCLI(t, state, config, socket, binary, home)
					var stdout, stderr bytes.Buffer
					if err := c.Run(append(args, "-o", "receipt"), &stdout, &stderr); err != nil {
						t.Fatalf("CLI: %v %s", err, &stderr)
					}
					receipt = stdout.String()
					reg := projectRegistry(t, state)
					var row createResult
					for _, a := range reg.Agents {
						if a.Metadata.Name == "worker" {
							row = createResult{kind: metadata.KindAgent, uid: a.Metadata.UID, name: a.Metadata.Name}
						}
					}
					var out bytes.Buffer
					if err := c.writeResults(&out, "create agent", "json", metadata.KindAgent, []createResult{row}); err != nil {
						t.Fatal(err)
					}
					projection = out.String()
				}
				if tc.provider == "claude" {
					waitSDKClaudeProcesses(t, state)
				}
				reg := projectRegistry(t, state)
				snapshots = append(snapshots, normalizeAgentCreateJSON(t, projectRegistryBytes(t, state), reg, socket))
				receipts = append(receipts, normalizeAgentCreateJSON(t, receipt, reg, socket))
				projections = append(projections, normalizeAgentCreateJSON(t, projection, reg, socket))
				runtimes = append(runtimes, normalizeAgentCreateJSON(t, run("list-panes", "-a", "-F", "#{window_name}|#{pane_current_path}|#{@projmux_pane_uid}|#{@projmux_ai_agent}|#{@projmux_codex_native_declared}"), reg, socket))
			}
			for i, pair := range [][]string{snapshots, receipts, projections, runtimes} {
				if pair[0] != pair[1] {
					t.Fatalf("pair %d SDK/CLI mismatch\nCLI %s\nSDK %s", i, pair[0], pair[1])
				}
			}
		})
	}
}
func TestCreateAgentSDKRefusalsBeforeTransport(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	state, config := t.TempDir(), t.TempDir()
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, ProjmuxExecutable: binary, SocketPath: filepath.Join(t.TempDir(), "absent")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.WriteProfile(context.Background(), "claude", []byte("provider = \"claude\"\n")); err != nil {
		t.Fatal(err)
	}
	if _, err = client.WriteInstructions(context.Background(), "reviewer", []byte("Review carefully.\n")); err != nil {
		t.Fatal(err)
	}
	if _, err = client.WriteProfile(context.Background(), "codex-instructions", []byte("provider = \"codex\"\ninstructions = \"reviewer\"\n")); err != nil {
		t.Fatal(err)
	}
	before := projectRegistryBytes(t, state)
	cases := []struct {
		req  core.CreateAgentRequest
		code projmux.Code
	}{
		{core.CreateAgentRequest{Project: "sample", Window: "w"}, projmux.CodeCreateRequestInvalid},
		{core.CreateAgentRequest{Project: "sample", Window: "w", Provider: "codex", Payload: []string{"hello"}}, projmux.CodeCreateNativeCodexUnavailable},
		{core.CreateAgentRequest{Project: "sample", Window: "w", Provider: "codex", Instructions: "reviewer"}, projmux.CodeInstructionsProviderUnsupported},
		{core.CreateAgentRequest{Project: "sample", Window: "w", Provider: "codex", Profile: "codex-instructions"}, projmux.CodeInstructionsProviderUnsupported},
		{core.CreateAgentRequest{Project: "sample", Window: "w", Provider: "antigravity", Profile: "claude"}, projmux.CodeProfileProviderMismatch},
		{core.CreateAgentRequest{Project: "sample", Window: "w", Provider: "claude", Creator: "uid:missing"}, projmux.CodeCreateCreatorInvalid},
		{core.CreateAgentRequest{Project: "sample", Window: "w", Provider: "claude", Effort: "bad"}, projmux.CodeCreateRequestInvalid},
	}
	for _, tc := range cases {
		_, err := client.CreateAgent(context.Background(), tc.req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != tc.code {
			t.Fatalf("%+v: %T %v wanted %s", tc.req, err, err, tc.code)
		}
		if strings.Contains(err.Error(), "CreateAgentRequest.Provider") || strings.Contains(err.Error(), "CreateAgentRequest.Profile") || strings.Contains(err.Error(), "CreateAgentRequest.Effort") {
			t.Fatalf("CLI refusal was rewritten: %s", err)
		}
		if after := projectRegistryBytes(t, state); after != before {
			t.Fatalf("refusal changed Registry")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CreateAgent(canceled, core.CreateAgentRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// The provider fixture never contacts a real provider. Claude with a payload
// deliberately emits no hook, exercising the committed/unconfirmed contract.
func TestCreateAgentSDKCLIFailureParity(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"pre-hook", "post-hook", "split-failure", "partial-split-failure", "activation-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			var snapshots, panes []string
			for _, sdk := range []bool{false, true} {
				state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				socket, run := sdkCreateSocket(t)
				run("set-option", "-w", "-t", "control:0", "automatic-rename", "off")
				run("rename-window", "-t", "control:0", "control")
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
					t.Fatal(err)
				}
				window := "extra"
				if scenario == "pre-hook" || scenario == "post-hook" {
					window = "uid:" + projectRegistry(t, state).Windows[0].Metadata.UID
				} else if _, err = client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"}); err != nil {
					t.Fatal(err)
				}
				before := projectRegistryBytes(t, state)
				oldPanes := run("list-panes", "-a", "-F", "#{pane_id}")
				if err = os.MkdirAll(config, 0700); err != nil {
					t.Fatal(err)
				}
				hook := ""
				if scenario == "pre-hook" {
					hook = "[hooks.pre-create]\nrun = \"exit 7\"\n"
				}
				if scenario == "post-hook" {
					hook = "[hooks.post-create]\nrun = \"exit 9\"\n"
				}
				if err = os.WriteFile(filepath.Join(config, "config.toml"), []byte(hook), 0600); err != nil {
					t.Fatal(err)
				}
				oldPath := os.Getenv("PATH")
				if strings.Contains(scenario, "split-failure") {
					shim := t.TempDir()
					failure := "exit 12"
					if scenario == "partial-split-failure" {
						failure = strconv.Quote(realTmux) + " \"$@\"; exit 12"
					}
					script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = split-window ]; then " + failure + "; fi\ndone\nexec " + strconv.Quote(realTmux) + " \"$@\"\n"
					if err = os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", shim+string(os.PathListSeparator)+oldPath)
				}
				req := core.CreateAgentRequest{Project: "sample", Window: window, Provider: "claude", Name: "worker"}
				args := []string{"agent", "--project", "sample", "--window", window, "--provider", "claude", "--name", "worker"}
				if scenario == "activation-timeout" {
					req.Payload = []string{"hello"}
					args = append(args, "--", "hello")
				}
				var result core.CreateAgentResult
				if sdk {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					observed := make(chan struct{})
					stop := make(chan struct{})
					if scenario == "activation-timeout" {
						// Options own the Registry even when the caller environment differs.
						t.Setenv("XDG_STATE_HOME", t.TempDir())
						t.Setenv("XDG_CONFIG_HOME", t.TempDir())
						go func() {
							ticker := time.NewTicker(5 * time.Millisecond)
							defer ticker.Stop()
							for {
								select {
								case <-stop:
									return
								case <-ticker.C:
									registry, e := store.NewStore(store.PathFor(state)).LoadReadOnly()
									if e == nil && len(registry.Agents) > 0 {
										cancel()
										close(observed)
										return
									}
								}
							}
						}()
					}
					result, err = client.CreateAgent(ctx, req)
					close(stop)
					if scenario == "activation-timeout" {
						select {
						case <-observed:
						default:
							t.Fatal("post-admission cancellation not exercised")
						}
						if errors.Is(err, context.Canceled) {
							t.Fatal("admitted activation stopped on cancellation")
						}
					}
				} else {
					command := sdkAgentCLI(t, state, config, socket, binary, home)
					if scenario == "activation-timeout" {
						command.agents = &sdkTimeoutLauncher{aiCommand: command.agents.(*aiCommand)}
					}
					var out, stderr bytes.Buffer
					err = command.Run(args, &out, &stderr)
				}
				t.Setenv("PATH", oldPath)
				if scenario == "post-hook" || scenario == "activation-timeout" {
					waitSDKClaudeProcesses(t, state)
				}
				reg := projectRegistry(t, state)
				if scenario == "post-hook" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil {
					t.Fatal("injected failure succeeded")
				}
				if sdk && err != nil {
					var refusal *projmux.Refusal
					if !errors.As(err, &refusal) {
						t.Fatalf("untyped failure: %T %v", err, err)
					}
					if scenario == "activation-timeout" && refusal.Code != projmux.CodeCreateActivationUnconfirmed {
						t.Fatalf("wrong activation refusal: %v", refusal.Code)
					}
				}
				if scenario == "activation-timeout" {
					if len(reg.Agents) != 1 || reg.Agents[0].Status.Activation.State != metadata.ActivationUnconfirmed {
						t.Fatalf("missing preserved activation: %+v", reg.Agents)
					}
					if sdk && (result.PaneID == "" || len(result.Projection.Items) != 1 || result.Agent.Metadata.UID == "" || len(result.Receipt.AffectedUIDs) == 0) {
						t.Fatalf("missing result with error: %+v", result)
					}
					if run("list-panes", "-a", "-F", "#{pane_id}") == oldPanes {
						t.Fatal("committed Pane rolled back")
					}
				} else if scenario != "post-hook" {
					if projectRegistryBytes(t, state) != before {
						t.Fatal("rollback wrote Registry")
					}
					if got := run("list-panes", "-a", "-F", "#{pane_id}"); got != oldPanes {
						t.Fatalf("rollback left Pane: %s", got)
					}
				}
				snapshots = append(snapshots, normalizeAgentCreateJSON(t, projectRegistryBytes(t, state), reg, socket))
				panes = append(panes, normalizeAgentCreateJSON(t, run("list-panes", "-a", "-F", "#{window_name}|#{@projmux_pane_uid}|#{@projmux_ai_agent}"), reg, socket))
			}
			for _, pair := range [][]string{snapshots, panes} {
				if pair[0] != pair[1] {
					t.Fatalf("SDK/CLI %s mismatch\nCLI %s\nSDK %s", scenario, pair[0], pair[1])
				}
			}
		})
	}
}

type sdkTimeoutLauncher struct{ *aiCommand }

func (s *sdkTimeoutLauncher) AwaitAgentActivation(context.Context, tmuxCommandRunner, string, time.Duration, time.Duration) (bool, string, error) {
	return false, "provider-hook", nil
}

func TestCreateAgentSDKInstructionsRefusalPreservesCause(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	state, config := t.TempDir(), t.TempDir()
	c, e := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, ProjmuxExecutable: binary, SocketPath: filepath.Join(t.TempDir(), "absent")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.CreateProject(context.Background(), core.CreateProjectRequest{Name: "sample", Root: t.TempDir()}); e != nil {
		t.Fatal(e)
	}
	before := projectRegistryBytes(t, state)
	_, e = c.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "w", Provider: "antigravity", Instructions: "reviewer"})
	var refusal *projmux.Refusal
	var cause *persona.Error
	if !errors.As(e, &refusal) || refusal.Code != projmux.CodeInstructionsProviderUnsupported || !errors.As(e, &cause) || cause.Reason != persona.ReasonProviderUnsupported || cause.Name != "reviewer" {
		t.Fatalf("untyped cause: %T %v", e, e)
	}
	if !strings.Contains(e.Error(), "--instructions") || strings.Contains(e.Error(), "CreateAgentRequest.Instructions") {
		t.Fatal(e)
	}
	if projectRegistryBytes(t, state) != before {
		t.Fatal("refusal wrote Registry")
	}
	cli := sdkAgentCLI(t, state, config, filepath.Join(t.TempDir(), "absent"), binary, home)
	var out, stderr bytes.Buffer
	cliError := cli.Run([]string{"agent", "--project", "sample", "--window", "w", "--provider", "antigravity", "--instructions", "reviewer"}, &out, &stderr)
	if !IsUsageError(cliError) || cliError.Error() != "create agent --instructions applies only to --provider claude and --provider codex (persona-provider-unsupported); nothing was created" {
		t.Fatalf("CLI prose/rc changed: %v", cliError)
	}
}
