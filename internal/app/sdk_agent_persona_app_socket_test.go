package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	configpkg "github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The logical app socket "projmux" lives under the liveguard's private
// TMUX_TMPDIR. These tests start and stop only that private server, by the
// exact socket path found there, and refuse to run against a TMUX_TMPDIR that
// is empty or is the live /tmp/tmux-<uid> tree.
func personaAppSocketGuard(t *testing.T) {
	t.Helper()
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" || strings.HasPrefix(dir, "/tmp/tmux-") {
		t.Fatalf("TMUX_TMPDIR %q is not an isolated directory", dir)
	}
}

func personaAppSockets(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(os.Getenv("TMUX_TMPDIR"), "tmux-*", "projmux"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func personaAppKillServers(t *testing.T) {
	t.Helper()
	for _, socket := range personaAppSockets(t) {
		sdkCreateKillServer(t, socket)
		_ = os.Remove(socket)
	}
}

// personaAppStartServer starts the private logical app server with one control
// session, the way an operator's app server is marked.
func personaAppStartServer(t *testing.T) {
	t.Helper()
	requireRealTmux(t)
	for _, args := range [][]string{
		{"-f", "/dev/null", "new-session", "-d", "-s", "control", "-n", "control", "/bin/sh"},
		{"set-option", "-g", tmuxopts.AppGlobal, "1"},
		{"set-option", "-g", runtimeMutationSocketNameOption, "projmux"},
	} {
		if out, err := exec.Command("tmux", append([]string{"-L", "projmux"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
	}
}

// personaAppRegistry returns an addressable Registry, whose lookups take a pointer.
func personaAppRegistry(t *testing.T, state string) *metadata.Registry {
	t.Helper()
	r := projectRegistry(t, state)
	return &r
}

type personaAppFixture struct {
	state, config, uid string
	client             *projmux.Client
}

// personaAppNewFixture builds a Registry with one Claude Agent in phase and the
// generated app tmux config the resume path needs, in an isolated state and
// config directory. No tmux server is started.
func personaAppNewFixture(t *testing.T, binary, home, optionsSocket string, phase metadata.AgentPhase) personaAppFixture {
	t.Helper()
	state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
	t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
	root := t.TempDir()
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: optionsSocket, ProjmuxExecutable: binary})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	uid := seedSDKResumeAgent(t, state, projectRegistry(t, state).Windows[0].Metadata.UID, "claude", root, phase)
	if _, err := persona.NewDefaultStore(configpkg.Paths{ConfigDir: config, StateDir: state}).Write("reviewer", []byte("Review carefully.\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "tmux.conf"), []byte("set -g "+tmuxopts.AppGlobal+" 1\nset -g default-shell /bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return personaAppFixture{state: state, config: config, uid: uid, client: client}
}

// personaAppCLI runs the persona command the way the web does: the logical app
// socket by name.
func personaAppCLI(t *testing.T, f personaAppFixture, binary, home, action string, dry bool) (core.AgentPersonaResult, error) {
	t.Helper()
	args := []string{action, "uid:" + f.uid}
	if action == "attach" {
		args = append(args, "reviewer")
	}
	args = append(args, "-o", "json", "--socket", "projmux")
	if dry {
		args = append(args, "--dry-run")
	} else {
		args = append(args, "--yes")
	}
	var stdout, stderr bytes.Buffer
	err := sdkRelaunchCLI(t, f.state, f.config, "", binary, home).runPersona(args, "persona", &stdout, &stderr)
	var out core.AgentPersonaResult
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("CLI output %q: %v", stdout.String(), err)
	}
	return out, nil
}

func personaAppSDK(f personaAppFixture, action, socketPath string, dry bool) (core.AgentPersonaResult, error) {
	if action == "attach" {
		return f.client.AttachAgentPersona(context.Background(), core.AttachAgentPersonaRequest{Ref: "uid:" + f.uid, Persona: "reviewer", DryRun: dry, Confirm: !dry, SocketPath: socketPath})
	}
	return f.client.DetachAgentPersona(context.Background(), core.DetachAgentPersonaRequest{Ref: "uid:" + f.uid, DryRun: dry, Confirm: !dry, SocketPath: socketPath})
}

// personaAppProjection drops the identities that differ between two fixtures.
func personaAppProjection(r core.AgentPersonaResult) core.AgentPersonaResult {
	r.AgentUID, r.PaneUID, r.NewPaneUID = "", "", ""
	return r
}

// TestAgentPersonaSDKCLIAppSocketParity pins the logical app socket rule of the
// persona request: an empty SocketPath (and an empty Options.SocketPath) is CLI
// --socket projmux. With no server running an Offline or Failed Agent is
// resumed on an app server the request starts; with the app server running a
// Running Agent's Pane is closed on it and the Agent resumed there. The CLI
// result JSON and the SDK result are the same projection.
func TestAgentPersonaSDKCLIAppSocketParity(t *testing.T) {
	personaAppSocketGuard(t)
	relaunchProviderFence(t)
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	t.Cleanup(func() { personaAppKillServers(t) })
	for _, phase := range []metadata.AgentPhase{metadata.PhaseOffline, metadata.PhaseFailed} {
		for _, action := range []string{"attach", "detach"} {
			for _, dry := range []bool{false, true} {
				name := string(phase) + "/" + action
				if dry {
					name += "/dry-run"
				}
				t.Run("no-server/"+name, func(t *testing.T) {
					personaAppKillServers(t)
					var results []core.AgentPersonaResult
					for _, sdk := range []bool{false, true} {
						f := personaAppNewFixture(t, binary, home, "", metadata.PhaseOffline)
						if phase == metadata.PhaseFailed {
							failPersonaAppAgent(t, f)
						}
						if len(personaAppSockets(t)) != 0 {
							t.Fatal("an app server is running before the request")
						}
						var r core.AgentPersonaResult
						var err error
						if sdk {
							r, err = personaAppSDK(f, action, "", dry)
						} else {
							r, err = personaAppCLI(t, f, binary, home, action, dry)
						}
						if err != nil {
							t.Fatalf("sdk=%t: %v", sdk, err)
						}
						started := len(personaAppSockets(t)) == 1
						if started == dry {
							t.Fatalf("sdk=%t dry-run=%t: app server started=%t", sdk, dry, started)
						}
						if !dry {
							waitSDKResumeBirth(t, f.state, f.uid)
							if a, _ := personaAppRegistry(t, f.state).Agent(f.uid); a.Status.Phase != metadata.PhaseRunning || r.NewPaneUID != a.Status.PaneRef {
								t.Fatalf("sdk=%t: Agent not resumed on the new Pane: %+v", sdk, r)
							}
						}
						results = append(results, personaAppProjection(r))
						personaAppKillServers(t)
					}
					want := "resumed"
					if dry {
						want = "would-resume"
					}
					if results[0].Outcome != want || !reflect.DeepEqual(results[0], results[1]) {
						t.Fatalf("CLI %+v SDK %+v", results[0], results[1])
					}
				})
			}
		}
	}
	for _, action := range []string{"attach", "detach"} {
		t.Run("running/"+action, func(t *testing.T) {
			personaAppKillServers(t)
			var results []core.AgentPersonaResult
			for _, sdk := range []bool{false, true} {
				// The fixture first: the app server inherits this test's
				// XDG_STATE_HOME, so its Panes log into the isolated state.
				f := personaAppNewFixture(t, binary, home, "", metadata.PhaseOffline)
				personaAppStartServer(t)
				if _, err := f.client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + f.uid}); err != nil {
					t.Fatal(err)
				}
				waitSDKResumeBirth(t, f.state, f.uid)
				if action == "detach" {
					// Detach only changes an Agent that has a persona: attach one
					// first, the same way on both sides.
					if _, err := personaAppSDK(f, "attach", "", false); err != nil {
						t.Fatal(err)
					}
					waitSDKResumeBirth(t, f.state, f.uid)
				}
				before, _ := personaAppRegistry(t, f.state).Agent(f.uid)
				var r core.AgentPersonaResult
				var err error
				if sdk {
					r, err = personaAppSDK(f, action, "", false)
				} else {
					r, err = personaAppCLI(t, f, binary, home, action, false)
				}
				if err != nil {
					t.Fatalf("sdk=%t: %v", sdk, err)
				}
				waitSDKResumeBirth(t, f.state, f.uid)
				after, _ := personaAppRegistry(t, f.state).Agent(f.uid)
				if after.Status.Phase != metadata.PhaseRunning || after.Status.PaneRef == before.Status.PaneRef || r.PaneUID != before.Status.PaneRef || r.NewPaneUID != after.Status.PaneRef || !after.Status.SessionRef.SameConversation(before.Status.SessionRef) {
					t.Fatalf("sdk=%t: Pane not replaced on the same conversation: %+v", sdk, r)
				}
				results = append(results, personaAppProjection(r))
				personaAppKillServers(t)
			}
			if results[0].Outcome != "restarted" || !results[0].Restart || !reflect.DeepEqual(results[0], results[1]) {
				t.Fatalf("CLI %+v SDK %+v", results[0], results[1])
			}
		})
	}
	t.Run("running/no-server", func(t *testing.T) {
		// A Registry that says Running while no app server exists: there is
		// no Pane to close. The request refuses before any persona stays
		// recorded, on the CLI and on the SDK alike, with the same message.
		var messages []string
		for _, sdk := range []bool{false, true} {
			personaAppKillServers(t)
			f := personaAppNewFixture(t, binary, home, "", metadata.PhaseOffline)
			if _, err := store.NewStore(store.PathFor(f.state)).Update(func(r *metadata.Registry) error {
				if _, err := store.DefaultMutator().AttachAgentPane(r, f.uid, metadata.BootstrapPane{Name: "running-pane", CWD: t.TempDir()}, "fixture"); err != nil {
					return err
				}
				a, _ := r.Agent(f.uid)
				a.Status.Interaction = metadata.AgentInteraction{Kind: metadata.InteractionIdle, ObservedAt: time.Now(), Source: string(metadata.InteractionSourceProviderHook)}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var err error
			if sdk {
				_, err = personaAppSDK(f, "attach", "", false)
			} else {
				_, err = personaAppCLI(t, f, binary, home, "attach", false)
			}
			if err == nil {
				t.Fatalf("sdk=%t: a Running Agent with no app server was accepted", sdk)
			}
			a, _ := personaAppRegistry(t, f.state).Agent(f.uid)
			if a.Status.Phase != metadata.PhaseRunning || metadata.PersonaAnnotationsOf(*a).Persona != "" || len(personaAppSockets(t)) != 0 {
				t.Fatalf("sdk=%t: phase %s persona %q servers %v", sdk, a.Status.Phase, metadata.PersonaAnnotationsOf(*a).Persona, personaAppSockets(t))
			}
			messages = append(messages, err.Error())
		}
		if messages[0] != messages[1] {
			t.Fatalf("refusal differs:\nCLI: %s\nSDK: %s", messages[0], messages[1])
		}
	})
}

func failPersonaAppAgent(t *testing.T, f personaAppFixture) {
	t.Helper()
	if _, err := store.NewStore(store.PathFor(f.state)).Update(func(r *metadata.Registry) error {
		_, err := store.DefaultMutator().TransitionAgent(r, f.uid, metadata.PhaseFailed, "fixture failure")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAgentPersonaSDKRefusesExplicitSocketPathWithoutServer keeps the explicit
// path promise: a SocketPath, on the request or on Options, is that exact live
// server. With none running the Offline Agent's resume is refused with
// create-route-refused, the persona stays recorded, and no server starts. The
// CLI ignores --socket-path for an Offline Agent; that difference is intended.
func TestAgentPersonaSDKRefusesExplicitSocketPathWithoutServer(t *testing.T) {
	personaAppSocketGuard(t)
	relaunchProviderFence(t)
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	t.Cleanup(func() { personaAppKillServers(t) })
	missing := filepath.Join(os.Getenv("TMUX_TMPDIR"), "no-such-dir", "sock")
	for _, action := range []string{"attach", "detach"} {
		for _, where := range []string{"request", "options"} {
			t.Run(action+"/"+where, func(t *testing.T) {
				personaAppKillServers(t)
				optionsSocket, requestSocket := "", missing
				if where == "options" {
					optionsSocket, requestSocket = missing, ""
				}
				f := personaAppNewFixture(t, binary, home, optionsSocket, metadata.PhaseOffline)
				_, err := personaAppSDK(f, action, requestSocket, false)
				var refusal *projmux.Refusal
				if !errors.As(err, &refusal) || refusal.Code != "create-route-refused" {
					t.Fatalf("err = %v, want create-route-refused", err)
				}
				a, _ := personaAppRegistry(t, f.state).Agent(f.uid)
				if a.Status.Phase != metadata.PhaseOffline || len(personaAppSockets(t)) != 0 {
					t.Fatalf("phase %s, app servers %v", a.Status.Phase, personaAppSockets(t))
				}
				if got := metadata.PersonaAnnotationsOf(*a).Persona; (action == "attach") != (got == "reviewer") {
					t.Fatalf("persona annotation %q after %s", got, action)
				}
			})
		}
	}
}
