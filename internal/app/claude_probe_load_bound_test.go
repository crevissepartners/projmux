package app

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Measured step distributions in ms (0.1ms precision) under a CPU hog of 3x
// nproc, loadavg 33-59. The readiness read is from helpers that answer after
// current(); the old helper cap censored every slower answer.
var claudeProbeMeasuredSteps = []struct {
	name     string
	bound    time.Duration
	p90, max float64
}{
	{"dial", claudeLeaseDialTimeout, 3.8, 24.5},
	{"readiness read", claudeLeaseReadinessReadTimeout, 54.4, 197.5},
	{"coordination probe", claudeLeaseCoordinationProbeTimeout, 63.5, 179.9},
	{"eligibility", claudeCoordinationEligibilityTimeout, 60.3, 123.3},
}

func TestClaudeCoordinationProbeBoundsCoverTheMeasuredLoadEnvelope(t *testing.T) {
	// A step keeps its previous 200ms unless max + (max - p90) exceeds it; no
	// bound is lowered, and none may exceed twice what the measurement asks.
	const previous = 200 * time.Millisecond
	for _, step := range claudeProbeMeasuredSteps {
		t.Run(step.name, func(t *testing.T) {
			rule := time.Duration((2*step.max - step.p90) * float64(time.Millisecond))
			want := max(rule, previous)
			if step.bound < want {
				t.Fatalf("%s bound = %s, below max + (max - p90) = %s over the measured peak %.1fms", step.name, step.bound, rule, step.max)
			}
			if step.bound > 2*want {
				t.Fatalf("%s bound = %s, above 2x the measured need %s; a dead-but-listening helper would be reported that much later", step.name, step.bound, want)
			}
		})
	}
	if claudeEndpointReadinessWriteDeadline <= claudeEndpointPollInterval {
		t.Fatalf("readiness write deadline %s must not be the accept-loop cadence %s", claudeEndpointReadinessWriteDeadline, claudeEndpointPollInterval)
	}
}

type recordingReadinessConn struct {
	calls    []string
	now      *time.Time
	deadline time.Time
	written  []byte
}

func (c *recordingReadinessConn) SetWriteDeadline(deadline time.Time) error {
	c.calls = append(c.calls, "deadline")
	c.deadline = deadline
	return nil
}

func (c *recordingReadinessConn) Write(data []byte) (int, error) {
	c.calls = append(c.calls, "write")
	if !c.deadline.IsZero() && c.now.After(c.deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	c.written = append(c.written, data...)
	return len(data), nil
}

func (c *recordingReadinessConn) Close() error {
	c.calls = append(c.calls, "close")
	return nil
}

func TestClaudeLeaseReadinessAnswersAfterASlowCurrentCheck(t *testing.T) {
	t.Run("slow current check still answers", func(t *testing.T) {
		clock := time.Unix(1_000, 0)
		conn := &recordingReadinessConn{now: &clock}
		current := func() bool {
			conn.calls = append(conn.calls, "current")
			// Under load current() outlasted the accept-loop cadence many times over.
			clock = clock.Add(10 * claudeEndpointPollInterval)
			return true
		}
		answerClaudeLeaseReadiness(conn, current, func() time.Time { return clock })
		if got := strings.Join(conn.calls, ","); got != "current,deadline,write,close" {
			t.Fatalf("calls = %s, want the check before any deadline", got)
		}
		if string(conn.written) != "\x01" {
			t.Fatalf("written = %q, want the readiness byte after a slow check", conn.written)
		}
		if want := clock.Add(claudeEndpointReadinessWriteDeadline); !conn.deadline.Equal(want) {
			t.Fatalf("write deadline = %s, want %s armed after the check", conn.deadline, want)
		}
	})
	t.Run("stale lease closes without answering", func(t *testing.T) {
		clock := time.Unix(1_000, 0)
		conn := &recordingReadinessConn{now: &clock}
		answerClaudeLeaseReadiness(conn, func() bool { conn.calls = append(conn.calls, "current"); return false },
			func() time.Time { return clock })
		if got := strings.Join(conn.calls, ","); got != "current,close" || len(conn.written) != 0 {
			t.Fatalf("calls = %s written = %q, want a close without the readiness byte", got, conn.written)
		}
	})
}

func TestClaudeProbeTimeoutIsUnansweredAndEveryOtherFailureIsStale(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want claudeProbeOutcome
	}{
		{"read deadline", &net.OpError{Op: "read", Net: "unix", Err: os.ErrDeadlineExceeded}, claudeProbeUnanswered},
		{"dial deadline", &net.OpError{Op: "dial", Net: "unix", Err: os.ErrDeadlineExceeded}, claudeProbeUnanswered},
		{"helper closed without answering", io.EOF, claudeProbeStale},
		{"helper closed mid-answer", io.ErrUnexpectedEOF, claudeProbeStale},
		{"nothing listening", &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}, claudeProbeStale},
		{"socket gone", &net.OpError{Op: "dial", Net: "unix", Err: syscall.ENOENT}, claudeProbeStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := claudeProbeWaitOutcome(test.err); got != test.want {
				t.Fatalf("outcome = %d, want %d", got, test.want)
			}
		})
	}
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelExpired()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		want claudeProbeOutcome
	}{
		{"coordination call bound expired", expired, claudeProbeUnanswered},
		{"coordination call canceled", canceled, claudeProbeStale},
		{"coordination call failed within its bound", context.Background(), claudeProbeStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := claudeCoordinationCallOutcome(test.ctx); got != test.want {
				t.Fatalf("outcome = %d, want %d", got, test.want)
			}
		})
	}
}

func TestDeadClaudePeerIsRefusedAsStaleBeforeAnyBound(t *testing.T) {
	t.Run("provider exited", func(t *testing.T) {
		f := newClaudeEndpointTestFixture(t)
		f.start(t)
		route, reason := f.route(t)
		if reason != "" || classifyClaudeRegistrationLease(f.bootstrap.RegistryPath, route) != claudeProbeReady {
			t.Fatalf("live fixture lease not ready: %s", reason)
		}
		_ = f.provider.Process.Kill()
		_ = f.provider.Wait()
		begin := time.Now()
		if got := classifyClaudeRegistrationLease(f.bootstrap.RegistryPath, route); got != claudeProbeStale {
			t.Fatalf("dead provider outcome = %d, want stale", got)
		}
		if got := classifyClaudeCoordinationEligibility(f.bootstrap.RegistryPath, route); got != claudeProbeStale {
			t.Fatalf("dead provider eligibility outcome = %d, want the lease's stale, not unqualified", got)
		}
		// Process identity is checked before the first bound, so no bound is spent.
		if elapsed := time.Since(begin); elapsed >= claudeLeaseDialTimeout {
			t.Fatalf("dead provider refusal took %s, at least the dial bound", elapsed)
		}
	})
	t.Run("helper exited", func(t *testing.T) {
		f := newClaudeEndpointTestFixture(t)
		cancel, done := f.start(t)
		route, reason := f.route(t)
		if reason != "" {
			t.Fatal(reason)
		}
		cancel()
		<-done
		if got := classifyClaudeRegistrationLease(f.bootstrap.RegistryPath, route); got != claudeProbeStale {
			t.Fatalf("exited helper outcome = %d, want stale", got)
		}
	})
}

func TestClaudeSendRefusalSaysRetryOnlyWhenTheProbeWentUnanswered(t *testing.T) {
	const retry = "retry the send and do not delete the Agent on this refusal"
	for _, test := range []struct {
		name string
		// lease answers the source probe, then the target probe.
		lease       [2]claudeProbeOutcome
		eligibility claudeProbeOutcome
		want        string
		transient   bool
	}{
		{name: "source lease unanswered", lease: [2]claudeProbeOutcome{claudeProbeUnanswered, claudeProbeReady},
			want: "source Agent readiness is unconfirmed: claude registration lease probe did not answer in time", transient: true},
		{name: "source lease stale", lease: [2]claudeProbeOutcome{claudeProbeStale, claudeProbeReady},
			want: "source Agent is not eligible: claude registration lease is stale or unavailable"},
		{name: "target lease unanswered", lease: [2]claudeProbeOutcome{claudeProbeReady, claudeProbeUnanswered},
			want: "target Agent readiness is unconfirmed: claude registration lease probe did not answer in time", transient: true},
		{name: "target lease stale", lease: [2]claudeProbeOutcome{claudeProbeReady, claudeProbeStale},
			want: "target Agent is not eligible: claude registration lease is stale or unavailable"},
		{name: "target eligibility unanswered", lease: [2]claudeProbeOutcome{claudeProbeReady, claudeProbeReady}, eligibility: claudeProbeUnanswered,
			want: "target Agent readiness is unconfirmed: claude coordination eligibility probe did not answer in time", transient: true},
		{name: "target eligibility lease stale", lease: [2]claudeProbeOutcome{claudeProbeReady, claudeProbeReady}, eligibility: claudeProbeStale,
			want: "target Agent is not eligible: claude registration lease is stale or unavailable"},
		{name: "target unqualified", lease: [2]claudeProbeOutcome{claudeProbeReady, claudeProbeReady}, eligibility: claudeProbeUnqualified,
			want: "target Agent is not eligible: claude coordination requires exact-version isolated qualification; use agent message qualify"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaudeCoordinationTestFixture(t)
			store := intmetadata.NewStore(fixture.registryPath)
			calls := 0
			cmd := &agentCommand{
				activeTarget: outsideTmux().lookup,
				messagePaths: agentMessagePaths{registryPath: fixture.registryPath, loadRegistry: store.LoadReadOnly},
				messageStore: messagestore.NewStore(t.TempDir()),
				messageRoute: liveAgentMessageRouteResolver{
					registryPath: fixture.registryPath,
					leaseProbe: func(string, coremetadata.AgentRouteRef) claudeProbeOutcome {
						calls++
						return test.lease[min(calls, 2)-1]
					},
					eligibilityProbe: func(string, coremetadata.AgentRouteRef) claudeProbeOutcome { return test.eligibility },
				},
				messageNow: time.Now,
			}
			agent := "uid:" + fixture.route.AgentUID
			_, _, err := runRoute(t, cmd, "message", "send", agent, "--source", agent, "--message-ref", "message-probe-refusal", "--", "coordination body")
			if err == nil {
				t.Fatal("send succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("refusal = %q, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), retry) != test.transient {
				t.Fatalf("refusal = %q, retry guidance present = %t, want %t", err, !test.transient, test.transient)
			}
			if strings.Contains(err.Error(), "projmux delete") {
				t.Fatalf("refusal suggests deletion: %q", err)
			}
			var unanswered claudeProbeUnansweredError
			if errors.As(err, &unanswered) != test.transient {
				t.Fatalf("refusal %q unanswered type = %t, want %t", err, !test.transient, test.transient)
			}
		})
	}
}
