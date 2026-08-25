package selector

import (
	"fmt"
	"maps"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Cardinality is how many resolved targets a <verb, kind> pair accepts.
type Cardinality string

// The closed cardinality set from the CLI information architecture v2
// selector contract.
const (
	// CardinalityAny is a list read: zero matches is a successful empty result.
	CardinalityAny Cardinality = "0..N"
	// CardinalityExactOne requires exactly one resolved target.
	CardinalityExactOne Cardinality = "exact-one"
	// CardinalityAtLeastOne requires at least one resolved target; the route
	// fans out over every match.
	CardinalityAtLeastOne Cardinality = "1..N"
)

// Verb is a shared CLI verb that resolves an existing target set.
type Verb string

// The verbs whose target resolution this matrix pins.
const (
	VerbGet      Verb = "get"
	VerbDescribe Verb = "describe"
	VerbCreate   Verb = "create"
	VerbAttach   Verb = "attach"
	VerbFocus    Verb = "focus"
	VerbRename   Verb = "rename"
	VerbRebind   Verb = "rebind"
	VerbDelete   Verb = "delete"
	// VerbResume is the Agent-domain resume workflow. It is spelled
	// `agent resume` rather than `resume agent`, but it resolves an existing
	// target set exactly like the shared verbs, so it belongs in this matrix
	// instead of re-deciding its own cardinality inside the handler.
	VerbResume Verb = "resume"
	VerbReview Verb = "review"
	VerbStatus Verb = "status"
	VerbTopic  Verb = "topic"
)

// Target is one cell of the <verb, kind> cardinality matrix.
//
// Kind is normally the kind the route *selects*, which is not always the kind
// it produces: `create pane --window a --window b` selects Windows and produces
// Panes, so its target Kind is Window.
//
// The create verb makes one documented exception, for a route whose produced
// kind is never a kind it selects. `create agent` is the only such route: it
// resolves no existing Agent anywhere -- rebinding an existing conversation is
// `agent resume`, which owns its own exact-one cell -- so there is no Agent set
// for a Window-shaped cell to describe. Its cell is therefore over the Agents
// the invocation produces, one per resolved target Window, and it is the cell
// the route enforces when its selector resolves nothing.
//
// List distinguishes the plural list spelling (`get windows`) from the singular
// single-resource spelling (`get window`).
type Target struct {
	Verb Verb
	Kind metadata.Kind
	List bool
}

// String renders the matrix key as "<verb> <kind>".
func (t Target) String() string {
	kind := strings.ToLower(string(t.Kind))
	if t.List {
		kind += "s"
	}
	return string(t.Verb) + " " + kind
}

// matrix is the declared <verb, kind> cardinality table.
//
// It is the single cardinality authority for every route that resolves an
// existing target set: the public verb-to-kind routes call Enforce against
// their own cell rather than re-deciding "exactly one" or "at least one" per
// handler. The create rows stay declaration-only until the materialization
// Phases move those routes.
var matrix = map[Target]Cardinality{
	// Reads. A plural list read succeeds on an empty result; a singular read
	// addresses exactly one resource.
	{Verb: VerbGet, Kind: metadata.KindProject, List: true}: CardinalityAny,
	{Verb: VerbGet, Kind: metadata.KindWindow, List: true}:  CardinalityAny,
	{Verb: VerbGet, Kind: metadata.KindPane, List: true}:    CardinalityAny,
	{Verb: VerbGet, Kind: metadata.KindAgent, List: true}:   CardinalityAny,
	{Verb: VerbGet, Kind: metadata.KindProject}:             CardinalityExactOne,
	{Verb: VerbGet, Kind: metadata.KindWindow}:              CardinalityExactOne,
	{Verb: VerbGet, Kind: metadata.KindPane}:                CardinalityExactOne,
	{Verb: VerbGet, Kind: metadata.KindAgent}:               CardinalityExactOne,
	{Verb: VerbDescribe, Kind: metadata.KindProject}:        CardinalityExactOne,
	{Verb: VerbDescribe, Kind: metadata.KindWindow}:         CardinalityExactOne,
	{Verb: VerbDescribe, Kind: metadata.KindPane}:           CardinalityExactOne,
	{Verb: VerbDescribe, Kind: metadata.KindAgent}:          CardinalityExactOne,
	{Verb: VerbStatus, Kind: metadata.KindAgent}:            CardinalityExactOne,
	{Verb: VerbTopic, Kind: metadata.KindAgent}:             CardinalityExactOne,

	// Navigation and rebinding address one resource.
	{Verb: VerbAttach, Kind: metadata.KindProject}: CardinalityExactOne,
	{Verb: VerbFocus, Kind: metadata.KindProject}:  CardinalityExactOne,
	{Verb: VerbFocus, Kind: metadata.KindWindow}:   CardinalityExactOne,
	{Verb: VerbFocus, Kind: metadata.KindPane}:     CardinalityExactOne,
	{Verb: VerbRename, Kind: metadata.KindProject}: CardinalityExactOne,
	{Verb: VerbRename, Kind: metadata.KindWindow}:  CardinalityExactOne,
	{Verb: VerbRename, Kind: metadata.KindPane}:    CardinalityExactOne,
	{Verb: VerbRename, Kind: metadata.KindAgent}:   CardinalityExactOne,
	{Verb: VerbRebind, Kind: metadata.KindProject}: CardinalityExactOne,

	// create resolves an exact-one Project scope, fans out over its resolved
	// parent Windows, and anchors on exactly one Pane inside each of them.
	//
	// The Project cell is what `create window` selects: a Window is created
	// below exactly one Project, so an ambiguous or absent --project is a usage
	// error rather than a fan-out.
	//
	// The Window cell is `create pane`'s fan-out; the Agent cell is
	// `create agent`'s. They are separate rows because the two routes fan out
	// over the same selector but produce different kinds, and the Agent route
	// resolves no existing Agent at all. Either way a selector that parses but
	// matches nothing is a usage error rather than a successful invocation that
	// created nothing.
	{Verb: VerbCreate, Kind: metadata.KindProject}: CardinalityExactOne,
	{Verb: VerbCreate, Kind: metadata.KindWindow}:  CardinalityAtLeastOne,
	{Verb: VerbCreate, Kind: metadata.KindPane}:    CardinalityExactOne,
	{Verb: VerbCreate, Kind: metadata.KindAgent}:   CardinalityAtLeastOne,

	// resume addresses exactly one existing Agent. It never fans out and never
	// falls back to a focus target: rebinding the wrong conversation is worse
	// than refusing an ambiguous reference.
	{Verb: VerbResume, Kind: metadata.KindAgent}: CardinalityExactOne,
	{Verb: VerbReview, Kind: metadata.KindAgent}: CardinalityExactOne,

	// delete fans out over every resolved target.
	//
	// The cells stay 1..N under the active-target containment of the empty
	// selector, and the containment is deliberately not expressed here.
	// Cardinality answers "how many targets may a resolved selector address",
	// and the answer for delete is still "as many as it resolved":
	// `delete pane zsh log` and `delete pane --project alpha` fan out exactly as
	// before. What changed is which resources an *empty* selector resolves in
	// the first place, which is a question about the query, not about the cell.
	// Tightening these to exact-one would have broken every explicit fan-out to
	// fix an argv shape that does not reach this table -- the route refuses the
	// unbounded empty selector before Enforce is ever called.
	{Verb: VerbDelete, Kind: metadata.KindProject}: CardinalityAtLeastOne,
	{Verb: VerbDelete, Kind: metadata.KindWindow}:  CardinalityAtLeastOne,
	{Verb: VerbDelete, Kind: metadata.KindPane}:    CardinalityAtLeastOne,
	{Verb: VerbDelete, Kind: metadata.KindAgent}:   CardinalityAtLeastOne,
}

// CardinalityFor returns the declared cardinality for one matrix cell.
func CardinalityFor(target Target) (Cardinality, bool) {
	cardinality, ok := matrix[target]
	return cardinality, ok
}

// Matrix returns a copy of the whole declared <verb, kind> cardinality table.
func Matrix() map[Target]Cardinality {
	out := make(map[Target]Cardinality, len(matrix))
	maps.Copy(out, matrix)
	return out
}

// MaxCandidates bounds the ambiguity listing written to stderr. The contract
// fixes it at five so a wide no-match never floods a terminal or a log.
const MaxCandidates = 5

// Candidate is one bounded ambiguity row: name, duplicate-allowed displayName,
// and owner context. It exists to help a human disambiguate, never to be parsed
// back in as a selector.
type Candidate struct {
	Kind        metadata.Kind
	UID         string
	Name        string
	DisplayName string
	Owner       string
}

// String renders one candidate row.
func (c Candidate) String() string {
	row := strings.ToLower(string(c.Kind)) + "/" + c.Name
	if c.DisplayName != "" {
		row += " displayName=" + c.DisplayName
	}
	if c.Owner != "" {
		row += " owner=" + c.Owner
	}
	return row
}

// SelectorError is a selector or cardinality failure caused by operator input.
//
// It implements the metadata usage-error marker, so app.MapMetadataError routes
// it onto the existing UsageError path and the process exits 2 with zero bytes
// on stdout.
type SelectorError struct {
	// Op is the failing operation, for example "resolve pane".
	Op string
	// Detail is the human-readable cause.
	Detail string
	// Kind is the target kind, when the failure has one.
	Kind metadata.Kind
	// Want is the required cardinality, when the failure is a cardinality
	// violation.
	Want Cardinality
	// Got is the number of resolved targets.
	Got int
	// Candidates is the bounded ambiguity listing, at most MaxCandidates rows.
	Candidates []Candidate
	// Omitted counts candidates dropped by the bound.
	Omitted int
}

// Error renders the summary line plus the bounded candidate listing.
func (e *SelectorError) Error() string {
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Detail)
	for _, candidate := range e.Candidates {
		b.WriteString("\n  ")
		b.WriteString(candidate.String())
	}
	if e.Omitted > 0 {
		fmt.Fprintf(&b, "\n  ... %d more omitted", e.Omitted)
	}
	return b.String()
}

// MetadataUsageError marks this as operator input error so the shared metadata
// classifier maps it to CLI exit code 2.
func (e *SelectorError) MetadataUsageError() bool { return true }

// IsNoMatch reports whether this error means the selector resolved nothing.
//
// Only a cardinality failure can be a no-match: Want is set by cardinalityErr
// and left empty by inputErr, so a malformed selector value is never reported
// as a missing resource even though it also resolved zero targets.
func (e *SelectorError) IsNoMatch() bool { return e.Want != "" && e.Got == 0 }

// Unwrap exposes metadata.ErrNotFound for a no-match failure only.
//
// metadata.ErrNotFound marks an unresolvable resource, so wrapping it
// unconditionally would report "not found" for the two failures that are not:
// a cardinality violation where too many targets matched, and a malformed
// selector value. Callers classifying with errors.Is need those to stay
// distinguishable. Exit code 2 does not depend on this: MetadataUsageError is
// the marker metadata.IsUsageError matches with errors.As.
func (e *SelectorError) Unwrap() error {
	if e.IsNoMatch() {
		return metadata.ErrNotFound
	}
	return nil
}

func inputErr(op, format string, args ...any) error {
	return &SelectorError{Op: op, Detail: fmt.Sprintf(format, args...)}
}

// cardinalityErr builds the bounded ambiguity error for one failed cell.
func cardinalityErr(op string, kind metadata.Kind, want Cardinality, selector string, matches []Match) error {
	candidates, omitted := boundCandidates(matches)
	return &SelectorError{
		Op:         op,
		Detail:     cardinalityDetail(kind, want, selector, len(matches)),
		Kind:       kind,
		Want:       want,
		Got:        len(matches),
		Candidates: candidates,
		Omitted:    omitted,
	}
}

func cardinalityDetail(kind metadata.Kind, want Cardinality, selector string, got int) string {
	subject := strings.ToLower(string(kind))
	if got != 1 {
		subject += "s"
	}
	matched := fmt.Sprintf("%d %s", got, subject)
	if got == 0 {
		matched = "no " + strings.ToLower(string(kind)) + "s"
	}
	if selector == "" {
		selector = "the current selector"
	}
	return fmt.Sprintf("%s matched %s, want %s", selector, matched, wantPhrase(want))
}

func wantPhrase(want Cardinality) string {
	switch want {
	case CardinalityExactOne:
		return "exactly one"
	case CardinalityAtLeastOne:
		return "at least one"
	default:
		return string(want)
	}
}

func boundCandidates(matches []Match) ([]Candidate, int) {
	shown := matches
	omitted := 0
	if len(shown) > MaxCandidates {
		omitted = len(shown) - MaxCandidates
		shown = shown[:MaxCandidates]
	}
	out := make([]Candidate, 0, len(shown))
	for _, match := range shown {
		out = append(out, Candidate{
			Kind:        match.Kind,
			UID:         match.UID,
			Name:        match.Name,
			DisplayName: match.DisplayName,
			Owner:       match.Owner.String(),
		})
	}
	return out, omitted
}

// Enforce checks a resolution against the declared cardinality for target.
//
// A violation returns a *SelectorError carrying the bounded candidate listing.
// Enforce never writes anything and never mutates the resolution, so a failing
// route can still guarantee zero bytes on stdout and zero mutations.
func Enforce(target Target, selector string, resolution Resolution) error {
	want, ok := CardinalityFor(target)
	if !ok {
		return fmt.Errorf("selector: no cardinality is declared for %q", target)
	}
	got := len(resolution.Matches)
	switch want {
	case CardinalityAny:
		return nil
	case CardinalityExactOne:
		if got == 1 {
			return nil
		}
	case CardinalityAtLeastOne:
		if got >= 1 {
			return nil
		}
	}
	return cardinalityErr("resolve "+strings.ToLower(string(target.Kind)), target.Kind, want, selector, resolution.Matches)
}

// DescribeSelector renders the selector occurrences of q for error text.
func DescribeSelector(q Query) string {
	var parts []string
	if q.Project != nil {
		parts = append(parts, "--project "+q.Project.Raw)
	}
	for _, ref := range q.Windows {
		parts = append(parts, "--window "+ref.Raw)
	}
	for _, ref := range q.Panes {
		parts = append(parts, "--pane "+ref.Raw)
	}
	// Agent refs arrive positionally, not through a scope flag, so they render
	// as the bare resource reference the operator actually typed.
	for _, ref := range q.Agents {
		parts = append(parts, "agent "+ref.Raw)
	}
	for _, label := range q.Labels {
		parts = append(parts, "--selector "+label.String())
	}
	return strings.Join(parts, " ")
}
