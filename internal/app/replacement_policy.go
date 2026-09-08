package app

import (
	"strings"
	"time"
)

// The L2 replacement policy: which long-lived processes an install may end, and
// how long a replacement is allowed to remain unfinished before the install
// says so.
//
// `make install` publishes a new executable and leaves every running child on
// the image it started with. The census in codex_controlplane_vintage.go names
// those children; this file decides what an install is allowed to do about each
// name, and doctor's L2 row reports the result.
//
// Two decisions are fixed here rather than at a call site, because both were
// made from measurement and neither is a call site's to re-open.

// The per-role disposition. A role is either something an install asks to
// drain, or something it reports and leaves alone.
//
// There is no third value on purpose. "Kill it" is not one of these: the drain
// this application ships carries live work to its end before the runtime
// closes, and the one hard kill this repository has measured -- 2026-09-05,
// four panes severed -- brought three of the four back automatically and left
// the fourth wedged. A disposition that severs work is therefore not a default
// this file offers.
const (
	// replacementDispositionDrain is a role an install asks to stand down
	// through a route this application already ships. The process decides when
	// it actually goes; the install only makes the request.
	replacementDispositionDrain = "drain"
	// replacementDispositionReportOnly is a role no install touches. It is
	// reported with the route that does replace it, so an operator reading the
	// row knows what would.
	replacementDispositionReportOnly = "report-only"
)

// replacementRolePolicy is one role's entry in the table.
type replacementRolePolicy struct {
	// Disposition is what an install does about this role.
	Disposition string
	// Route is how this role is actually replaced. For a drain role it is the
	// shipped path the install asks; for a report-only role it is what an
	// operator or the ordinary lifecycle does instead.
	Route string
}

// replacementRolePolicies fixes the disposition of every role the census names.
//
// Exactly one role drains. That is not a placeholder for a longer list: a drain
// is a protocol, this phase is explicitly forbidden from inventing one, and
// `broker-runtime` is the only role that ships with a drain today. Every other
// role is replaced by an event that already exists -- a pane recreated, an
// activation restarted, a lease taken again -- and an install that signalled
// them would be severing work to save the operator an action they can take
// themselves.
//
// `session-client` is the one entry that is a decision rather than an
// observation, and it is not reconsidered here. That role is the operator's own
// attached tmux session, the longest-lived role on this repository's ledger at
// 169h29m, and a uniform cutoff applied to it ends the terminal the install was
// typed into. TestReplacementSessionClientIsNeverADrainTarget holds it.
var replacementRolePolicies = map[string]replacementRolePolicy{
	codexControlPlaneRoleBroker: {
		Disposition: replacementDispositionDrain,
		Route:       "broker-drain",
	},
	codexControlPlaneRoleObserver: {
		Disposition: replacementDispositionReportOnly,
		Route:       "pane-relaunch",
	},
	projmuxProcessRoleSupervisor: {
		Disposition: replacementDispositionReportOnly,
		Route:       "pane-relaunch",
	},
	projmuxProcessRoleSessionClient: {
		Disposition: replacementDispositionReportOnly,
		Route:       "operator-reattach",
	},
	projmuxProcessRoleAgentEndpoint: {
		Disposition: replacementDispositionReportOnly,
		Route:       "pane-relaunch",
	},
	projmuxProcessRoleUsageWatcher: {
		Disposition: replacementDispositionReportOnly,
		Route:       "lease-expiry",
	},
	projmuxProcessRoleOther: {
		Disposition: replacementDispositionReportOnly,
		Route:       "process-exit",
	},
}

// replacementRoleDisposition answers what an install does about one role.
//
// A role with no entry is report-only. The census keeps an unnamed remainder so
// the fleet total stays whole, and a name this table has not been taught is
// exactly the case where doing nothing is right.
func replacementRoleDisposition(role string) string {
	policy, ok := replacementRolePolicies[role]
	if !ok {
		return replacementDispositionReportOnly
	}
	return policy.Disposition
}

// The drain cutoff.
//
// A drain that carries live work to its end has no bound of its own: the
// runtime stays up while it still has bindings, and a binding that is never
// released keeps it up forever. The install has to decide when to stop calling
// such a replacement "in progress" and start calling it unfinished, and the
// only defensible place to put that line is the measured survival of the
// processes an install actually leaves behind.
//
// `projmux internal install-residue --survival` on this repository's ledger at
// 2026-09-08 (55 records, 884 residual observations deduplicated to 137
// distinct processes) gives the fraction of residual processes still running at
// each cutoff:
//
//	1h    61.3%      12h   13.9%      72h    5.1%
//	4h    27.7%      24h    9.5%      168h   2.9%
//
// Read as a truncation rate, that is the fraction of replacements this cutoff
// would report as unfinished. 1h and 4h report a majority and a large minority
// of ordinary drains as failures, which makes the token noise rather than a
// discriminant. Past 24h the curve flattens -- 24h to 72h buys 4.4 points and
// costs two more days before an operator learns a replacement never completed.
//
// 24h is also the horizon the answer is read on. A residual process a day old
// is not a drain still finishing its work; it is a drain that is not going to.
//
// Adopted: 24h, truncation rate 9.5% by process identity, 13 of 137.
//
// The rate is what gets reported, not what gets severed. Reaching this cutoff
// ends the install's claim about the replacement, never the process: the row
// turns to `replacement-cutoff-reached` and the process keeps running. That is
// the whole difference between this bound and a kill timer.
const replacementDrainCutoff = 24 * time.Hour

// replacementCutoffEnv overrides the cutoff for one process.
//
// It exists so the cutoff branch is reachable in a bounded test and in an
// isolated-fleet smoke, where waiting a day to observe the token is not an
// option. It takes a Go duration string.
const replacementCutoffEnv = "PROJMUX_REPLACEMENT_CUTOFF"

// resolveReplacementCutoff reads the cutoff this process runs under.
//
// An unset, unparsable, or non-positive value is the adopted default. There is
// no value that disables the cutoff: an unbounded drain is the state this
// bound exists to rule out, and offering a spelling for it would put that state
// one environment variable away.
func resolveReplacementCutoff(getenv func(string) string) time.Duration {
	if getenv == nil {
		return replacementDrainCutoff
	}
	raw := strings.TrimSpace(getenv(replacementCutoffEnv))
	if raw == "" {
		return replacementDrainCutoff
	}
	cutoff, err := time.ParseDuration(raw)
	if err != nil || cutoff <= 0 {
		return replacementDrainCutoff
	}
	return cutoff
}

// replacementResidualBeyondCutoff counts the residual processes that have
// outlived the cutoff, across every role of one census.
//
// It reads the age distribution the census already carries. A residual process
// whose start instant the platform could not establish contributes no sample
// and is therefore not counted as beyond the cutoff: an unreadable age is not
// evidence of an old process, and inventing one here would put a replacement
// into the failed column on no evidence at all.
func replacementResidualBeyondCutoff(roles []projmuxProcessRoleVintage, cutoff time.Duration) int {
	if cutoff <= 0 {
		return 0
	}
	bound := int(cutoff / time.Second)
	beyond := 0
	for _, role := range roles {
		for _, age := range role.ReplacedAgeSeconds {
			if age > bound {
				beyond++
			}
		}
	}
	return beyond
}

// replacementResidualByDisposition splits one census's residual processes into
// the count an install asks to drain and the count it only reports.
func replacementResidualByDisposition(roles []projmuxProcessRoleVintage) (drain int, report int) {
	for _, role := range roles {
		if role.Replaced <= 0 {
			continue
		}
		if replacementRoleDisposition(role.Role) == replacementDispositionDrain {
			drain += role.Replaced
			continue
		}
		report += role.Replaced
	}
	return drain, report
}
