package app

import (
	"context"
	"errors"
	"io"
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
	"github.com/crevissepartners/projmux/internal/core/selector"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func digestCLIProfile(t *testing.T, model string) string {
	t.Helper()
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return writeCodexProfile(t, profile.NewDefaultStore(paths), "same", "model = \""+model+"\"\neffort = \"low\"\n")
}

func TestSameNameProfileDigestOrdinaryResumeFailureActualCLI(t *testing.T) {
	t.Run("born", testProfileDigestBornResumeFailure)
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, fault := range []string{"lease", "supervisor", "competing-recipe", "competing-writer"} {
			t.Run(provider+"/"+fault, func(t *testing.T) {
				var f processCreateCLI
				if provider == aiModeCodex {
					f = processCodexRelaunchFixture(t)
				} else {
					f = processResumeCLIFixture(t, provider)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				oldDigest := digestCLIProfile(t, "stub-model")
				first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "same", "--", "controls"))
				awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool {
					return r.ConnectionID != "" && processRelaunchConversation(r) != ""
				})
				first.shutdown(t)
				old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool { return r.ResumeState == m.ProcessResumable })
				before := mustRegistry(t, f)
				oldAgent, _ := before.Agent(old.Binding.AgentUID)
				oldPane, _ := before.Pane(old.Binding.PaneUID)
				next := digestCLIProfile(t, "new-model")
				if oldAgent.Metadata.Annotations[m.AnnotationAgentProfileDigest] != oldDigest || oldDigest == next {
					t.Fatal("same-name digest fixture did not change")
				}
				command := New().agent
				const generation = "digest-unspawned-resume"
				command.rebind.create.newGeneration = func() (string, error) { return generation, nil }
				var competing *m.Registry
				if strings.HasPrefix(fault, "competing-") {
					update := command.rebind.create.store.update
					command.rebind.create.store.update = func(fn func(*m.Registry) error) (m.Registry, error) {
						reg, err := update(fn)
						pane, _ := reg.Pane(old.Binding.PaneUID)
						if err == nil && competing == nil && pane != nil && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding.Generation == generation {
							changed, changeErr := f.store.Update(func(r *m.Registry) error {
								if fault == "competing-recipe" {
									_, e := (m.Mutator{}).SetAgentEffort(r, old.Binding.AgentUID, "high", m.SettingSourceFlag)
									return e
								}
								p, _ := r.Pane(old.Binding.PaneUID)
								p.Status.ProcessSession.Binding.Generation = "competing-generation"
								p.Status.ProcessSession.Binding.OperationID = "competing-operation"
								p.Status.ProcessSession.Binding.HostInstanceID = "competing-host"
								return r.Validate()
							})
							if changeErr != nil {
								return reg, changeErr
							}
							competing = &changed
						}
						return reg, err
					}
				}
				var release func()
				if fault == "supervisor" {
					executable, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					stat, err := os.Stat(executable)
					if err != nil {
						t.Fatal(err)
					}
					release = func() {
						if err := os.Chmod(executable, stat.Mode().Perm()); err != nil {
							t.Error(err)
						}
					}
					t.Cleanup(release)
					if err := os.Chmod(executable, stat.Mode().Perm()&^0111); err != nil {
						t.Fatal(err)
					}
				} else {
					lease := claudeadapter.ActivationLeaseDir(f.store.Path(), old.Binding.PaneUID, generation)
					file, err := os.OpenFile(lease, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_ = file.Close()
					release = func() { _ = os.Remove(lease) }
					t.Cleanup(release)
				}
				request, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: selector.Ref{Kind: m.KindAgent, UID: old.Binding.AgentUID}, Prompt: processResumeFirstFrame{Kind: "user", Text: "retry task"}})
				if err != nil {
					t.Fatal(err)
				}
				result, startErr := command.resumeProcessAgent(ctx, request)
				release()
				if strings.HasPrefix(fault, "competing-") && competing == nil {
					t.Fatal("competing change was not injected after reservation")
				}
				if startErr == nil || !result.hasNoChild() || (fault == "supervisor" && result.Handle == nil) {
					t.Fatal("fixture did not fail before child birth", startErr)
				}
				failure := result.fail(startErr)
				if strings.Contains(failure.Error(), "Wait evidence is unavailable") {
					t.Fatal("no-child failure invented Wait guidance", failure)
				}
				after := mustRegistry(t, f)
				a, _ := after.Agent(old.Binding.AgentUID)
				pane, _ := after.Pane(old.Binding.PaneUID)
				if competing != nil {
					if !reflect.DeepEqual(after, *competing) {
						t.Fatal("rollback changed competing recipe/writer")
					}
					return
				}
				if !reflect.DeepEqual(pane.Status.ProcessSession, &old) || a.Status.Phase != m.PhaseOffline || !pane.Status.Activation.IsZero() || !reflect.DeepEqual(pane.Status.LastTermination, oldPane.Status.LastTermination) {
					t.Fatal("no-child failure changed record/conversation or invented birth/Wait")
				}
				if !maps.Equal(a.Metadata.Annotations, oldAgent.Metadata.Annotations) {
					t.Errorf("no-child failure retained recipe: digest=%q want=%q", a.Metadata.Annotations[m.AnnotationAgentProfileDigest], oldDigest)
				}
				resolution := agentsettings.Resolve(agentsettings.Input{Annotations: a.Metadata.Annotations, Profile: &agentsettings.Profile{Name: "same", Digest: next, Model: "new-model", Effort: "low"}})
				if !slices.Contains(resolution.Reasons, agentsettings.ReasonProfileChanged) {
					t.Error("no-child failure hid profile-changed")
				}
				retry := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "controls"})
				current := awaitProcessResumeRecord(t, ctx, f, retry.ref, func(r *m.ProcessSessionRecord) bool {
					return r.ConnectionID != "" && processRelaunchConversation(r) != ""
				})
				if retry.ref != first.ref || current.Binding.PaneUID != old.Binding.PaneUID || processRelaunchConversation(&current) != processRelaunchConversation(&old) || digestCLIAnnotations(t, f, retry.ref)[m.AnnotationAgentProfileDigest] != next {
					t.Fatal("successful retry lost identity/conversation or applied digest")
				}
				retry.shutdown(t)
			})
		}
	}
}

type digestUnconfirmedWaitHandle struct{ processOwnedHandle }

func (h digestUnconfirmedWaitHandle) Wait(context.Context, processhost.Binding) (processhost.Snapshot, error) {
	return processhost.Snapshot{}, context.DeadlineExceeded
}

func testProfileDigestBornResumeFailure(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		faults := []string{"startup", "competing-recipe", "competing-writer", "unconfirmed-cleanup", "after-ready-output"}
		if provider == aiModeCodex {
			faults = append(faults, "after-ready-turn")
		}
		for _, fault := range faults {
			t.Run(provider+"/"+fault, func(t *testing.T) {
				var f processCreateCLI
				if provider == aiModeCodex {
					f = processCodexRelaunchFixture(t)
				} else {
					f = processResumeCLIFixture(t, provider)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				oldDigest := digestCLIProfile(t, "stub-model")
				first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "same", "--", "controls"))
				awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool {
					return r.ConnectionID != "" && processRelaunchConversation(r) != ""
				})
				first.shutdown(t)
				old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool { return r.ResumeState == m.ProcessResumable })
				before := digestCLIAnnotations(t, f, first.ref)
				next := digestCLIProfile(t, "new-model")
				ready := strings.HasPrefix(fault, "after-ready-")
				if !ready || fault == "after-ready-turn" {
					if provider == aiModeClaude {
						// Keep the real birth/registration, then reject the recorded init.
						path := filepath.Join(f.root, "provider.py")
						raw, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						needle := "emit({'type':'system','subtype':'init','session_id':'process-session'})"
						if !strings.Contains(string(raw), needle) {
							t.Fatal("fixture init refusal patch did not match")
						}
						if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), needle, "emit({'type':'system','subtype':'init','session_id':'rejected-session'})")), 0600); err != nil {
							t.Fatal(err)
						}
					} else {
						path := filepath.Join(f.root, "codex-provider.py")
						raw, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						needle := "p=n['params'];model=p.get('model',model);effort=p.get('effort',effort);sandbox=p.get('sandboxPolicy',sandbox);approval=p.get('approvalPolicy',approval);reply({})"
						replacement := "emit({'id':n['id'],'error':{'code':-32603,'message':'fixture settings refusal'}})"
						if ready {
							needle = "elif method=='turn/start':"
							replacement = "elif method=='turn/start':\n  emit({'id':n['id'],'error':{'code':-32603,'message':'fixture turn refusal'}});continue"
						}
						if !strings.Contains(string(raw), needle) {
							t.Fatal("fixture refusal patch did not match")
						}
						if err := os.WriteFile(path, []byte(strings.Replace(string(raw), needle, replacement, 1)), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				command := New().agent
				request, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: selector.Ref{Kind: m.KindAgent, UID: old.Binding.AgentUID}, Prompt: processResumeFirstFrame{Kind: "user", Text: "controls"}})
				if err != nil {
					t.Fatal(err)
				}
				result, startErr := command.resumeProcessAgent(ctx, request)
				if result.Handle == nil || result.hasNoChild() || (!ready && startErr == nil) || (fault == "after-ready-turn" && startErr == nil) || (fault == "after-ready-output" && startErr != nil) {
					t.Fatal("fixture did not reach the intended born/ready boundary", startErr)
				}
				if strings.HasPrefix(fault, "competing-") {
					stop, stopCancel := context.WithCancel(context.Background())
					stopCancel()
					_, _ = result.owner.waitProcessAgent(stop, nil)
					if !result.owner.waitRecorded {
						t.Fatal("competing fixture lacks actual owned Wait")
					}
					_, err = f.store.Update(func(r *m.Registry) error {
						if fault == "competing-recipe" {
							_, e := (m.Mutator{}).SetAgentEffort(r, old.Binding.AgentUID, "high", m.SettingSourceFlag)
							return e
						}
						p, _ := r.Pane(old.Binding.PaneUID)
						b := p.Status.ProcessSession.Binding
						b.Generation, b.OperationID, b.HostInstanceID = "born-competing-generation", "born-competing-operation", "born-competing-host"
						return (m.Mutator{}).ReserveProcessResume(r, p.Status.ProcessSession.Binding, b)
					})
					if err != nil {
						t.Fatal(err)
					}
					competing := mustRegistry(t, f)
					_ = result.fail(startErr)
					if !reflect.DeepEqual(mustRegistry(t, f), competing) {
						t.Fatal("born compensation overwrote competing recipe/writer or Wait history")
					}
					return
				}
				if fault == "unconfirmed-cleanup" {
					real := result.owner.Handle
					result.owner.Handle = digestUnconfirmedWaitHandle{real}
					unconfirmed := mustRegistry(t, f)
					oldPane, _ := unconfirmed.Pane(old.Binding.PaneUID)
					failure := result.fail(startErr)
					if failure == nil || !errors.Is(failure, startErr) || !errors.Is(failure, context.DeadlineExceeded) || !strings.Contains(failure.Error(), old.Binding.AgentUID) || result.owner.waitRecorded || digestCLIAnnotations(t, f, first.ref)[m.AnnotationAgentProfileDigest] != next {
						t.Fatal("unconfirmed cleanup claimed compensation success")
					}
					uncleaned := mustRegistry(t, f)
					pane, _ := uncleaned.Pane(old.Binding.PaneUID)
					if !reflect.DeepEqual(pane.Status.ProcessSession, oldPane.Status.ProcessSession) || !reflect.DeepEqual(pane.Status.LastTermination, oldPane.Status.LastTermination) || !reflect.DeepEqual(pane.Status.Activation, oldPane.Status.Activation) {
						t.Fatal("unconfirmed cleanup changed Wait/history or retired activation")
					}
					result.owner.Handle = real
					stop, stopCancel := context.WithCancel(context.Background())
					stopCancel()
					_, _ = result.owner.waitProcessAgent(stop, nil)
					if !result.owner.waitRecorded {
						t.Fatal("fixture cleanup did not finally preserve actual Wait")
					}
					return
				}
				if fault == "after-ready-output" {
					startErr = io.ErrClosedPipe
				}
				failure := result.fail(startErr)
				if failure == nil || !strings.Contains(failure.Error(), old.Binding.AgentUID) {
					t.Fatal("failure lost its original error or exact refs", failure)
				}
				reg := mustRegistry(t, f)
				a, _ := reg.Agent(old.Binding.AgentUID)
				pane, _ := reg.Pane(old.Binding.PaneUID)
				if !result.owner.waitRecorded || a.Status.Phase != m.PhaseOffline || !pane.Status.Activation.IsZero() || !m.MatchesProcessWait(metadataProcessBinding(result.Binding), pane.Status.LastTermination) || !m.SameProcessWait(a.Status.LastTermination, pane.Status.LastTermination) || processRelaunchConversation(pane.Status.ProcessSession) != processRelaunchConversation(&old) {
					t.Fatal("born failure did not preserve actual Wait/retirement/conversation")
				}
				activation, err := mutationruntime.ReadProcessRetirement(f.store.Path(), metadataProcessBinding(result.Binding), pane.Status.LastTermination)
				if err != nil || !processIdentityAbsent(activation.Child, command.readProcessIdentity()) {
					t.Fatal("owned child retirement is not confirmed", err)
				}
				if ready {
					if a.Metadata.Annotations[m.AnnotationAgentProfileDigest] != next {
						t.Fatal("ready turn/output failure rolled back applied recipe")
					}
					return
				}
				if !maps.Equal(a.Metadata.Annotations, before) || a.Metadata.Annotations[m.AnnotationAgentProfileDigest] != oldDigest || !slices.Contains(agentsettings.Resolve(agentsettings.Input{Annotations: a.Metadata.Annotations, Profile: &agentsettings.Profile{Name: "same", Digest: next}}).Reasons, agentsettings.ReasonProfileChanged) {
					t.Errorf("born startup failure retained recipe: digest=%q want=%q", a.Metadata.Annotations[m.AnnotationAgentProfileDigest], oldDigest)
				}
			})
		}
	}
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
