package app

import (
	"fmt"
	"strings"
)

// The seven control-plane surfaces whose broken states this application had no
// detection for.
//
// They are named here, in one closed list, for a reason this track paid for:
// three defects survived a neighbouring eight-phase track closing green from
// end to end, because nothing asserted that these surfaces were alive. A
// diagnosis that reports subsystem counters without naming the surfaces leaves
// an operator to infer which question each counter answers, and the inference
// is exactly what went unmade.
const (
	codexSurfaceBrokerDiagnostics    = "broker-diagnostics"
	codexSurfaceHookAttribution      = "hook-attribution"
	codexSurfaceObserverReason       = "observer-reason"
	codexSurfaceConnectionContinuity = "connection-continuity"
	codexSurfaceTurnAdmission        = "turn-admission"
	codexSurfaceHookDelivery         = "hook-delivery"
	codexSurfacePaneOwnership        = "pane-ownership"
)

// The verdict one surface carries.
//
// `unobserved` is not a fourth degree of health. It says the reading was not
// taken, and it is separate from `ok` because a surface nobody looked at and a
// surface that answered well produce the same silence otherwise — which is the
// silence this whole section exists to end.
const (
	codexSurfaceStatusOK         = "ok"
	codexSurfaceStatusDegraded   = "degraded"
	codexSurfaceStatusBroken     = "broken"
	codexSurfaceStatusUnobserved = "unobserved"
)

// codexControlPlaneSurface is one surface's verdict and the evidence it was
// reached on. Detail is assembled from counters and closed tokens only.
type codexControlPlaneSurface struct {
	Surface string `json:"surface"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
}

// codexControlPlaneReport is the whole projection: what the five surfaces say,
// and how current the processes they were read from are.
type codexControlPlaneReport struct {
	Vintage  codexControlPlaneVintage   `json:"vintage"`
	Surfaces []codexControlPlaneSurface `json:"surfaces"`
}

// projectCodexControlPlaneSurfaces reaches one verdict per surface from the
// readings doctor already took.
//
// Nothing is read again here. The section is a projection of the same broker
// telemetry, authority census, and hook log the detailed sections render, so a
// verdict can never disagree with the numbers printed above it.
func projectCodexControlPlaneSurfaces(
	broker *codexBrokerDiagnostic,
	census *codexAuthorityCensus,
	hooks codexHookHealth,
	vintage codexControlPlaneVintage,
) codexControlPlaneReport {
	report := codexControlPlaneReport{Vintage: vintage}
	report.Surfaces = []codexControlPlaneSurface{
		codexBrokerDiagnosticsSurface(broker),
		codexHookAttributionSurface(hooks.Attribution),
		codexObserverReasonSurface(census),
		codexConnectionContinuitySurface(broker),
		codexTurnAdmissionSurface(census),
		codexHookDeliverySurface(hooks.Delivery),
		codexPaneOwnershipSurface(hooks.Ownership),
	}
	return report
}

// codexHookHealth is the three questions one read of the hook ingest log
// answers: whether an event found its Pane, whether it changed it, and whether
// that Pane was its own.
//
// They travel together because they are layers of one path and only make sense
// read against each other. An event that never found a Pane cannot have failed
// to change one, and an event that changed the wrong Pane is not a success.
type codexHookHealth struct {
	Attribution aiIngestAttributionHealth
	Delivery    aiIngestDeliveryHealth
	Ownership   aiIngestOwnershipHealth
}

// codexBrokerDiagnosticsSurface judges whether the broker diagnosis is looking
// at the runtime that actually serves this state domain.
//
// The broken shape is the exact one that cost this machine a foreign process:
// a published discovery record with nothing answering behind it, rendered as
// `absent`, read by an operator as "no broker is running".
func codexBrokerDiagnosticsSurface(broker *codexBrokerDiagnostic) codexControlPlaneSurface {
	surface := codexControlPlaneSurface{Surface: codexSurfaceBrokerDiagnostics}
	if broker == nil {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no broker reading was taken"
		return surface
	}
	switch broker.State {
	case codexBrokerStateRunning:
		surface.Status = codexSurfaceStatusOK
		surface.Detail = fmt.Sprintf("runtime running; published endpoints %d; observed endpoint %s",
			broker.Published, codexEndpointPresence(broker.Endpoint))
	case codexBrokerStateAbsent:
		if broker.Published > 0 {
			surface.Status = codexSurfaceStatusBroken
			surface.Detail = fmt.Sprintf("published endpoints %d answered none; reason %s",
				broker.Published, codexSurfaceReasonOrNone(broker.Reason))
			return surface
		}
		surface.Status = codexSurfaceStatusOK
		surface.Detail = "no endpoint published; no native Agent is bound"
	case codexBrokerStateUnsupported:
		surface.Status = codexSurfaceStatusUnobserved
		surface.Detail = "platform hosts no broker runtime"
	default:
		surface.Status = codexSurfaceStatusDegraded
		surface.Detail = fmt.Sprintf("runtime %s; reason %s", broker.State, codexSurfaceReasonOrNone(broker.Reason))
	}
	return surface
}

// codexHookAttributionSurface judges whether provider hook events are reaching
// the Pane that owns them.
func codexHookAttributionSurface(attribution aiIngestAttributionHealth) codexControlPlaneSurface {
	surface := codexControlPlaneSurface{Surface: codexSurfaceHookAttribution}
	if !attribution.Observed {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no hook ingest log was readable"
		return surface
	}
	if attribution.Records == 0 {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no hook event in the reading window"
		return surface
	}
	parts := make([]string, 0, len(attribution.Sources))
	for _, source := range attribution.Sources {
		part := fmt.Sprintf("%s %d attributed, %d unattributed", source.Source, source.Attributed, source.Unattributed)
		if len(source.Reasons) > 0 {
			reasons := make([]string, 0, len(source.Reasons))
			for _, reason := range source.Reasons {
				reasons = append(reasons, fmt.Sprintf("%s=%d", reason.Reason, reason.Count))
			}
			part += " (" + strings.Join(reasons, ", ") + ")"
		}
		parts = append(parts, part)
	}
	surface.Detail = strings.Join(parts, "; ")
	switch {
	case attribution.Unattributed() == 0:
		surface.Status = codexSurfaceStatusOK
	case attributionSourceLostEveryEvent(attribution):
		// Every event of one provider and no Pane is the state defect B held
		// for the entire life of the neighbouring track, and it is not a
		// degradation of attribution: it is attribution not happening.
		//
		// The test is per source rather than over the total, because that
		// defect was one provider's. A busy neighbour attributing everything
		// would carry the aggregate and render the dead provider as a few
		// stale panes, which is the reading that let it live for eight phases.
		surface.Status = codexSurfaceStatusBroken
	default:
		surface.Status = codexSurfaceStatusDegraded
	}
	return surface
}

// attributionSourceLostEveryEvent reports whether some provider hook source
// failed to attribute every event it produced in the window.
func attributionSourceLostEveryEvent(attribution aiIngestAttributionHealth) bool {
	for _, source := range attribution.Sources {
		if source.Attributed == 0 && source.Unattributed > 0 {
			return true
		}
	}
	return false
}

// codexObserverReasonSurface judges whether authority transitions carry a
// reason that was captured rather than assumed.
//
// The two tokens it fails on are the ones that mean "nothing recorded why":
// the explicit unrecorded bucket, and the pre-instrumentation literal that a
// process older than the reason vocabulary still publishes.
func codexObserverReasonSurface(census *codexAuthorityCensus) codexControlPlaneSurface {
	surface := codexControlPlaneSurface{Surface: codexSurfaceObserverReason}
	if census == nil || census.Agents == 0 {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no managed Codex Agent to read"
		return surface
	}
	captured, uncaptured := 0, 0
	uncapturedTokens := make([]string, 0, 2)
	for _, reason := range census.Reasons {
		if codexReasonIsUncaptured(reason.Reason) {
			uncaptured += reason.Count
			uncapturedTokens = append(uncapturedTokens, fmt.Sprintf("%s=%d", reason.Reason, reason.Count))
			continue
		}
		captured += reason.Count
	}
	surface.Detail = fmt.Sprintf("captured reasons %d; uncaptured %d", captured, uncaptured)
	if uncaptured > 0 {
		surface.Detail += " (" + strings.Join(uncapturedTokens, ", ") + ")"
	}
	switch {
	case uncaptured == 0:
		surface.Status = codexSurfaceStatusOK
	case captured == 0:
		surface.Status = codexSurfaceStatusBroken
	default:
		surface.Status = codexSurfaceStatusDegraded
	}
	return surface
}

// codexReasonIsUncaptured reports whether one authority reason token means that
// no reason was captured.
//
// Both members are inside the closed vocabulary on purpose, so a reader can
// render them truthfully. That is also why they must be named here: a token
// that is legal to read is not thereby an observation, and treating one as an
// observation is what let a test certify a silent control plane as healthy.
func codexReasonIsUncaptured(reason string) bool {
	return reason == string(codexObserverReasonUnrecorded) ||
		reason == string(codexObserverReasonRetired) ||
		reason == "bounded reason unavailable"
}

// codexConnectionContinuitySurface judges whether the upstream app-server
// connection is staying up.
//
// The count is cumulative over the runtime's life and is reported as such. No
// rate is derived from it: this reader does not know the runtime's uptime, and
// inventing a rate from a single sample would put a number on the section that
// the sample does not support.
func codexConnectionContinuitySurface(broker *codexBrokerDiagnostic) codexControlPlaneSurface {
	surface := codexControlPlaneSurface{Surface: codexSurfaceConnectionContinuity}
	if broker == nil || broker.State != codexBrokerStateRunning {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no running runtime to read continuity from"
		return surface
	}
	detail := fmt.Sprintf("connection epoch %d; reconnects %d; upstream connections %d",
		broker.ConnectionEpoch, broker.Reconnects, broker.Connections)
	if len(broker.Revocations) > 0 {
		reasons := make([]string, 0, len(broker.Revocations))
		for _, revocation := range broker.Revocations {
			reasons = append(reasons, fmt.Sprintf("%s=%d", revocation.Reason, revocation.Count))
		}
		detail += " (revocations " + strings.Join(reasons, ", ") + ")"
	}
	surface.Detail = detail
	if broker.Reconnects == 0 {
		surface.Status = codexSurfaceStatusOK
		return surface
	}
	surface.Status = codexSurfaceStatusDegraded
	return surface
}

// codexTurnAdmissionSurface judges whether a turn admission would read a
// completed authority transition.
//
// A torn snapshot is the observable signature of the read that refused live
// Panes their turn. An unfenced Pane is reported beside it rather than folded
// into it: that Pane's writer never took a fence, which is a statement about
// which build is running, not about whether the fence works.
func codexTurnAdmissionSurface(census *codexAuthorityCensus) codexControlPlaneSurface {
	surface := codexControlPlaneSurface{Surface: codexSurfaceTurnAdmission}
	if census == nil || census.Agents == 0 {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no managed Codex Agent to read"
		return surface
	}
	observed := census.Settled + census.Contended + census.Unfenced
	if observed == 0 {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no authority snapshot was classified"
		return surface
	}
	surface.Detail = fmt.Sprintf("authority snapshots %d settled, %d contended, %d unfenced; torn %d",
		census.Settled, census.Contended, census.Unfenced, census.Torn)
	switch {
	case census.Torn > 0:
		surface.Status = codexSurfaceStatusBroken
	case census.Unfenced > 0:
		surface.Status = codexSurfaceStatusDegraded
	default:
		surface.Status = codexSurfaceStatusOK
	}
	return surface
}

// codexHookDeliverySurface judges whether an attributed hook event actually
// changed the Pane it reached.
//
// The verdict turns on whether a failure said anything. A delivery that fails
// with a bounded reason is a fault an operator can act on; one that fails with
// a raw process-exit string reports that something ended and nothing else, and
// a path in that position is a leak besides. The two are separated because the
// second is the state this surface was opened on.
func codexHookDeliverySurface(delivery aiIngestDeliveryHealth) codexControlPlaneSurface {
	surface := codexControlPlaneSurface{Surface: codexSurfaceHookDelivery}
	if !delivery.Observed {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no hook ingest log was readable"
		return surface
	}
	if delivery.Records == 0 {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no attributed hook event in the reading window"
		return surface
	}
	parts := make([]string, 0, len(delivery.Sources))
	for _, source := range delivery.Sources {
		part := fmt.Sprintf("%s %d delivered, %d failed", source.Source, source.Delivered, source.Failed)
		if len(source.Reasons) > 0 {
			reasons := make([]string, 0, len(source.Reasons))
			for _, reason := range source.Reasons {
				reasons = append(reasons, fmt.Sprintf("%s=%d", reason.Reason, reason.Count))
			}
			part += " (" + strings.Join(reasons, ", ") + ")"
		}
		parts = append(parts, part)
	}
	surface.Detail = strings.Join(parts, "; ")
	switch {
	case delivery.Opaque() > 0:
		surface.Status = codexSurfaceStatusBroken
	case delivery.Failed() > 0:
		surface.Status = codexSurfaceStatusDegraded
	default:
		surface.Status = codexSurfaceStatusOK
	}
	return surface
}

// codexPaneOwnershipSurface judges whether attributed hook events landed on a
// Pane of their own provider.
//
// A foreign attribution is the one failure mode that looks like success from
// every other angle: the event was attributed, the write succeeded, and the
// Pane it moved belongs to someone else. Nothing else in this diagnosis can
// see it, which is why it is a surface of its own rather than a note on
// attribution.
func codexPaneOwnershipSurface(ownership aiIngestOwnershipHealth) codexControlPlaneSurface {
	surface := codexControlPlaneSurface{Surface: codexSurfacePaneOwnership}
	if !ownership.Observed {
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no hook ingest log and Registry pair was readable"
		return surface
	}
	if ownership.Classified == 0 {
		surface.Status = codexSurfaceStatusUnobserved
		surface.Detail = fmt.Sprintf("no attribution could be judged; %d landed on a Pane the Registry no longer holds", ownership.Unresolved)
		return surface
	}
	surface.Detail = fmt.Sprintf("attributions judged %d; foreign %d; unresolved %d",
		ownership.Classified, ownership.Foreign, ownership.Unresolved)
	if ownership.Foreign > 0 {
		surface.Status = codexSurfaceStatusBroken
		return surface
	}
	surface.Status = codexSurfaceStatusOK
	return surface
}

func codexEndpointPresence(endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		return "absent"
	}
	return "present"
}

func codexSurfaceReasonOrNone(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "none"
	}
	return reason
}

// codexControlPlaneVintageText renders the deployment vintage line.
//
// It leads the section rather than sitting inside it because it qualifies every
// verdict below it. `make install` replaces the file on disk and leaves running
// processes on the image they started with, so a green surface read from a
// replaced-image process describes code that process never loaded.
func codexControlPlaneVintageText(vintage codexControlPlaneVintage) string {
	if !vintage.Supported {
		return "unknown on this platform; a process running a replaced image cannot be told apart from one running the installed build"
	}
	if len(vintage.Roles) == 0 {
		return "no control-plane process observed"
	}
	parts := make([]string, 0, len(vintage.Roles))
	for _, role := range vintage.Roles {
		parts = append(parts, fmt.Sprintf("%s %d (%s %d, %s %d)",
			role.Role, role.Processes,
			codexProcessVintageCurrent, role.Current,
			codexProcessVintageReplaced, role.Replaced))
	}
	text := strings.Join(parts, "; ")
	if vintage.Replaced() > 0 {
		text += "; a replaced image did not load the installed build, so every verdict below it describes older code"
	}
	return text
}
