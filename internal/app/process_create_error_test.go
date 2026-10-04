package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessCreateFailureTypedCleanupAndUnchangedText(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, outcome := range []processCreateRuntime{processCreateRuntimeNone, processCreateRuntimeOffline, processCreateRuntimeUnknown} {
			t.Run(provider+"/"+string(outcome), func(t *testing.T) {
				store := newFakeResourceStore(t)
				command, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				opts := processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, Name: "alpha"}, Window: selector.Ref{Kind: coremetadata.KindWindow, Name: "review"}, Provider: provider}
				project, window, err := resolveProcessCreateScope(store.registry, opts)
				if err != nil {
					t.Fatal(err)
				}
				result, err := command.reserveProcessAgent(context.Background(), processAgentCreatePlan{project: project, window: window, workspace: coremetadata.AgentWorkspace{CWD: project.Spec.Root}}, opts, "op-error", "gen-error")
				if err != nil {
					t.Fatal(err)
				}
				cause := errors.New("process-post-create-hook-failed: hook exited 7")
				want := cause.Error() + "; remaining: none"
				if outcome != processCreateRuntimeNone {
					cleanup := errors.New("fixture delete failed")
					command.store.update = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
						return coremetadata.Registry{}, cleanup
					}
					result.waitRecorded = outcome == processCreateRuntimeOffline
					want = fmt.Sprintf("%s\nruntime=%s; remaining agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s; cleanup: projmux delete agent uid:%s: %s", cause, outcome, result.Binding.Agent, result.Binding.Pane, result.Binding.Agent, result.Binding.Agent, cleanup)
				}
				err = command.failProcessCreate(&result, cause)
				var failure *processCreateError
				if !errors.As(err, &failure) || failure.Runtime != outcome || failure.Token != "process-post-create-hook-failed" || err.Error() != want || !errors.Is(err, cause) {
					t.Fatalf("typed cleanup/text: %+v, %v; want %q", failure, err, want)
				}
				if outcome == processCreateRuntimeNone {
					_, agentRemains := store.registry.Agent(result.Binding.Agent)
					_, paneRemains := store.registry.Pane(result.Binding.Pane)
					if failure.Remaining != (processCreateRemaining{}) || agentRemains || paneRemains {
						t.Fatal("successful rollback retained resources")
					}
				} else if failure.Remaining != processCreateRemainingRefs(result) {
					t.Fatal("remaining exact refs lost")
				}
				if cli.ClassifyFailure(err, false).ExitCode != 1 {
					t.Fatal("creation failure exit changed")
				}
			})
		}
	}
}

func TestProcessCreateFailureBeforeReservationPreservesClassification(t *testing.T) {
	command := &createCommand{}
	for _, cause := range []error{errors.New("process-provider-unsupported: unsupported"), usageError("invalid scope"), superviseExitError{code: 143}, processhost.ErrStale} {
		err := command.failProcessCreate(&processAgentCreateResult{}, cause)
		var failure *processCreateError
		if !errors.As(err, &failure) || failure.Runtime != processCreateRuntimeNone || failure.Remaining != (processCreateRemaining{}) || err.Error() != cause.Error() || !errors.Is(err, cause) {
			t.Fatalf("pre-reservation error: %v", err)
		}
		var usage *UsageError
		oldUsage := errors.As(cause, &usage)
		newUsage := errors.As(err, &usage)
		if oldUsage != newUsage || cli.ClassifyFailure(err, newUsage) != cli.ClassifyFailure(cause, oldUsage) {
			t.Fatal("error classification changed")
		}
	}
}
