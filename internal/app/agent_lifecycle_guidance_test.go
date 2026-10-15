package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

func lifecycleEntry(t *testing.T, a *agentSettingsApplication, setting string) agentSettingApplication {
	t.Helper()
	for _, e := range a.Entries {
		if e.Setting == setting {
			return e
		}
	}
	t.Fatalf("missing %s in %+v", setting, a)
	return agentSettingApplication{}
}
func lifecycleSource(provider, host string) agentApplicationSource {
	return agentApplicationSource{provider: provider, host: host, settings: agentSettingsLaunch{layered: true, resolution: agentsettings.Resolution{PassModel: true, New: agentsettings.Settings{Profile: agentsettings.ProfileLayer{Name: "role"}, Model: agentsettings.Setting{Value: "actual-model"}, Effort: agentsettings.Setting{Value: "high"}, Instructions: agentsettings.Setting{Value: "lead"}}}}, guidance: agentGuidanceLaunch{active: true}, links: projectLinksLaunch{active: true, project: projectGuidanceLaunch{active: true}}, workspace: coremetadata.AgentWorkspace{CWD: "/fixture/work"}}
}
func TestAgentSettingsApplicationProjection(t *testing.T) {
	guidanceHome := t.TempDir()
	t.Setenv("HOME", guidanceHome)
	t.Setenv("XDG_CONFIG_HOME", guidanceHome+"/config")
	t.Setenv("XDG_STATE_HOME", guidanceHome+"/state")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	for _, host := range []string{"tmux", "process"} {
		for _, provider := range []string{aiModeClaude, aiModeCodex} {
			for _, verb := range []string{"resume", "relaunch"} {
				t.Run(host+"/"+provider+"/"+verb, func(t *testing.T) {
					s := lifecycleSource(provider, host)
					s.snapshot = host == "process" && verb == "resume"
					a := preparedAgentSettingsApplication(s)
					want := []string{"profile", "model", "effort", "instructions", "agent-guidance", "project-guidance", "link-rules", "workspace"}
					got := []string{}
					for _, e := range a.Entries {
						got = append(got, e.Setting)
					}
					if !slices.Equal(got, want) || a.NotAppliedCount != 0 {
						t.Fatalf("projection %+v", a)
					}
					if e := lifecycleEntry(t, a, "profile"); e.Status != "recorded" || *e.Value != "role" {
						t.Fatal(e)
					}
					if e := lifecycleEntry(t, a, "model"); e.Status != "delivered" || *e.Value != "actual-model" {
						t.Fatal(e)
					}
					if e := lifecycleEntry(t, a, "effort"); e.Status != "delivered" || *e.Value != "high" {
						t.Fatal(e)
					}
					if provider == aiModeCodex {
						if e := lifecycleEntry(t, a, "instructions"); e.Status != "preserved" || e.Reason != "codex-instructions-immutable" {
							t.Fatal(e)
						}
					}
					var out bytes.Buffer
					writeAgentSettingsNotice(&out, a)
					if out.Len() != 0 {
						t.Fatalf("zero nonapplications added %q", out.String())
					}
				})
			}
		}
	}
	t.Run("fixed reply-only and unspecified process values stay unobserved", func(t *testing.T) {
		s := lifecycleSource(aiModeClaude, "process")
		s.settings.resolution.New.Model.Value = ""
		e := lifecycleEntry(t, preparedAgentSettingsApplication(s), "model")
		if e.Status != "unobserved" || e.Value != nil {
			t.Fatal(e)
		}
		s.replyOnly = true
		for _, e := range preparedAgentSettingsApplication(s).Entries {
			if e.Setting != "workspace" && e.Status != "unobserved" {
				t.Fatal(e)
			}
		}
	})
	t.Run("Codex native readiness differs from CLI argv", func(t *testing.T) {
		s := lifecycleSource(aiModeCodex, "tmux")
		for _, ready := range []bool{false, true} {
			s.nativeReady = ready
			a := preparedAgentSettingsApplication(s)
			want := "launch-arguments"
			if ready {
				want = "thread-settings-ready"
			}
			for _, setting := range []string{"model", "effort"} {
				if e := lifecycleEntry(t, a, setting); e.Reason != want {
					t.Fatal(e)
				}
			}
		}
	})
	t.Run("ordinary process preserves recorded guidance snapshots", func(t *testing.T) {
		s := lifecycleSource(aiModeClaude, "process")
		s.snapshot = true
		a := preparedAgentSettingsApplication(s)
		for _, setting := range []string{"agent-guidance", "link-rules"} {
			e := lifecycleEntry(t, a, setting)
			if e.Status != "preserved" || e.Reason != "recorded-snapshot" {
				t.Fatal(e)
			}
		}
		if e := lifecycleEntry(t, a, "project-guidance"); e.Status != "delivered" {
			t.Fatal(e)
		}
	})
	t.Run("legacy explicit model still describes actual arguments", func(t *testing.T) {
		s := lifecycleSource(aiModeClaude, "tmux")
		s.settings = agentSettingsLaunch{}
		s.modelArg = "explicit-model"
		e := lifecycleEntry(t, preparedAgentSettingsApplication(s), "model")
		if e.Status != "delivered" || *e.Value != "explicit-model" {
			t.Fatal(e)
		}
	})
	t.Run("preserved model has no invented value", func(t *testing.T) {
		s := lifecycleSource(aiModeClaude, "tmux")
		s.settings.resolution.PassModel = false
		e := lifecycleEntry(t, preparedAgentSettingsApplication(s), "model")
		if e.Status != "preserved" || e.Value != nil {
			t.Fatal(e)
		}
	})
	t.Run("multiple failures override planned delivery once", func(t *testing.T) {
		s := lifecycleSource(aiModeClaude, "tmux")
		s.settings.instructionsErr = errors.New("private instructions error")
		s.settings.resolution.WantedInstructions = "replacement"
		s.resumed.effortSkipped = true
		s.resumed.effortInvalid = "invalid"
		s.guidance.unavailable = errors.New("secret body")
		s.resumed.agentGuidanceUnavailable = errors.New("duplicate")
		s.links.project.unavailable = errors.New("private path")
		s.links.unavailable = errors.New("secret URL")
		a := preparedAgentSettingsApplication(s)
		if a.NotAppliedCount != 5 {
			t.Fatalf("count %+v", a)
		}
		e := lifecycleEntry(t, a, "instructions")
		if e.Value != nil || e.RequestedValue == nil || *e.RequestedValue != "replacement" || e.NextAction == "" {
			t.Fatal(e)
		}
		var b bytes.Buffer
		writeAgentSettingsNotice(&b, a)
		wantDelivered := "delivered=2 [model (passed in launch arguments), workspace (resolved launch workspace)]"
		if !strings.Contains(b.String(), wantDelivered) || strings.Count(b.String(), "\n") != 1 {
			t.Fatalf("delivered list/one-line summary: %s", &b)
		}
		last := -1
		for _, label := range []string{"model (", "workspace (", "effort (", "instructions (", "agent-guidance (", "project-guidance (", "link-rules ("} {
			at := strings.Index(b.String(), label)
			if at <= last || strings.Count(b.String(), label) != 1 {
				t.Fatalf("unordered/duplicate logical setting %s: %s", label, &b)
			}
			last = at
		}
		for _, value := range []string{"actual-model", "/fixture/work", "replacement", "secret body", "private path", "secret URL"} {
			if strings.Contains(b.String(), value) {
				t.Fatal("new summary disclosed a value or private body")
			}
		}
		for _, w := range []string{"not applied=5", "instructions", "effort", "agent-guidance", "project-guidance", "link-rules", "unavailable"} {
			if !strings.Contains(b.String(), w) {
				t.Fatalf("missing %q: %s", w, &b)
			}
		}
		raw, _ := json.Marshal(a)
		for _, secret := range []string{"secret body", "private path", "secret URL", "duplicate"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("private source leaked")
			}
		}
	})
	t.Run("Codex thread guidance is preserved despite current file errors", func(t *testing.T) {
		source := lifecycleSource(aiModeCodex, "process")
		source.guidance.unavailable = errors.New("private current guidance")
		source.links.project.unavailable = errors.New("private current project")
		source.links.unavailable = errors.New("private current rules")
		source.resumed.agentGuidanceUnavailable = errors.New("private duplicate")
		for _, override := range []bool{false, true} {
			source.settings.resolution.InstructionsNotApplied = override
			source.settings.resolution.WantedInstructions = "replacement"
			a := preparedAgentSettingsApplication(source)
			want := 0
			if override {
				want = 1
			}
			if a.NotAppliedCount != want {
				t.Fatalf("invented changed setting from an unavailable current file: %+v", a)
			}
			for _, name := range []string{"agent-guidance", "project-guidance", "link-rules"} {
				e := lifecycleEntry(t, a, name)
				if e.Status != "preserved" || e.Reason != "codex-instructions-immutable" || e.Value != nil || e.RequestedValue != nil || e.NextAction != "" {
					t.Fatal(e)
				}
			}
		}
	})
	t.Run("immutable requested name is optional without losing explicit none", func(t *testing.T) {
		for _, wanted := range []string{"lead", "replacement", ""} {
			source := lifecycleSource(aiModeCodex, "tmux")
			source.settings.resolution.InstructionsNotApplied = true
			source.settings.resolution.WantedInstructions = wanted
			a := preparedAgentSettingsApplication(source)
			e := lifecycleEntry(t, a, "instructions")
			if a.NotAppliedCount != 1 || e.Status != "not-applied" || e.Reason != "codex-instructions-immutable" || e.Value == nil || *e.Value != "lead" {
				t.Fatal(e)
			}
			if wanted == "lead" {
				if e.RequestedValue != nil {
					t.Fatal("duplicate requested name", e)
				}
			} else if e.RequestedValue == nil || *e.RequestedValue != wanted {
				t.Fatal("lost distinct request or explicit none", e)
			}
		}
		source := lifecycleSource(aiModeClaude, "tmux")
		source.settings.instructionsErr = errors.New("unavailable")
		source.settings.resolution.WantedInstructions = "lead"
		e := lifecycleEntry(t, preparedAgentSettingsApplication(source), "instructions")
		if e.Value != nil || e.RequestedValue == nil || *e.RequestedValue != "lead" {
			t.Fatal("unavailable effective value lost its known request", e)
		}
	})
	t.Run("unspecified frozen provider values stay unobserved after startup", func(t *testing.T) {
		record := &deferredLaunchRecord{NewAnnotations: map[string]string{}}
		record.Command.Dir = "/frozen"
		for _, pending := range []bool{true, false} {
			a := frozenAgentSettingsApplication(record, pending)
			if a.NotAppliedCount != 0 || len(a.Entries) != 8 {
				t.Fatal(a)
			}
			for _, setting := range []string{"model", "effort"} {
				e := lifecycleEntry(t, a, setting)
				if pending {
					if e.Status != "pending" || e.Value == nil || *e.Value != "" {
						t.Fatal("lost frozen recorded empty value", e)
					}
				} else if e.Status != "unobserved" || e.Reason != "application-unobserved" || e.Value != nil {
					t.Fatal("invented provider value", e)
				}
			}
			for _, setting := range []string{"profile", "instructions"} {
				e := lifecycleEntry(t, a, setting)
				if e.Value == nil || *e.Value != "" {
					t.Fatal("lost explicit none", e)
				}
			}
		}
	})
	t.Run("unavailable instructions beat delivery", func(t *testing.T) {
		s := lifecycleSource(aiModeClaude, "tmux")
		s.settings.instructionsErr = errors.New("unavailable")
		s.settings.resolution.WantedInstructions = "missing"
		a := preparedAgentSettingsApplication(s)
		if a.NotAppliedCount != 1 || lifecycleEntry(t, a, "instructions").Reason != "instructions-unavailable" {
			t.Fatal(a)
		}
	})
	t.Run("explicit none and frozen first input", func(t *testing.T) {
		r := &deferredLaunchRecord{Model: "frozen-model", Effort: "medium", NewAnnotations: map[string]string{}, NewSpec: coremetadata.AgentSpec{Workspace: coremetadata.AgentWorkspace{CWD: "/frozen"}}}
		r.Command.Dir = "/frozen"
		for _, pending := range []bool{true, false} {
			a := frozenAgentSettingsApplication(r, pending)
			if a.NotAppliedCount != 0 {
				t.Fatal(a)
			}
			e := lifecycleEntry(t, a, "instructions")
			if e.Value == nil || *e.Value != "" {
				t.Fatal("explicit none lost", e)
			}
			for _, e := range a.Entries {
				if pending && e.Setting != "profile" && (e.Status != "pending" || e.Reason != "first-input-required") {
					t.Fatal(e)
				}
			}
			for _, setting := range []string{"model", "effort"} {
				e := lifecycleEntry(t, a, setting)
				if pending {
					want := "frozen-model"
					if setting == "effort" {
						want = "medium"
					}
					if e.Value == nil || *e.Value != want {
						t.Fatal("pending recorded value lost", e)
					}
				} else if e.Status != "unobserved" || e.Reason != "application-unobserved" || e.Value != nil {
					t.Fatal("outer snapshot became actual provider evidence", e)
				}
			}
		}
	})
}

type lifecycleChangingLauncher struct {
	*exactArgvResumeLauncher
	calls int
}

func (l *lifecycleChangingLauncher) ResolveAgentSettingsRequest(p string, a map[string]string, q agentSettingsRequest) (agentSettingsLaunch, error) {
	v, err := l.planner.ResolveAgentSettingsRequest(p, a, q)
	l.calls++
	if err == nil {
		v.resolution.New.Model.Value = "preflight-only"
		if l.calls > 1 {
			v.resolution.New.Model.Value = "actually-prepared"
		}
	}
	return v, err
}
func TestRelaunchApplicationUsesPreparedSettings(t *testing.T) {
	guidanceHome := t.TempDir()
	t.Setenv("HOME", guidanceHome)
	t.Setenv("XDG_CONFIG_HOME", guidanceHome+"/config")
	t.Setenv("XDG_STATE_HOME", guidanceHome+"/state")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	f := newRelaunchLayersFixture(t)
	f.onRoleWithOverrides(t)
	launcher := &lifecycleChangingLauncher{exactArgvResumeLauncher: f.launcher}
	f.command.rebind.launcher = launcher
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "requested", "-o", "json")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	var result agentRelaunchResult
	if err = json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err, stdout)
	}
	if launcher.calls != 2 || result.NewSettings.Model.Value != "preflight-only" || *lifecycleEntry(t, result.SettingsApplication, "model").Value != "actually-prepared" {
		t.Fatalf("calls=%d result=%+v", launcher.calls, result)
	}
	if !slices.Contains(f.lastArgvTail(t), "actually-prepared") {
		t.Fatal(f.lastArgvTail(t))
	}
	if f.command.rebind.settingsApplication != nil {
		t.Fatal("invocation receiver escaped command copy")
	}
	// A failed preparation never publishes its candidate values.
	failed := newRelaunchFixture(t)
	stopFixtureAgent(t, failed)
	failed.command.rebind.launcher = &failingRelaunchResumeLauncher{failed.launcher}
	received := 0
	failed.command.rebind.settingsApplication = func(*agentSettingsApplication) { received++ }
	_, _, err = runRoute(t, failed.command, "resume", "uid:"+personaAttachAgent)
	if err == nil || received != 0 {
		t.Fatalf("failure=%v disclosures=%d", err, received)
	}
}

func TestProcessRelaunchApplicationRetainsActualPreparedSource(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := newRelaunchFixture(t)
			f.command.ai = f.planner
			f.command.rebind.create.homeDir = f.planner.homeDir
			f.command.rebind.create.lookupEnv = f.planner.lookupEnv
			paths, err := configPaths(f.planner.homeDir, f.planner.lookupEnv)
			if err != nil {
				t.Fatal(err)
			}
			profiles := profile.NewDefaultStore(paths)
			digest := writeCodexProfile(t, profiles, "changing", "model = \"preview-model\"\neffort = \"low\"\n")
			annotations := map[string]string{coremetadata.AnnotationAgentProfile: "changing", coremetadata.AnnotationAgentProfileDigest: digest, coremetadata.AnnotationAgentProfileSource: coremetadata.SettingSourceRelaunch}
			outer, err := f.planner.ResolveAgentSettingsRequest(provider, annotations, agentSettingsRequest{projectUID: "prj-alpha", source: "relaunch"})
			if err != nil {
				t.Fatal(err)
			}
			actualDir := t.TempDir()
			f.command.resolveWorkspace = func(_ string, _ coremetadata.Registry, _ coremetadata.Project, _ string, _ string, _ []string) (coremetadata.AgentWorkspace, error) {
				return coremetadata.AgentWorkspace{CWD: actualDir}, nil
			}
			recipe := processRelaunchRecipe{restart: &agentRestart{provider: provider, settings: outer}, annotations: outer.launchAnnotations(annotations), workspace: coremetadata.AgentWorkspace{CWD: "/preview-only"}}
			writeCodexProfile(t, profiles, "changing", "model = \"actual-model\"\neffort = \"high\"\n")
			existingPane, _ := f.store.registry.Pane(personaAttachPane)
			pane := existingPane.Clone()
			pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: provider, Binding: coremetadata.ProcessBinding{ProjectUID: "prj-alpha"}}
			launch, err := f.command.planProcessRelaunchLaunch(f.agent(t), pane, agentRelaunchRequest{}, recipe)
			if err != nil {
				t.Fatal(err)
			}
			if launch.settings.resolution.New.Model.Value != "preview-model" || launch.applicationSettings.resolution.New.Model.Value != "actual-model" || launch.applicationSettings.resolution.New.Effort.Value != "high" || launch.command.Dir != actualDir || recipe.workspace.CWD != "/preview-only" {
				t.Fatalf("runtime preview or disclosure source lost: %+v", launch)
			}
			if provider == aiModeClaude {
				if argvValue(launch.command.Args, "--model") != "actual-model" || argvValue(launch.command.Args, "--effort") != "high" {
					t.Fatal(launch.command.Args)
				}
			} else if launch.config.Settings.Model != "actual-model" || launch.config.Settings.Effort != "high" {
				t.Fatal(launch.config.Settings)
			}
			if f.store.writes != 0 || f.store.transactions != 0 || len(splitWindowCalls(f.tmux)) != 0 || len(f.deletes.killed) != 0 {
				t.Fatal("preparation changed runtime state")
			}
		})
	}
}

func TestAgentPromptTrailingFlagNoticePreservesInput(t *testing.T) {
	guidanceHome := t.TempDir()
	t.Setenv("HOME", guidanceHome)
	t.Setenv("XDG_CONFIG_HOME", guidanceHome+"/config")
	t.Setenv("XDG_STATE_HOME", guidanceHome+"/state")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	for _, tc := range []struct {
		prompt []string
		want   string
	}{
		{[]string{"keep  spaces", "--effort"}, "--effort"}, {[]string{"--effort=high"}, "--effort"}, {[]string{"--unknown"}, ""}, {[]string{"---effort"}, ""}, {[]string{"--effort", "later"}, ""}, {[]string{"--"}, ""}, {nil, ""},
	} {
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		fs.String("effort", "", "")
		before := slices.Clone(tc.prompt)
		got := trailingAgentPromptFlag(fs, tc.prompt)
		if got != tc.want || !reflect.DeepEqual(tc.prompt, before) {
			t.Fatalf("%q -> %q", tc.prompt, got)
		}
	}
	for _, token := range []string{"--effort", "--effort=high", "--unknown"} {
		args := []string{"uid:fixture", "--", " exact  input ", token}
		before := slices.Clone(args)
		r, err := parseAgentRelaunchArgs(args, io.Discard)
		if err != nil || !slices.Equal(args, before) || !slices.Equal(r.prompt, args[2:]) {
			t.Fatalf("%+v %v", r, err)
		}
		want := token
		if token == "--effort=high" {
			want = "--effort"
		}
		if token == "--unknown" {
			want = ""
		}
		if r.trailingPromptFlag != want {
			t.Fatalf("got %q", r.trailingPromptFlag)
		}
	}
	const canary = "fixture-private-canary"
	prompt := []string{"keep  exact spacing", "--effort=" + canary}
	before := slices.Clone(prompt)
	r, err := parseAgentRelaunchArgs(append([]string{"uid:fixture", "-o", "json", "--"}, prompt...), io.Discard)
	if err != nil || !slices.Equal(r.prompt, before) || r.trailingPromptFlag != "--effort" {
		t.Fatal("option classification changed input", err)
	}
	var warnings, wire bytes.Buffer
	writeAgentLifecyclePromptNotice(&warnings, r.trailingPromptFlag)
	if !strings.Contains(warnings.String(), "--effort") || strings.Contains(warnings.String(), canary) {
		t.Fatal("warning disclosed prompt value")
	}
	if err = writeAgentRelaunchResult(&wire, r, agentRelaunchResult{}); err != nil || !json.Valid(wire.Bytes()) || bytes.Contains(wire.Bytes(), []byte(canary)) {
		t.Fatal("result disclosed prompt value", err)
	}

}

func TestExistingProcessForegroundNoticePreservesLifetime(t *testing.T) {
	guidanceHome := t.TempDir()
	t.Setenv("HOME", guidanceHome)
	t.Setenv("XDG_CONFIG_HOME", guidanceHome+"/config")
	t.Setenv("XDG_STATE_HOME", guidanceHome+"/state")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	f := newRelaunchFixture(t)
	fixtureHome := t.TempDir()
	f.command.rebind.create.homeDir = func() (string, error) { return fixtureHome, nil }
	f.command.rebind.create.lookupEnv = func(string) string { return "" }
	paths, err := savedSettingsPaths(f.command.rebind.create.homeDir, f.command.rebind.create.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.SaveAgentDefaultHostFile(paths.AgentDefaultHostFile(), config.AgentHostProcess); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--detach=false"}, {"--detach=true"}, {"--host=process"}, {"--host=tmux"}} {
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		fs.Bool("detach", false, "")
		fs.String("host", "", "")
		if err = fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		n := f.command.lifecycleNotices(fs, nil)
		if n.foreground != (len(args) == 0) {
			t.Fatalf("%v -> %+v", args, n)
		}
		var b bytes.Buffer
		n.writeForMode(&b, cli.OutputModeNone)
		if b.Len() != 0 {
			t.Fatal(b.String())
		}
	}
	if f.command.defaultProcessForegroundNotice(false) {
		t.Fatal("explicit choice got default hint")
	}
}

type lifecycleBrokenWriter struct{}

func (lifecycleBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("display failed") }
func TestAgentLifecycleGuidanceOutputModes(t *testing.T) {
	guidanceHome := t.TempDir()
	t.Setenv("HOME", guidanceHome)
	t.Setenv("XDG_CONFIG_HOME", guidanceHome+"/config")
	t.Setenv("XDG_STATE_HOME", guidanceHome+"/state")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	f := newRelaunchFixture(t)
	a := preparedAgentSettingsApplication(lifecycleSource(aiModeCodex, "process"))
	a.Entries[3].Status = "not-applied"
	a.Entries[3].NextAction = lifecycleText("next.codex-instructions-immutable")
	a.NotAppliedCount = 1
	r := processAgentResumeResult{settingsApplication: a}
	r.Binding.Agent = personaAttachAgent
	r.Binding.Pane = personaAttachPane
	r.owner.Created = createResult{kind: coremetadata.KindAgent, uid: personaAttachAgent, name: "codex"}
	for _, mode := range []cli.OutputMode{cli.OutputModeJSON, cli.OutputModeReceipt, cli.OutputModeUID, cli.OutputModeName, cli.OutputModeMetadata, cli.OutputModeNone} {
		t.Run(string(mode), func(t *testing.T) {
			var stdout, stderr, base bytes.Buffer
			if err := r.writeResumeResult(f.command.rebind.create, &stdout, &stderr, mode); err != nil {
				t.Fatal(err)
			}
			receipt := processResumeReceipt(r.Binding, r.owner.Created.name)
			if err := f.command.rebind.create.writeResultsWithReceipt(&base, "agent resume", mode, coremetadata.KindAgent, []createResult{r.owner.Created}, receipt); err != nil {
				t.Fatal(err)
			}
			if mode == cli.OutputModeJSON {
				var got, old map[string]json.RawMessage
				if json.Unmarshal(stdout.Bytes(), &got) != nil || json.Unmarshal(base.Bytes(), &old) != nil {
					t.Fatal(stdout.String())
				}
				if !bytes.Equal(got["items"], old["items"]) {
					t.Fatalf("Items changed: %s / %s", got["items"], old["items"])
				}
				if _, ok := got["settingsApplication"]; !ok || len(got) != len(old)+1 {
					t.Fatal(stdout.String())
				}
			} else if stdout.String() != base.String() {
				t.Fatalf("format changed: %s / %s", &stdout, &base)
			}
			if mode == cli.OutputModeNone && stderr.Len() != 0 {
				t.Fatal(stderr.String())
			}
		})
	}
	// New warnings are best effort; existing success stdout errors still propagate.
	writeAgentSettingsNotice(lifecycleBrokenWriter{}, a)
	writeAgentLifecyclePromptNotice(lifecycleBrokenWriter{}, "--effort")
	if err := r.writeResumeResult(f.command.rebind.create, lifecycleBrokenWriter{}, io.Discard, cli.OutputModeJSON); err == nil {
		t.Fatal("lost existing stdout error")
	}
	for _, mode := range []string{"none", "receipt", "uid"} {
		if _, err := parseAgentRelaunchArgs([]string{"uid:x", "-o", mode}, io.Discard); err == nil {
			t.Fatal("relaunch accepted", mode)
		}
	}
	result := agentRelaunchResult{SettingsApplication: a}
	var b bytes.Buffer
	if err := writeAgentRelaunchResult(&b, agentRelaunchRequest{json: true}, result); err != nil || !json.Valid(b.Bytes()) {
		t.Fatalf("JSON polluted: %v %s", err, &b)
	}
}

func TestAgentLifecyclePendingDefaultLine(t *testing.T) {
	guidanceHome := t.TempDir()
	t.Setenv("HOME", guidanceHome)
	t.Setenv("XDG_CONFIG_HOME", guidanceHome+"/config")
	t.Setenv("XDG_STATE_HOME", guidanceHome+"/state")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	record := &deferredLaunchRecord{Model: "frozen-model", Effort: "medium"}
	record.Command.Dir = "/fixture/work"
	for _, pending := range []bool{true, false} {
		a := frozenAgentSettingsApplication(record, pending)
		result := agentRelaunchResult{Outcome: personaOutcomeResumed, AgentName: "fixture", SettingsApplication: a}
		var out bytes.Buffer
		if err := writeAgentRelaunchResult(&out, agentRelaunchRequest{}, result); err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.String(), "\n") != 1 || strings.Contains(out.String(), "waiting for first input") != pending {
			t.Fatalf("pending=%v: %s", pending, &out)
		}
		out.Reset()
		if err := writeAgentRelaunchResult(&out, agentRelaunchRequest{json: true}, result); err != nil || !json.Valid(out.Bytes()) || strings.Contains(out.String(), "waiting for first input") {
			t.Fatalf("wire changed: %v %s", err, &out)
		}
		var notices bytes.Buffer
		writeAgentSettingsNotice(&notices, a)
		if notices.Len() != 0 {
			t.Fatal("zero nonapplications added a notice", notices.String())
		}
		if err := writeAgentRelaunchResult(lifecycleBrokenWriter{}, agentRelaunchRequest{}, result); err == nil {
			t.Fatal("lost stdout failure")
		}
	}
	var notice bytes.Buffer
	writeAgentFirstInputNotice(&notice, false)
	if notice.Len() != 0 {
		t.Fatal("nondefault claim gained lifetime text")
	}
	writeAgentFirstInputNotice(&notice, true)
	if notice.String() != "Waiting for first input; the provider has not started.\n" {
		t.Fatal(notice.String())
	}
	writeAgentFirstInputNotice(lifecycleBrokenWriter{}, true)
}
