package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type fakeReviewBindingLookup struct {
	threadID string
	live     bool
	err      error
	calls    []string
}

func (f *fakeReviewBindingLookup) LiveThreadID(_ context.Context, paneUID string) (string, bool, error) {
	f.calls = append(f.calls, paneUID)
	return f.threadID, f.live, f.err
}

type fakeReviewStarter struct {
	result corecap.ReviewResult
	err    error
	start  func(context.Context, string, corecap.ReviewTarget) (corecap.ReviewResult, error)
	calls  []fakeReviewCall
}

type fakeReviewCall struct {
	threadID string
	target   corecap.ReviewTarget
}

func (f *fakeReviewStarter) Start(ctx context.Context, threadID string, target corecap.ReviewTarget) (corecap.ReviewResult, error) {
	f.calls = append(f.calls, fakeReviewCall{threadID: threadID, target: target})
	if f.start != nil {
		return f.start(ctx, threadID, target)
	}
	return f.result, f.err
}

func prepareExactReviewAgent(t *testing.T, store *fakeResourceStore) {
	t.Helper()
	setFixtureSessionRef(t, store, "agt-alpha-codex", &coremetadata.AgentSessionRef{
		Provider: "codex", ObservedAt: time.Now().UTC(), Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-exact"},
	})
	pane, ok := store.registry.Pane("pan-alpha-codex")
	if !ok {
		t.Fatal("fixture pane missing")
	}
	pane.Status.Activation.Generation = "generation-current"
}

func TestAgentReviewRequiresExactBindingAndProjectsInitialLifecycle(t *testing.T) {
	store := newFakeResourceStore(t)
	prepareExactReviewAgent(t, store)
	cmd, _, _ := newTestAgentCommand(t, store)
	binding := &fakeReviewBindingLookup{threadID: "thread-exact", live: true}
	starter := &fakeReviewStarter{result: corecap.ReviewResult{
		ThreadID: "thread-exact", TurnID: "turn-review", Status: corecap.ReviewInProgress,
	}}
	mirror := &fakeAgentMutationMirror{target: "%9"}
	cmd.reviewBinding = binding
	cmd.reviews = starter
	cmd.mirror = mirror

	stdout, _, err := runRoute(t, cmd, "review", "uid:agt-alpha-codex", "--base", "main")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "review started thread=thread-exact turn=turn-review status=in-progress\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(starter.calls) != 1 || starter.calls[0].threadID != "thread-exact" || starter.calls[0].target != (corecap.ReviewTarget{Kind: corecap.ReviewBaseBranch, Value: "main"}) {
		t.Fatalf("review calls = %#v", starter.calls)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Interaction.Kind != coremetadata.InteractionInProgress || agent.Status.Interaction.Source != string(coremetadata.InteractionSourceProviderControl) {
		t.Fatalf("initial lifecycle = %#v", agent.Status.Interaction)
	}
	if got := strings.Join(mirror.calls, "|"); got != "find pan-alpha-codex|status %9 in_progress" {
		t.Fatalf("mirror calls = %q", got)
	}
}

func TestAgentReviewUnavailableAndStalePathsWriteNothing(t *testing.T) {
	t.Run("live thread mismatch", func(t *testing.T) {
		store := newFakeResourceStore(t)
		prepareExactReviewAgent(t, store)
		cmd, _, _ := newTestAgentCommand(t, store)
		starter := &fakeReviewStarter{}
		cmd.reviewBinding = &fakeReviewBindingLookup{threadID: "thread-other", live: true}
		cmd.reviews = starter
		before := store.writes
		_, _, err := runRoute(t, cmd, "review", "uid:agt-alpha-codex")
		if err == nil || !strings.Contains(err.Error(), "live Pane thread does not match") {
			t.Fatalf("error = %v", err)
		}
		if len(starter.calls) != 0 || store.writes != before {
			t.Fatalf("review calls=%d writes=%d, want zero", len(starter.calls), store.writes-before)
		}
	})

	t.Run("unsupported review method", func(t *testing.T) {
		store := newFakeResourceStore(t)
		prepareExactReviewAgent(t, store)
		cmd, _, _ := newTestAgentCommand(t, store)
		cmd.reviewBinding = &fakeReviewBindingLookup{threadID: "thread-exact", live: true}
		cmd.reviews = &fakeReviewStarter{err: corecap.ErrUnavailable}
		before := store.writes
		_, _, err := runRoute(t, cmd, "review", "uid:agt-alpha-codex")
		if err == nil || !strings.Contains(err.Error(), "agent review unavailable") {
			t.Fatalf("error = %v", err)
		}
		if store.writes != before {
			t.Fatalf("writes = %d, want zero", store.writes-before)
		}
	})

	for _, test := range []struct {
		name   string
		result corecap.ReviewResult
	}{
		{name: "empty review thread", result: corecap.ReviewResult{TurnID: "turn-review", Status: corecap.ReviewInProgress}},
		{name: "empty turn", result: corecap.ReviewResult{ThreadID: "thread-exact", Status: corecap.ReviewInProgress}},
		{name: "unknown status", result: corecap.ReviewResult{ThreadID: "thread-exact", TurnID: "turn-review", Status: corecap.ReviewUnknown}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			prepareExactReviewAgent(t, store)
			cmd, _, _ := newTestAgentCommand(t, store)
			cmd.reviewBinding = &fakeReviewBindingLookup{threadID: "thread-exact", live: true}
			cmd.reviews = &fakeReviewStarter{result: test.result}
			before := store.writes
			_, _, err := runRoute(t, cmd, "review", "uid:agt-alpha-codex")
			if err == nil || !strings.Contains(err.Error(), "incomplete or unknown") {
				t.Fatalf("error = %v", err)
			}
			if store.writes != before {
				t.Fatalf("writes = %d, want zero", store.writes-before)
			}
		})
	}

	t.Run("binding changes before lifecycle commit", func(t *testing.T) {
		store := newFakeResourceStore(t)
		prepareExactReviewAgent(t, store)
		cmd, _, _ := newTestAgentCommand(t, store)
		cmd.reviewBinding = &fakeReviewBindingLookup{threadID: "thread-exact", live: true}
		cmd.reviews = &fakeReviewStarter{start: func(context.Context, string, corecap.ReviewTarget) (corecap.ReviewResult, error) {
			pane, _ := store.registry.Pane("pan-alpha-codex")
			pane.Status.Activation.Generation = "generation-replaced"
			return corecap.ReviewResult{ThreadID: "thread-exact", TurnID: "turn-review", Status: corecap.ReviewInProgress}, nil
		}}
		_, _, err := runRoute(t, cmd, "review", "uid:agt-alpha-codex")
		if err == nil || !strings.Contains(err.Error(), "binding changed") {
			t.Fatalf("error = %v", err)
		}
		agent, _ := store.registry.Agent("agt-alpha-codex")
		if agent.Status.Interaction.Source == string(coremetadata.InteractionSourceProviderControl) {
			t.Fatalf("stale lifecycle was committed: %#v", agent.Status.Interaction)
		}
	})
}

func TestAgentReviewActionContextIsBounded(t *testing.T) {
	store := newFakeResourceStore(t)
	prepareExactReviewAgent(t, store)
	cmd, _, _ := newTestAgentCommand(t, store)
	cmd.reviewBinding = &fakeReviewBindingLookup{threadID: "thread-exact", live: true}
	cmd.reviewTimeout = 5 * time.Millisecond
	cmd.reviews = &fakeReviewStarter{start: func(ctx context.Context, _ string, _ corecap.ReviewTarget) (corecap.ReviewResult, error) {
		<-ctx.Done()
		return corecap.ReviewResult{}, ctx.Err()
	}}
	before := store.writes
	_, _, err := runRoute(t, cmd, "review", "uid:agt-alpha-codex")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if store.writes != before {
		t.Fatalf("writes = %d, want zero", store.writes-before)
	}
}

func TestParseReviewTargetIsClosedAndDefaultsToUncommitted(t *testing.T) {
	t.Parallel()
	if got, err := parseReviewTarget("", "", ""); err != nil || got.Kind != corecap.ReviewUncommitted {
		t.Fatalf("default target = %#v, %v", got, err)
	}
	if _, err := parseReviewTarget("main", "abc", ""); err == nil {
		t.Fatal("multiple review target flags succeeded")
	}
}
