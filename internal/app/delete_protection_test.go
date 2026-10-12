package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func protectFixture(reg *metadata.Registry, uid, value string) {
	a, _ := reg.Agent(uid)
	a.Metadata.Labels = map[string]string{metadata.LabelProtected: value}
}
func assertProtection(t *testing.T, err error, uid string) {
	t.Helper()
	var f *mutations.Failure
	if !errors.As(err, &f) || f.Code != mutations.CodeAgentProtected || !strings.Contains(err.Error(), "uid:"+uid+" projmux.io/protected-") {
		t.Fatalf("protection refusal = %v", err)
	}
}

func TestProtectedExplicitDeleteSurfaces(t *testing.T) {
	for _, value := range []string{"true", "false", ""} {
		for _, route := range []struct{ name, kind, uid string }{
			{"D1 agent", "agent", "agt-alpha-codex"},
			{"D3 window", "window", "win-alpha-main"},
			{"D4 delete project", "project", "prj-alpha"},
			{"D4 unregister project", "unregister", "prj-alpha"},
		} {
			for _, dry := range []bool{true, false} {
				t.Run(route.name+"/"+value+"/"+map[bool]string{true: "preview", false: "execute"}[dry], func(t *testing.T) {
					s := newFakeResourceStore(t)
					protectFixture(&s.registry, "agt-alpha-codex", value)
					before := s.snapshot()
					c := newTestDeleteCommand(s, false, false, nil)
					args := []string{route.kind, "uid:" + route.uid, "--yes"}
					if route.kind == "unregister" {
						args = []string{"project", "uid:" + route.uid, "--yes"}
					}
					if dry {
						args = append(args, "--dry-run")
					}
					var out, stderr bytes.Buffer
					var err error
					if route.kind == "unregister" {
						err = (&unregisterCommand{delete: c}).Run(args, &out, &stderr)
					} else {
						err = c.Run(args, &out, &stderr)
					}
					assertProtection(t, err, "agt-alpha-codex")
					if s.writes != 0 || s.snapshot() != before {
						t.Fatal("protected deletion wrote Registry")
					}
				})
			}
		}
	}
}

func TestProtectedSDKDeleteAndPreflight(t *testing.T) {
	for _, kind := range []core.Kind{core.KindAgent, core.KindWindow, core.KindProject} {
		for _, dry := range []bool{true, false} {
			t.Run(string(kind)+map[bool]string{true: "preview", false: "execute"}[dry], func(t *testing.T) {
				reg, _ := deleteSDKFixture(t)
				protectFixture(&reg, "agt-alpha-codex", "false")
				state, _, client := renameState(t, reg)
				path := store.PathFor(state)
				before, _ := os.ReadFile(path)
				uid := map[core.Kind]string{core.KindAgent: "agt-alpha-codex", core.KindWindow: "win-alpha-main", core.KindProject: "prj-alpha"}[kind]
				req := core.DeleteRequest{Kind: kind, Ref: "uid:" + uid, DryRun: dry, Confirm: true}
				_, err := client.PreflightDelete(context.Background(), req)
				var refusal *projmux.Refusal
				if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAgentProtected {
					t.Fatalf("preflight %v", err)
				}
				if kind == core.KindProject {
					_, err = client.UnregisterProject(context.Background(), core.UnregisterRequest{Ref: req.Ref, DryRun: dry, Confirm: true})
				} else {
					_, err = client.Delete(context.Background(), req)
				}
				if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAgentProtected {
					t.Fatalf("delete %v", err)
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("SDK changed Registry")
				}
			})
		}
	}
}

func TestProtectedPlainPaneAllowedAndLabelRemoval(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(map[bool]string{false: "D2 plain Pane retains Agent", true: "label removal allows delete"}[release], func(t *testing.T) {
			s := newFakeResourceStore(t)
			protectFixture(&s.registry, "agt-alpha-codex", "")
			kind, uid := "pane", "pan-alpha-codex"
			if release {
				_, _, err := s.mutator().SetLabels(&s.registry, metadata.KindAgent, "agt-alpha-codex", nil, []string{metadata.LabelProtected})
				if err != nil {
					t.Fatal(err)
				}
				kind, uid = "agent", "agt-alpha-codex"
			}
			c := newTestDeleteCommand(s, false, false, nil)
			_, _, err := runRoute(t, c, kind, "uid:"+uid, "--yes")
			if err != nil {
				t.Fatal(err)
			}
			a, exists := s.registry.Agent("agt-alpha-codex")
			if !release && (!exists || a.Status.Phase != metadata.PhaseOffline || a.Status.PaneRef != "") {
				t.Fatalf("plain Pane lost Agent: %+v", a)
			}
			if release && exists {
				t.Fatal("unprotected Agent retained")
			}
		})
	}
}

func TestProtectedPruneSurfaces(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run("D6 agent"+map[bool]string{true: "execute", false: "preview"}[yes], func(t *testing.T) {
			s := newFakeResourceStore(t)
			s.registry = pruneAgentFixtureRegistry(t)
			protectFixture(&s.registry, "agt-beta-codex", "true")
			before := s.snapshot()
			c, _ := newTestPruneAgentCommand(s.store(), func() time.Time { return s.now })
			args := []string{"--no-pane", "--older-than", "720h"}
			if yes {
				args = append(args, "--yes")
			}
			_, _, err := runRoute(t, c, args...)
			assertProtection(t, err, "agt-beta-codex")
			if s.writes != 0 || before != s.snapshot() {
				t.Fatal("prune wrote")
			}
		})
		t.Run("D7 project"+map[bool]string{true: "execute", false: "preview"}[yes], func(t *testing.T) {
			s := newFakeResourceStore(t)
			// Make the protected Agent's Project an old missing-root candidate.
			p, _ := s.registry.Project("prj-alpha")
			p.Spec.Root = "/srv/missing-protected"
			p.Status.Conditions = []metadata.Condition{{Type: metadata.ConditionMissingRoot, Status: metadata.ConditionTrue, FirstObservedAt: s.now.Add(-1000 * time.Hour)}}
			p.Status.Session = nil
			protectFixture(&s.registry, "agt-alpha-codex", "true")
			before := s.snapshot()
			c := newTestPruneProjectCommand(s)
			args := []string{"--missing", "--older-than", "720h"}
			if yes {
				args = append(args, "--yes")
			}
			_, _, err := runRoute(t, c, args...)
			assertProtection(t, err, "agt-alpha-codex")
			if s.writes != 0 || before != s.snapshot() {
				t.Fatal("prune wrote")
			}
		})
	}
}

func TestProtectedProcessDeleteRaceAndVirtualPane(t *testing.T) {
	for _, kind := range []metadata.Kind{metadata.KindAgent, metadata.KindPane, metadata.KindWindow} {
		t.Run(string(kind), func(t *testing.T) {
			reg, uid, paneUID := processDeleteFixture(t, "codex")
			a, _ := reg.Agent(uid)
			w, _ := reg.Window(a.Metadata.OwnerUID())
			// An exact process-only virtual Window: its last Pane cascades the Agent.
			reg.Panes = []metadata.Pane{*func() *metadata.Pane { p, _ := reg.Pane(paneUID); return p }()}
			reg.Agents = []metadata.Agent{*a}
			w.Spec.AnchorPaneRef = paneUID
			w.Spec.DefaultShellPaneRef = ""
			w.Status.RuntimeID = ""
			w.Status.RuntimeSessionID = ""
			targetUID := map[metadata.Kind]string{metadata.KindAgent: uid, metadata.KindPane: paneUID, metadata.KindWindow: w.Metadata.UID}[kind]
			plan := buildDeletePlan(*reg, kind, selector.Resolution{Kind: kind, Matches: []selector.Match{{Kind: kind, UID: targetUID}}})
			probe := &processDeleteProbe{host: true, child: true}
			d := probe.deleter(reg)
			targets, err := d.classifyPlan(*reg, plan)
			if err != nil {
				t.Fatal(err)
			}
			// Protection appears after preflight without changing the UID signature.
			protectFixture(reg, uid, "false")
			before := reg.Clone()
			_, err = d.execute(context.Background(), plan, targets, nil)
			assertProtection(t, err, uid)
			if probe.stops != 0 || !reflect.DeepEqual(*reg, before) {
				t.Fatal("protected process mutated")
			}
		})
	}
}

func TestProtectedHostStopAdmissionRechecksCurrentSnapshot(t *testing.T) {
	reg, uid, paneUID := processDeleteFixture(t, "codex")
	pane, _ := reg.Pane(paneUID)
	plan := buildDeletePlan(*reg, metadata.KindAgent, selector.Resolution{Kind: metadata.KindAgent, Matches: []selector.Match{{Kind: metadata.KindAgent, UID: uid}}})
	admission := processDeleteAdmission{Kind: plan.Kind, UIDs: plan.Uids(), Signature: plan.Signature()}
	payload, _ := json.Marshal(admission)
	protectFixture(reg, uid, "true")
	state, _, _ := renameState(t, *reg)
	stopped := 0
	err := admitProcessDeleteStop(store.PathFor(state), processForegroundRequest{Authority: processhost.Authority{Binding: processSchemaBinding(pane.Status.ProcessSession.Binding)}, Action: "delete-stop", Prompt: string(payload)}, func() error { stopped++; return nil })
	assertProtection(t, err, uid)
	if stopped != 0 {
		t.Fatal("host accepted Stop")
	}
}

func TestProtectedDeleteLockedRevalidationBeforeIntent(t *testing.T) {
	for _, kind := range []string{"agent", "window", "project"} {
		t.Run(kind, func(t *testing.T) {
			s := newFakeResourceStore(t)
			c := newTestDeleteCommand(s, false, false, nil)
			update := c.store.update
			c.store.update = func(apply func(*metadata.Registry) error) (metadata.Registry, error) {
				protectFixture(&s.registry, "agt-alpha-codex", "true")
				return update(apply)
			}
			uid := map[string]string{"agent": "agt-alpha-codex", "window": "win-alpha-main", "project": "prj-alpha"}[kind]
			_, _, err := runRoute(t, c, kind, "uid:"+uid, "--yes")
			assertProtection(t, err, "agt-alpha-codex")
			if s.writes != 0 {
				t.Fatalf("%d writes", s.writes)
			}
		})
	}
}

func TestProtectedPruneLockedRevalidation(t *testing.T) {
	s := newFakeResourceStore(t)
	s.registry = pruneAgentFixtureRegistry(t)
	storage := s.store()
	update := storage.update
	storage.update = func(apply func(*metadata.Registry) error) (metadata.Registry, error) {
		protectFixture(&s.registry, "agt-beta-codex", "true")
		return update(apply)
	}
	c, _ := newTestPruneAgentCommand(storage, func() time.Time { return s.now })
	_, _, err := runRoute(t, c, "--no-pane", "--older-than", "720h", "--yes")
	assertProtection(t, err, "agt-beta-codex")
	if s.writes != 0 {
		t.Fatal("prune race wrote")
	}
}

func TestProtectedVirtualCascadeCLIAndSDK(t *testing.T) {
	for _, kind := range []string{"agent", "pane", "window"} {
		for _, dry := range []bool{true, false} {
			t.Run(kind+map[bool]string{true: "preview", false: "execute"}[dry], func(t *testing.T) {
				s := realTmuxNameHandoffRegistry(t, t.TempDir(), "virtual", false)
				uid, pane := addVirtualLifecycleProcess(t, s)
				if err := s.mutator().DeletePane(&s.registry, "pan-name-handoff"); err != nil {
					t.Fatal(err)
				}
				protectFixture(&s.registry, uid, "")
				c := newTestDeleteCommand(s, false, false, nil)
				c.windows, c.panes = nil, nil
				target := map[string]string{"agent": uid, "pane": pane, "window": "win-name-handoff"}[kind]
				args := []string{kind, "uid:" + target, "--yes"}
				if dry {
					args = append(args, "--dry-run")
				}
				before := s.registry.Clone()
				_, _, err := runRoute(t, c, args...)
				assertProtection(t, err, uid)
				if s.writes != 0 || !reflect.DeepEqual(before, s.registry) {
					t.Fatal("virtual CLI wrote")
				}
				_, _, client := renameState(t, s.registry)
				req := core.DeleteRequest{Kind: core.Kind(strings.ToUpper(kind[:1]) + kind[1:]), Ref: "uid:" + target, DryRun: dry, Confirm: true}
				_, err = client.Delete(context.Background(), req)
				var refusal *projmux.Refusal
				if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAgentProtected {
					t.Fatalf("SDK process protection %v", err)
				}
			})
		}
	}
}

func TestProtectedLabelAfterIntentDoesNotCancelAdmission(t *testing.T) {
	s := newFakeResourceStore(t)
	c := newTestDeleteCommand(s, false, false, nil)
	update := c.store.update
	calls := 0
	c.store.update = func(apply func(*metadata.Registry) error) (metadata.Registry, error) {
		calls++
		if calls == 2 {
			protectFixture(&s.registry, "agt-alpha-codex", "true")
		}
		return update(apply)
	}
	_, _, err := runRoute(t, c, "agent", "uid:agt-alpha-codex", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.registry.Agent("agt-alpha-codex"); ok {
		t.Fatal("admitted delete canceled by late label")
	}
}

func TestProtectedLabelAfterProcessStopDoesNotCancelAdmission(t *testing.T) {
	reg, uid, pane := processDeleteFixture(t, "codex")
	probe := &processDeleteProbe{host: true, child: true}
	probe.onStop = func() error { recordFixtureWait(t, reg, pane); protectFixture(reg, uid, "true"); return nil }
	d := probe.deleter(reg)
	plan := buildDeletePlan(*reg, metadata.KindAgent, selector.Resolution{Kind: metadata.KindAgent, Matches: []selector.Match{{Kind: metadata.KindAgent, UID: uid}}})
	targets, err := d.classifyPlan(*reg, plan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.execute(context.Background(), plan, targets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if probe.stops != 1 {
		t.Fatalf("stops=%d", probe.stops)
	}
	if _, exists := reg.Agent(uid); exists {
		t.Fatal("admitted process delete canceled by late label")
	}
}

func TestProtectedMultiHostOperationAdmission(t *testing.T) {
	for _, beforeAdmission := range []bool{true, false} {
		t.Run(map[bool]string{true: "before-first", false: "after-first"}[beforeAdmission], func(t *testing.T) {
			reg, first, firstPane := processDeleteFixture(t, "codex")
			template, _ := reg.Pane(firstPane)
			activation := *template.Status.Activation.Process
			owner, _ := reg.Agent(first)
			window := owner.Metadata.OwnerUID()
			n := 0
			mutator := metadata.Mutator{Now: func() time.Time { return sessionRefObservedAt }, NewUID: func(metadata.Kind) (string, error) { n++; return []string{"agent-second", "pane-second"}[n-1], nil }}
			second, err := mutator.CreateAgent(reg, window, metadata.CreateAgentOptions{Provider: "codex", OperationID: "op-second"})
			if err != nil {
				t.Fatal(err)
			}
			secondPane, err := mutator.AttachAgentPane(reg, second.Metadata.UID, metadata.BootstrapPane{Command: "codex", CWD: "/src/app"}, "op-second")
			if err != nil {
				t.Fatal(err)
			}
			activation.Binding.AgentUID, activation.Binding.PaneUID = second.Metadata.UID, secondPane.Metadata.UID
			activation.Binding.Generation, activation.Binding.OperationID, activation.Binding.HostInstanceID = "gen-second", "op-second", "host-second"
			p, _ := reg.Pane(secondPane.Metadata.UID)
			p.Spec.Runtime.Kind = metadata.RuntimeProcess
			p.Status.Activation = metadata.PaneActivation{Kind: metadata.RuntimeProcess, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID, AgentUID: second.Metadata.UID, Process: &activation}
			p.Status.ProcessSession = &metadata.ProcessSessionRecord{Provider: "codex", Binding: activation.Binding, ResumeState: metadata.ProcessResumeUnknown}
			a, _ := reg.Agent(second.Metadata.UID)
			a.Status.Phase = metadata.PhaseRunning
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
			plan := buildDeletePlan(*reg, metadata.KindAgent, selector.Resolution{Kind: metadata.KindAgent, Matches: []selector.Match{{Kind: metadata.KindAgent, UID: first}, {Kind: metadata.KindAgent, UID: second.Metadata.UID}}})
			probe := &processDeleteProbe{host: true, child: true}
			d := probe.deleter(reg)
			targets, err := d.classifyPlan(*reg, plan)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			d.stop = func(ctx context.Context, _ metadata.Registry, agent metadata.Agent) error {
				admission := ctx.Value(processDeleteAdmissionKey{}).(processDeleteAdmission)
				if admission.Admitted != (calls > 0) {
					t.Fatalf("admitted=%v calls=%d", admission.Admitted, calls)
				}
				calls++
				recordFixtureWait(t, reg, agent.Status.PaneRef)
				if calls == 1 {
					protectFixture(reg, second.Metadata.UID, "false")
				}
				return nil
			}
			if beforeAdmission {
				protectFixture(reg, second.Metadata.UID, "")
			}
			before := reg.Clone()
			_, err = d.execute(context.Background(), plan, targets, nil)
			if beforeAdmission {
				assertProtection(t, err, second.Metadata.UID)
				if calls != 0 || !reflect.DeepEqual(*reg, before) {
					t.Fatal("pre-admission protection had effects")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if calls != 2 {
					t.Fatalf("stops=%d", calls)
				}
				for _, uid := range []string{first, second.Metadata.UID} {
					if _, ok := reg.Agent(uid); ok {
						t.Fatalf("Agent %s survived admitted deletion", uid)
					}
				}
			}
		})
	}
}

func TestProtectedLaterHostAdmissionKeepsOperationPermission(t *testing.T) {
	reg, uid, paneUID := processDeleteFixture(t, "codex")
	pane, _ := reg.Pane(paneUID)
	plan := buildDeletePlan(*reg, metadata.KindAgent, selector.Resolution{Kind: metadata.KindAgent, Matches: []selector.Match{{Kind: metadata.KindAgent, UID: uid}}})
	protectFixture(reg, uid, "true")
	state, _, _ := renameState(t, *reg)
	payload, _ := json.Marshal(processDeleteAdmission{Kind: plan.Kind, UIDs: plan.Uids(), Signature: plan.Signature(), Admitted: true})
	stops := 0
	err := admitProcessDeleteStop(store.PathFor(state), processForegroundRequest{Authority: processhost.Authority{Binding: processSchemaBinding(pane.Status.ProcessSession.Binding)}, Action: "delete-stop", Prompt: string(payload)}, func() error { stops++; return nil })
	if err != nil || stops != 1 {
		t.Fatalf("later admission err=%v stops=%d", err, stops)
	}
}
