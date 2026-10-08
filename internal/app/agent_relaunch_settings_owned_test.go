package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// One opt-in topcase extends the copied-product inventory. The existing guarded
// supervisor init and provider fixtures supply the actual production ownership.
func TestOwnedProcessSettingsActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	assertOffline := func(t *testing.T, f processCreateCLI, life *agentHostTransferLifetime) coremetadata.Registry {
		t.Helper()
		reg := mustRegistry(t, f)
		agent, ok := reg.Agent(life.Target.Binding.Agent)
		pane, present := reg.Pane(life.Target.Binding.Pane)
		if !ok || !present || !life.Target.owner.waitRecorded || agent.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(metadataProcessBinding(life.Target.Binding), pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
			t.Fatal("missing exact durable actual Wait/Offline")
		}
		return reg
	}
	assertSurvives := func(t *testing.T, ctx context.Context, life *agentHostTransferLifetime) {
		t.Helper()
		bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		snapshot, err := life.Target.Handle.Wait(bounded, life.Target.Binding)
		if !errors.Is(err, context.DeadlineExceeded) || snapshot.Exit != nil || life.Context.Err() != nil {
			t.Fatalf("owned lifetime did not survive producer/old cancellation: %v %+v", err, snapshot)
		}
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, mode := range []string{"cancel", "registration-failure", "shutdown", "concurrent-wait", "admission-cancel"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				var f processCreateCLI
				if provider == aiModeClaude {
					f = deferredRelaunchFixture(t)
				} else {
					f = processCodexRelaunchFixture(t)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer cancel()
				prompt := "initial"
				if provider == aiModeCodex {
					prompt = "controls"
				}
				first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", prompt))
				old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return processRelaunchConversation(r) != "" && (provider == aiModeClaude || len(r.Pending) > 0)
				})
				if provider == aiModeClaude {
					deferredReady(t, ctx, f, first.ref)
				}
				command := New().agent
				reg := mustRegistry(t, f)
				source, _ := reg.Agent(old.Binding.AgentUID)
				request, err := parseAgentRelaunchArgs([]string{first.ref, "--model", "new-model", "--effort", "high", "--yes"}, io.Discard)
				if err != nil {
					t.Fatal(err)
				}
				if provider == aiModeClaude {
					request.prompt = []string{"initial"}
				}
				wireBefore, _ := os.ReadFile(f.trace)
				previewRequest := request
				previewRequest.dryRun = true
				preview, err := command.runOwnedProcessRelaunch(ctx, reg, source.Clone(), previewRequest)
				if err != nil || preview.State != agentProcessPreview || preview.Owned != nil || preview.Prepared != nil || preview.Result.AgentUID != source.Metadata.UID {
					t.Fatalf("typed preview=%+v err=%v", preview, err)
				}
				canceled, cancelAdmission := context.WithCancel(ctx)
				cancelAdmission()
				refused, err := command.runOwnedProcessRelaunch(canceled, reg, source.Clone(), request)
				if !errors.Is(err, context.Canceled) || refused.State != "" || refused.Owned != nil {
					t.Fatalf("pre-admission cancellation=%+v err=%v", refused, err)
				}
				after := mustRegistry(t, f)
				wire, _ := os.ReadFile(f.trace)
				if !reflect.DeepEqual(reg, after) || !bytes.Equal(wireBefore, wire) {
					t.Fatal("preview/pre-admission cancellation wrote Registry/provider")
				}
				if mode == "admission-cancel" {
					// The existing generation seam cancels after real old Stop/Wait,
					// before reservation. No new provider or fake owner is introduced.
					producer, producerCancel := context.WithCancel(ctx)
					defer producerCancel()
					command.rebind.create.newGeneration = func() (string, error) {
						producerCancel()
						return "", context.Canceled
					}
					refused, err := command.runOwnedProcessRelaunch(producer, reg, source.Clone(), request)
					if !errors.Is(err, context.Canceled) || refused.Owned != nil || refused.State != "" {
						t.Fatalf("admitted cancellation=%+v err=%v", refused, err)
					}
					_ = first.cmd.Wait()
					first.done = true
					after = mustRegistry(t, f)
					current, _ := after.Agent(old.Binding.AgentUID)
					pane, _ := after.Pane(old.Binding.PaneUID)
					if current.Status.Phase != coremetadata.PhaseOffline || !reflect.DeepEqual(current.Metadata.Annotations, source.Metadata.Annotations) || !coremetadata.MatchesProcessWait(old.Binding, pane.Status.LastTermination) || pane.Status.ProcessSession.Binding != old.Binding || processRelaunchConversation(pane.Status.ProcessSession) != processRelaunchConversation(&old) {
						t.Fatal("pre-handoff cancellation lost old exact retirement/recipe")
					}
					return
				}
				producer, producerCancel := context.WithCancel(ctx)
				defer producerCancel()
				applied, err := command.runOwnedProcessRelaunch(producer, reg, source.Clone(), request)
				producerCancel()
				if err != nil || applied.State != agentProcessOwned || applied.Owned == nil || applied.Prepared != nil {
					t.Fatalf("typed owned=%+v err=%v", applied, err)
				}
				life := applied.Owned
				defer func() { life.Cancel(); _ = life.Wait(context.Background()) }()
				_ = first.cmd.Wait()
				first.done = true
				if life.Target.Binding.Agent != old.Binding.AgentUID || life.Target.Binding.Pane != old.Binding.PaneUID || life.Target.Binding.Generation == old.Binding.Generation || applied.Result.NewPaneUID != life.Target.Binding.Pane {
					t.Fatal("typed target differs from actual replacement")
				}
				if command.hostTransferContext != nil || command.hostTransferResult != nil {
					t.Fatal("producer injection escaped command copy")
				}
				assertSurvives(t, ctx, life)
				after = mustRegistry(t, f)
				current, _ := after.Agent(old.Binding.AgentUID)
				pane, _ := after.Pane(old.Binding.PaneUID)
				if !reflect.DeepEqual(current.Spec, source.Spec) || !current.Status.SessionRef.SameConversation(source.Status.SessionRef) || processRelaunchConversation(pane.Status.ProcessSession) != processRelaunchConversation(&old) || current.Metadata.Annotations[coremetadata.AnnotationAgentModel] != "new-model" || current.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != "high" {
					t.Fatal("settings application lost recipe/conversation")
				}
				if pane.Status.ProcessSession.History == nil || pane.Status.ProcessSession.History.Binding != old.Binding || len(pane.Status.ProcessSession.History.Expired) != len(old.Pending) {
					t.Fatal("settings apply lost original pending fence/history")
				}
				// The actual owner must still reject a self-targeted settings call.
				rejected, err := command.runOwnedProcessRelaunch(ctx, after, current.Clone(), request)
				if err == nil || rejected.Owned != nil || !strings.Contains(err.Error(), relaunchReasonSelfTarget) {
					t.Fatalf("self guard=%+v err=%v", rejected, err)
				}
				var consumed atomic.Int32
				attention := life.synchronization.attention
				life.synchronization.attention = func() error { consumed.Add(1); return attention() }
				consumer, consumerCancel := context.WithCancel(ctx)
				defer consumerCancel()
				done := make(chan error, 2)
				waits := 1
				if mode == "registration-failure" {
					life.Cancel()
					done <- life.Wait(ctx)
				} else {
					if mode == "concurrent-wait" {
						waits = 2
					}
					start := make(chan struct{})
					for range waits {
						go func() { <-start; done <- life.Wait(consumer) }()
					}
					close(start)
					if mode == "shutdown" {
						consumerCancel()
					} else {
						life.Cancel()
					}
				}
				var waitError error
				for i := range waits {
					select {
					case err := <-done:
						if i == 0 {
							waitError = err
						} else if err != waitError {
							t.Fatal("concurrent Wait returned different errors")
						}
					case <-ctx.Done():
						t.Fatal("exact Wait did not finish")
					}
				}
				if consumed.Load() != 1 {
					t.Fatal("concurrent Wait consumed synchronization more than once")
				}
				after = assertOffline(t, f, life)
				if mode == "concurrent-wait" {
					// Fresh Offline reapplication is the supported two-stage route.
					current, _ = after.Agent(old.Binding.AgentUID)
					next, err := command.runOwnedProcessRelaunch(ctx, after, current.Clone(), request)
					if err != nil || next.State != agentProcessOwned || next.Owned == nil || next.Owned.Target.Binding.Generation == life.Target.Binding.Generation {
						t.Fatalf("fresh Offline apply=%+v err=%v", next, err)
					}
					defer func() { next.Owned.Cancel(); _ = next.Owned.Wait(context.Background()) }()
					life.Cancel()
					assertSurvives(t, ctx, next.Owned)
					next.Owned.Cancel()
					_ = next.Owned.Wait(ctx)
					assertOffline(t, f, next.Owned)
				}
			})
		}
	}

	t.Run("codex/unchanged", func(t *testing.T) {
		f := processCodexRelaunchFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
		awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ThreadID != "" && r.TurnID == "" })
		reg := mustRegistry(t, f)
		source, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
		wireBefore, _ := os.ReadFile(f.trace)
		result, err := New().agent.runOwnedProcessRelaunch(ctx, reg, source.Clone(), agentRelaunchRequest{agentRef: first.ref})
		wireAfter, _ := os.ReadFile(f.trace)
		if err != nil || result.State != agentProcessUnchanged || !result.Result.Unchanged || result.Owned != nil || result.Prepared != nil || !reflect.DeepEqual(reg, mustRegistry(t, f)) || !bytes.Equal(wireBefore, wireAfter) {
			t.Fatalf("unchanged=%+v err=%v", result, err)
		}
		first.shutdown(t)
	})

	for _, mode := range []string{"settings", "resume-without-plan", "failed-init"} {
		t.Run("claude/prepared/"+mode, func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = persona.NewDefaultStore(paths).Write("settings-owned", []byte("Retain these fixture instructions.")); err != nil {
				t.Fatal(err)
			}
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--instructions", "settings-owned", "--model", "stub-model", "--effort", "low", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			first.shutdown(t)
			c := New().agent
			uid := strings.TrimPrefix(first.ref, "uid:")
			reg := mustRegistry(t, f)
			source, _ := reg.Agent(uid)
			pane, _ := processResumePane(reg, uid)
			oldWait := pane.Status.LastTermination
			oldRecord := pane.Status.ProcessSession.Clone()
			opts := processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: uid}}
			wireBefore, _ := os.ReadFile(f.trace)
			var prepared agentHostTransferResult
			if mode == "resume-without-plan" {
				prepared, err = c.prepareOwnedClaudeResume(ctx, opts)
			} else {
				prepared, err = c.runOwnedProcessRelaunch(ctx, reg, source.Clone(), agentRelaunchRequest{agentRef: first.ref, model: "new-model", effort: "high"})
			}
			if err != nil || prepared.State != agentProcessPrepared || prepared.Prepared == nil || prepared.Owned != nil || prepared.Prepared.Conversation != oldRecord.SessionID {
				t.Fatalf("Prepared=%+v err=%v", prepared, err)
			}
			wireAfter, _ := os.ReadFile(f.trace)
			claimRecord, err := readDeferredClaim(c.deferredClaimPath(uid))
			after := mustRegistry(t, f)
			current, _ := after.Agent(uid)
			pane, _ = after.Pane(oldRecord.Binding.PaneUID)
			if err != nil || claimRecord.Nonce != "" || len(deferredArgv(t, f)) != 1 || !bytes.Equal(wireBefore, wireAfter) || current.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || !processResumeRecordEqual(pane.Status.ProcessSession, oldRecord) || !coremetadata.SameProcessWait(oldWait, pane.Status.LastTermination) || !current.Status.SessionRef.SameConversation(source.Status.SessionRef) || !reflect.DeepEqual(current.Spec, source.Spec) {
				t.Fatal("Prepared retained a child/claim or lost retired conversation/Wait")
			}
			if mode == "resume-without-plan" && (current.Metadata.Annotations[coremetadata.AnnotationAgentModel] != source.Metadata.Annotations[coremetadata.AnnotationAgentModel] || current.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != source.Metadata.Annotations[coremetadata.AnnotationAgentEffort]) {
				t.Fatal("no-firstinput Resume changed model/effort")
			}
			frozen, err := c.readDeferredLaunch(uid)
			if err != nil || frozen == nil || !frozen.matches(deferredCandidate(t, f, first.ref)) || deferredLaunchDigest(frozen) != prepared.Prepared.LaunchDigest {
				t.Fatal("Prepared lacks durable recipe proof", err)
			}
			launchPath := c.deferredStatePath("deferred-launches", uid)
			launchBefore, _ := os.ReadFile(launchPath)
			// Consume the adopted validator through the private producer; a legacy
			// frozen mode must be refused without recovery, replacement or a child.
			if err = frozen.validatePermissionMode(); err != nil {
				t.Fatal("prepared auto recipe", err)
			}
			legacy := *frozen
			legacy.Command.Args = append([]string(nil), frozen.Command.Args...)
			changedMode := false
			for i, arg := range legacy.Command.Args {
				if arg == "--permission-mode" && i+1 < len(legacy.Command.Args) {
					legacy.Command.Args[i+1] = "default"
					changedMode = true
					break
				}
				if arg == "--permission-mode=auto" {
					legacy.Command.Args[i] = "--permission-mode=default"
					changedMode = true
					break
				}
			}
			if !changedMode {
				t.Fatal("common planner auto mode missing")
			}
			if err = writeDeferredState(launchPath, &legacy); err != nil {
				t.Fatal(err)
			}
			legacyBefore, _ := os.ReadFile(launchPath)
			_, refusal := c.prepareOwnedClaudeResume(ctx, opts)
			legacyAfter, _ := os.ReadFile(launchPath)
			wireLegacy, _ := os.ReadFile(f.trace)
			unchangedLegacy := reflect.DeepEqual(after, mustRegistry(t, f)) && bytes.Equal(legacyBefore, legacyAfter) && bytes.Equal(wireAfter, wireLegacy)
			if err = os.WriteFile(launchPath, launchBefore, 0600); err != nil {
				t.Fatal(err)
			}
			if refusal == nil || !unchangedLegacy {
				t.Fatal("legacy prepared Resume recovered, wrote or spawned")
			}
			reused, err := c.prepareOwnedClaudeResume(ctx, opts)
			launchAfter, _ := os.ReadFile(launchPath)
			if err != nil || reused.State != agentProcessPrepared || !reflect.DeepEqual(prepared.Prepared, reused.Prepared) || !bytes.Equal(launchBefore, launchAfter) || !reflect.DeepEqual(after, mustRegistry(t, f)) || len(deferredArgv(t, f)) != 1 {
				t.Fatalf("existing prepared Resume changed its recipe=%+v err=%v", reused, err)
			}
			// A conflicting override never replaces the frozen recipe.
			override := opts
			override.Model = "conflicting-model"
			if _, err = c.prepareOwnedClaudeResume(ctx, override); err == nil {
				t.Fatal("conflicting frozen recipe accepted")
			}
			// Snapshot validation must refuse without clearing the durable plan.
			if len(frozen.Files) == 0 {
				t.Fatal("fixture did not freeze a referenced snapshot")
			}
			for path := range frozen.Files {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, append(bytes.Clone(raw), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = c.prepareOwnedClaudeResume(ctx, opts)
				if restoreErr := os.WriteFile(path, raw, 0600); restoreErr != nil {
					t.Fatal(restoreErr)
				}
				if err == nil {
					t.Fatal("changed snapshot accepted")
				}
				break
			}
			launchAfter, _ = os.ReadFile(launchPath)
			if !bytes.Equal(launchBefore, launchAfter) || !reflect.DeepEqual(after, mustRegistry(t, f)) {
				t.Fatal("prepared refusal changed durable recipe/source")
			}
			// A different current recipe is not authority to reuse the frozen plan.
			_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				a, _ := reg.Agent(uid)
				a.Metadata.Annotations[coremetadata.AnnotationAgentModel] = "foreign-model"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			foreign := mustRegistry(t, f)
			if _, err = c.prepareOwnedClaudeResume(ctx, opts); err == nil {
				t.Fatal("changed current source accepted")
			}
			launchAfter, _ = os.ReadFile(launchPath)
			if !bytes.Equal(launchBefore, launchAfter) || !reflect.DeepEqual(foreign, mustRegistry(t, f)) {
				t.Fatal("source refusal changed recipe/pending")
			}
			_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				a, _ := reg.Agent(uid)
				a.Metadata.Annotations[coremetadata.AnnotationAgentModel] = current.Metadata.Annotations[coremetadata.AnnotationAgentModel]
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := c.claimDeferredProcessAgent(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = claim.Close() }()
			if mode == "failed-init" {
				path := filepath.Join(f.root, "provider.py")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				broken := bytes.ReplaceAll(raw, []byte("emit({'type':'system','subtype':'init','session_id':'process-session'})"), []byte("emit({'type':'system','subtype':'init','session_id':'wrong-session'})"))
				if bytes.Equal(raw, broken) {
					t.Fatal("existing init fixture anchor missing")
				}
				if err = os.WriteFile(path, broken, 0600); err != nil {
					t.Fatal(err)
				}
				_, err = claim.Resume(ctx, processResumeFirstFrame{Kind: "user", Text: "actual first input"})
				if err == nil {
					t.Fatal("wrong-session init accepted")
				}
				if err = claim.Close(); err != nil {
					t.Fatal(err)
				}
				retained, readErr := c.readDeferredLaunch(uid)
				candidate := deferredCandidate(t, f, first.ref)
				if readErr != nil || retained == nil || !retained.matches(candidate) || !reflect.DeepEqual(retained.Command, frozen.Command) || candidate.Record.SessionID != oldRecord.SessionID || !coremetadata.MatchesProcessWait(candidate.Record.Binding, candidate.Pane.Status.LastTermination) {
					t.Fatal("failed spawn lost durable recipe or exact retirement", readErr)
				}
				return
			}
			// Hold the first real resume before reservation, using the existing
			// generation seam; concurrent first input cannot become a second writer.
			entered, release := make(chan struct{}), make(chan struct{})
			c.rebind.create.newGeneration = func() (string, error) {
				close(entered)
				select {
				case <-release:
					return coremetadata.NewGeneration()
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			type resumed struct {
				owned processAgentResumeResult
				err   error
			}
			done := make(chan resumed, 1)
			literal := "actual raw first input\nwith a literal newline"
			go func() {
				owned, err := claim.Resume(ctx, processResumeFirstFrame{Kind: "user", Text: literal})
				done <- resumed{owned, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first-input reservation barrier not reached")
			}
			beforeRace := mustRegistry(t, f)
			loser, err := claim.Resume(ctx, processResumeFirstFrame{Kind: "user", Text: "losing input"})
			if err == nil || loser.Handle != nil || !reflect.DeepEqual(beforeRace, mustRegistry(t, f)) {
				t.Fatal("losing first input changed winner/pending")
			}
			close(release)
			var winner resumed
			select {
			case winner = <-done:
			case <-ctx.Done():
				t.Fatal("first input did not finish")
			}
			if winner.err != nil || winner.owned.Handle == nil {
				t.Fatal("first input resume", winner.err)
			}
			changed, controls, attention, err := winner.owned.resumeSynchronization(c.rebind.create)
			if err != nil {
				_ = winner.owned.fail(err)
				t.Fatal(err)
			}
			consumer, consumerCancel := context.WithCancel(context.Background())
			life := &agentHostTransferLifetime{Context: consumer, Cancel: consumerCancel, Target: &winner.owned, synchronization: processRelaunchSynchronization{changed, controls, attention}}
			defer func() { life.Cancel(); _ = life.Wait(context.Background()) }()
			deferredReady(t, ctx, f, first.ref)
			currentRecord := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != oldRecord.Binding.Generation && r.SessionID == oldRecord.SessionID
			})
			if currentRecord.History == nil || currentRecord.History.Binding != oldRecord.Binding {
				t.Fatal("first input lost old pending/history fence")
			}
			texts := deferredWireTexts(t, f)
			if len(texts) != 2 || texts[1] != literal || strings.Contains(texts[1], "projmux-coordination") || len(deferredArgv(t, f)) != 2 {
				t.Fatal("first input duplicated/replaced raw user frame")
			}
			if remaining, err := c.readDeferredLaunch(uid); err != nil || remaining != nil {
				t.Fatal("successful init did not consume exact plan", err)
			}
			if record, err := readDeferredClaim(c.deferredClaimPath(uid)); err != nil || record.Nonce != "" {
				t.Fatal("first input left an active claim", err)
			}
			latest := mustRegistry(t, f)
			bound, _ := latest.Agent(uid)
			if !bound.Status.SessionRef.SameConversation(source.Status.SessionRef) {
				t.Fatal("first input lost sessions binding")
			}
			life.Cancel()
			_ = life.Wait(ctx)
			assertOffline(t, f, life)
		})
	}
}
