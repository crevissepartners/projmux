package app

import (
	"context"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Measured stage cost of a real `claude` create under CPU load on a 14-core
// machine, 2026-09-18: `while :; do :; done` at 1x and 3x nproc on top of an
// already busy machine, n=20 pooled, observed loadavg 37-72, timestamps taken
// from the product's own activation authority rather than from an observer.
//
// Raw pooled values are startup p90 13924ms / max 24431ms and acknowledgement
// p90 5198ms / max 8604ms; they are held here at the tenth of a second the
// sizing rule was stated in, because a tail heuristic does not deserve
// millisecond precision.
const (
	measuredP90ActivationStartup          = 13900 * time.Millisecond
	measuredPeakActivationStartup         = 24400 * time.Millisecond
	measuredP90ActivationAcknowledgement  = 5200 * time.Millisecond
	measuredPeakActivationAcknowledgement = 8600 * time.Millisecond
)

func TestActivationDeadlinesCoverTheMeasuredLoadEnvelope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		stage    string
		deadline time.Duration
		p90      time.Duration
		peak     time.Duration
	}{
		{
			stage:    "startup",
			deadline: agentActivationStartupDeadline,
			p90:      measuredP90ActivationStartup,
			peak:     measuredPeakActivationStartup,
		},
		{
			stage:    "acknowledgement",
			deadline: agentActivationAcknowledgementDeadline,
			p90:      measuredP90ActivationAcknowledgement,
			peak:     measuredPeakActivationAcknowledgement,
		},
	} {
		t.Run(test.stage, func(t *testing.T) {
			// One more step of the observed tail slope past the peak. Twenty
			// samples make a max a weak tail estimate, and a bound sized to the
			// max alone is one unlucky run from calling a live Agent a failure --
			// which is also what the nonzero exit code claims not to mean.
			want := test.peak + (test.peak - test.p90)
			if test.deadline < want {
				t.Fatalf("%s deadline = %v, under max+(max-p90) = %v for a measured peak of %v: a provider that is merely slow would be reported as a failure",
					test.stage, test.deadline, want, test.peak)
			}
			// Headroom is not free: every extra second is a second a genuinely
			// dead provider stays unreported.
			if test.deadline > 2*want {
				t.Fatalf("%s deadline = %v, more than twice the sized bound %v: unjustified headroom delays real failure reports",
					test.stage, test.deadline, want)
			}
		})
	}
}

func TestActivationStagesAreSeparatelyBoundedAroundTheirOwnDeadline(t *testing.T) {
	// Each case drives the two provider-hook commits with an exact clock, so the
	// stage under test is the only thing that moves. The delays deliberately sit
	// past the five seconds both stages used to allow, which is what makes a
	// revert of the deadlines fail here instead of silently passing.
	for _, test := range []struct {
		name             string
		startupAfter     time.Duration
		acknowledgeAfter time.Duration
		wantAcknowledged bool
	}{
		{
			name:             "startup late but inside its own bound",
			startupAfter:     agentActivationStartupDeadline - time.Second,
			acknowledgeAfter: time.Second,
			wantAcknowledged: true,
		},
		{
			name:             "acknowledgement late but inside its own bound",
			startupAfter:     time.Second,
			acknowledgeAfter: agentActivationAcknowledgementDeadline - time.Second,
			wantAcknowledged: true,
		},
		{
			name:             "both stages late but each inside its own bound",
			startupAfter:     agentActivationStartupDeadline - time.Second,
			acknowledgeAfter: agentActivationAcknowledgementDeadline - time.Second,
			wantAcknowledged: true,
		},
		{
			// Absolute, not derived: this is the slowest real provider startup
			// measured under load, so shrinking the bound back under it turns
			// this case red instead of leaving the regression to review.
			name:             "startup at the measured load peak",
			startupAfter:     measuredPeakActivationStartup,
			acknowledgeAfter: time.Second,
			wantAcknowledged: true,
		},
		{
			name:             "acknowledgement at the measured load peak",
			startupAfter:     time.Second,
			acknowledgeAfter: measuredPeakActivationAcknowledgement,
			wantAcknowledged: true,
		},
		{
			name:         "startup past its bound",
			startupAfter: agentActivationStartupDeadline + time.Second,
		},
		{
			name:             "acknowledgement past its bound",
			startupAfter:     time.Second,
			acknowledgeAfter: agentActivationAcknowledgementDeadline + time.Second,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newSessionRefHarness(t, aiModeClaude)
			agent, _ := h.registry.Agent(h.agentUID)
			agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
			runner := &activationAuthorityRunner{paneUID: h.paneUID}

			now := sessionRefObservedAt
			h.cmd.now = func() time.Time { return now }
			var startupCommitted, acknowledgeCommitted bool
			h.cmd.sleep = func(d time.Duration) {
				now = now.Add(d)
				elapsed := now.Sub(sessionRefObservedAt)
				if !startupCommitted && elapsed >= test.startupAfter {
					startupCommitted = true
					commitActivation(t, h, now, coremetadata.ActivationPending)
				}
				if startupCommitted && !acknowledgeCommitted && test.acknowledgeAfter > 0 &&
					elapsed >= test.startupAfter+test.acknowledgeAfter {
					acknowledgeCommitted = true
					commitActivation(t, h, now, coremetadata.ActivationAcknowledged)
				}
			}

			acknowledged, source, err := h.cmd.AwaitAgentActivation(context.Background(), runner, "%7",
				agentActivationStartupDeadline, agentActivationAcknowledgementDeadline)
			if err != nil {
				t.Fatalf("bounded wait: %v", err)
			}
			if acknowledged != test.wantAcknowledged {
				t.Fatalf("acknowledged = %t, want %t (waited %v)", acknowledged, test.wantAcknowledged,
					now.Sub(sessionRefObservedAt))
			}
			if source != string(coremetadata.InteractionSourceProviderHook) {
				t.Fatalf("source = %q, want provider-hook", source)
			}
			elapsed := now.Sub(sessionRefObservedAt)
			if test.wantAcknowledged {
				// A stage that is allowed to finish late must not have been cut
				// off by the *other* stage's budget.
				if want := test.startupAfter + test.acknowledgeAfter; elapsed < want {
					t.Fatalf("acknowledged after %v, want at least %v", elapsed, want)
				}
				return
			}
			// The whole wait is still bounded: startup, or startup plus one
			// acknowledgement window anchored on the SessionStart commit.
			ceiling := agentActivationStartupDeadline
			if test.acknowledgeAfter > 0 {
				ceiling = test.startupAfter + agentActivationAcknowledgementDeadline
			}
			if elapsed > ceiling {
				t.Fatalf("unconfirmed wait = %v, want at most %v", elapsed, ceiling)
			}
		})
	}
}

// commitActivation writes one provider-hook activation refinement with an exact
// ObservedAt, which is the timestamp the acknowledgement window anchors on.
func commitActivation(t *testing.T, h *sessionRefHarness, at time.Time, state coremetadata.AgentActivationState) {
	t.Helper()
	mutator := coremetadata.Mutator{Now: func() time.Time { return at }}
	if _, err := mutator.SetAgentActivation(h.registry, h.agentUID, state,
		string(coremetadata.InteractionSourceProviderHook), ""); err != nil {
		t.Fatalf("commit %s activation: %v", state, err)
	}
}

func TestActivationUnconfirmedDiagnosticRereadsAuthorityBeforeSuggestingDelete(t *testing.T) {
	t.Parallel()
	target := agentActivationTarget{
		agentUID:   "agent-load-bound",
		agentName:  "worker-one",
		paneUID:    "pane-load-bound",
		paneID:     "%41",
		generation: "gen-load-bound",
	}
	diagnostic := activationUnconfirmedDiagnostic(target, coremetadata.ActivationReasonTimedOut)

	// The order is the contract: the two cheap reads come before the destructive
	// suggestion, so an operator -- or automation propagating the exit code --
	// cannot follow the first sentence and delete a live, working Agent.
	ordered := []string{
		"has live managed Pane %41",
		"still live; nothing was rolled back",
		"projmux get agent uid:agent-load-bound",
		"tmux capture-pane -p -t %41",
		"retry it through the provider",
		"projmux delete agent uid:agent-load-bound --yes",
	}
	cursor := 0
	for _, fragment := range ordered {
		index := strings.Index(diagnostic[cursor:], fragment)
		if index < 0 {
			t.Fatalf("diagnostic %q is missing %q at or after offset %d", diagnostic, fragment, cursor)
		}
		cursor += index + len(fragment)
	}
	if removal, recheck := strings.Index(diagnostic, "delete agent"), strings.Index(diagnostic, "get agent"); removal < recheck {
		t.Fatalf("diagnostic suggests delete before re-reading authority: %q", diagnostic)
	}
	if !strings.HasSuffix(diagnostic, "--yes`") {
		t.Fatalf("delete is not the last option offered: %q", diagnostic)
	}
	if !strings.Contains(diagnostic, coremetadata.ActivationReasonTimedOut) {
		t.Fatalf("diagnostic dropped the bounded reason: %q", diagnostic)
	}
	steps := activationUnconfirmedDiagnosticSteps(target)
	if len(steps) != 4 {
		t.Fatalf("remediation steps = %d, want the four ordered steps: %q", len(steps), steps)
	}
	for i, step := range steps[:len(steps)-1] {
		if strings.Contains(step, "delete agent") {
			t.Fatalf("step %d suggests delete before the reads: %q", i, step)
		}
	}
	if !strings.Contains(steps[len(steps)-1], "only when neither read shows activation evidence") {
		t.Fatalf("delete step is unconditional: %q", steps[len(steps)-1])
	}
}
