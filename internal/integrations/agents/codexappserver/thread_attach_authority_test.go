package codexappserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNativeCreateAndResumeAttachToTheExactCurrentUnmanagedEndpoint runs the
// real product create and resume paths against a fake Codex whose every argv is
// recorded and whose daemon reports a running, exact-current endpoint the
// official manager does not own.
//
// Before the cutover this endpoint was refused, because the product gate read
// the rendered native-action axis, which treats every unmanaged endpoint as
// unsafe. The gate now reads endpoint attach authority instead, so the exact
// same endpoint is attachable while its daemon lifecycle authority stays
// `none`: the recorded argv must contain no start, stop, restart, kill,
// bootstrap, remote-control, login, or config mutation.
func TestNativeCreateAndResumeAttachToTheExactCurrentUnmanagedEndpoint(t *testing.T) {
	ledger := installFakeCodex(t, `{"status":"running","managedCodexVersion":"0.150.1","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	created, err := StartDefaultThread(ctx, "0.13.0", "/work/project", nil, "hello", "gen-1")
	if err != nil {
		t.Fatalf("create on an exact-current unmanaged endpoint: %v", err)
	}
	if created.ThreadID != "thread-preturn" || created.TurnID != "turn-1" {
		t.Fatalf("create binding = %+v", created)
	}
	resumed, err := ResumeDefaultThread(ctx, "0.13.0", "/work/project", nil, "thread-preturn")
	if err != nil {
		t.Fatalf("resume on an exact-current unmanaged endpoint: %v", err)
	}
	if resumed.ThreadID != "thread-preturn" {
		t.Fatalf("resume binding = %+v", resumed)
	}

	argv, methods := ledger(t)
	for _, forbidden := range []string{
		"daemon start", "daemon stop", "daemon restart", "daemon kill", "daemon bootstrap",
		"enable-remote-control", "disable-remote-control", "login", "logout", "config set", "config write",
	} {
		for _, line := range argv {
			if strings.Contains(line, forbidden) {
				t.Fatalf("daemon lifecycle mutation %q reached the fake Codex: %v", forbidden, argv)
			}
		}
	}
	for method, want := range map[string]int{methodThreadStart: 1, methodTurnStart: 1, methodThreadResume: 1} {
		if got := methods[method]; got != want {
			t.Fatalf("%s count = %d, want %d; all=%v", method, got, want, methods)
		}
	}
}

// TestNativeCreateAndResumeKeepSafeFallbackOnEveryRefusedAttachRow pins the
// compatibility half of the same gate. An endpoint whose exact identity cannot
// be proven still refuses, still refuses before the connection opens and
// therefore before any provider conversation is mutated, and still refuses as a
// safe fallback carrying its typed reason, so the current CLI/hook lane keeps
// working exactly as it did.
func TestNativeCreateAndResumeKeepSafeFallbackOnEveryRefusedAttachRow(t *testing.T) {
	for _, row := range []struct {
		name          string
		daemonVersion string
		reason        string
	}{
		{
			name:          "unmanaged version skew",
			daemonVersion: `{"status":"running","managedCodexVersion":"0.150.1","cliVersion":"0.150.1","appServerVersion":"0.149.0"}`,
			reason:        string(LifecycleReasonUnsafeUnmanaged),
		},
		{
			name:          "ownership unknown",
			daemonVersion: `{"status":"stopped"}`,
			reason:        string(LifecycleReasonUnsafeOwnershipUnknown),
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			ledger := installFakeCodex(t, row.daemonVersion)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			if _, err := StartDefaultThread(ctx, "0.13.0", "/work/project", nil, "hello", "gen-1"); !CanFallback(err) {
				t.Fatalf("create err = %v, want a safe fallback", err)
			} else if action := new(ThreadActionError); !errors.As(err, &action) || action.Reason != row.reason {
				t.Fatalf("create reason = %v, want %q", err, row.reason)
			}
			if _, err := ResumeDefaultThread(ctx, "0.13.0", "/work/project", nil, "thread-preturn"); !CanFallback(err) {
				t.Fatalf("resume err = %v, want a safe fallback", err)
			}

			_, methods := ledger(t)
			for _, method := range []string{methodThreadStart, methodTurnStart, methodThreadResume} {
				if methods[method] != 0 {
					t.Fatalf("%s reached a refused endpoint: %v", method, methods)
				}
			}
		})
	}
}

// TestEndpointAttachAuthorityIsTheOnlyWidenedProductRow compares the retired
// product gate and the attach-authority gate across the whole readiness x
// ownership x version table.
//
// Both gates are evaluated as the product evaluates them, behind the source and
// availability preconditions that come first, because those preconditions are
// what makes the pure `dead` row - where the cold-start contract keeps native
// readiness while there is nothing to attach to - unreachable from a create or
// resume. Exactly one row may then differ: a ready, exact-current endpoint the
// official manager does not own. Every other row must decide identically,
// because this phase is a compatibility cutover rather than a relaxation of
// exact identity.
func TestEndpointAttachAuthorityIsTheOnlyWidenedProductRow(t *testing.T) {
	available := func(health Health) bool {
		return health.Source == SourceAppServer && health.Availability == AvailabilityAvailable
	}
	widened := 0
	for _, availability := range []Availability{
		AvailabilityAvailable, AvailabilityUnavailable, AvailabilityUnsupported, AvailabilityTimeout, AvailabilityProtocolError,
	} {
		for _, ownership := range []ManagerOwnership{ManagerManaged, ManagerUnmanaged, ManagerUnknown} {
			for _, relation := range []VersionRelation{VersionCurrent, VersionSkew, VersionUnknown} {
				health := Decide(availability, ReasonNone, "0.150.1", EndpointStdioProxy, ConnectionReady, true)
				health.ManagerOwnership, health.VersionRelation = ownership, relation
				health = withNativeActionReadiness(health)

				attach := available(health) && AuthorityFor(health).Attach == EndpointAttachAllowed
				native := available(health) && health.NativeAction != NativeActionRefused
				if attach == native {
					continue
				}
				widened++
				if !attach || availability != AvailabilityAvailable || ownership != ManagerUnmanaged || relation != VersionCurrent {
					t.Fatalf("unexpected divergence at availability=%s ownership=%s version=%s: attach=%v native=%v",
						availability, ownership, relation, attach, native)
				}
			}
		}
	}
	if widened != 1 {
		t.Fatalf("widened rows = %d, want exactly the ready exact-current unmanaged row", widened)
	}
}
