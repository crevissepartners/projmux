package app

import (
	"bytes"
	"context"
	"encoding/json"
	configpkg "github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAgentPersonaSDKCLIExecutionParity(t *testing.T) {
	relaunchProviderFence(t)
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	for _, phase := range []metadata.AgentPhase{metadata.PhaseOffline, metadata.PhaseFailed, metadata.PhaseRunning} {
		t.Run(string(phase), func(t *testing.T) {
			var settings []string
			for _, sdk := range []bool{false, true} {
				state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				socket, _ := sdkCreateSocket(t)
				client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
					t.Fatal(err)
				}
				w, err := client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"})
				if err != nil {
					t.Fatal(err)
				}
				uid := seedSDKResumeAgent(t, state, w.Window.Metadata.UID, "claude", root, metadata.PhaseOffline)
				if phase == metadata.PhaseFailed {
					_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
						_, err := store.DefaultMutator().TransitionAgent(r, uid, metadata.PhaseFailed, "fixture failure")
						return err
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				if phase == metadata.PhaseRunning {
					// A live pane is obtained by the existing shared resume transaction.
					_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
						a, _ := r.Agent(uid)
						a.Status.Phase = metadata.PhaseOffline
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + uid}); err != nil {
						t.Fatal(err)
					}
					waitSDKResumeBirth(t, state, uid)
				}
				reg := projectRegistry(t, state)
				old, _ := reg.Agent(uid)
				oldPane := old.Status.PaneRef
				paths := configpkg.Paths{ConfigDir: config, StateDir: state}
				if _, e := persona.NewDefaultStore(paths).Write("reviewer", []byte("Review this carefully.\n")); e != nil {
					t.Fatal(e)
				}
				var stderr bytes.Buffer
				if sdk {
					result, err := client.AttachAgentPersona(context.Background(), core.AttachAgentPersonaRequest{Ref: "uid:" + uid, Persona: "reviewer", Confirm: true})
					if err != nil {
						t.Fatal(err)
					}
					if result.AgentUID != uid || result.NewPaneUID == "" || result.NewPaneUID == oldPane {
						t.Fatalf("bad result %+v", result)
					}
				} else {
					c := sdkRelaunchCLI(t, state, config, socket, binary, home)
					var output bytes.Buffer
					err = c.runPersona([]string{"attach", "uid:" + uid, "reviewer", "--yes", "--output", "json", "--socket-path", socket}, "persona", &output, &stderr)
					if err != nil {
						t.Fatalf("%v %s", err, stderr.String())
					}
				}
				waitSDKResumeBirth(t, state, uid)
				after := projectRegistry(t, state)
				a, _ := after.Agent(uid)
				if a.Status.SessionRef.ConversationID() != old.Status.SessionRef.ConversationID() || a.Status.PaneRef == oldPane || a.Status.Phase != metadata.PhaseRunning {
					t.Fatal("identity/conversation/pane parity lost")
				}
				layers := map[string]string{}
				for _, k := range []string{metadata.AnnotationAgentPersona, metadata.AnnotationAgentPersonaDigest, metadata.AnnotationAgentInstructionsSource, metadata.AnnotationAgentSystemPromptSnapshot} {
					layers[k] = a.Metadata.Annotations[k]
				}
				if layers[metadata.AnnotationAgentInstructionsSource] != metadata.SettingSourceAttach || layers[metadata.AnnotationAgentSystemPromptSnapshot] != metadata.SystemPromptSnapshotOff || layers[metadata.AnnotationAgentPersonaDigest] != persona.Digest([]byte("Review this carefully.\n")) {
					t.Fatalf("attach annotations %+v", layers)
				}
				if sdk {
					result, e := client.DetachAgentPersona(context.Background(), core.DetachAgentPersonaRequest{Ref: "uid:" + uid, Confirm: true})
					if e != nil {
						t.Fatal(e)
					}
					if result.Action != "detach" || result.NewPersona != "" || result.CurrentPersona != "reviewer" || result.NewPaneUID == a.Status.PaneRef {
						t.Fatalf("detach result %+v", result)
					}
				} else {
					c := sdkRelaunchCLI(t, state, config, socket, binary, home)
					if e := c.runPersona([]string{"detach", "uid:" + uid, "--yes", "--output", "json", "--socket-path", socket}, "persona", io.Discard, &stderr); e != nil {
						t.Fatalf("%v %s", e, stderr.String())
					}
				}
				waitSDKResumeBirth(t, state, uid)
				detachedRegistry := projectRegistry(t, state)
				detached, _ := detachedRegistry.Agent(uid)
				d := metadata.PersonaAnnotationsOf(*detached)
				if d.Persona != "" || d.PersonaDigest != "" || d.InstructionsSource != metadata.SettingSourceAttach || d.SystemPromptSnapshot != metadata.SystemPromptSnapshotOff || !detached.Status.SessionRef.SameConversation(old.Status.SessionRef) {
					t.Fatalf("detach state %+v", detached)
				}

				raw, _ := json.Marshal(layers)
				settings = append(settings, string(raw))
			}
			if settings[0] != settings[1] {
				t.Fatalf("launch annotations differ CLI %s SDK %s", settings[0], settings[1])
			}
		})
	}
}

func TestAgentPersonaSDKCLIPlanParity(t *testing.T) {
	relaunchProviderFence(t)
	home := sdkAgentHome(t)
	for _, phase := range []metadata.AgentPhase{metadata.PhaseRunning, metadata.PhaseOffline, metadata.PhaseFailed} {
		for _, action := range []string{"attach", "detach"} {
			t.Run(string(phase)+"/"+action, func(t *testing.T) {
				state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
				t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
				t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
				c, e := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home})
				if e != nil {
					t.Fatal(e)
				}
				root := t.TempDir()
				if _, e = c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); e != nil {
					t.Fatal(e)
				}
				reg := projectRegistry(t, state)
				uid := seedSDKResumeAgent(t, state, reg.Windows[0].Metadata.UID, "claude", root, metadata.PhaseOffline)
				if _, e = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
					a, _ := r.Agent(uid)
					if phase == metadata.PhaseRunning {
						if _, e := store.DefaultMutator().AttachAgentPane(r, uid, metadata.BootstrapPane{Name: "running-pane", CWD: root}, "fixture"); e != nil {
							return e
						}
					} else {
						a.Status.Phase = phase
					}
					a.Status.Interaction = metadata.AgentInteraction{Kind: metadata.InteractionIdle, ObservedAt: time.Now(), Source: string(metadata.InteractionSourceProviderHook)}
					return nil
				}); e != nil {
					t.Fatal(e)
				}
				if _, e = persona.NewDefaultStore(configpkg.Paths{ConfigDir: config, StateDir: state}).Write("reviewer", []byte("Review carefully.\n")); e != nil {
					t.Fatal(e)
				}
				before := projectRegistryBytes(t, state)
				var got core.AgentPersonaResult
				if action == "attach" {
					got, e = c.AttachAgentPersona(context.Background(), core.AttachAgentPersonaRequest{Ref: "uid:" + uid, Persona: "reviewer", DryRun: true})
				} else {
					got, e = c.DetachAgentPersona(context.Background(), core.DetachAgentPersonaRequest{Ref: "uid:" + uid, DryRun: true})
				}
				if e != nil {
					t.Fatal(e)
				}
				args := []string{action, "uid:" + uid}
				if action == "attach" {
					args = append(args, "reviewer")
				}
				args = append(args, "--dry-run", "-o", "json")
				cli := sdkResumeCLI(t, state, config, "", "", home)
				var stdout, stderr bytes.Buffer
				if e = cli.runPersona(args, "persona", &stdout, &stderr); e != nil {
					t.Fatalf("%v %s", e, stderr.String())
				}
				var expected core.AgentPersonaResult
				if e = json.Unmarshal(stdout.Bytes(), &expected); e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("SDK %+v CLI %+v", got, expected)
				}
				if before != projectRegistryBytes(t, state) {
					t.Fatal("dry-run changed Registry")
				}
			})
		}
	}
}
