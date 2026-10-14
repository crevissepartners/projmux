package app

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

func digestCLIProfile(t *testing.T, model string) string {
	t.Helper()
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return writeCodexProfile(t, profile.NewDefaultStore(paths), "same", "model = \""+model+"\"\neffort = \"low\"\n")
}

func digestCLIAnnotations(t *testing.T, f processCreateCLI, ref string) map[string]string {
	t.Helper()
	reg := mustRegistry(t, f)
	a, ok := reg.Agent(strings.TrimPrefix(ref, "uid:"))
	if !ok {
		t.Fatal("agent missing")
	}
	return maps.Clone(a.Metadata.Annotations)
}

func TestSameNameProfileDigestImmediateActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, route := range []string{"relaunch", "resume"} {
			t.Run(provider+"/"+route, func(t *testing.T) {
				var f processCreateCLI
				if provider == aiModeClaude {
					f = deferredRelaunchFixture(t)
				} else {
					f = processCodexRelaunchFixture(t)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer cancel()
				oldDigest := digestCLIProfile(t, "stub-model")
				first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "same", "--", "initial"))
				old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool { return processRelaunchConversation(r) != "" && r.TurnID == "" })
				if provider == aiModeClaude {
					deferredReady(t, ctx, f, first.ref)
				}
				first.shutdown(t)
				next := digestCLIProfile(t, "new-model")
				if digestCLIAnnotations(t, f, first.ref)[m.AnnotationAgentProfileDigest] != oldDigest {
					t.Fatal("fixture initial digest")
				}
				var second *processResumeCLIInvocation
				if route == "relaunch" {
					reg := mustRegistry(t, f)
					out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--dry-run", "-o", "json").CombinedOutput()
					if err != nil || !strings.Contains(string(out), "profile-changed") || !reflect.DeepEqual(reg, mustRegistry(t, f)) {
						t.Fatalf("dry-run: %v %s", err, out)
					}
					var result agentRelaunchResult
					second, result = startProcessRelaunchCLI(t, ctx, f, first.ref, "--", "second")
					if result.AgentUID != old.Binding.AgentUID || result.NewPaneUID != old.Binding.PaneUID || !slices.Contains(result.RelaunchReasons, agentsettings.ReasonProfileChanged) {
						t.Fatal("relaunch result identity/reason")
					}
				} else {
					second = startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "second"})
				}
				current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && processRelaunchConversation(r) != "" && r.TurnID == ""
				})
				if provider == aiModeClaude {
					deferredReady(t, ctx, f, first.ref)
				}
				if current.Binding.AgentUID != old.Binding.AgentUID || current.Binding.PaneUID != old.Binding.PaneUID || processRelaunchConversation(&current) != processRelaunchConversation(&old) {
					t.Fatal("identity/conversation changed")
				}
				after := digestCLIAnnotations(t, f, first.ref)
				if after[m.AnnotationAgentProfileDigest] != next || after[m.AnnotationAgentProfile] != "same" || after[m.AnnotationAgentProfileSource] != m.SettingSourceFlag {
					t.Fatalf("record digest=%q want=%q", after[m.AnnotationAgentProfileDigest], next)
				}
				r := agentsettings.Resolve(agentsettings.Input{Annotations: after, Profile: &agentsettings.Profile{Name: "same", Digest: next, Model: "new-model", Effort: "low"}})
				if slices.Contains(r.Reasons, agentsettings.ReasonProfileChanged) {
					t.Fatal("profile-changed repeated")
				}
				second.shutdown(t)
			})
		}
	}
}

func TestSameNameProfileDigestFrozenAndOrdinaryClaimActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	oldDigest := digestCLIProfile(t, "stub-model")
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "same", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	next := digestCLIProfile(t, "frozen-model")
	// An ordinary child-free claim does not commit current profile settings.
	ordinary := startDeferredCLIClaim(t, ctx, f, first.ref)
	if len(deferredArgv(t, f)) != 1 || digestCLIAnnotations(t, f, first.ref)[m.AnnotationAgentProfileDigest] != oldDigest {
		t.Fatal("ordinary claim committed settings or birthed provider")
	}
	ordinary.finish(t)
	run := deferredRelaunchCLI(t, ctx, f, first.ref)
	uid := strings.TrimPrefix(first.ref, "uid:")
	c := deferredFixtureCommand(f)
	frozen, err := c.readDeferredLaunch(uid)
	if err != nil || frozen == nil || frozen.NewAnnotations[m.AnnotationAgentProfileDigest] != next || digestCLIAnnotations(t, f, first.ref)[m.AnnotationAgentProfileDigest] != next || len(deferredArgv(t, f)) != 1 {
		t.Fatalf("frozen commit digest/birth: %v", err)
	}
	run.finish(t)
	later := digestCLIProfile(t, "later-model")
	resumed := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "first frozen input"})
	deferredReady(t, ctx, f, first.ref)
	resumed.shutdown(t)
	args := deferredArgv(t, f)
	if len(args) != 2 || argvValue(args[1], "--model") != "frozen-model" || digestCLIAnnotations(t, f, first.ref)[m.AnnotationAgentProfileDigest] != next {
		t.Fatal("first input re-read current profile instead of frozen recipe")
	}
	after := digestCLIAnnotations(t, f, first.ref)
	if !slices.Contains(agentsettings.Resolve(agentsettings.Input{Annotations: after, Profile: &agentsettings.Profile{Name: "same", Digest: later}}).Reasons, agentsettings.ReasonProfileChanged) {
		t.Fatal("post-commit file change hidden")
	}
}

func TestSameNameProfileDigestFailedLaunchActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	old := digestCLIProfile(t, "stub-model")
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "same", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	before := digestCLIAnnotations(t, f, first.ref)
	digestCLIProfile(t, "new-model")
	// Fail the actual owned provider before init, after recipe reservation.
	if err := os.WriteFile(filepath.Join(f.root, "claude"), []byte("#!/bin/sh\nexit 17\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--", "failure").CombinedOutput()
	if err == nil {
		t.Fatalf("launch unexpectedly passed: %s", out)
	}
	after := digestCLIAnnotations(t, f, first.ref)
	if after[m.AnnotationAgentProfileDigest] != old || !maps.Equal(before, after) {
		t.Fatal("failed launch retained changed recipe")
	}
	reg := mustRegistry(t, f)
	a, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
	pane, _ := reg.Pane(a.Status.PaneRef)
	if a.Status.Phase != m.PhaseOffline || pane.Status.LastTermination == nil {
		t.Fatal("failed launch left writer without actual Wait")
	}
}
