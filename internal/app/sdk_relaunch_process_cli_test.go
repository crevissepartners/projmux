package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	metadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSDKDetachedProcessRelaunchCancellationSettlesBornGeneration(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
			if err != nil {
				t.Fatal(err)
			}
			ctx, done := context.WithTimeout(context.Background(), 35*time.Second)
			defer done()
			created, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "none", Model: "stub-model", Effort: "low", Host: "process", Detach: true, Payload: []string{"initial"}})
			if err != nil {
				t.Fatal(err)
			}
			ref := "uid:" + created.Agent.Metadata.UID
			t.Cleanup(func() {
				clean, finish := context.WithTimeout(context.Background(), 15*time.Second)
				defer finish()
				if _, e := c.StopAgent(clean, core.StopAgentRequest{Ref: ref}); e != nil {
					t.Error(e)
					_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("relaunch cancellation cleanup unconfirmed"), 0600)
				}
			})
			processCLIUntil(t, ctx, func() bool {
				r, e := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: ref})
				if e != nil {
					t.Fatal(e)
				}
				return r.(core.Agent).Status.SessionRef != nil
			})
			if stopped, e := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: created.Binding}); e != nil || !stopped.Stopped {
				t.Fatalf("initial Stop %+v %v", stopped, e)
			}
			// Birth succeeds, but provider initialization waits for owner cancellation.
			python, e := exec.LookPath("python3")
			if e != nil {
				t.Fatal(e)
			}
			script := fmt.Sprintf("#!/bin/sh\nexec %q -u -c 'import sys; sys.stdin.buffer.read()'\n", python)
			if e = os.WriteFile(filepath.Join(f.root, provider), []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			startup, cancel := context.WithCancel(ctx)
			defer cancel()
			type outcome struct {
				result core.RelaunchAgentResult
				err    error
			}
			returned := make(chan outcome, 1)
			go func() {
				r, e := c.RelaunchAgent(startup, core.RelaunchAgentRequest{Ref: ref, Detach: true, Binding: created.Binding, Prompt: "resume"})
				returned <- outcome{r, e}
			}()
			var activation m.ProcessActivation
			processCLIUntil(t, ctx, func() bool {
				reg, e := f.store.LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				pane, _ := reg.Pane(created.Binding.PaneUID)
				if pane.Status.Activation.Process == nil || pane.Status.ProcessSession.Binding.Generation == created.Binding.Generation {
					return false
				}
				activation = *pane.Status.Activation.Process
				return true
			})
			cancel()
			var out outcome
			select {
			case out = <-returned:
			case <-ctx.Done():
				t.Fatal("born startup cancellation did not settle")
			}
			var detail *projmux.RelaunchProcessError
			if !errors.Is(out.err, context.Canceled) || !errors.As(out.err, &detail) || out.result.Binding.Generation != activation.Binding.Generation || detail.Binding != out.result.Binding {
				t.Fatalf("startup cause/binding lost: result=%+v detail=%+v error=%v", out.result.Binding, detail, out.err)
			}
			if strings.Contains(out.err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("actual retired startup reported unknown cleanup: %v", out.err)
			}
			reg, e := f.store.LoadReadOnly()
			if e != nil {
				t.Fatal(e)
			}
			agent, _ := reg.Agent(created.Agent.Metadata.UID)
			pane, _ := reg.Pane(created.Binding.PaneUID)
			if agent.Status.Phase != m.PhaseOffline || !m.MatchesProcessWait(activation.Binding, pane.Status.LastTermination) || !m.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) || !processActivationAbsent(activation, localipc.Process) {
				t.Fatal("born cancellation lost exact actual Wait/retirement")
			}
		})
	}
}

func TestSDKDetachedProcessRelaunchFrozenDomains(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		for _, domain := range []string{"a", "b"} {
			t.Run(provider+domain, func(t *testing.T) {
				f := deferredResumeCLIFixture(t, provider)
				paths := config.Paths{ConfigDir: filepath.Join(f.root, domain, "settings"), StateDir: filepath.Join(f.root, domain, "records")}
				original, e := os.ReadFile(f.store.Path())
				if e != nil {
					t.Fatal(e)
				}
				reg, e := f.store.LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				storage := metadata.NewStore(metadata.PathFor(paths.StateDir))
				if _, e = storage.Update(func(r *m.Registry) error { *r = reg.Clone(); return nil }); e != nil {
					t.Fatal(e)
				}
				c, e := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary, SocketPath: filepath.Join(f.root, "unused-client-tmux.sock")})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = c.WriteProfile(context.Background(), "sdk-relaunch", fmt.Appendf(nil, "provider = %q\nmodel = %q\neffort = %q\n", provider, "stub-model", "low")); e != nil {
					t.Fatal(e)
				}
				for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
					t.Setenv(key, f.root+"/poison")
				}
				ctx, done := context.WithTimeout(context.Background(), 45*time.Second)
				defer done()
				created, e := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "sdk-relaunch", Host: "process", Detach: true, Payload: []string{"initial"}})
				if e != nil {
					t.Fatal(e)
				}
				ref := "uid:" + created.Agent.Metadata.UID
				deleted := false
				t.Cleanup(func() {
					clean, done := context.WithTimeout(context.Background(), 15*time.Second)
					defer done()
					if deleted {
						return
					}
					if _, e := c.StopAgent(clean, core.StopAgentRequest{Ref: ref}); e != nil {
						t.Error(e)
						_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK relaunch cleanup unconfirmed"), 0600)
					}
				})
				// Init binds SessionRef before the owner's snapshot records ACK
				// and the completed initial turn. Compare bytes only after those
				// fixture writers have committed and two observations agree.
				var previous []byte
				var previousRevision time.Time
				processCLIUntil(t, ctx, func() bool {
					r, e := storage.LoadReadOnly()
					if e != nil {
						t.Fatal(e)
					}
					a, _ := r.Agent(created.Agent.Metadata.UID)
					p, _ := r.Pane(created.Binding.PaneUID)
					if a.Status.SessionRef == nil || a.Status.Activation.State != m.ActivationAcknowledged || p.Status.ProcessSession == nil || p.Status.ProcessSession.TurnID != "" || len(p.Status.ProcessSession.Pending) != 0 {
						previous = nil
						return false
					}
					raw, e := os.ReadFile(storage.Path())
					if e != nil {
						t.Fatal(e)
					}
					stable := r.UpdatedAt.Equal(previousRevision) && bytes.Equal(previous, raw)
					previous, previousRevision = raw, r.UpdatedAt
					return stable
				})
				before, e := storage.LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				old, _ := before.Pane(created.Binding.PaneUID)
				request := core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, Binding: created.Binding, Prompt: "continue"}
				dry := request
				dry.DryRun = true
				rawBefore, e := os.ReadFile(storage.Path())
				if e != nil {
					t.Fatal(e)
				}
				plan, e := c.RelaunchAgent(ctx, dry)
				if e != nil || !plan.DryRun || plan.CurrentHost != "process" || plan.TargetHost != "process" {
					t.Fatalf("plan %+v %v", plan, e)
				}
				stale := request
				stale.Binding.Generation = "stale"
				invalid := request
				invalid.Host = "invalid"
				for _, q := range []core.RelaunchAgentRequest{stale, invalid, {Ref: ref}, {Ref: ref, Detach: true, SocketPath: "/wrong/socket"}} {
					if _, e := c.RelaunchAgent(ctx, q); e == nil {
						t.Fatalf("accepted %+v", q)
					}
				}
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				if _, e := c.RelaunchAgent(canceled, request); !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
				rawAfter, e := os.ReadFile(storage.Path())
				if e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(rawBefore, rawAfter) {
					t.Fatalf("preflight changed Registry: %s", strings.Join(sdkRelaunchRegistryChanges(rawBefore, rawAfter), "; "))
				}
				startup, cancel := context.WithCancel(ctx)
				result, e := c.RelaunchAgent(startup, request)
				cancel()
				if e != nil {
					t.Fatal(e)
				}
				if result.Binding.AgentUID != created.Binding.AgentUID || result.Binding.PaneUID != created.Binding.PaneUID || result.Binding.Generation == created.Binding.Generation || result.ClaimWaiting || result.NewPaneUID != created.Binding.PaneUID {
					t.Fatalf("result %+v", result)
				}
				after, e := storage.LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				p, _ := after.Pane(created.Binding.PaneUID)
				a, _ := after.Agent(created.Agent.Metadata.UID)
				oldAgent, _ := before.Agent(a.Metadata.UID)
				if !a.Status.SessionRef.SameConversation(oldAgent.Status.SessionRef) || !m.MatchesProcessWait(old.Status.ProcessSession.Binding, p.Status.LastTermination) || !processIdentityAbsent(old.Status.Activation.Process.HostProcess, localipc.Process) {
					t.Fatal("conversation or exact old Wait/retirement lost")
				}
				if p.Status.Activation.Process == nil || processIdentityAbsent(p.Status.Activation.Process.HostProcess, localipc.Process) {
					t.Fatal("caller cancellation stopped detached owner")
				}
				stopped, e := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: result.Binding})
				if e != nil || !stopped.Stopped || stopped.Termination == nil {
					t.Fatalf("Stop %+v %v", stopped, e)
				}
				if _, e = c.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: ref, Confirm: true}); e != nil {
					t.Fatal(e)
				}
				deleted = true
				again, _ := os.ReadFile(f.store.Path())
				if !bytes.Equal(original, again) {
					t.Fatal("ambient Registry changed")
				}
			})
		}
	}
}

// Preserve the full byte comparison while exposing which fixture facts raced.
func sdkRelaunchRegistryChanges(before, after []byte) []string {
	var left, right any
	if json.Unmarshal(before, &left) != nil || json.Unmarshal(after, &right) != nil {
		return []string{fmt.Sprintf("invalid JSON: before=%q after=%q", before, after)}
	}
	var changes []string
	var compare func(string, any, any)
	compare = func(path string, a, b any) {
		if reflect.DeepEqual(a, b) {
			return
		}
		if am, ok := a.(map[string]any); ok {
			if bm, ok := b.(map[string]any); ok {
				keys := make([]string, 0, len(am)+len(bm))
				for key := range am {
					keys = append(keys, key)
				}
				for key := range bm {
					if _, exists := am[key]; !exists {
						keys = append(keys, key)
					}
				}
				slices.Sort(keys)
				for _, key := range keys {
					compare(path+"/"+key, am[key], bm[key])
				}
				return
			}
		}
		if aa, ok := a.([]any); ok {
			if ba, ok := b.([]any); ok && len(aa) == len(ba) {
				for i := range aa {
					compare(fmt.Sprintf("%s/%d", path, i), aa[i], ba[i])
				}
				return
			}
		}
		aJSON, _ := json.Marshal(a)
		bJSON, _ := json.Marshal(b)
		changes = append(changes, fmt.Sprintf("%s: %s -> %s", path, aJSON, bJSON))
	}
	compare("", left, right)
	return changes
}

func TestSDKDetachedProcessRelaunchClaimAndHostMoves(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var f processCreateCLI
			var source m.Agent
			var socket string
			if provider == "claude" {
				f, source, _, socket = hostMoveCLIFixture(t)
				// The host-move source fixture holds its first turn open. Use
				// the existing deferred-peer protocol recipe for this claim test.
				path := filepath.Join(f.root, "provider.py")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				script := strings.Replace(string(raw), "  inp={'questions'", "  emit({'type':'result','subtype':'success','session_id':'process-session'})\n  continue\n  inp={'questions'", 1)
				script = strings.Replace(script, "'provider.sock'", "'provider-%s.sock'%os.getpid()", 1)
				if err = os.WriteFile(path, []byte(script), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				f, source, _, socket = codexHostMoveCLIFixture(t)
			}
			paths, e := config.DefaultPathsFromEnv()
			if e != nil {
				t.Fatal(e)
			}
			c, e := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary})
			if e != nil {
				t.Fatal(e)
			}
			ctx, done := context.WithTimeout(context.Background(), 60*time.Second)
			defer done()
			ref := "uid:" + source.Metadata.UID
			cleanupDetachedResume(t, f, ref)
			q := core.RelaunchAgentRequest{Ref: ref, Host: "process", Detach: true, Confirm: true, SocketPath: socket}
			if provider == "claude" {
				q.Prompt = "continue"
			}
			moved, e := c.RelaunchAgent(ctx, q)
			if e != nil {
				t.Fatal(e)
			}
			if moved.Binding.AgentUID != source.Metadata.UID || moved.Binding.Generation == "" {
				t.Fatalf("move %+v", moved)
			}
			awaitProcessResumeRecord(t, ctx, f, ref, func(r *m.ProcessSessionRecord) bool { return r.ConnectionID != "" })
			if provider == "claude" {
				claim, e := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, Model: "frozen-model"})
				if e != nil || !claim.ClaimWaiting || claim.Binding != moved.Binding {
					t.Fatalf("claim %+v %v", claim, e)
				}
				before, _ := f.store.LoadReadOnly()
				p, _ := before.Pane(claim.Binding.PaneUID)
				status := p.Clone().Status
				stopped, e := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: claim.Binding})
				if e != nil || !stopped.Stopped {
					t.Fatalf("claim Stop %+v %v", stopped, e)
				}
				after, _ := f.store.LoadReadOnly()
				p, _ = after.Pane(claim.Binding.PaneUID)
				if !reflect.DeepEqual(status, p.Status) {
					t.Fatal("claim Stop invented Wait")
				}
				claim, e = c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true})
				if e != nil || !claim.ClaimWaiting {
					t.Fatalf("second claim %+v %v", claim, e)
				}
				peer := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "peer-source", "--", "source"))
				defer peer.shutdown(t)
				awaitDetachedRelaunchInitialConversation(t, ctx, f, peer.ref)
				detachedResumeCLI(t, ctx, f, "agent", "message", "send", ref, "--source", peer.ref, "--message-ref", "sdk-relaunch-frozen-peer", "--", "continue")
				deferredCLIStatus(t, ctx, f, "sdk-relaunch-frozen-peer", "delivered")
				next := awaitProcessResumeRecord(t, ctx, f, ref, func(p *m.ProcessSessionRecord) bool {
					return p.Binding.Generation != claim.Binding.Generation && p.ConnectionID != ""
				})
				r, _ := f.store.LoadReadOnly()
				a, _ := r.Agent(source.Metadata.UID)
				if a.Metadata.Annotations[m.AnnotationAgentModel] != "frozen-model" || next.SessionID != source.Status.SessionRef.ConversationID() {
					t.Fatal("claim lost frozen recipe or conversation")
				}
			}
			back, e := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Host: "tmux", SocketPath: socket, Confirm: true})
			if e != nil {
				t.Fatal(e)
			}
			if back.AgentUID != source.Metadata.UID || back.CurrentHost != "process" || back.TargetHost != "tmux" || back.NewPaneUID == "" {
				t.Fatalf("back %+v", back)
			}
			r, e := f.store.LoadReadOnly()
			if e != nil {
				t.Fatal(e)
			}
			a, _ := r.Agent(source.Metadata.UID)
			if !a.Status.SessionRef.SameConversation(source.Status.SessionRef) {
				t.Fatal("host move replaced conversation")
			}
		})
	}
}

func TestSDKDetachedHostMoveFrozenSupervisorDomains(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		for _, rejectStart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reject-start=%t", provider, rejectStart), func(t *testing.T) {
				var f processCreateCLI
				if provider == "claude" {
					f = processResumeCLIFixture(t, provider)
					wrapper := "#!/bin/sh\n" + processFixtureExports(f.root) + "if [ -n \"$PMX_INTERNAL_CLAUDE_PROCESS_BINDING\" ]; then exec python3 -u " + fmt.Sprintf("%q", filepath.Join(f.root, "provider.py")) + " \"$@\"; fi\nprintf 'born\\n' >> " + fmt.Sprintf("%q", filepath.Join(f.root, "tmux-provider-births")) + "\nexec /bin/sleep 300\n"
					if err := os.WriteFile(filepath.Join(f.root, provider), []byte(wrapper), 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					f = processCodexRelaunchFixture(t)
					codexHome, err := os.MkdirTemp(os.TempDir(), "c")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(codexHome) })
					t.Setenv("CODEX_HOME", codexHome)
					script := filepath.Join(f.root, "shared.py")
					if err = os.WriteFile(script, []byte(codexHostMoveServerFixture), 0600); err != nil {
						t.Fatal(err)
					}
					control := filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
					server := exec.Command("python3", "-u", script, "serve", control)
					var stderr bytes.Buffer
					server.Stderr = &stderr
					if err = server.Start(); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						_ = server.Process.Kill()
						_ = server.Wait()
						if t.Failed() {
							t.Log(stderr.String())
						}
					})
					ready, done := context.WithTimeout(context.Background(), 10*time.Second)
					defer done()
					processCLIUntil(t, ready, func() bool { _, err := os.Stat(control); return err == nil })
					wrapper := "#!/bin/sh\ncase \"$1 $2\" in\n'--version '| 'app-server proxy' | 'app-server daemon') exec python3 -u " + fmt.Sprintf("%q", script) + " \"$@\";;\n'app-server --listen') exec python3 -u " + fmt.Sprintf("%q", filepath.Join(f.root, "codex-provider.py")) + ";;\nesac\nprintf 'born\\n' >> " + fmt.Sprintf("%q", filepath.Join(f.root, "tmux-provider-births")) + "\nexec /bin/sleep 300\n"
					if err = os.WriteFile(filepath.Join(f.root, provider), []byte(wrapper), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Remove(filepath.Join(f.root, "tmux")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				socket, _ := sdkCreateSocket(t)
				paths := config.Paths{ConfigDir: filepath.Join(f.root, "deep", "settings"), StateDir: filepath.Join(f.root, "deep", "records")}
				original, err := os.ReadFile(f.store.Path())
				if err != nil {
					t.Fatal(err)
				}
				reg, err := f.store.LoadReadOnly()
				if err != nil || len(reg.Agents) != 0 {
					t.Fatal("fixture must have no live source in the default domain", err)
				}
				storage := metadata.NewStore(metadata.PathFor(paths.StateDir))
				if _, err = storage.Update(func(r *m.Registry) error { *r = reg.Clone(); return nil }); err != nil {
					t.Fatal(err)
				}
				sibling := filepath.Join(filepath.Dir(paths.StateDir), "projmux")
				siblingBefore := sdkCreateSeedSibling(t, sibling)
				assertSibling := func(stage string) {
					t.Helper()
					after := sdkCreateDirectorySnapshot(t, sibling)
					if !reflect.DeepEqual(siblingBefore, after) {
						t.Fatalf("sibling changed after %s: before=%v after=%v", stage, siblingBefore, after)
					}
					t.Logf("sibling unchanged after %s", stage)
				}
				c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary, SocketPath: socket})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				providerBirths := func() int {
					data, err := os.ReadFile(filepath.Join(f.root, "tmux-provider-births"))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					return bytes.Count(data, []byte("born\n"))
				}
				t.Cleanup(func() { cleanupSDKNativePeers(t, paths.StateDir) })
				create := core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "none", Host: "tmux", Model: "stub-model", Effort: "low"}
				if provider == "codex" {
					create.Payload = []string{"source context"}
				}
				created, err := c.CreateAgent(ctx, create)
				if err != nil {
					t.Fatal(err)
				}
				assertSibling("SDK tmux Create")
				processCLIUntil(t, ctx, func() bool { return providerBirths() == 1 })
				if provider == "claude" {
					// The persistent shell fixture emits no native hook. Record the
					// same exact process/session evidence as hostMoveCLIFixture,
					// against the SDK's State from the source's first creation.
					pidText, err := exec.CommandContext(ctx, "tmux", "-S", socket, "display-message", "-p", "-t", created.PaneID, "#{pane_pid}").Output()
					if err != nil {
						t.Fatal(err)
					}
					var pid int
					if _, err = fmt.Sscan(string(pidText), &pid); err != nil {
						t.Fatal(err)
					}
					identity, _, err := localipc.Process(pid)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = storage.Update(func(r *m.Registry) error {
						mut := metadata.DefaultMutator()
						p, _ := r.Pane(created.Agent.Status.PaneRef)
						if p.Status.Activation.Claude == nil {
							if err := mut.RecordClaudeProcess(r, p.Metadata.UID, created.Agent.Metadata.UID, p.Status.Activation.Generation, identity); err != nil {
								return err
							}
						}
						if _, _, err := mut.RecordAgentSessionRef(r, created.Agent.Metadata.UID, m.AgentSessionObservation{Provider: provider, SessionID: "process-session"}); err != nil {
							return err
						}
						_, err := mut.SetAgentInteraction(r, created.Agent.Metadata.UID, m.InteractionIdle, "provider-hook")
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				reg, err = storage.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				source, ok := reg.Agent(created.Agent.Metadata.UID)
				activation := source.Status.Activation.State
				if activation == "" {
					activation = m.ActivationNotRequested
				}
				wantActivation := m.ActivationAcknowledged
				if provider == "claude" {
					// This interactive source has no requested initial task. A
					// session hook cannot turn not_requested into acknowledged.
					wantActivation = m.ActivationNotRequested
					p, _ := reg.Pane(source.Status.PaneRef)
					if p.Status.Activation.Claude == nil || !p.Status.Activation.Claude.Process.Valid() {
						t.Fatal("explicit Claude source has no exact process proof")
					}
				}
				if !ok || source.Status.Phase != m.PhaseRunning || source.Status.SessionRef == nil || activation != wantActivation {
					t.Fatalf("explicit source conversation/activation mismatch: %+v", source)
				}
				t.Logf("explicit source provider=%s phase=%s activation=%s", provider, source.Status.Phase, source.Status.Activation.State)
				cleanupPath := os.Getenv("PATH")
				for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
					t.Setenv(key, f.root+"/poison")
				}
				ref := "uid:" + source.Metadata.UID
				t.Cleanup(func() {
					clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					r, err := storage.LoadReadOnly()
					if err != nil {
						t.Error(err)
						return
					}
					p, _ := processResumePane(r, source.Metadata.UID)
					if p != nil && p.Status.Activation.Process != nil {
						if _, err = c.StopAgent(clean, core.StopAgentRequest{Ref: ref}); err != nil {
							t.Error(err)
							_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK host move cleanup unconfirmed"), 0600)
						}
					}
				})
				if rejectStart {
					if err = os.WriteFile(filepath.Join(f.root, provider), []byte("#!"+filepath.Join(f.root, "missing-interpreter")+"\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				q := core.RelaunchAgentRequest{Ref: ref, Host: "process", Detach: true, Confirm: true, SocketPath: socket}
				if provider == "claude" {
					q.Prompt = "continue"
				}
				moved, err := c.RelaunchAgent(ctx, q)
				assertSibling("tmux to process Relaunch")
				if rejectStart {
					var detail *projmux.RelaunchProcessError
					if !errors.As(err, &detail) || detail.Owner.PID == 0 || errors.Unwrap(detail) == nil {
						t.Fatalf("startup refusal lost typed owner/cause: result=%+v error=%v", moved, err)
					}
				} else {
					if err != nil || moved.Binding.AgentUID != source.Metadata.UID || moved.Binding.Generation == "" {
						t.Fatalf("host move %+v: %v", moved, err)
					}
					stopped, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: moved.Binding})
					if err != nil || !stopped.Stopped || stopped.Termination == nil {
						t.Fatalf("exact Stop %+v: %v", stopped, err)
					}
					assertSibling("exact process Stop")
					r, err := storage.LoadReadOnly()
					if err != nil {
						t.Fatal(err)
					}
					p, _ := r.Pane(moved.Binding.PaneUID)
					if !m.MatchesProcessWait(p.Status.ProcessSession.Binding, p.Status.LastTermination) || p.Status.Activation.Process != nil {
						t.Fatal("host move lost actual Wait/retirement")
					}
					back, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Host: "tmux", Confirm: true, SocketPath: socket})
					if err != nil || back.AgentUID != source.Metadata.UID || back.TargetHost != "tmux" || back.NewPaneUID == "" {
						t.Fatalf("reverse host move %+v: %v", back, err)
					}
					for providerBirths() != 2 {
						select {
						case <-ctx.Done():
							t.Fatalf("reverse provider birth missing: want 2 births, got %d: %v", providerBirths(), ctx.Err())
						case <-time.After(10 * time.Millisecond):
						}
					}
					assertSibling("process to tmux Relaunch")
				}
				r, err := storage.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				a, _ := r.Agent(source.Metadata.UID)
				if !a.Status.SessionRef.SameConversation(source.Status.SessionRef) {
					t.Fatal("host move changed conversation")
				}
				if after := sdkCreateDirectorySnapshot(t, sibling); !reflect.DeepEqual(siblingBefore, after) {
					t.Fatalf("host move changed sibling Config/State: before=%v after=%v", siblingBefore, after)
				}
				if after, err := os.ReadFile(f.store.Path()); err != nil || !bytes.Equal(original, after) {
					t.Fatalf("host move changed default Registry: %v", err)
				}
				journal, err := os.ReadFile(filepath.Join(paths.StateDir, "logs", "operations.jsonl"))
				if err != nil || len(bytes.TrimSpace(journal)) == 0 {
					t.Fatalf("host move journal escaped explicit State: %v", err)
				}
				for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
					if !json.Valid(line) {
						t.Fatalf("invalid explicit journal row: %q", line)
					}
				}
				// Delete's existing tmux runner resolves PATH in the caller. All
				// host-move assertions above retain poisoned ambient inputs;
				// restore only PATH for this separate cleanup operation.
				t.Setenv("PATH", cleanupPath)
				if _, err = c.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: ref, Confirm: true, SocketPath: socket}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestSDKDetachedProcessRelaunchBusyStopsNothing(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := processResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--effort", "low", "--", "question"))
	defer first.shutdown(t)
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool { return len(r.Pending) > 0 })
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.store.LoadReadOnly()
	wireBefore, _ := os.ReadFile(f.trace)
	_, err = c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: first.ref, Detach: true, Prompt: "unconfirmed"})
	var refused *projmux.Refusal
	if !errors.As(err, &refused) || refused.Code != projmux.CodeRelaunchAgentBusy || strings.Contains(err.Error(), "cleanup-unconfirmed") {
		t.Fatalf("busy refusal: %v", err)
	}
	after, _ := f.store.LoadReadOnly()
	wireAfter, _ := os.ReadFile(f.trace)
	if !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wireAfter) {
		t.Fatal("SDK busy refusal wrote state/provider frame")
	}
}

type sdkRelaunchCanceledOutput struct{ cancel context.CancelFunc }

func (w sdkRelaunchCanceledOutput) Write([]byte) (int, error) {
	w.cancel()
	return 0, errors.New("fixture-relaunch-output-failed")
}

// Golden CLI diagnostics are measured with a copied main 6c875e75 binary.
// No legacy relaunch verifier is retained in the product for this comparison.
func TestSDKDetachedProcessRelaunchCLIErrorParity(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, mode := range []string{"canceled-output", "stale-source"} {
		t.Run(mode, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, "claude")
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			cleanupDetachedResume(t, f, first.ref)
			caller, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			var text []string
			for _, shared := range []bool{false, true} {
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				a, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
				p, _ := reg.Pane(a.Status.PaneRef)
				clone := p.Clone()
				wire := processDetachedRelaunchRequest{Source: a.Clone(), Pane: &clone, Args: []string{first.ref, "--detach", "--yes", "--", "continue"}}
				if mode == "stale-source" {
					wire.Source.Status.SessionRef = nil
				}
				admission, abort := context.WithCancel(ctx)
				var output io.Writer = io.Discard
				if mode == "canceled-output" {
					output = sdkRelaunchCanceledOutput{abort}
				}
				if shared {
					_, err = mutationruntime.LaunchDetachedProcessRelaunch(admission, f.binary, mutationruntime.ProcessDetachedRelaunchEnvelope{Relaunch: &wire, Caller: caller, RegistryPath: f.store.Path()}, f.store.LoadReadOnly, output, io.Discard, nil, false)
				} else {
					err = launchProcessDetached(admission, f.binary, processDetachedRequest{Relaunch: &wire, Caller: caller, RegistryPath: f.store.Path()}, &createCommand{store: &resourceStore{snapshot: f.store.LoadReadOnly}}, output, io.Discard)
				}
				abort()
				if err == nil {
					t.Fatal("expected deterministic refusal/output failure")
				}
				text = append(text, err.Error())
			}
			if text[0] != text[1] {
				t.Fatalf("CLI error text changed from main adapter: before=%q after=%q", text[0], text[1])
			}
			golden := "stale process binding or request"
			if mode == "canceled-output" {
				golden = "fixture-relaunch-output-failed"
			}
			if text[0] != golden {
				t.Fatalf("main CLI golden changed: got=%q want=%q", text[0], golden)
			}
			t.Logf("main/shared CLI %s error=%q", mode, text[0])
		})
	}
}

// Host-lost recovery deliberately remains CLI-only. Restoring the captured
// Running record after retiring our fixture processes supplies no actual Wait
// evidence and must not admit a Stop, replacement writer, or Offline success.
func TestSDKDetachedProcessRelaunchLostHostRefuses(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
			wireBefore, err := os.ReadFile(f.trace)
			if err != nil {
				t.Fatal(err)
			}
			c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, Prompt: "unsupported recovery"})
			var refused *projmux.Refusal
			var detail *projmux.RelaunchProcessError
			unavailable := err != nil && (strings.Contains(err.Error(), "process-host-unavailable") || strings.Contains(err.Error(), "has no current process ownership"))
			if !errors.As(err, &refused) || !errors.As(err, &detail) || (refused.Code != projmux.CodeRelaunchExecutionFailed && refused.Code != projmux.CodeRelaunchNoConversation) || detail.Owner.PID <= 0 || !unavailable || result.Binding != (core.ProcessBinding{}) || result.ClaimWaiting {
				t.Fatalf("lost-host admission: result=%+v error=%v", result, err)
			}
			after := mustRegistry(t, f)
			wireAfter, readErr := os.ReadFile(f.trace)
			if readErr != nil || !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wireAfter) {
				t.Fatalf("lost-host refusal wrote Registry/provider frames: %v", readErr)
			}
			a, _ := after.Agent(old.Binding.AgentUID)
			p, _ := after.Pane(old.Binding.PaneUID)
			if a.Status.Phase != m.PhaseRunning || p.Status.LastTermination != nil || a.Status.LastTermination != nil || p.Status.ProcessSession.Binding != old.Binding {
				t.Fatal("lost-host refusal fabricated termination or a new generation")
			}
		})
	}
}
