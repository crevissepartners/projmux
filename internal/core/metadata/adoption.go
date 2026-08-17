package metadata

import "strings"

// Adoption is how a live tmux window or pane that carries no `@projmux_*_uid`
// gets reattached to the registry object it already belongs to.
//
// The `@projmux_window_uid` / `@projmux_pane_uid` options are the binding
// store, and they used to be written exactly once, at legacy-session import
// time. A tmux server restart, an option reset, or a registry written by a
// build that predates the mirror leaves the machine with live windows and panes
// that carry no uid at all -- and every route that resolves "the active target"
// through the mirror then fails with "carries no @projmux_pane_uid". The old
// import guard made that permanent: it skipped a Project that already owned
// Windows, so a drifted registry never repaired itself, it only avoided
// duplicating the drift.
//
// Adoption replaces that avoidance. It never re-identifies a registry object:
// no uid is ever changed, merged, or reassigned. It only decides which registry
// object a live tmux object is the runtime of, and the caller then writes that
// object's existing uid back through the one mirror write path.
//
// The matching key is structural, in two layers, and deliberately carries no
// content heuristic:
//
//  1. Project scope. A live tmux session resolves to at most one Project --
//     through `@projmux_project_path` on the import path, or through the
//     Project<->session name edge the reconciler itself maintains. Registry
//     objects outside that Project never enter the candidate set.
//  2. Ordinal alignment inside that Project. The session's tmux windows, in
//     window_index ascending order, pair with that Project's registry Windows
//     in creation order; panes pair the same way inside an adopted Window.
//     This is not an invented alignment -- it is the one the import path
//     already created (`legacy.Windows[wi]`) and the one `mirrorImported`
//     already maps back through (`ImportedWindow.SourceIndex`). Adoption
//     restores it.
//
// window_name, `@projmux_window_name`, `@projmux_pane_label`, pane cwd,
// basename, git origin, and inode are explicitly *not* matching keys. Names in
// particular are worthless here: the real registry carries the Window name
// `zsh` across nine different Projects.
//
// Everything ambiguous is refused rather than guessed. See BindingMatcher.

// AdoptionKind is the outcome of matching one live tmux object against the
// registry.
type AdoptionKind string

const (
	// AdoptionRebind means the live object already carries a uid the registry
	// knows and the scope owns. Nothing is adopted and nothing is
	// re-identified; the caller reapplies the binding it already had.
	AdoptionRebind AdoptionKind = "rebind"
	// AdoptionAdopt means the live object carries no uid and the next eligible
	// unbound registry object of the scope, in creation order, takes it.
	AdoptionAdopt AdoptionKind = "adopt"
	// AdoptionUnmatched means the live object carries no uid and the scope has
	// no eligible registry object left. Both write paths create here, but not
	// for the same kinds. The import path mints whichever object is missing,
	// Window or Pane, because its `@projmux_project_path` anchor is what makes
	// minting a whole topology safe. The binding-repair path has no anchor, so
	// it mints a Pane only, and only inside a Window it has already paired --
	// a live window it could not match is still left exactly as it was found.
	AdoptionUnmatched AdoptionKind = "unmatched"
	// AdoptionForeign means the live object carries a uid this registry has
	// never heard of. It is never adopted -- pointing an existing registry
	// object at it would be re-identification on the strength of a blank
	// lookup, which is precisely the heuristic uid merge the contract forbids.
	//
	// It is treated as unmatched by whichever path is allowed to create there,
	// which mints a fresh object for it rather than reusing one. That is not a
	// re-identification: no
	// registry uid changes, a new one is allocated. And it is the only reading
	// that does not strand the machine, because projmux itself produces
	// unknown uids -- a reconcile whose transaction is later rolled back by a
	// pre-create hook refusal has already written its allocated uids onto tmux,
	// and tmux options are not transactional. Refusing those outright would
	// leave the operator's windows permanently unmanageable.
	AdoptionForeign AdoptionKind = "foreign"
	// AdoptionRefused means the pairing is ambiguous in a way that has a real
	// registry object on the other side of it. The caller must neither adopt
	// nor create nor write: a refusal is a decision to leave a live tmux object
	// exactly as it was found, because claiming it would take a binding that
	// belongs to something else.
	AdoptionRefused AdoptionKind = "refused"
)

// AdoptionMatch is one matching decision. UID is set only for AdoptionRebind
// and AdoptionAdopt.
type AdoptionMatch struct {
	Kind AdoptionKind
	UID  string
}

// Matched reports whether the decision names a registry object the caller
// should write a binding for.
func (m AdoptionMatch) Matched() bool {
	return m.Kind == AdoptionRebind || m.Kind == AdoptionAdopt
}

// BindingMatcher pairs the live tmux objects seen during one reconciliation
// pass with registry objects.
//
// It is pass-scoped, not session-scoped, and that is load bearing: a registry
// Window may be the runtime of exactly one live tmux window, so once a walk has
// claimed it -- by rebinding it or by adopting it -- no later walk in the same
// pass may claim it again, even from a different session that resolves to the
// same Project.
//
// The zero value is not usable; build one with NewBindingMatcher.
type BindingMatcher struct {
	// runtime is the live uid inventory read once, before this pass wrote
	// anything. A registry uid present here is already the binding of some live
	// tmux object, so adoption must not steal it.
	runtime RuntimeObservation
	// claimed is every registry uid this pass has already paired.
	claimed map[string]bool
	// refuseForeign keeps public repair from replacing a live uid that the
	// authoritative Registry does not know. Lifecycle convergence preserves its
	// historical mint-and-rebind behavior; explicit reconciliation reports the
	// drift and leaves the object untouched instead.
	refuseForeign bool
}

// DeletedPaneMirrorPrefix marks a live Pane whose Registry resource was
// durably deleted before its self-target kill could be queued. It is transport
// state, not a resource uid: every binding/import path must refuse it so the
// deleted Pane cannot be minted back under a new identity.
const DeletedPaneMirrorPrefix = "deleted:"

// NewBindingMatcher builds a matcher over one pre-pass live-tmux observation.
//
// An empty observation is the fail-closed reading: nothing is known to be bound
// elsewhere, so adoption is decided purely by scope, ordinal, and what this
// pass has already claimed. That is the same tolerance the rest of reconcile
// extends to a tmux server that is down -- it can never invent a binding, only
// decline to protect one that no longer exists.
func NewBindingMatcher(runtime RuntimeObservation) *BindingMatcher {
	return &BindingMatcher{runtime: runtime, claimed: map[string]bool{}}
}

// NewRepairBindingMatcher builds the fail-closed matcher used by explicit
// resource reconciliation. Unknown live uids are diagnostic evidence, not an
// invitation to mint a replacement identity.
func NewRepairBindingMatcher(runtime RuntimeObservation) *BindingMatcher {
	return &BindingMatcher{runtime: runtime, claimed: map[string]bool{}, refuseForeign: true}
}

// Claim marks a registry uid as paired for the rest of the pass.
//
// The import path calls it for every object it mints. Without that, a Window
// created for the third live tmux window of a session would still be an
// unclaimed candidate when the fourth is matched, and the fourth would adopt
// the Window that was just created for the third.
func (b *BindingMatcher) Claim(uid string) {
	if b == nil {
		return
	}
	if uid = strings.TrimSpace(uid); uid != "" {
		b.claimed[uid] = true
	}
}

// Claimed reports whether this pass already paired a registry uid.
//
// Agent runtime linkage reads it for the same reason the pane and window walks
// keep the set at all: one registry object is the runtime of at most one live
// tmux object, so an Agent a previous pane of this pass already attached to is
// no longer a candidate for the next one. Agent uids share this set with Window
// and Pane uids because uids are globally unique across kinds, which the
// registry's own Validate enforces.
func (b *BindingMatcher) Claimed(uid string) bool {
	if b == nil {
		return false
	}
	return b.claimed[strings.TrimSpace(uid)]
}

// MatchWindow decides which registry Window of projectUID a live tmux window is
// the runtime of. observedUID is the `@projmux_window_uid` the live window
// already carries, empty when it carries none.
func (b *BindingMatcher) MatchWindow(reg *Registry, projectUID, observedUID string) AdoptionMatch {
	if b == nil || reg == nil || strings.TrimSpace(projectUID) == "" {
		// No resolved Project scope. Never push a live window into some other
		// Project: that is the one mistake adoption can make that no later pass
		// can undo.
		return AdoptionMatch{Kind: AdoptionRefused}
	}
	return b.match(
		observedUID,
		func(uid string) bool {
			_, ok := reg.Window(uid)
			return ok
		},
		func(uid string) bool {
			window, ok := reg.Window(uid)
			return ok && window.Metadata.OwnerUID() == projectUID
		},
		func() []string {
			windows := reg.WindowsOf(projectUID)
			uids := make([]string, 0, len(windows))
			for _, window := range windows {
				uids = append(uids, window.Metadata.UID)
			}
			return uids
		},
		b.runtime.Windows,
	)
}

// MatchPane decides which registry Pane of windowUID a live tmux pane is the
// runtime of. It is only ever called for a Window that was itself matched: a
// live tmux window nobody adopted contributes none of its panes.
func (b *BindingMatcher) MatchPane(reg *Registry, windowUID, observedUID string) AdoptionMatch {
	if b == nil || reg == nil || strings.TrimSpace(windowUID) == "" {
		return AdoptionMatch{Kind: AdoptionRefused}
	}
	if strings.HasPrefix(strings.TrimSpace(observedUID), DeletedPaneMirrorPrefix) {
		return AdoptionMatch{Kind: AdoptionRefused}
	}
	owned := reg.paneUIDsInWindowOrder(windowUID)
	ownedSet := make(map[string]bool, len(owned))
	for _, uid := range owned {
		ownedSet[uid] = true
	}
	return b.match(
		observedUID,
		func(uid string) bool {
			_, ok := reg.Pane(uid)
			return ok
		},
		func(uid string) bool { return ownedSet[uid] },
		func() []string { return owned },
		b.runtime.Panes,
	)
}

// match is the single matching rule both kinds share.
//
// Every refusal is here, and each one refuses rather than guesses:
//
//   - No resolved scope (handled by the callers above).
//   - The live object carries a uid the registry has never heard of. Never
//     adopted; reported as AdoptionForeign so the import path mints instead of
//     reusing.
//   - The live object carries a uid that exists but belongs to another scope --
//     another Project, or a sibling Window. Refused outright: claiming it would
//     take a binding that is genuinely somebody else's.
//   - The live object carries a uid this pass already paired with another live
//     object. Two live objects claiming one registry object is ambiguous;
//     neither wins twice.
//   - Every candidate is already bound to a different live tmux object -- it is
//     in the pre-pass inventory -- or already claimed by this pass. A binding is
//     never stolen; the walk moves on to the next candidate and, failing that,
//     reports Unmatched.
func (b *BindingMatcher) match(
	observedUID string,
	known func(string) bool,
	ownedByScope func(string) bool,
	candidates func() []string,
	boundElsewhere map[string]bool,
) AdoptionMatch {
	observedUID = strings.TrimSpace(observedUID)
	if observedUID != "" {
		switch {
		case !known(observedUID):
			if b.refuseForeign {
				return AdoptionMatch{Kind: AdoptionRefused}
			}
			return AdoptionMatch{Kind: AdoptionForeign}
		case !ownedByScope(observedUID) || b.claimed[observedUID]:
			return AdoptionMatch{Kind: AdoptionRefused}
		}
		b.claimed[observedUID] = true
		return AdoptionMatch{Kind: AdoptionRebind, UID: observedUID}
	}
	for _, uid := range candidates() {
		if b.claimed[uid] || boundElsewhere[uid] {
			continue
		}
		b.claimed[uid] = true
		return AdoptionMatch{Kind: AdoptionAdopt, UID: uid}
	}
	return AdoptionMatch{Kind: AdoptionUnmatched}
}

// paneUIDsInWindowOrder returns every Pane uid a Window transitively owns --
// its own shell Panes and the managed Panes of its Agents -- in registry
// insertion order.
//
// Insertion order is the ordinal adoption aligns against, and it is exactly the
// order the legacy import created them in: one Pane per observed tmux pane, in
// pane order, whether that pane became a shell Pane owned by the Window or a
// managed Pane owned by a freshly minted Agent. Registry.snapshotPanesOf groups
// shell Panes ahead of managed ones for the snapshot projection's own reasons;
// borrowing that grouping here would silently shear the alignment for every
// Window that mixes the two.
func (r *Registry) paneUIDsInWindowOrder(windowUID string) []string {
	owners := map[string]bool{windowUID: true}
	for _, agent := range r.Agents {
		if agent.Metadata.OwnerUID() == windowUID {
			owners[agent.Metadata.UID] = true
		}
	}
	var out []string
	for _, pane := range r.Panes {
		if owners[pane.Metadata.OwnerUID()] {
			out = append(out, pane.Metadata.UID)
		}
	}
	return out
}
