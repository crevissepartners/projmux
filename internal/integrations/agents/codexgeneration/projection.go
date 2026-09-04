package codexgeneration

type CreateRoute string

const CreateRoutePlainFallback CreateRoute = "plain-fallback"

// Projection is the byte-semantic value shared by Doctor and the create
// planner. Phase 1 publishes qualification truth but never opens the Phase 2
// remote-new route, so every row deliberately retains the plain fallback.
type Projection struct {
	CacheKey      string      `json:"cache_key"`
	DurableResume Verdict     `json:"durable_zero_turn_resume"`
	RemoteNew     Verdict     `json:"remote_new_session"`
	CreateRoute   CreateRoute `json:"create_route"`
	Reason        string      `json:"reason"`
}

func Project(record Record) Projection {
	projection := Projection{
		CacheKey: record.CacheKey, DurableResume: VerdictUnknown, RemoteNew: VerdictUnknown,
		CreateRoute: CreateRoutePlainFallback, Reason: "exact-capability-unknown",
	}
	if record.Validate() != nil {
		return projection
	}
	projection.DurableResume = record.DurableResume.Verdict
	projection.RemoteNew = record.RemoteNew.Verdict
	projection.Reason = "phase-2-native-route-disabled"
	if record.DurableResume.Verdict == VerdictUnsupported && record.RemoteNew.Verdict != VerdictSupported {
		projection.Reason = "payload-free-capability-unsupported"
	}
	return projection
}
