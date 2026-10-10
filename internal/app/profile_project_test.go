package app

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const profileProjectP = "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"
const profileProjectQ = "proj-bbbbbbbbbbbbbbbbbbbbbbbbba"

func profileProjectRegistry(t *testing.T, registry coremetadata.Registry) coremetadata.Registry {
	t.Helper()
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "prj-alpha", profileProjectP), "prj-beta", profileProjectQ))
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestProjectProfileCreateScopeAndRoleMapping(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		for _, target := range []string{"alpha", "beta"} {
			t.Run(target+map[bool]string{true: "/flag", false: "/role"}[explicit], func(t *testing.T) {
				f := newProfileFixture(t)
				f.store.registry = profileProjectRegistry(t, f.store.registry)
				f.writeProfile(t, "scoped", "project = \""+profileProjectP+"\"\nprovider = \"claude\"\nroles = [\"review\"]")
				args := []string{"agent", "--provider", "claude", "--project", target, "--window", "main", "--name", "scoped-agent", "-o", "receipt"}
				if explicit {
					args = append(args, "--profile", "scoped")
				} else {
					args = append(args, "--label", "role=review")
				}
				args = append(args, "--", "task")
				before := f.store.snapshot()
				panes := len(f.store.registry.Panes)
				stdout, stderr, err := runRoute(t, f.create, args...)
				if explicit && target == "beta" {
					if err == nil || exitCodeOf(err) != 2 || !strings.Contains(err.Error(), profile.ReasonOutOfScope) || !strings.Contains(err.Error(), "scoped") || !strings.Contains(err.Error(), profileProjectP) {
						t.Fatalf("err=%v", err)
					}
					if stdout != "" || before != f.store.snapshot() || f.store.writes != 0 || len(f.store.registry.Panes) != panes || len(f.launcher.argv) != 0 {
						t.Fatal("refusal created resources")
					}
					return
				}
				if err != nil {
					t.Fatalf("stdout=%s stderr=%s err=%v", stdout, stderr, err)
				}
				var receipt struct {
					Profile *cli.ReceiptProfile `json:"profile"`
				}
				if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
					t.Fatal(err)
				}
				if target == "alpha" {
					if receipt.Profile == nil || receipt.Profile.Name != "scoped" {
						t.Fatalf("receipt=%s", stdout)
					}
				} else if receipt.Profile != nil {
					t.Fatalf("foreign role applied profile: %s", stdout)
				}
			})
		}
	}
}

type projectProfileProcessLauncher struct{ *exactArgvAgentLauncher }

func (l projectProfileProcessLauncher) PlanProcessClaudeCommand(workspace coremetadata.AgentWorkspace, options processClaudeLaunchOptions) (processhost.Command, error) {
	return processhost.Command{Path: "/fixture/claude", Dir: workspace.CWD, Args: claudeLaunchOptionArgs(options.Model, options.Effort, options.InstructionsFile)}, nil
}

func TestProjectProfileProcessAndUIPlansRefuseBeforeAllocation(t *testing.T) {
	f := newProfileFixture(t)
	f.store.registry = profileProjectRegistry(t, f.store.registry)
	f.writeProfile(t, "scoped", "project = \""+profileProjectP+"\"\nprovider = \"claude\"")
	f.create.agents = projectProfileProcessLauncher{f.launcher}
	f.create.store.stateDir = func() (string, error) { return f.stateDir, nil }
	for _, target := range []string{"alpha", "beta"} {
		before := f.store.snapshot()
		plan, err := f.create.planProcessAgent(processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, Name: target}, Window: selector.Ref{Kind: coremetadata.KindWindow, Name: "main"}, Profile: "scoped"})
		if target == "alpha" {
			if err != nil || plan.flags.profileLaunch.name != "scoped" {
				t.Fatalf("plan=%+v err=%v", plan.flags, err)
			}
		} else if err == nil || exitCodeOf(err) != 2 || !strings.Contains(err.Error(), profile.ReasonOutOfScope) {
			t.Fatalf("err=%v", err)
		}
		if before != f.store.snapshot() || f.store.transactions != 0 {
			t.Fatal("process plan allocated resources")
		}
		uid := profileProjectP
		if target == "beta" {
			uid = profileProjectQ
		}
		intent, err := f.create.prepareIntentAgent(aiModeClaude, resourceCreateFlags{profile: "scoped", profileProjectUID: uid})
		if target == "alpha" {
			if err != nil || intent.flags.profileLaunch.name != "scoped" {
				t.Fatalf("intent=%+v err=%v", intent, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), profile.ReasonOutOfScope) {
			t.Fatalf("UI err=%v", err)
		}
		if before != f.store.snapshot() || f.store.transactions != 0 {
			t.Fatal("UI plan allocated resources")
		}
	}
	// The CLI process route also returns exit 2 before creating an Agent/Pane.
	before := f.store.snapshot()
	_, _, err := runRoute(t, f.create, "agent", "--host", "process", "--project", "beta", "--window", "main", "--profile", "scoped")
	if err == nil || exitCodeOf(err) != 2 || !strings.Contains(err.Error(), profile.ReasonOutOfScope) || before != f.store.snapshot() {
		t.Fatalf("process CLI err=%v", err)
	}
}

func TestProjectProfileResumeAndRelaunchRefuseWithoutChangingAgent(t *testing.T) {
	for _, action := range []string{"resume", "relaunch"} {
		t.Run(action, func(t *testing.T) {
			f := newRelaunchFixture(t)
			f.store.registry = profileProjectRegistry(t, f.store.registry)
			paths, err := configPaths(f.planner.homeDir, f.planner.lookupEnv)
			if err != nil {
				t.Fatal(err)
			}
			store := profile.NewDefaultStore(paths)
			entry, err := store.Write("scoped", []byte("project = \""+profileProjectP+"\"\n"))
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile] = "scoped"
			agent.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] = entry.Digest
			if action == "resume" {
				agent.Status.Phase = coremetadata.PhaseOffline
				agent.Status.PaneRef = ""
			}
			// A hand edit bypasses Store.Write and moves the recorded profile out of scope.
			path, _ := store.Path("scoped")
			if err := os.WriteFile(path, []byte("project = \""+profileProjectQ+"\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before := f.store.snapshot()
			annotations := maps.Clone(agent.Metadata.Annotations)
			stdout, _, err := runRoute(t, f.command, action, "uid:"+personaAttachAgent)
			if err == nil || !strings.Contains(err.Error(), profile.ReasonOutOfScope) || !strings.Contains(err.Error(), "scoped") || !strings.Contains(err.Error(), profileProjectQ) {
				t.Fatalf("err=%v", err)
			}
			after, _ := f.store.registry.Agent(personaAttachAgent)
			if stdout != "" || before != f.store.snapshot() || !maps.Equal(annotations, after.Metadata.Annotations) || len(f.deletes.killed) != 0 {
				t.Fatal("scope refusal changed Agent")
			}
		})
	}
}

func TestProjectProfileCodexRecordedAndSwitchScope(t *testing.T) {
	f := newProfileFixture(t)
	f.writeProfile(t, "scoped", "project = \""+profileProjectP+"\"\nprovider = \"codex\"")
	annotations := map[string]string{coremetadata.AnnotationAgentProfile: "scoped"}
	if _, _, err := f.create.codexResumeProfile(annotations, profileProjectP); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.create.codexResumeProfile(annotations, profileProjectQ); profile.ReasonOf(err) != profile.ReasonOutOfScope {
		t.Fatalf("err=%v", err)
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		_, err := resolveAgentSettings(f.planner.homeDir, f.planner.lookupEnv, provider, annotations, agentSettingsRequest{projectUID: profileProjectQ})
		if profile.ReasonOf(err) != profile.ReasonOutOfScope {
			t.Fatalf("provider=%s err=%v", provider, err)
		}
	}
	if _, _, err := resolveSwitchProfile(f.planner.homeDir, f.planner.lookupEnv, aiModeCodex, "scoped", profileProjectQ); profile.ReasonOf(err) != profile.ReasonOutOfScope {
		t.Fatalf("switch err=%v", err)
	}
}

func TestProfileListScopesAndProjectReferenceFilter(t *testing.T) {
	cmd, _ := newProfileTestCommand(t, "")
	store, err := cmd.store()
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"global": "", "p": "project = \"" + profileProjectP + "\"", "q": "project = \"" + profileProjectQ + "\""} {
		if _, err := store.Write(name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := configPaths(cmd.homeDir, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	registry := profileProjectRegistry(t, resourceFixtureRegistry(t))
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	path := intmetadata.PathFor(paths.StateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runProfile(cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	rows := profileListRows(t, stdout)
	if len(rows) != 3 || rows["global"][9] != "global" || rows["p"][9] != profileProjectP || rows["q"][9] != profileProjectQ {
		t.Fatalf("stdout=%s", stdout)
	}
	for _, ref := range []string{"alpha", "uid:" + profileProjectP} {
		stdout, _, err := runProfile(cmd, "list", "--project", ref)
		if err != nil {
			t.Fatal(err)
		}
		rows := profileListRows(t, stdout)
		if len(rows) != 2 || rows["q"] != nil || rows["p"] == nil || rows["global"] == nil {
			t.Fatalf("stdout=%s", stdout)
		}
	}
	if alias, _, err := runProfile(cmd, "list", "-p", "alpha"); err != nil || len(profileListRows(t, alias)) != 2 {
		t.Fatalf("alias=%s err=%v", alias, err)
	}
	if _, _, err := runProfile(cmd, "list", "--project", "missing"); err == nil {
		t.Fatal("missing Project accepted")
	}
}

func TestProjectProfileProcessReservationKeepsProfileAnnotation(t *testing.T) {
	f := newProfileFixture(t)
	f.store.registry = profileProjectRegistry(t, f.store.registry)
	f.create.store.stateDir = func() (string, error) { return f.stateDir, nil }
	f.create.agents = projectProfileProcessLauncher{f.launcher}
	f.writeProfile(t, "scoped", "project = \""+profileProjectP+"\"\nprovider = \"claude\"\n")
	opts := processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, Name: "alpha"}, Window: selector.Ref{Kind: coremetadata.KindWindow, Name: "main"}, Profile: "scoped", Name: "process-profile"}
	plan, err := f.create.planProcessAgent(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.create.reserveProcessAgent(context.Background(), plan, opts, "op-profile", "gen-profile")
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := f.store.registry.Agent(result.Binding.Agent)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != "scoped" {
		t.Fatal("profile annotation missing")
	}
}
