package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// The production transfer starts its supervisor through os.Executable. In
// this copied fixture executable, dispatch only that exact production route.
// The supervisor receives only the provider environment allowlist, so the
// exact argv selects the dispatch rather than a test marker; go test never
// passes it.
func init() {
	if len(os.Args) == 3 && os.Args[1] == "internal" && os.Args[2] == "process-host-supervisor" {
		if err := Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func TestOwnedHostTransferActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, mode := range []string{"cancel", "registration-failure", "shutdown", "reverse", "cli-eof", "cli-output-failure"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				var f processCreateCLI
				var source coremetadata.Agent
				var oldPane, socket string
				if provider == aiModeClaude {
					f, source, oldPane, socket = hostMoveCLIFixture(t)
				} else {
					f, source, oldPane, socket = codexHostMoveCLIFixture(t)
				}
				historyPath := sessionhistory.Path(filepath.Dir(filepath.Dir(f.store.Path())))
				beforeHistory, beforeHistoryErr := os.ReadFile(historyPath)
				if beforeHistoryErr != nil && !os.IsNotExist(beforeHistoryErr) {
					t.Fatal(beforeHistoryErr)
				}
				command := New().agent
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"uid:" + source.Metadata.UID, "--host", "process", "--socket-path", socket, "--yes", "-o", "json"}
				if provider == aiModeClaude {
					args = append(args, "--", "continue")
				}
				request, err := parseAgentRelaunchArgs(args, io.Discard)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
				defer cancel()
				previewRequest := request
				previewRequest.dryRun = true
				preview, err := command.runOwnedHostRelaunch(ctx, reg, source, previewRequest)
				if err != nil || preview.Owned != nil || preview.Result.CurrentHost != "tmux" || preview.Result.TargetHost != "process" {
					t.Fatalf("preview=%+v err=%v", preview, err)
				}
				after, _ := f.store.LoadReadOnly()
				if !reflect.DeepEqual(reg, after) {
					t.Fatal("preview wrote Registry")
				}
				canceled, cancelAdmission := context.WithCancel(ctx)
				cancelAdmission()
				refused, err := command.runOwnedHostRelaunch(canceled, reg, source, request)
				if !errors.Is(err, context.Canceled) || refused.Owned != nil {
					t.Fatalf("pre-admission result=%+v err=%v", refused, err)
				}
				after, _ = f.store.LoadReadOnly()
				if !reflect.DeepEqual(reg, after) {
					t.Fatal("pre-admission cancellation wrote Registry")
				}

				if mode == "cli-output-failure" {
					err := command.runHostRelaunch(reg, source, request, failingWriter{}, io.Discard)
					if err == nil || !strings.Contains(err.Error(), "preview rejected") {
						t.Fatalf("output failure: %v", err)
					}
					after, _ := f.store.LoadReadOnly()
					current, _ := after.Agent(source.Metadata.UID)
					if current.Status.Phase != coremetadata.PhaseOffline || !current.Status.SessionRef.SameConversation(source.Status.SessionRef) {
						t.Fatalf("failed output lost recovery: %+v", current)
					}
					if provider == aiModeCodex {
						journal, err := command.codexHostTransferPath(source.Metadata.UID)
						if err != nil {
							t.Fatal(err)
						}
						pending, err := readCodexHostTransfer(journal)
						if err != nil || pending == nil {
							t.Fatalf("missing fenced recovery: %+v %v", pending, err)
						}
						beforeRegistry, err := os.ReadFile(f.store.Path())
						if err != nil {
							t.Fatal(err)
						}
						beforeJournal, err := os.ReadFile(journal)
						if err != nil {
							t.Fatal(err)
						}
						rejected, err := command.runOwnedHostRelaunch(ctx, after, current.Clone(), request)
						if err == nil || rejected.Owned != nil || !strings.Contains(err.Error(), "requires CLI recovery") || !strings.Contains(err.Error(), codexTransferRecoveryCommand(current.Metadata.UID, request)) {
							t.Fatalf("typed recovery: %+v %v", rejected, err)
						}
						afterRegistry, registryErr := os.ReadFile(f.store.Path())
						afterJournal, journalErr := os.ReadFile(journal)
						if registryErr != nil || journalErr != nil || !bytes.Equal(beforeRegistry, afterRegistry) || !bytes.Equal(beforeJournal, afterJournal) {
							t.Fatal("typed pending refusal mutated recovery")
						}
					} else if !reflect.DeepEqual(current.Spec, source.Spec) {
						t.Fatal("failed output changed recipe")
					}
					return
				}
				if mode == "cli-eof" {
					flags := []string{"--host", "process", "--socket-path", socket, "--yes"}
					if provider == aiModeClaude {
						flags = append(flags, "--", "continue")
					}
					owner, moved := startProcessRelaunchCLI(t, ctx, f, request.agentRef, flags...)
					record := awaitProcessResumeRecord(t, ctx, f, request.agentRef, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" || r.ThreadID != "" })
					if moved.NewPaneUID != record.Binding.PaneUID {
						t.Fatal("CLI returned different target")
					}
					_ = owner.input.Close()
					_ = owner.cmd.Wait()
					owner.done = true
					after, _ := f.store.LoadReadOnly()
					current, _ := after.Agent(source.Metadata.UID)
					pane, ok := after.Pane(record.Binding.PaneUID)
					if !ok || current.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(record.Binding, pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, current.Status.LastTermination) {
						t.Fatal("EOF lost actual Wait")
					}
					return
				}

				producer, producerCancel := context.WithCancel(ctx)
				transfer, err := command.runOwnedHostRelaunch(producer, reg, source, request)
				producerCancel() // HTTP/request scope ends immediately after return.
				if err != nil || transfer.Owned == nil {
					t.Fatalf("typed transfer=%+v err=%v", transfer, err)
				}
				life := transfer.Owned
				defer func() { life.Cancel(); _ = life.Wait(context.Background()) }()
				if transfer.Result.NewPaneUID == oldPane || transfer.Result.NewPaneUID != life.Target.Binding.Pane || life.Target.Binding.Agent != source.Metadata.UID {
					t.Fatal("result does not own exact replacement")
				}
				if command.hostTransferResult != nil || command.hostTransferContext != nil {
					t.Fatal("injection escaped command copy")
				}
				// Bounded real Wait proves both early return and survival beyond producer
				// cancellation; observing only an immediate snapshot could miss late cancel.
				poll, pollCancel := context.WithTimeout(ctx, 150*time.Millisecond)
				snapshot, waitErr := life.Target.Handle.Wait(poll, life.Target.Binding)
				pollCancel()
				if !errors.Is(waitErr, context.DeadlineExceeded) || snapshot.Exit != nil || life.Context.Err() != nil {
					t.Fatalf("producer killed child: %+v %v", snapshot, waitErr)
				}
				record := awaitProcessResumeRecord(t, ctx, f, request.agentRef, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" || r.ThreadID != "" })
				after, _ = f.store.LoadReadOnly()
				current, _ := after.Agent(source.Metadata.UID)
				if _, ok := after.Pane(oldPane); ok {
					t.Fatal("old tmux Pane remains")
				}
				if !reflect.DeepEqual(current.Spec, source.Spec) || current.Metadata.OwnerUID() != source.Metadata.OwnerUID() || current.Metadata.Annotations[coremetadata.AnnotationCreatorAgent] != source.Metadata.Annotations[coremetadata.AnnotationCreatorAgent] {
					t.Fatal("recipe/Window/creator changed")
				}
				wire, _ := os.ReadFile(f.trace)
				if provider == aiModeCodex && bytes.Contains(wire, []byte("turn/start")) {
					t.Fatal("Codex no-prompt synthesized turn")
				}

				done := make(chan error, 1)
				waited := false
				consumer, consumerCancel := context.WithCancel(ctx)
				defer consumerCancel()
				if mode == "registration-failure" {
					// Registration refused: there is no installed consumer to clean up for us.
					life.Cancel()
					done <- life.Wait(ctx)
				} else {
					go func() { done <- life.Wait(consumer) }()
					if mode == "reverse" {
						backRequest := request
						backRequest.host, backRequest.prompt = "tmux", nil
						// Same-process ownership remains fenced while the target is live.
						rejected, err := command.runOwnedHostRelaunch(ctx, after, current.Clone(), backRequest)
						if err == nil || rejected.Owned != nil || !strings.Contains(err.Error(), relaunchReasonSelfTarget) {
							t.Fatalf("live owner bypassed self fence: %+v %v", rejected, err)
						}
						life.Cancel()
						select {
						case <-done:
						case <-ctx.Done():
							t.Fatal("reverse retirement Wait did not finish")
						}
						waited = true
						after, err = f.store.LoadReadOnly()
						if err != nil {
							t.Fatal(err)
						}
						current, _ = after.Agent(source.Metadata.UID)
						retiredPane, ok := after.Pane(record.Binding.PaneUID)
						if !ok || !life.Target.owner.waitRecorded || current.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(record.Binding, retiredPane.Status.LastTermination) || !coremetadata.SameProcessWait(retiredPane.Status.LastTermination, current.Status.LastTermination) {
							t.Fatal("reverse started before exact durable retirement")
						}
						backPreview := backRequest
						backPreview.dryRun = true
						preview, err := command.runOwnedHostRelaunch(ctx, after, current.Clone(), backPreview)
						if err != nil || preview.Owned != nil {
							t.Fatalf("reverse preview: %+v %v", preview, err)
						}
						unchanged, _ := f.store.LoadReadOnly()
						if !reflect.DeepEqual(after, unchanged) {
							t.Fatal("reverse preview wrote Registry")
						}
						back, err := command.runOwnedHostRelaunch(ctx, after, current.Clone(), backRequest)
						if err != nil || back.Owned != nil || back.Result.CurrentHost != "process" || back.Result.TargetHost != "tmux" || back.Result.NewPaneUID == record.Binding.PaneUID {
							t.Fatalf("reverse: %+v %v", back, err)
						}
					} else if mode == "shutdown" {
						consumerCancel()
					} else {
						life.Cancel()
					}
				}
				if !waited {
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal("owned Wait did not finish")
					}
				}
				if !life.Target.owner.waitRecorded {
					t.Fatal("actual target Wait was not durable")
				}
				after, _ = f.store.LoadReadOnly()
				current, _ = after.Agent(source.Metadata.UID)
				if mode == "reverse" {
					if _, ok := after.Pane(record.Binding.PaneUID); ok {
						t.Fatal("reverse retained process Pane")
					}
					if !current.Status.SessionRef.SameConversation(source.Status.SessionRef) {
						t.Fatal("reverse lost conversation")
					}
					life.Cancel() // A late old-generation cancel must not touch new tmux Pane.
					hostCLIOutput(t, f, "get", "pane", "--pane", "uid:"+current.Status.PaneRef, "-o", "json")
				} else {
					pane, ok := after.Pane(record.Binding.PaneUID)
					if !ok || current.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(record.Binding, pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, current.Status.LastTermination) {
						t.Fatal("consumer lost actual Wait/Offline")
					}

				}
				history, historyErr := os.ReadFile(historyPath)
				if historyErr != nil && (!os.IsNotExist(historyErr) || beforeHistoryErr == nil) {
					t.Fatalf("history lost: %v", historyErr)
				}
				if !bytes.HasPrefix(history, beforeHistory) {
					t.Fatal("existing session history was lost or truncated")
				}
				if provider == aiModeCodex && mode != "cli-output-failure" {
					wire, _ := os.ReadFile(filepath.Join(f.root, "shared-wire.jsonl"))
					if !bytes.Contains(wire, []byte("thread/unsubscribe")) {
						t.Fatal("production broker transfer bypassed")
					}
				}
			})
		}
	}
}
