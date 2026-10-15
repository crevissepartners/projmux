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
	t.Setenv("HOME", t.TempDir())
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
	t.Run("preserved model has no invented value", func(t *testing.T) {
		s := lifecycleSource(aiModeClaude, "tmux")
		s.settings.resolution.PassModel = false
		e := lifecycleEntry(t, preparedAgentSettingsApplication(s), "model")
		if e.Status != "preserved" || e.Value != nil {
			t.Fatal(e)
		}
	})
	t.Run("multiple failures override planned delivery once", func(t *testing.T) {
		s := lifecycleSource(aiModeCodex, "tmux")
		s.settings.resolution.InstructionsNotApplied = true
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
		if *e.Value != "lead" || *e.RequestedValue != "replacement" || e.NextAction == "" {
			t.Fatal(e)
		}
		var b bytes.Buffer
		writeAgentSettingsNotice(&b, a)
		for _, w := range []string{"not applied=5", "instructions", "effort", "agent-guidance", "project-guidance", "link-rules", "cannot replace"} {
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
			if *lifecycleEntry(t, a, "model").Value != "frozen-model" {
				t.Fatal(a)
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

func TestAgentPromptTrailingFlagNoticePreservesInput(t *testing.T) {
	for _, tc := range []struct {
		prompt []string
		want   string
	}{
		{[]string{"keep  spaces", "--effort"}, "--effort"}, {[]string{"--effort=high"}, "--effort=high"}, {[]string{"--unknown"}, ""}, {[]string{"---effort"}, ""}, {[]string{"--effort", "later"}, ""}, {[]string{"--"}, ""}, {nil, ""},
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
		if token == "--unknown" {
			want = ""
		}
		if r.trailingPromptFlag != want {
			t.Fatalf("got %q", r.trailingPromptFlag)
		}
	}
}

func TestExistingProcessForegroundNoticePreservesLifetime(t *testing.T) {
	f := newRelaunchFixture(t)
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
