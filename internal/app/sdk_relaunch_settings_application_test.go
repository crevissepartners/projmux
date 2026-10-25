package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The recorded model and effort are the Agent annotations the CLI reports as
// currentModel and currentEffort; a dry run reports no settings application.
func TestRelaunchAgentResultCarriesRecordedModelAndEffort(t *testing.T) {
	relaunchProviderFence(t)
	home, root := sdkAgentHome(t), t.TempDir()
	state, config := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
	t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	reg := projectRegistry(t, state)
	uid := seedSDKResumeAgent(t, state, reg.Windows[0].Metadata.UID, "claude", root, metadata.PhaseOffline)
	_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
		m := store.DefaultMutator()
		if _, err := m.SetAgentModel(r, uid, "haiku", metadata.SettingSourceRelaunch); err != nil {
			return err
		}
		_, err := m.SetAgentEffort(r, uid, "max", metadata.SettingSourceRelaunch)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + uid, Model: "sonnet", Effort: "low", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	c := sdkResumeCLI(t, state, config, "", "", home)
	reg = projectRegistry(t, state)
	a, _ := reg.Agent(uid)
	var output, stderr bytes.Buffer
	if err := c.runTmuxRelaunch(reg, *a, agentRelaunchRequest{model: "sonnet", effort: "low", dryRun: true, json: true}, &output, &stderr); err != nil {
		t.Fatal(err)
	}
	var cli agentRelaunchResult
	if err := json.Unmarshal(output.Bytes(), &cli); err != nil {
		t.Fatal(err)
	}
	if got.CurrentModel != "haiku" || got.CurrentEffort != "max" || got.CurrentModel != cli.CurrentModel || got.CurrentEffort != cli.CurrentEffort {
		t.Fatalf("recorded model/effort SDK %q/%q CLI %q/%q", got.CurrentModel, got.CurrentEffort, cli.CurrentModel, cli.CurrentEffort)
	}
	if got.SettingsApplication != nil || cli.SettingsApplication != nil {
		t.Fatalf("dry run reported a settings application SDK %+v CLI %+v", got.SettingsApplication, cli.SettingsApplication)
	}
}

// The same fixture relaunched by the CLI and by the SDK reports the same
// settingsApplication, compared exactly after decoding the CLI JSON.
func TestRelaunchAgentSettingsApplicationMatchesCLI(t *testing.T) {
	relaunchProviderFence(t)
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	root := t.TempDir()
	for _, phase := range []metadata.AgentPhase{metadata.PhaseOffline, metadata.PhaseRunning} {
		t.Run(string(phase), func(t *testing.T) {
			var applications [2]*core.AgentSettingsApplication
			var recorded [2][2]string
			for i, sdk := range []bool{false, true} {
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
				_, err = store.NewStore(store.PathFor(state)).Update(func(r *metadata.Registry) error {
					m := store.DefaultMutator()
					if _, err := m.SetAgentModel(r, uid, "haiku", metadata.SettingSourceRelaunch); err != nil {
						return err
					}
					_, err := m.SetAgentEffort(r, uid, "max", metadata.SettingSourceRelaunch)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				if phase == metadata.PhaseRunning {
					if _, err = client.ResumeAgent(context.Background(), core.ResumeAgentRequest{Ref: "uid:" + uid}); err != nil {
						t.Fatal(err)
					}
					waitSDKResumeBirth(t, state, uid)
				}
				reg := projectRegistry(t, state)
				old, _ := reg.Agent(uid)
				if sdk {
					result, err := client.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + uid, Model: "sonnet", Effort: "high", Confirm: true})
					if err != nil {
						t.Fatal(err)
					}
					applications[i] = result.SettingsApplication
					recorded[i] = [2]string{result.CurrentModel, result.CurrentEffort}
				} else {
					c := sdkRelaunchCLI(t, state, config, socket, binary, home)
					var output, stderr bytes.Buffer
					if err := c.runTmuxRelaunch(reg, *old, agentRelaunchRequest{model: "sonnet", effort: "high", yes: true, json: true, socket: deleteSocketFlags{socketPath: socket}}, &output, &stderr); err != nil {
						t.Fatalf("%v %s", err, stderr.String())
					}
					var cli struct {
						CurrentModel, CurrentEffort string
						SettingsApplication         json.RawMessage `json:"settingsApplication"`
					}
					if err := json.Unmarshal(output.Bytes(), &cli); err != nil {
						t.Fatal(err)
					}
					if len(cli.SettingsApplication) == 0 {
						t.Fatalf("CLI reported no settingsApplication: %s", output.String())
					}
					applications[i] = &core.AgentSettingsApplication{}
					if err := json.Unmarshal(cli.SettingsApplication, applications[i]); err != nil {
						t.Fatal(err)
					}
					recorded[i] = [2]string{cli.CurrentModel, cli.CurrentEffort}
				}
				waitSDKResumeBirth(t, state, uid)
			}
			if applications[1] == nil || len(applications[1].Entries) == 0 {
				t.Fatalf("SDK reported no settings application: %+v", applications[1])
			}
			if !reflect.DeepEqual(applications[0], applications[1]) {
				cli, _ := json.Marshal(applications[0])
				sdk, _ := json.Marshal(applications[1])
				t.Fatalf("settings application differs\nCLI %s\nSDK %s", cli, sdk)
			}
			if recorded[0] != recorded[1] || recorded[1] != [2]string{"haiku", "max"} {
				t.Fatalf("recorded model/effort CLI %v SDK %v", recorded[0], recorded[1])
			}
		})
	}
}

// A process relaunch decodes the CLI's relaunch JSON into the public result, so
// the new fields are filled by those JSON keys without a second computation.
func TestRelaunchProcessAgentDecodesRecordedSettings(t *testing.T) {
	application := preparedAgentSettingsApplication(agentApplicationSource{provider: aiModeClaude, host: "process", settings: agentSettingsLaunch{layered: true, resolution: agentsettings.Resolution{New: agentsettings.Settings{Model: agentsettings.Setting{Value: "sonnet"}, Instructions: agentsettings.Setting{Value: "lead"}}, InstructionsUnavailable: "lead", WantedInstructions: "lead"}}, workspace: metadata.AgentWorkspace{CWD: "/fixture/work"}})
	if application.NotAppliedCount == 0 {
		t.Fatalf("fixture must carry a not-applied entry: %+v", application)
	}
	raw, err := json.Marshal(agentRelaunchResult{Action: "relaunch", AgentUID: "agent-fixture", CurrentModel: "haiku", CurrentEffort: "max", NewModel: "sonnet", NewEffort: "high", SettingsApplication: application})
	if err != nil {
		t.Fatal(err)
	}
	var got core.RelaunchAgentResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var want core.AgentSettingsApplication
	inner, _ := json.Marshal(application)
	if err := json.Unmarshal(inner, &want); err != nil {
		t.Fatal(err)
	}
	if got.CurrentModel != "haiku" || got.CurrentEffort != "max" || got.SettingsApplication == nil || !reflect.DeepEqual(*got.SettingsApplication, want) {
		t.Fatalf("decoded %+v from %s", got, raw)
	}
	if want.NotAppliedCount != application.NotAppliedCount || len(want.Entries) != len(application.Entries) {
		t.Fatalf("decoded application %+v want %+v", want, application)
	}
	for i, e := range application.Entries {
		d := want.Entries[i]
		if d.Setting != e.Setting || d.Status != e.Status || d.Reason != e.Reason || d.NextAction != e.NextAction || !reflect.DeepEqual(d.Value, e.Value) || !reflect.DeepEqual(d.RequestedValue, e.RequestedValue) {
			t.Fatalf("entry %d decoded %+v want %+v", i, d, e)
		}
	}
	var zero core.RelaunchAgentResult
	if err := json.Unmarshal([]byte(`{"action":"relaunch","outcome":"would-resume"}`), &zero); err != nil || zero.SettingsApplication != nil || zero.CurrentModel != "" {
		t.Fatalf("absent keys decoded %+v %v", zero, err)
	}
}
