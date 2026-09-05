package app

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// TestCodexBrokerDiagnosticsSurfaceNamesThePublishedButUnreachableRuntime
// holds the C-1 verdict on the exact shape that cost this machine a process.
//
// A discovery record with nothing answering behind it used to render as
// `absent`, which reads as "no broker is running". It is the opposite: a
// runtime announced itself here and the diagnosis could not reach it. Nothing
// published is the ordinary resting state and must stay quiet, or the row
// trains an operator to skip it.
func TestCodexBrokerDiagnosticsSurfaceNamesThePublishedButUnreachableRuntime(t *testing.T) {
	for _, test := range []struct {
		name       string
		broker     *codexBrokerDiagnostic
		wantStatus string
		wantDetail string
	}{
		{
			name:       "published and unreachable is broken",
			broker:     &codexBrokerDiagnostic{State: codexBrokerStateAbsent, Reason: string(codexbroker.RefusalHostUnavailable), Published: 1},
			wantStatus: codexSurfaceStatusBroken,
			wantDetail: "published endpoints 1 answered none",
		},
		{
			name:       "nothing published is the resting state",
			broker:     &codexBrokerDiagnostic{State: codexBrokerStateAbsent, Reason: string(codexbroker.RefusalHostUnavailable)},
			wantStatus: codexSurfaceStatusOK,
			wantDetail: "no endpoint published",
		},
		{
			name:       "a running runtime names the endpoint it was read on",
			broker:     &codexBrokerDiagnostic{State: codexBrokerStateRunning, Published: 1, Endpoint: "codex-app-server:g1:key"},
			wantStatus: codexSurfaceStatusOK,
			wantDetail: "observed endpoint present",
		},
		{
			name:       "no reading is not a healthy reading",
			broker:     nil,
			wantStatus: codexSurfaceStatusUnobserved,
			wantDetail: "no broker reading",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface := codexBrokerDiagnosticsSurface(test.broker)
			if surface.Surface != codexSurfaceBrokerDiagnostics {
				t.Fatalf("surface = %q, want %q", surface.Surface, codexSurfaceBrokerDiagnostics)
			}
			if surface.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", surface.Status, test.wantStatus, surface.Detail)
			}
			if !strings.Contains(surface.Detail, test.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", surface.Detail, test.wantDetail)
			}
		})
	}
}

// TestHookAttributionSurfaceSeparatesTotalFailureFromPartial holds the C-2
// verdict.
//
// Attribution failing for every event is not a degradation of attribution; it
// is attribution not happening, and it is the state defect B held for the whole
// life of a neighbouring track. Folding it in with a handful of stale panes
// would put the two on one row and lose the difference an operator acts on.
func TestHookAttributionSurfaceSeparatesTotalFailureFromPartial(t *testing.T) {
	for _, test := range []struct {
		name       string
		health     aiIngestAttributionHealth
		wantStatus string
	}{
		{
			name:       "no readable log is unobserved, not healthy",
			health:     aiIngestAttributionHealth{},
			wantStatus: codexSurfaceStatusUnobserved,
		},
		{
			name:       "a readable log with no hook event answers nothing",
			health:     aiIngestAttributionHealth{Observed: true},
			wantStatus: codexSurfaceStatusUnobserved,
		},
		{
			name: "every event and no pane is broken",
			health: aiIngestAttributionHealth{Observed: true, Records: 898, Sources: []aiIngestAttributionSource{
				{Source: "codex-hook", Unattributed: 898, Reasons: []aiIngestAttributionReason{{Reason: aiPaneMatchReasonNoInventory, Count: 898}}},
			}},
			wantStatus: codexSurfaceStatusBroken,
		},
		{
			name: "some events losing their pane is a degradation",
			health: aiIngestAttributionHealth{Observed: true, Records: 100, Sources: []aiIngestAttributionSource{
				{Source: "claude-hook", Attributed: 95, Unattributed: 5, Reasons: []aiIngestAttributionReason{{Reason: aiPaneMatchReasonExplicitStale, Count: 5}}},
			}},
			wantStatus: codexSurfaceStatusDegraded,
		},
		{
			name: "every event reaching its pane is healthy",
			health: aiIngestAttributionHealth{Observed: true, Records: 23, Sources: []aiIngestAttributionSource{
				{Source: "codex-hook", Attributed: 23},
			}},
			wantStatus: codexSurfaceStatusOK,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface := codexHookAttributionSurface(test.health)
			if surface.Surface != codexSurfaceHookAttribution {
				t.Fatalf("surface = %q, want %q", surface.Surface, codexSurfaceHookAttribution)
			}
			if surface.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", surface.Status, test.wantStatus, surface.Detail)
			}
			if strings.TrimSpace(surface.Detail) == "" {
				t.Fatal("a verdict with no evidence behind it")
			}
		})
	}
}

// TestObserverReasonSurfaceRefusesToCallAnUncapturedReasonHealthy holds the
// C-3 verdict, and it is the product-side half of the false-certification gate.
//
// Every Pane reporting the pre-instrumentation literal is exactly the state
// this track found in the field: six Panes, one reason, and that reason a
// default that means nothing was recorded. A diagnosis that renders it as a
// reason distribution and calls the row healthy repeats the E2E assertion that
// pinned the same literal as its passing condition.
func TestObserverReasonSurfaceRefusesToCallAnUncapturedReasonHealthy(t *testing.T) {
	for _, test := range []struct {
		name       string
		census     *codexAuthorityCensus
		wantStatus string
	}{
		{
			name: "every reason uncaptured is broken",
			census: &codexAuthorityCensus{Agents: 6, Reasons: []codexAuthorityReasonCount{
				// uncaptured-default: the field state this Phase exists to make
				// visible, driven as input so the verdict can be asserted.
				{Reason: string(codexObserverReasonRetired), Count: 6},
			}},
			wantStatus: codexSurfaceStatusBroken,
		},
		{
			name: "the explicit nothing-recorded bucket is never healthy",
			census: &codexAuthorityCensus{Agents: 2, Reasons: []codexAuthorityReasonCount{
				{Reason: string(codexObserverReasonEndpointSuspended), Count: 1},
				// uncaptured-default: a producer reached the journal with no
				// token; the bucket names that rather than hiding it.
				{Reason: string(codexObserverReasonUnrecorded), Count: 1},
			}},
			wantStatus: codexSurfaceStatusDegraded,
		},
		{
			name: "an out-of-vocabulary value read back is not an observation",
			census: &codexAuthorityCensus{Agents: 1, Reasons: []codexAuthorityReasonCount{
				// uncaptured-default: what a bounded read renders for a value
				// outside the vocabulary, which is a read failure, not a reason.
				{Reason: "bounded reason unavailable", Count: 1},
			}},
			wantStatus: codexSurfaceStatusBroken,
		},
		{
			name: "captured tokens are healthy",
			census: &codexAuthorityCensus{Agents: 2, Reasons: []codexAuthorityReasonCount{
				{Reason: string(codexObserverReasonEndpointSuspended), Count: 1},
				{Reason: string(codexObserverReasonReady), Count: 1},
			}},
			wantStatus: codexSurfaceStatusOK,
		},
		{
			name:       "no managed Agent answers nothing",
			census:     &codexAuthorityCensus{},
			wantStatus: codexSurfaceStatusUnobserved,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface := codexObserverReasonSurface(test.census)
			if surface.Surface != codexSurfaceObserverReason {
				t.Fatalf("surface = %q, want %q", surface.Surface, codexSurfaceObserverReason)
			}
			if surface.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", surface.Status, test.wantStatus, surface.Detail)
			}
		})
	}
}

// TestConnectionContinuitySurfaceReportsCumulativeReconnects holds the Phase 3
// verdict.
//
// The count is reported as what it is: a total over the runtime's life. No rate
// is derived from a single sample, because this reader does not know how long
// the runtime has been up and a fabricated rate is the kind of number an
// operator would act on.
func TestConnectionContinuitySurfaceReportsCumulativeReconnects(t *testing.T) {
	cycling := codexConnectionContinuitySurface(&codexBrokerDiagnostic{
		State:           codexBrokerStateRunning,
		ConnectionEpoch: 26500,
		Reconnects:      26499,
		Connections:     1,
		Revocations:     []codexBrokerRevocation{{Reason: string(codexbroker.RefusalSnapshotUnavailable), Count: 219}},
	})
	if cycling.Status != codexSurfaceStatusDegraded {
		t.Fatalf("status = %q, want %q for a runtime that reconnected 26499 times", cycling.Status, codexSurfaceStatusDegraded)
	}
	for _, want := range []string{"reconnects 26499", "connection epoch 26500", string(codexbroker.RefusalSnapshotUnavailable)} {
		if !strings.Contains(cycling.Detail, want) {
			t.Fatalf("detail = %q, want it to contain %q", cycling.Detail, want)
		}
	}
	if strings.Contains(cycling.Detail, "/s") {
		t.Fatalf("detail = %q, want no rate derived from one sample", cycling.Detail)
	}
	steady := codexConnectionContinuitySurface(&codexBrokerDiagnostic{State: codexBrokerStateRunning, ConnectionEpoch: 1})
	if steady.Status != codexSurfaceStatusOK {
		t.Fatalf("status = %q, want %q for a connection that never dropped", steady.Status, codexSurfaceStatusOK)
	}
	absent := codexConnectionContinuitySurface(&codexBrokerDiagnostic{State: codexBrokerStateAbsent})
	if absent.Status != codexSurfaceStatusUnobserved {
		t.Fatalf("status = %q, want %q with no running runtime to read", absent.Status, codexSurfaceStatusUnobserved)
	}
}

// TestTurnAdmissionSurfaceReportsATornAuthoritySnapshotAsBroken holds the C-5
// verdict.
//
// A torn snapshot is the observable signature of the read that refused live
// Panes their turn. An unfenced Pane is a different fact and stays a different
// verdict: its writer never took a fence, which says which build is running
// rather than that the fence stopped working.
func TestTurnAdmissionSurfaceReportsATornAuthoritySnapshotAsBroken(t *testing.T) {
	for _, test := range []struct {
		name       string
		census     *codexAuthorityCensus
		wantStatus string
	}{
		{
			name:       "a torn settled snapshot is broken",
			census:     &codexAuthorityCensus{Agents: 4, Settled: 4, Torn: 1},
			wantStatus: codexSurfaceStatusBroken,
		},
		{
			name:       "a pane whose writer never fenced is a degradation",
			census:     &codexAuthorityCensus{Agents: 4, Settled: 3, Unfenced: 1},
			wantStatus: codexSurfaceStatusDegraded,
		},
		{
			name:       "settled and coherent is healthy",
			census:     &codexAuthorityCensus{Agents: 4, Settled: 4},
			wantStatus: codexSurfaceStatusOK,
		},
		{
			name:       "a transition caught in flight says nothing either way",
			census:     &codexAuthorityCensus{Agents: 1, Contended: 1},
			wantStatus: codexSurfaceStatusOK,
		},
		{
			name:       "no classified snapshot answers nothing",
			census:     &codexAuthorityCensus{Agents: 2},
			wantStatus: codexSurfaceStatusUnobserved,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface := codexTurnAdmissionSurface(test.census)
			if surface.Surface != codexSurfaceTurnAdmission {
				t.Fatalf("surface = %q, want %q", surface.Surface, codexSurfaceTurnAdmission)
			}
			if surface.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", surface.Status, test.wantStatus, surface.Detail)
			}
		})
	}
}

// TestControlPlaneProjectionAnswersForEverySurface pins that the diagnosis
// answers for all five surfaces, in contract order, on every input.
//
// A surface that silently drops out of the projection is the same silence this
// section replaced: the row is gone, nothing is red, and the guarantee it
// carried is unobserved.
func TestControlPlaneProjectionAnswersForEverySurface(t *testing.T) {
	empty := projectCodexControlPlaneSurfaces(nil, nil, aiIngestAttributionHealth{}, codexControlPlaneVintage{})
	if !codexControlPlaneSurfacesComplete(empty) {
		t.Fatalf("projection over empty readings = %+v, want one verdict per named surface", empty.Surfaces)
	}
	for _, surface := range empty.Surfaces {
		if surface.Status != codexSurfaceStatusUnobserved {
			t.Fatalf("surface %q = %q with nothing read, want %q", surface.Surface, surface.Status, codexSurfaceStatusUnobserved)
		}
	}
	populated := projectCodexControlPlaneSurfaces(
		&codexBrokerDiagnostic{State: codexBrokerStateRunning, Published: 1, Endpoint: "codex-app-server:g1:key"},
		&codexAuthorityCensus{Agents: 1, Settled: 1, Reasons: []codexAuthorityReasonCount{{Reason: string(codexObserverReasonReady), Count: 1}}},
		aiIngestAttributionHealth{Observed: true, Records: 1, Sources: []aiIngestAttributionSource{{Source: "codex-hook", Attributed: 1}}},
		codexControlPlaneVintage{Supported: true},
	)
	if !codexControlPlaneSurfacesComplete(populated) {
		t.Fatalf("projection over live readings = %+v, want one verdict per named surface", populated.Surfaces)
	}
	for _, name := range codexControlPlaneSurfaceOrder {
		surface, ok := codexControlPlaneSurfaceOf(populated, name)
		if !ok {
			t.Fatalf("surface %q missing from a populated projection", name)
		}
		if surface.Status != codexSurfaceStatusOK {
			t.Fatalf("surface %q = %q on healthy readings, want %q (detail %q)", name, surface.Status, codexSurfaceStatusOK, surface.Detail)
		}
	}
}

// TestDoctorRendersEveryControlPlaneSurfaceAndItsVintage is the installed-read
// acceptance: an operator running `projmux doctor` can read each of the five
// surfaces individually, and can see whether the processes those readings came
// from are running the build that is installed.
//
// The vintage line is asserted alongside the surfaces because the two are one
// answer. `make install` never replaces a running process image, so a section
// of green verdicts read from a replaced-image process states that older code
// is healthy — and this track lost two acceptance criteria to exactly that.
func TestDoctorRendersEveryControlPlaneSurfaceAndItsVintage(t *testing.T) {
	var buf bytes.Buffer
	writeDoctorCodexControlPlaneText(&buf, &codexControlPlaneReport{
		Vintage: codexControlPlaneVintage{Supported: true, Roles: []codexControlPlaneRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
			{Role: codexControlPlaneRoleObserver, Processes: 8, Current: 1, Replaced: 7},
		}},
		Surfaces: []codexControlPlaneSurface{
			{Surface: codexSurfaceBrokerDiagnostics, Status: codexSurfaceStatusBroken, Detail: "published endpoints 1 answered none"},
			{Surface: codexSurfaceHookAttribution, Status: codexSurfaceStatusBroken, Detail: "codex-hook 0 attributed, 898 unattributed"},
			{Surface: codexSurfaceObserverReason, Status: codexSurfaceStatusBroken, Detail: "captured reasons 0; uncaptured 6"},
			{Surface: codexSurfaceConnectionContinuity, Status: codexSurfaceStatusDegraded, Detail: "reconnects 26499"},
			{Surface: codexSurfaceTurnAdmission, Status: codexSurfaceStatusBroken, Detail: "torn 1"},
		},
	})
	rendered := buf.String()
	if !strings.Contains(rendered, "Codex control-plane surfaces") {
		t.Fatalf("rendered:\n%s\nwant the section heading", rendered)
	}
	for _, surface := range codexControlPlaneSurfaceOrder {
		if !strings.Contains(rendered, surface) {
			t.Fatalf("rendered:\n%s\nwant surface %q to be individually readable", rendered, surface)
		}
	}
	for _, want := range []string{
		codexControlPlaneRoleBroker, codexControlPlaneRoleObserver,
		codexProcessVintageReplaced, "older code",
		codexSurfaceStatusBroken, codexSurfaceStatusDegraded,
		"898 unattributed", "reconnects 26499", "torn 1",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered:\n%s\nwant it to contain %q", rendered, want)
		}
	}
	var quiet bytes.Buffer
	writeDoctorCodexControlPlaneText(&quiet, nil)
	if quiet.Len() != 0 {
		t.Fatalf("rendered %q with no projection, want nothing", quiet.String())
	}
}

// codexControlPlaneSurfaceOrder is the audit order the diagnosis is checked against. It is the
// contract order, so a reader comparing the section against the contract walks
// both in one direction.
var codexControlPlaneSurfaceOrder = []string{
	codexSurfaceBrokerDiagnostics,
	codexSurfaceHookAttribution,
	codexSurfaceObserverReason,
	codexSurfaceConnectionContinuity,
	codexSurfaceTurnAdmission,
}

// codexControlPlaneSurfaceOf returns one surface by token. It exists so the
// audit gate can address a surface by name rather than by position.
func codexControlPlaneSurfaceOf(report codexControlPlaneReport, name string) (codexControlPlaneSurface, bool) {
	for _, surface := range report.Surfaces {
		if surface.Surface == name {
			return surface, true
		}
	}
	return codexControlPlaneSurface{}, false
}

// codexControlPlaneSurfacesComplete reports whether a projection answered for
// every named surface exactly once.
func codexControlPlaneSurfacesComplete(report codexControlPlaneReport) bool {
	if len(report.Surfaces) != len(codexControlPlaneSurfaceOrder) {
		return false
	}
	for index, surface := range report.Surfaces {
		if surface.Surface != codexControlPlaneSurfaceOrder[index] {
			return false
		}
		if !slices.Contains([]string{
			codexSurfaceStatusOK, codexSurfaceStatusDegraded,
			codexSurfaceStatusBroken, codexSurfaceStatusUnobserved,
		}, surface.Status) {
			return false
		}
		if strings.TrimSpace(surface.Detail) == "" {
			return false
		}
	}
	return true
}
