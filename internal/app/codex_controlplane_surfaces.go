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
	Vintage codexControlPlaneVintage `json:"vintage"`
	// HookWindow is the record span the three hook surfaces were counted over.
	HookWindow string                     `json:"hook_window,omitempty"`
	Surfaces   []codexControlPlaneSurface `json:"surfaces"`
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
	report := codexControlPlaneReport{Vintage: vintage, HookWindow: codexHookWindowText(hooks)}
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
	// From and To bound the records all three were counted over.
	From string
	To   string
}

// codexHookWindowText names the span the hook counts are cumulative over.
//
// Without it, a repair that lands mid-window looks like it did nothing until
// the records that predate it scroll out, and then looks like time fixed it.
func codexHookWindowText(hooks codexHookHealth) string {
	if strings.TrimSpace(hooks.From) == "" || strings.TrimSpace(hooks.To) == "" {
		return ""
	}
	return hooks.From + " to " + hooks.To
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
//
// The verdict counts only the events the contract owed a Pane. An event whose
// conversation no live Pane owns is outside that scope, and reporting the
// refusal as breakage would make this row fire on a machine whose hooks are
// behaving exactly as specified. A gate that cries wolf gets ignored, and being
// ignored is how the defects this section exists to catch survived in the first
// place.
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
		if reasons := codexReasonBreakdown(source.Reasons); reasons != "" {
			part += " (" + reasons + ")"
		}
		if source.Refused > 0 {
			// Reported, never folded in. A retired conversation still firing
			// hooks is the contract declining what it never promised, and an
			// operator who cannot see that number would read the zero above it
			// as nothing having happened.
			part += fmt.Sprintf("; %d out of contract", source.Refused)
			if reasons := codexReasonBreakdown(source.RefusalReasons); reasons != "" {
				part += " (" + reasons + ")"
			}
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
		surface.Status, surface.Detail = codexSurfaceStatusUnobserved, "no reflection write was attempted in the reading window"
		return surface
	}
	parts := make([]string, 0, len(delivery.Sources))
	for _, source := range delivery.Sources {
		part := fmt.Sprintf("%s %d delivered, %d failed", source.Source, source.Delivered, source.Failed)
		if reasons := codexReasonBreakdown(source.Reasons); reasons != "" {
			// The breakdown stays next to the count it explains. Anything
			// pushed between them reads as explaining the wrong number.
			part += " (" + reasons + ")"
		}
		if source.Quiet > 0 {
			// Beside the two counts, never inside them. A lane that writes
			// nothing cannot fail to write, and folding it in dilutes the rate
			// the verdict is about.
			part += fmt.Sprintf("; %d made no write", source.Quiet)
		}
		if source.PathBearing > 0 {
			// Counted, never folded into the verdict. A reason can name its
			// cause precisely and still carry a path in the explanation behind
			// it; those are two properties about two halves of one string.
			part += fmt.Sprintf("; %d carrying a path", source.PathBearing)
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
//
// An `ok` here is narrower than it looks, and the detail says so. The
// guarantee behind it closed the case where a Pane positively records another
// provider; a Pane the Registry holds without a provider, or one it no longer
// holds at all, resolves the old way. Those are counted beside the verdict
// rather than folded into it, so nobody reads a zero as "nothing was
// misattributed" when what it means is "nothing provably was".
//
// The same holds, for a different reason, of attributions naming a Pane the
// Registry no longer has. That count moves with events this track does not
// cause and cannot see: when a neighbouring track retired one worker, a single
// Pane carrying 134 records in the window went from live to absent, and the
// unresolved count went from zero to two hundred odd between two readings of
// unchanged code. A verdict driven by that would go red and green on its own,
// which is the definition of a signal nobody can act on. It is reported and
// never judged.
//
// Why the unjudgeable share does not make this row red, when it has been more
// than a third of the window on a live machine:
//
// It is not a fault. A Pane that records no provider is silence, and the
// ownership rule deliberately treats silence as permission -- refusing there
// would break attribution for every ordinary Pane. Colouring a designed,
// permanent condition red would ask an operator to act on behaviour that is
// specified, and a row that is always red is a row that gets skipped. This
// diagnosis has already had to unlearn that once, when a contractual refusal
// was being counted as breakage.
//
// What must not happen is the number being invisible, and it is not: the
// denominator leads the detail, so `foreign 0` cannot be read without also
// reading how much of the window it covers. If that share ever needs a verdict
// of its own, it needs a contract of its own first -- a claim about how many
// Panes ought to record a provider, which nothing here makes today.
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
	// The denominator leads, because the verdict is only about the numerator.
	// "judged 718" beside a separate "unrecorded 267" invites reading the first
	// number as the whole population; "judged 718 of 989" cannot be read that
	// way.
	surface.Detail = fmt.Sprintf("judged %d of %d attributions; foreign %d; unresolved %d; provider unrecorded %d",
		ownership.Classified, ownership.Classified+ownership.Unresolved+ownership.Unrecorded,
		ownership.Foreign, ownership.Unresolved, ownership.Unrecorded)
	if ownership.Misrouted > 0 {
		// Counted over the whole window rather than over the judged
		// attributions, because most misrouted records never reach a Pane at
		// all: they fail to attribute, land in the contractual refusal bucket
		// where they belong, and become indistinguishable from an ordinary
		// retired conversation. That is where 96 of 98 of them hid.
		surface.Detail += fmt.Sprintf("; %d record(s) misrouted across providers in the window", ownership.Misrouted)
	}
	if directions := codexReasonBreakdown(ownership.Directions); directions != "" {
		// Which way the mismatch runs is the whole diagnosis. One direction is
		// a hook landing on the Pane that launched its host; the other is an
		// event that arrived under the wrong source before attribution ran.
		surface.Detail += " (" + directions + ")"
	}
	if ownership.Foreign > 0 {
		surface.Status = codexSurfaceStatusBroken
		return surface
	}
	if ownership.Misrouted > 0 {
		// Degraded rather than broken. A misrouted record is real and worth
		// seeing, but the routing that produced it lives outside this
		// application -- a provider host still serving a route its
		// configuration no longer carries -- so there is nothing here to
		// repair and a permanent red would train the row away.
		surface.Status = codexSurfaceStatusDegraded
		return surface
	}
	surface.Status = codexSurfaceStatusOK
	return surface
}

// codexReasonBreakdown renders one closed-token distribution.
func codexReasonBreakdown(reasons []aiIngestAttributionReason) string {
	if len(reasons) == 0 {
		return ""
	}
	rendered := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		rendered = append(rendered, fmt.Sprintf("%s=%d", reason.Reason, reason.Count))
	}
	return strings.Join(rendered, ", ")
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
