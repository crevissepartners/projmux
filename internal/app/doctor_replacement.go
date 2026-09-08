package app

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The replacement and restoration table.
//
// Three layers replace an execution image, and a consumer of `make install`,
// `npm install`, or a provider generation upgrade believes one sentence about
// all three: from the moment the install returned success, the old image takes
// no new work. That sentence is false in a different way on each layer, and
// until now an operator had to establish each of them by hand from a different
// surface -- /proc links for one, a JSON Lines ledger for another, a generation
// journal for the third.
//
// This section is the one place that states, per layer, whether the replacement
// completed and whether the pre-replacement state can be restored. It reads
// only signals other sections already produce; it observes nothing new, starts
// nothing, writes nothing, and never repairs what it reports.
//
// docs/replacement-contract.md is the contract this renders. The reason-token
// vocabulary below and the vocabulary published there are held equal by
// TestDoctorReplacementReasonTokensMatchTheContractDocument.

// The replacement axis. It answers whether the layer holds a completed
// replacement: whether the image an install published is the one this layer now
// serves new work from.
const (
	doctorReplacementReplaced    = "replaced"
	doctorReplacementNotReplaced = "not-replaced"
	doctorReplacementUnknown     = "unknown"
)

// The restoration axis. It answers whether the pre-replacement state of this
// layer can be reached again through a route this application ships, without
// discarding durable state.
//
// `not-restorable` is a statement about the shipped routes, never a prediction
// about what an operator could achieve by other means. `unknown` is the honest
// answer when the evidence that would decide it was not readable.
const (
	doctorRestorationRestorable    = "restorable"
	doctorRestorationNotRestorable = "not-restorable"
	doctorRestorationUnknown       = "unknown"
)

// The three layers, and the subject each one is a verdict about.
const (
	doctorReplacementLayerImage     = "L1"
	doctorReplacementLayerProcesses = "L2"
	doctorReplacementLayerProvider  = "L3"

	doctorReplacementSubjectImage     = "installed-executable-image"
	doctorReplacementSubjectProcesses = "long-lived-projmux-processes"
	doctorReplacementSubjectProvider  = "provider-sessions-and-generations"
)

// The closed reason-token vocabulary.
//
// One token per row: the governing fact, chosen by the documented precedence of
// its layer. Everything else the layer observed stays on the row as signals, so
// a fact that lost the precedence contest is never lost from the report.
const (
	// doctorReplacementReasonUnsupportedPlatform is the one token shared by two
	// layers. Darwin exposes no /proc/<pid>/exe, so neither the image link nor
	// the process census can be taken there, and both answer `unknown` rather
	// than implying currency.
	doctorReplacementReasonUnsupportedPlatform = "unsupported-platform"

	// L1 tokens.
	doctorReplacementReasonImageCurrent    = "image-current"
	doctorReplacementReasonImageUnlinked   = "image-unlinked"
	doctorReplacementReasonImageUnresolved = "image-unresolved"

	// L2 tokens.
	//
	// doctorReplacementReasonCutoffReached is the bounded drain's own verdict.
	// A residual process that has outlived the replacement cutoff is not a
	// drain still finishing; it is a replacement this install is not going to
	// complete, and saying so is the whole reason the cutoff exists. It never
	// ends the process: the row changes, the fleet does not.
	doctorReplacementReasonCutoffReached     = "replacement-cutoff-reached"
	doctorReplacementReasonResidualProcesses = "residual-processes-present"
	doctorReplacementReasonNoResidual        = "no-residual-processes"
	doctorReplacementReasonLedgerResidue     = "install-residue-recorded"
	doctorReplacementReasonLedgerClean       = "install-residue-clean"
	doctorReplacementReasonNoObservedProcess = "no-observed-processes"

	// L3 tokens.
	doctorReplacementReasonPoolUnobserved    = "generation-pool-unobserved"
	doctorReplacementReasonSessionDead       = "registry-running-provider-session-dead"
	doctorReplacementReasonQualification     = "qualification-missing"
	doctorReplacementReasonPoolBlocked       = "generation-pool-blocked"
	doctorReplacementReasonHandoverRequired  = "generation-handover-required"
	doctorReplacementReasonSessionUnobserved = "registry-running-session-unobserved"
	doctorReplacementReasonPoolNotInstalled  = "generation-pool-not-installed"
	doctorReplacementReasonPoolReady         = "generation-pool-ready"
)

// doctorReplacementLayerReasons is the per-layer closed token list, in
// precedence order. The first entry a layer's evidence selects is the row's
// reason; the rest of that layer's evidence stays in the row's signals.
var doctorReplacementLayerReasons = map[string][]string{
	doctorReplacementLayerImage: {
		doctorReplacementReasonUnsupportedPlatform,
		doctorReplacementReasonImageUnresolved,
		doctorReplacementReasonImageUnlinked,
		doctorReplacementReasonImageCurrent,
	},
	doctorReplacementLayerProcesses: {
		doctorReplacementReasonUnsupportedPlatform,
		doctorReplacementReasonCutoffReached,
		doctorReplacementReasonResidualProcesses,
		doctorReplacementReasonNoResidual,
		doctorReplacementReasonLedgerResidue,
		doctorReplacementReasonLedgerClean,
		doctorReplacementReasonNoObservedProcess,
	},
	doctorReplacementLayerProvider: {
		doctorReplacementReasonPoolUnobserved,
		doctorReplacementReasonSessionDead,
		doctorReplacementReasonQualification,
		doctorReplacementReasonPoolBlocked,
		doctorReplacementReasonHandoverRequired,
		doctorReplacementReasonSessionUnobserved,
		doctorReplacementReasonPoolNotInstalled,
		doctorReplacementReasonPoolReady,
	},
}

// doctorReplacementLayerOrder is the render order of the table.
var doctorReplacementLayerOrder = []string{
	doctorReplacementLayerImage,
	doctorReplacementLayerProcesses,
	doctorReplacementLayerProvider,
}

// The signal keys a row can carry.
//
// A closed list, for the same reason the codes of every other doctor section
// are closed: a support-report allowlist that tracks this surface has to be
// able to name what can appear on it, and a key invented at a call site is a
// key nothing tracks.
const (
	doctorReplacementSignalPlatform          = "platform.observable"
	doctorReplacementSignalImageLink         = "image.link"
	doctorReplacementSignalRetainedImage     = "retained.previous-image"
	doctorReplacementSignalProcessesObserved = "processes.observed"
	doctorReplacementSignalProcessesResidual = "processes.residual"
	doctorReplacementSignalResidualOldest    = "residual.oldest-seconds"
	doctorReplacementSignalLedgerRecords     = "ledger.records"
	doctorReplacementSignalLedgerInstaller   = "ledger.latest.installer"
	doctorReplacementSignalLedgerObserved    = "ledger.latest.observed"
	doctorReplacementSignalLedgerResidual    = "ledger.latest.residual"
	// The replacement-pass signals. The first two are read from the live
	// census and the cutoff this process runs under, so they are on every
	// supported L2 row whether or not an install ever ran a pass. The rest are
	// the last pass's own account of what it did, and they are absent when no
	// pass has run -- an absent account is a different fact from a pass that
	// found nothing, and collapsing the two would hide which one happened.
	doctorReplacementSignalCutoffSeconds    = "replacement.cutoff-seconds"
	doctorReplacementSignalBeyondCutoff     = "replacement.beyond-cutoff"
	doctorReplacementSignalPassOutcome      = "replacement.outcome"
	doctorReplacementSignalPassRefusal      = "replacement.refusal"
	doctorReplacementSignalPassAttempted    = "replacement.attempted"
	doctorReplacementSignalPassDrained      = "replacement.drained"
	doctorReplacementSignalPassReported     = "replacement.reported"
	doctorReplacementSignalRegistryObserved = "registry.observed"
	doctorReplacementSignalPoolStatus       = "pool.status"
	doctorReplacementSignalPoolReason       = "pool.reason"
	doctorReplacementSignalPoolAction       = "pool.action"
	doctorReplacementSignalPoolLive         = "pool.generations.live"
	doctorReplacementSignalPoolDraining     = "pool.generations.draining"
	doctorReplacementSignalQualVerdict      = "qualification.verdict"
	doctorReplacementSignalQualReason       = "qualification.reason"
	doctorReplacementSignalSessionsRunning  = "sessions.running"
	doctorReplacementSignalSessionsLive     = "sessions.live"
	doctorReplacementSignalSessionsDead     = "sessions.dead"
	doctorReplacementSignalSessionsUnobs    = "sessions.unobservable"
)

// doctorReplacementSignalRoleResidualPrefix names one process role's residual
// count on the L2 row.
//
// The row's totals say how much an install left behind; they do not say what.
// A named role is the difference between a number an operator reads and a
// target they can act on, and the role vocabulary is the same closed list the
// install ledger records, so the two surfaces name the same processes with the
// same words. The keys stay a closed list for the same reason every other key
// here does: the role order below is the whole of it.
const doctorReplacementSignalRoleResidualPrefix = "residual.role."

// doctorReplacementSignalRoleResidual is one role's key on the L2 row.
func doctorReplacementSignalRoleResidual(role string) string {
	return doctorReplacementSignalRoleResidualPrefix + role
}

// doctorReplacementSignalInventory is every key this section can emit.
//
// The per-role L2 keys are expanded from the census role order rather than
// written out, so a role added to that vocabulary reaches this inventory --
// and, through the drift test, the contract document -- without a second edit
// that could be forgotten.
var doctorReplacementSignalInventory = buildDoctorReplacementSignalInventory()

func buildDoctorReplacementSignalInventory() []string {
	keys := append([]string(nil), doctorReplacementFixedSignalInventory...)
	for _, role := range projmuxProcessRoleOrder {
		keys = append(keys, doctorReplacementSignalRoleResidual(role))
	}
	return keys
}

var doctorReplacementFixedSignalInventory = []string{
	doctorReplacementSignalPlatform,
	doctorReplacementSignalImageLink,
	doctorReplacementSignalRetainedImage,
	doctorReplacementSignalProcessesObserved,
	doctorReplacementSignalProcessesResidual,
	doctorReplacementSignalResidualOldest,
	doctorReplacementSignalLedgerRecords,
	doctorReplacementSignalLedgerInstaller,
	doctorReplacementSignalLedgerObserved,
	doctorReplacementSignalLedgerResidual,
	doctorReplacementSignalCutoffSeconds,
	doctorReplacementSignalBeyondCutoff,
	doctorReplacementSignalPassOutcome,
	doctorReplacementSignalPassRefusal,
	doctorReplacementSignalPassAttempted,
	doctorReplacementSignalPassDrained,
	doctorReplacementSignalPassReported,
	doctorReplacementSignalRegistryObserved,
	doctorReplacementSignalPoolStatus,
	doctorReplacementSignalPoolReason,
	doctorReplacementSignalPoolAction,
	doctorReplacementSignalPoolLive,
	doctorReplacementSignalPoolDraining,
	doctorReplacementSignalQualVerdict,
	doctorReplacementSignalQualReason,
	doctorReplacementSignalSessionsRunning,
	doctorReplacementSignalSessionsLive,
	doctorReplacementSignalSessionsDead,
	doctorReplacementSignalSessionsUnobs,
}

// doctorReplacementSafeValue is the shape a signal value must have to be
// serialized.
//
// This surface publishes its discriminants in JSON, which the report's existing
// privacy rule allows only for closed-vocabulary tokens and counters: a stored
// refusal reason quotes the Registry's own absolute paths, and `doctorFinding`
// keeps those out of every serialization by construction. Every value here is
// either a counter this file formats or a token drawn from another closed
// vocabulary, so the pattern below is what proves the claim rather than
// restates it -- a path, an argv word, a pid rendered with its process, or any
// free-form sentence fails to match and is replaced with `unclassified`.
var doctorReplacementSafeValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,63}$`)

// doctorReplacementUnclassified is what an out-of-shape value becomes. The row
// keeps a discriminant -- the key still names which evidence decided it -- and
// the report keeps its promise that nothing free-form reaches a serialized
// surface.
const doctorReplacementUnclassified = "unclassified"

// doctorReplacementSignal is one piece of the underlying evidence a reason
// token was read from.
//
// The token alone is not a discriminant: `residual-processes-present` and
// `install-residue-recorded` both say an install left work behind, and only the
// counts under them say how much, how old, and whether the reading was live or
// from the ledger. Recovering the verdict from the report is what these exist
// for, and the negative test requires every row to carry at least one.
type doctorReplacementSignal struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// doctorReplacementRow is one layer's verdict on both axes.
type doctorReplacementRow struct {
	Layer       string                    `json:"layer"`
	Subject     string                    `json:"subject"`
	Replacement string                    `json:"replacement"`
	Restoration string                    `json:"restoration"`
	Reason      string                    `json:"reason"`
	Signals     []doctorReplacementSignal `json:"signals"`
}

// doctorReplacementReport is the whole table.
type doctorReplacementReport struct {
	Rows []doctorReplacementRow `json:"rows"`
}

// doctorInstalledImage is what a reader can establish about its own executable
// image without touching any other process.
//
// It is the L1 evidence, and it is deliberately three booleans rather than a
// path: the question is whether the image this diagnosis runs is still the one
// the installed path publishes, and answering it does not require naming the
// path on a diagnostics surface.
type doctorInstalledImage struct {
	// Supported reports whether this platform exposes the executable link this
	// reading is taken from. False is `unknown`, never `current`.
	Supported bool `json:"supported"`
	// Resolved reports that both the executable path and its link were
	// readable and named the same file.
	Resolved bool `json:"resolved"`
	// Unlinked reports that the running image was unlinked out from under this
	// process, which is what a completed publication leaves behind for every
	// process that started before it.
	Unlinked bool `json:"unlinked"`
}

// doctorProviderSessionCensus separates two facts the Registry states with one
// word.
//
// `Running` is a statement about a Pane's managed activation. It is not a
// statement about the provider session behind it: a Pane can be present in the
// runtime while the provider session it was bound to is gone, and the Registry
// spells both `Running`. This census counts how many Running Agents have
// positive live provider evidence, how many have evidence that positively
// contradicts `Running`, and how many rest on the Registry alone.
//
// It is count-only. No Agent UID, Pane UID, pid, or path reaches it, for the
// reason codexAuthorityCensus states: a diagnostics surface that names Agents
// is one that cannot be shared.
type doctorProviderSessionCensus struct {
	// Observed reports that a Registry was readable. Without it the zero
	// counts below would read as "no Running Agent", which is a different
	// claim from "nothing was read".
	Observed int `json:"observed"`
	// Running is how many Agents the Registry calls Running.
	Running int `json:"running"`
	// Live is how many of them have provider evidence that they are still
	// addressable.
	Live int `json:"live"`
	// Dead is how many of them have provider evidence that contradicts
	// Running. This is the number the mismatch token is emitted for.
	Dead int `json:"dead"`
	// Unobservable is how many of them rest on the Registry alone, with no
	// provider-side handle to check.
	Unobservable int `json:"unobservable"`
}

// doctorProviderSessionProbe is the provider-side evidence one activation can
// be checked against.
//
// The discriminant differs by provider and that difference is structural, not
// incidental. A Claude Pane carries a real process handle in
// `status.activation.claude.process`, so liveness is a direct question about a
// pid. A Codex Pane carries no pid at all: its activation names a thread and a
// composite authority, so the only thing that can be checked is whether that
// authority still belongs to the runtime currently serving the state domain.
type doctorProviderSessionProbe struct {
	// ProcessAlive answers whether one recorded process handle still names a
	// live process. Nil means no process evidence is available at all.
	ProcessAlive func(coremetadata.ProcessIdentity) bool
	// BrokerRuntimeID is the runtime identity currently serving this state
	// domain, and empty when no running runtime was observed. Empty is
	// unobservable, never foreign: a reader that reached no runtime cannot
	// tell a stale binding from a live one.
	BrokerRuntimeID string
}

// censusDoctorProviderSessions classifies every Running Agent in one Registry
// snapshot.
func censusDoctorProviderSessions(registry coremetadata.Registry, probe doctorProviderSessionProbe) doctorProviderSessionCensus {
	census := doctorProviderSessionCensus{Observed: 1}
	for _, agent := range registry.Agents {
		if agent.Status.Phase != coremetadata.PhaseRunning {
			continue
		}
		census.Running++
		switch classifyDoctorProviderSession(registry, agent, probe) {
		case doctorProviderSessionLive:
			census.Live++
		case doctorProviderSessionDead:
			census.Dead++
		default:
			census.Unobservable++
		}
	}
	return census
}

const (
	doctorProviderSessionLive         = "live"
	doctorProviderSessionDead         = "dead"
	doctorProviderSessionUnobservable = "unobservable"
)

// classifyDoctorProviderSession reads the strongest provider evidence one
// Running Agent's activation carries.
//
// Absence of evidence is `unobservable` on every path. A missing Pane, a
// missing binding, a Codex authority with no live runtime to compare against:
// none of them says the session is gone, and reporting any of them as `dead`
// would put an Agent into the number an operator acts on.
func classifyDoctorProviderSession(registry coremetadata.Registry, agent coremetadata.Agent, probe doctorProviderSessionProbe) string {
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok {
		return doctorProviderSessionUnobservable
	}
	activation := pane.Status.Activation
	switch agent.Spec.Provider {
	case aiModeClaude:
		if activation.Claude == nil || !activation.Claude.Process.Valid() || probe.ProcessAlive == nil {
			return doctorProviderSessionUnobservable
		}
		if probe.ProcessAlive(activation.Claude.Process) {
			return doctorProviderSessionLive
		}
		return doctorProviderSessionDead
	case aiModeCodex:
		binding := activation.Codex
		if binding == nil || binding.Authority == nil || !binding.Authority.Valid() {
			return doctorProviderSessionUnobservable
		}
		if strings.TrimSpace(probe.BrokerRuntimeID) == "" {
			return doctorProviderSessionUnobservable
		}
		if binding.Authority.BrokerRuntimeID == probe.BrokerRuntimeID {
			return doctorProviderSessionLive
		}
		return doctorProviderSessionDead
	default:
		return doctorProviderSessionUnobservable
	}
}

// doctorReplacementInputs is everything the table is projected from. Every
// field is a value another section already produced.
type doctorReplacementInputs struct {
	Image doctorInstalledImage
	// Processes is the whole-fleet vintage census, read through the seam the
	// runtime section already renders.
	Processes projmuxProcessVintage
	// Residue is the newest install-residue ledger record whose own census
	// observed something, and how many records the ledger holds. It answers
	// the L2 question on a machine where the live census observes nothing -- a
	// diagnosis taken from a build that is not the installed one observes no
	// children of itself, and the ledger is still the record of what the last
	// install left behind.
	//
	// Newest *informative* rather than newest: a record whose census observed
	// nothing is silent for exactly the reason the live census can be silent,
	// and reading it as a clean fleet would certify a replacement from
	// evidence that says nothing about one. ResidueOK is false when the ledger
	// holds no informative record at all, and ResidueRecords still reports its
	// length so the difference between "no ledger" and "a ledger of silent
	// records" stays on the row.
	Residue        installResidueRecord
	ResidueRecords int
	ResidueOK      bool
	// Cutoff is the drain cutoff this reader runs under. It is what turns the
	// residual age distribution into a verdict: below it a residual process is
	// a drain still in progress, above it the replacement is one this install
	// is not going to finish. Zero means the adopted default.
	Cutoff time.Duration
	// Replacement is the last install replacement pass's own account of what
	// it did, and ReplacementOK whether one was readable at all. The row
	// reaches its verdict from the live census either way; this is what lets a
	// reader tell a fleet nobody tried to replace from one where the attempt
	// was made and refused.
	Replacement   installReplacementOutcome
	ReplacementOK bool
	// Pool is the generation-pool diagnosis, nil when it was not read.
	Pool *doctorCodexGenerationPool
	// Sessions is the Running-versus-live-provider-session census.
	Sessions doctorProviderSessionCensus
}

// projectDoctorReplacement builds the three-layer table.
func projectDoctorReplacement(in doctorReplacementInputs) doctorReplacementReport {
	return doctorReplacementReport{Rows: []doctorReplacementRow{
		projectDoctorReplacementImageRow(in.Image),
		projectDoctorReplacementProcessRow(in),
		projectDoctorReplacementProviderRow(in.Pool, in.Sessions),
	}}
}

// projectDoctorReplacementImageRow reads L1: the installed executable image.
//
// Replacement here is the completed publication seen from the image this
// diagnosis runs. `image-current` is the state after a publication that
// finished and was not superseded; `image-unlinked` is a reader whose own image
// was replaced under it, which is a diagnosis taken from a superseded build.
//
// Restoration is `not-restorable` on every supported path, and that is a fact
// about the shipped install rather than a measurement: the publication copies
// the new binary over the installed path and retains no copy of what was there,
// and the recovery text the install itself prints on failure is config
// convergence, never a binary rollback.
func projectDoctorReplacementImageRow(image doctorInstalledImage) doctorReplacementRow {
	row := doctorReplacementRow{Layer: doctorReplacementLayerImage, Subject: doctorReplacementSubjectImage}
	if !image.Supported {
		row.Replacement, row.Restoration = doctorReplacementUnknown, doctorRestorationUnknown
		row.Reason = doctorReplacementReasonUnsupportedPlatform
		row.Signals = doctorReplacementSignals(
			doctorReplacementSignalPlatform, "false",
		)
		return row
	}
	if !image.Resolved {
		row.Replacement, row.Restoration = doctorReplacementUnknown, doctorRestorationUnknown
		row.Reason = doctorReplacementReasonImageUnresolved
		row.Signals = doctorReplacementSignals(
			doctorReplacementSignalPlatform, "true",
			doctorReplacementSignalImageLink, "unresolved",
		)
		return row
	}
	if image.Unlinked {
		row.Replacement, row.Restoration = doctorReplacementNotReplaced, doctorRestorationNotRestorable
		row.Reason = doctorReplacementReasonImageUnlinked
	} else {
		row.Replacement, row.Restoration = doctorReplacementReplaced, doctorRestorationNotRestorable
		row.Reason = doctorReplacementReasonImageCurrent
	}
	link := "current"
	if image.Unlinked {
		link = "unlinked"
	}
	row.Signals = doctorReplacementSignals(
		doctorReplacementSignalPlatform, "true",
		doctorReplacementSignalImageLink, link,
		doctorReplacementSignalRetainedImage, "none",
	)
	return row
}

// projectDoctorReplacementProcessRow reads L2: this application's own
// long-lived processes.
//
// An install replaces a file and leaves every running process on the image it
// started with, so this layer is `not-replaced` exactly while a process from
// before the last install is still running. Restoration is `restorable`
// wherever the census could be taken: a residual process is ended and
// relaunched through routes this application already ships, and its age
// distribution is observable, so the drain is bounded rather than open-ended.
//
// The ledger is consulted only when the live census observed nothing. That is
// not a fallback for convenience: the census counts children of *this*
// executable, so a diagnosis run from a build that is not the installed one
// observes an empty fleet, and reporting that as `replaced` would certify a
// replacement from evidence that says nothing about it. A ledger record whose
// own census observed nothing is silent for the same reason and is skipped by
// the reader, so a run of silent records cannot stand in for an install.
func projectDoctorReplacementProcessRow(in doctorReplacementInputs) doctorReplacementRow {
	vintage, residue, records, residueOK := in.Processes, in.Residue, in.ResidueRecords, in.ResidueOK
	cutoff := in.Cutoff
	if cutoff <= 0 {
		cutoff = replacementDrainCutoff
	}
	row := doctorReplacementRow{Layer: doctorReplacementLayerProcesses, Subject: doctorReplacementSubjectProcesses}
	if !vintage.Supported {
		row.Replacement, row.Restoration = doctorReplacementUnknown, doctorRestorationUnknown
		row.Reason = doctorReplacementReasonUnsupportedPlatform
		row.Signals = doctorReplacementSignals(doctorReplacementSignalPlatform, "false")
		return row
	}
	observed, replaced := vintage.Observed(), vintage.Replaced()
	beyond := replacementResidualBeyondCutoff(vintage.Roles, cutoff)
	signals := []string{
		doctorReplacementSignalPlatform, "true",
		doctorReplacementSignalProcessesObserved, strconv.Itoa(observed),
		doctorReplacementSignalProcessesResidual, strconv.Itoa(replaced),
		doctorReplacementSignalCutoffSeconds, strconv.FormatInt(int64(cutoff/time.Second), 10),
		doctorReplacementSignalBeyondCutoff, strconv.Itoa(beyond),
	}
	// Roles keep the census order rather than the notice's biggest-first
	// order: this row is read down a column against other runs, and a row
	// whose keys reorder with the fleet cannot be diffed against yesterday's.
	// A role with no residual process is omitted rather than printed as a
	// zero, exactly as the install notice omits it -- the row states what an
	// install did not replace, and a zero is not one of those.
	for _, role := range vintage.Roles {
		if role.Replaced <= 0 {
			continue
		}
		signals = append(signals, doctorReplacementSignalRoleResidual(role.Role), strconv.Itoa(role.Replaced))
	}
	if oldest, ok := projmuxProcessRolesOldestResidualAge(vintage.Roles); ok {
		signals = append(signals, doctorReplacementSignalResidualOldest, strconv.Itoa(oldest))
	}
	if records > 0 {
		signals = append(signals, doctorReplacementSignalLedgerRecords, strconv.Itoa(records))
	}
	if residueOK {
		signals = append(signals,
			doctorReplacementSignalLedgerInstaller, residue.Installer,
			doctorReplacementSignalLedgerObserved, strconv.Itoa(residue.Observed),
			doctorReplacementSignalLedgerResidual, strconv.Itoa(residue.Replaced),
		)
	}
	// The last pass's account, when there is one. `replacement-not-attempted`
	// is written by no pass: it is what this reader says when the record is
	// absent, so an install that never ran the pass and a pass that ran and
	// found nothing stay two different answers.
	outcome := installReplacementOutcomeNotAttempted
	if in.ReplacementOK {
		outcome = in.Replacement.Outcome
		signals = append(signals,
			doctorReplacementSignalPassAttempted, strconv.Itoa(in.Replacement.Attempted),
			doctorReplacementSignalPassDrained, strconv.Itoa(in.Replacement.Drained),
			doctorReplacementSignalPassReported, strconv.Itoa(in.Replacement.Reported),
		)
		if refusal := strings.TrimSpace(in.Replacement.Refusal); refusal != "" {
			signals = append(signals, doctorReplacementSignalPassRefusal, refusal)
		}
	}
	signals = append(signals, doctorReplacementSignalPassOutcome, outcome)
	row.Signals = doctorReplacementSignals(signals...)
	switch {
	case replaced > 0 && beyond > 0:
		// The bounded drain's terminal state. The residual processes are still
		// running and are left running: this row is the report the cutoff
		// produces instead of a kill, and `restorable` stays true because the
		// route that ends and relaunches them is the same one it always was.
		row.Replacement, row.Restoration = doctorReplacementNotReplaced, doctorRestorationRestorable
		row.Reason = doctorReplacementReasonCutoffReached
	case replaced > 0:
		row.Replacement, row.Restoration = doctorReplacementNotReplaced, doctorRestorationRestorable
		row.Reason = doctorReplacementReasonResidualProcesses
	case observed > 0:
		row.Replacement, row.Restoration = doctorReplacementReplaced, doctorRestorationRestorable
		row.Reason = doctorReplacementReasonNoResidual
	case residueOK && residue.Replaced > 0:
		row.Replacement, row.Restoration = doctorReplacementNotReplaced, doctorRestorationRestorable
		row.Reason = doctorReplacementReasonLedgerResidue
	case residueOK:
		row.Replacement, row.Restoration = doctorReplacementReplaced, doctorRestorationRestorable
		row.Reason = doctorReplacementReasonLedgerClean
	default:
		// No live child of this executable and no ledger to read. This is the
		// one L2 combination that collapses to `unknown` on a supported
		// platform, and it does so because both readings are silent rather
		// than because either says the fleet is clean.
		row.Replacement, row.Restoration = doctorReplacementUnknown, doctorRestorationUnknown
		row.Reason = doctorReplacementReasonNoObservedProcess
	}
	return row
}

// projmuxProcessRolesOldestResidualAge is the age of the longest-running
// residual process across every role, and false when no role carried a sample.
func projmuxProcessRolesOldestResidualAge(roles []projmuxProcessRoleVintage) (int, bool) {
	oldest, found := 0, false
	for _, role := range roles {
		if len(role.ReplacedAgeSeconds) == 0 {
			continue
		}
		// The distribution is stored ascending, so the last entry is the age
		// a bounded drain would have to outlast.
		if age := role.ReplacedAgeSeconds[len(role.ReplacedAgeSeconds)-1]; !found || age > oldest {
			oldest, found = age, true
		}
	}
	return oldest, found
}

// projectDoctorReplacementProviderRow reads L3: provider sessions and the
// managed generation pool.
//
// The replacement axis follows the pool, because a generation that has entered
// `draining` takes no new admission -- which is exactly the sentence the
// contract's Assumption makes about a replaced image. The restoration axis is
// where this layer differs from the other two: a draining generation's live
// obligations move only through a qualified handover, and without a
// qualification result there is no route back at all.
//
// A Running Agent whose provider session is contradicted by provider evidence
// outranks every pool token. The pool's own state is a property of a managed
// upgrade an operator may not have started; a Running Agent with a dead session
// is a live inconsistency, and it must not be masked by a token about a pool
// that is merely absent.
func projectDoctorReplacementProviderRow(pool *doctorCodexGenerationPool, sessions doctorProviderSessionCensus) doctorReplacementRow {
	row := doctorReplacementRow{Layer: doctorReplacementLayerProvider, Subject: doctorReplacementSubjectProvider}
	// The two coverage facts are unconditional. A row whose token is
	// `generation-pool-unobserved` would otherwise carry nothing at all, and a
	// token with no discriminant is the one shape C-3 forbids: it cannot be
	// told apart from a pool that was read and found empty.
	signals := []string{
		doctorReplacementSignalRegistryObserved, strconv.FormatBool(sessions.Observed > 0),
	}
	if pool == nil {
		signals = append(signals, doctorReplacementSignalPoolStatus, "unobserved")
	}
	if pool != nil {
		live, draining := 0, 0
		for _, generation := range pool.Generations {
			if generation.State == codexgeneration.StateRetired {
				continue
			}
			live++
			if generation.State == codexgeneration.StateDraining || generation.State == codexgeneration.StateHandoverPending {
				draining++
			}
		}
		signals = append(signals,
			doctorReplacementSignalPoolStatus, pool.Status,
			doctorReplacementSignalPoolReason, pool.Reason,
			doctorReplacementSignalPoolLive, strconv.Itoa(live),
			doctorReplacementSignalPoolDraining, strconv.Itoa(draining),
		)
		if strings.TrimSpace(pool.Action) != "" {
			signals = append(signals, doctorReplacementSignalPoolAction, pool.Action)
		}
		if pool.Qualification != nil {
			signals = append(signals,
				doctorReplacementSignalQualVerdict, string(pool.Qualification.Verdict),
				doctorReplacementSignalQualReason, string(pool.Qualification.Reason),
			)
		}
		row.Replacement = doctorReplacementNotReplaced
		if draining > 0 || len(pool.Generations) > live {
			row.Replacement = doctorReplacementReplaced
		}
		if pool.Status == "absent" {
			row.Replacement = doctorReplacementNotReplaced
		}
	} else {
		row.Replacement = doctorReplacementUnknown
	}
	if sessions.Observed > 0 {
		signals = append(signals,
			doctorReplacementSignalSessionsRunning, strconv.Itoa(sessions.Running),
			doctorReplacementSignalSessionsLive, strconv.Itoa(sessions.Live),
			doctorReplacementSignalSessionsDead, strconv.Itoa(sessions.Dead),
			doctorReplacementSignalSessionsUnobs, strconv.Itoa(sessions.Unobservable),
		)
	}
	row.Signals = doctorReplacementSignals(signals...)

	switch {
	case pool == nil:
		row.Restoration = doctorRestorationUnknown
		row.Reason = doctorReplacementReasonPoolUnobserved
	case sessions.Dead > 0:
		row.Restoration = doctorRestorationNotRestorable
		row.Reason = doctorReplacementReasonSessionDead
	case pool.Status != "absent" && pool.Qualification == nil:
		// The whole of C-1's L3 Guarantee: a pool can enter draining without a
		// qualified version pair, and once there the handover that would move
		// its obligations back has no verdict to run under.
		row.Restoration = doctorRestorationNotRestorable
		row.Reason = doctorReplacementReasonQualification
	case pool.Status == "blocked":
		row.Restoration = doctorRestorationNotRestorable
		row.Reason = doctorReplacementReasonPoolBlocked
	case pool.Status == "action-required":
		row.Restoration = doctorRestorationRestorable
		row.Reason = doctorReplacementReasonHandoverRequired
	case sessions.Observed > 0 && sessions.Unobservable > 0:
		// Running Agents resting on the Registry alone. Nothing contradicts
		// them, and nothing confirms them either, so the restore route cannot
		// be established for those obligations.
		row.Restoration = doctorRestorationUnknown
		row.Reason = doctorReplacementReasonSessionUnobserved
	case pool.Status == "absent":
		// No journal, so no evidence about a restore route -- which is not the
		// same as evidence that one exists.
		row.Restoration = doctorRestorationUnknown
		row.Reason = doctorReplacementReasonPoolNotInstalled
	default:
		row.Restoration = doctorRestorationRestorable
		row.Reason = doctorReplacementReasonPoolReady
	}
	return row
}

// doctorReplacementSignals builds the signal list from alternating key/value
// arguments, dropping an empty value and sanitizing anything that is not a
// closed token or a counter.
func doctorReplacementSignals(pairs ...string) []doctorReplacementSignal {
	out := make([]doctorReplacementSignal, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		key, value := pairs[i], strings.TrimSpace(pairs[i+1])
		if value == "" {
			continue
		}
		if !doctorReplacementSafeValue.MatchString(value) {
			value = doctorReplacementUnclassified
		}
		out = append(out, doctorReplacementSignal{Key: key, Value: value})
	}
	return out
}

// writeDoctorReplacementText renders the three-layer table.
//
// Both axes and the reason token sit on one line so the three layers can be
// compared down a column, and the underlying goes on its own indented line
// because it is what an operator carries into the next command. Nothing here is
// hidden behind --verbose: a layer whose verdict is not printed is a layer an
// operator has no reason to look at, which is the silence this section exists
// to end.
func writeDoctorReplacementText(buf *bytes.Buffer, report *doctorReplacementReport) {
	if report == nil {
		return
	}
	buf.WriteString("\nReplacement and restoration\n")
	for _, row := range report.Rows {
		fmt.Fprintf(buf, "  [%s] %-34s replacement=%-12s restoration=%-14s reason=%s\n",
			row.Layer, row.Subject, row.Replacement, row.Restoration, row.Reason)
		if len(row.Signals) == 0 {
			continue
		}
		parts := make([]string, 0, len(row.Signals))
		for _, signal := range row.Signals {
			parts = append(parts, signal.Key+"="+signal.Value)
		}
		fmt.Fprintf(buf, "       underlying: %s\n", strings.Join(parts, "; "))
	}
}
