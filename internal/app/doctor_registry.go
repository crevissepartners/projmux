package app

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// The Registry materialization invariant audit.
//
// `Registry.Validate` is the one gate every write passes and
// `planRegistryTopology` is what turns a stored Project back into a running one.
// The two do not share a precondition: Validate accepts a Window that owns no
// shell Pane and therefore carries an empty `spec.primaryPaneRef`, and the
// planner cannot build a Window from it. That difference set is reachable
// through ordinary use, and until now nothing reported it: a Registry whose
// stored topology can no longer be materialized passed both validation and
// diagnostics as healthy.
//
// This section reports that difference. The verdict is deliberately not a
// hand-written list of suspect shapes -- it is the consumer predicate itself.
// Every Project is planned through the shipped planner with no observed
// sessions, and the refusals that plan records *are* the difference. A second
// copy of "which stored topology can be materialized" living here is exactly the
// drift the section exists to detect, so there is none: the audit owns the
// grouping and the counting, and nothing else.
//
// The audit is read-only in the strongest available sense. The Registry read is
// the zero-write snapshot read, so a machine that has never created a Project
// does not get a state directory out of running diagnostics, and the tmux runner
// handed to the planner refuses every call rather than reaching a server.
const (
	doctorRegistryCodeAudited     = "registry.materialize.audited"
	doctorRegistryCodeClean       = "registry.materialize.clean"
	doctorRegistryCodeUnavailable = "registry.materialize.unavailable"

	doctorRegistryCodePrefix = "registry.materialize."
)

// doctorRegistryRefusalScopes are the two consequences one refusal can have,
// read off the shipped refusalScope split rather than re-decided here. `fatal`
// means the Project does not activate at all; `skipped` means that one stored
// resource is left out and the rest of the Project still opens.
var doctorRegistryRefusalScopes = []string{"fatal", "skipped"}

// doctorRegistryRefusalKinds is the closed resource-kind vocabulary the section
// reports. `other` is the catch-all that keeps a future refusal kind inside the
// published code inventory instead of inventing an unlisted code.
var doctorRegistryRefusalKinds = []string{"project", "window", "pane", "agent", "other"}

// doctorRegistryAuditCodeInventory is every code this section can emit. It is
// derived from the two vocabularies above so a new scope or kind cannot reach
// the report without also reaching the inventory the support-report allowlist
// tracks.
var doctorRegistryAuditCodeInventory = doctorRegistryAuditCodes()

func doctorRegistryAuditCodes() []string {
	out := []string{doctorRegistryCodeAudited, doctorRegistryCodeClean, doctorRegistryCodeUnavailable}
	for _, scope := range doctorRegistryRefusalScopes {
		for _, kind := range doctorRegistryRefusalKinds {
			out = append(out, doctorRegistryRefusalCode(scope, kind))
		}
	}
	return out
}

func doctorRegistryRefusalCode(scope, kind string) string {
	return doctorRegistryCodePrefix + scope + "." + kind
}

// doctorRegistryRefusalKind maps one refused plan item onto the published kind
// vocabulary. An unrecognized kind becomes `other` so the emitted code is always
// one the inventory names.
func doctorRegistryRefusalKind(raw string) string {
	kind := strings.ToLower(strings.TrimSpace(raw))
	for _, known := range doctorRegistryRefusalKinds {
		if known != "other" && kind == known {
			return kind
		}
	}
	return "other"
}

var errDoctorRegistryAuditOffline = errors.New("doctor registry invariant audit does not read a tmux server")

// doctorOfflineTmuxRunner is the audit's proof that the invariant section never
// touches a tmux server.
//
// planRegistryTopology reads tmux only for a Project some live session already
// claims, and the audit observes no sessions at all, so every path that would
// reach this runner is one the audit does not take. It answers with an error
// rather than being a nil interface so that if a future change does put a read
// on the offline path, the section degrades to `unavailable` instead of
// panicking inside a read-only diagnostic.
type doctorOfflineTmuxRunner struct{}

func (doctorOfflineTmuxRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errDoctorRegistryAuditOffline
}

// evaluateRegistryInvariants counts the admission difference between what the
// Registry writer accepts and what the materializer can build.
//
// Zero is a reported result, not silence. A clean audit and a section that never
// ran are indistinguishable to an operator, so the clean case emits its own
// finding and the number of Projects actually planned is always stated.
func (c *doctorCommand) evaluateRegistryInvariants() []doctorFinding {
	read := c.readRegistry
	if read == nil {
		read = snapshotResourceRegistry
	}
	registry, err := read()
	if err != nil {
		return []doctorFinding{doctorRegistryUnavailableFinding()}
	}
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		return []doctorFinding{doctorRegistryUnavailableFinding()}
	}
	// The reconciler is the production one, wired to the offline runner. It is
	// consulted for exactly one thing here -- the session name a Project with no
	// recorded session projection would open under -- and taking that from
	// anywhere else would make the audit refuse Projects the real activation
	// route opens fine.
	runner := doctorOfflineTmuxRunner{}
	reconciler := newRegistryReconciler(runner, inttmux.NewClient(runner))
	reconciler.refuseForeign = true
	reconciler.targetLiveOnly = true

	ctx := context.Background()
	audited := 0
	counts := map[string]int{}
	reasons := map[string]map[string]int{}
	for i := range registry.Projects {
		plan, err := planRegistryTopology(ctx, runner, registry,
			selector.UIDPrefix+registry.Projects[i].Metadata.UID, reconciler, nil, target, nil)
		if err != nil || plan == nil {
			return []doctorFinding{doctorRegistryUnavailableFinding()}
		}
		audited++
		fatal, skipped := plan.refusalScope()
		for _, group := range []struct {
			scope string
			items []resourceReconcileItem
		}{{scope: "fatal", items: fatal}, {scope: "skipped", items: skipped}} {
			for _, item := range group.items {
				code := doctorRegistryRefusalCode(group.scope, doctorRegistryRefusalKind(item.Kind))
				counts[code]++
				if reasons[code] == nil {
					reasons[code] = map[string]int{}
				}
				reasons[code][item.Reason]++
			}
		}
	}

	findings := []doctorFinding{{
		Severity: doctorSeverityInfo, Code: doctorRegistryCodeAudited,
		Remediation: doctorRemediationNone, Count: audited,
	}}
	if len(counts) == 0 {
		return append(findings, doctorFinding{
			Severity: doctorSeverityInfo, Code: doctorRegistryCodeClean,
			Remediation: doctorRemediationNone,
		})
	}
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		severity := doctorSeverityWarning
		if strings.HasPrefix(code, doctorRegistryCodePrefix+"fatal.") {
			severity = doctorSeverityError
		}
		findings = append(findings, doctorFinding{
			Severity: severity, Code: code, Remediation: doctorRemediationInspectRegistryTopology,
			Count: counts[code], Details: doctorRegistryReasonDetails(reasons[code]),
		})
	}
	return findings
}

func doctorRegistryUnavailableFinding() doctorFinding {
	return doctorFinding{
		Severity: doctorSeverityWarning, Code: doctorRegistryCodeUnavailable,
		Remediation: doctorRemediationInspectState,
	}
}

// doctorRegistryReasonDetails renders one code's stored refusal reasons with the
// number of resources each accounts for.
//
// The reasons are the planner's own wording. They are never classified into a
// second vocabulary here: a reason table maintained beside the planner is a copy
// of the planner's verdict, and it would drift the moment a refusal is reworded.
func doctorRegistryReasonDetails(reasons map[string]int) []string {
	if len(reasons) == 0 {
		return nil
	}
	texts := make([]string, 0, len(reasons))
	for reason := range reasons {
		texts = append(texts, reason)
	}
	sort.Strings(texts)
	out := make([]string, 0, len(texts))
	for _, reason := range texts {
		label := strings.TrimSpace(reason)
		if label == "" {
			label = "(no reason recorded)"
		}
		out = append(out, label+" ("+strconv.Itoa(reasons[reason])+")")
	}
	return out
}
