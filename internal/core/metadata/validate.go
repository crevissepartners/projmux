package metadata

import (
	"path/filepath"
	"slices"
	"strings"
)

// Validate checks every structural invariant of the registry: envelope
// spelling, uid uniqueness, in-scope name uniqueness, ownerRef integrity,
// anchor/default-shell validity, Agent phase membership, and reservation
// consistency.
func (r Registry) Validate() error {
	const op = "validate registry"

	if r.APIVersion != APIVersion {
		return stateErr(op, ErrInvalidRegistry, "apiVersion %q is not %q", r.APIVersion, APIVersion)
	}
	if r.SchemaVersion != SchemaVersion {
		return stateErr(op, ErrInvalidRegistry, "schemaVersion %d is not %d", r.SchemaVersion, SchemaVersion)
	}

	uids := map[string]Kind{}
	claim := func(kind Kind, uid string) error {
		if uid == "" {
			return stateErr(op, ErrInvalidRegistry, "%s has an empty uid", kind)
		}
		if existing, ok := uids[uid]; ok {
			return stateErr(op, ErrInvalidRegistry, "uid %q is used by both %s and %s", uid, existing, kind)
		}
		uids[uid] = kind
		return nil
	}

	roots := map[string]string{}
	for _, project := range r.Projects {
		if err := claim(KindProject, project.Metadata.UID); err != nil {
			return err
		}
		if project.Kind != KindProject || project.APIVersion != APIVersion {
			return stateErr(op, ErrInvalidRegistry, "project %q has envelope %s/%s", project.Metadata.UID, project.APIVersion, project.Kind)
		}
		if err := ValidateName(project.Metadata.Name); err != nil {
			return err
		}
		if project.Metadata.removedDisplayName.present {
			return stateErr(op, ErrInvalidRegistry, "schemaVersion 4 Project %q contains removed metadata.displayName", project.Metadata.Name)
		}
		if project.Metadata.OwnerRef != nil {
			return stateErr(op, ErrInvalidRegistry, "project %q must not have an ownerRef", project.Metadata.Name)
		}
		if project.Spec.Root == "" || !filepath.IsAbs(project.Spec.Root) {
			return stateErr(op, ErrInvalidRegistry, "project %q root %q must be absolute", project.Metadata.Name, project.Spec.Root)
		}
		if owner, ok := roots[project.Spec.Root]; ok {
			return stateErr(op, ErrInvalidRegistry, "root %q is bound to both %s and %s", project.Spec.Root, owner, project.Metadata.UID)
		}
		roots[project.Spec.Root] = project.Metadata.UID
	}

	sessions := map[string]string{}
	for _, control := range r.ControlSessions {
		if err := claim(KindControlSession, control.Metadata.UID); err != nil {
			return err
		}
		if control.Kind != KindControlSession || control.APIVersion != APIVersion {
			return stateErr(op, ErrInvalidRegistry, "control session %q has envelope %s/%s", control.Metadata.UID, control.APIVersion, control.Kind)
		}
		if err := ValidateName(control.Metadata.Name); err != nil {
			return err
		}
		if control.Metadata.removedDisplayName.present {
			return stateErr(op, ErrInvalidRegistry, "schemaVersion 4 ControlSession %q contains removed metadata.displayName", control.Metadata.Name)
		}
		if control.Metadata.OwnerRef != nil {
			return stateErr(op, ErrInvalidRegistry, "control session %q must not have an ownerRef", control.Metadata.Name)
		}
		if strings.TrimSpace(control.Spec.Session) == "" {
			return stateErr(op, ErrInvalidRegistry, "control session %q names no tmux session", control.Metadata.Name)
		}
		// One tmux session projects onto at most one control session, for the
		// same reason one root binds at most one Project: two roots claiming the
		// same session would make "which resource is this session" unanswerable
		// and let a later pass adopt the same live windows twice.
		if owner, ok := sessions[control.Spec.Session]; ok {
			return stateErr(op, ErrInvalidRegistry, "tmux session %q is bound to both %s and %s", control.Spec.Session, owner, control.Metadata.UID)
		}
		sessions[control.Spec.Session] = control.Metadata.UID
	}

	for _, window := range r.Windows {
		if err := claim(KindWindow, window.Metadata.UID); err != nil {
			return err
		}
		if window.Kind != KindWindow || window.APIVersion != APIVersion {
			return stateErr(op, ErrInvalidRegistry, "window %q has envelope %s/%s", window.Metadata.UID, window.APIVersion, window.Kind)
		}
		if err := ValidateName(window.Metadata.Name); err != nil {
			return err
		}
		if window.Metadata.removedDisplayName.present {
			return stateErr(op, ErrInvalidRegistry, "schemaVersion 4 Window %q contains removed metadata.displayName", window.Metadata.Name)
		}
		if err := r.requireOwner(op, KindWindow, window.Metadata, KindProject, KindControlSession); err != nil {
			return err
		}
		sessionID := strings.TrimSpace(window.Status.RuntimeSessionID)
		windowID := strings.TrimSpace(window.Status.RuntimeID)
		if (sessionID == "") != (windowID == "") {
			return stateErr(op, ErrInvalidRegistry, "window %q has an incomplete runtime owner binding", window.Metadata.Name)
		}
		if sessionID != "" && (!validRuntimeHandle(sessionID, '$') || !validRuntimeHandle(windowID, '@')) {
			return stateErr(op, ErrInvalidRegistry, "window %q has an invalid runtime owner binding", window.Metadata.Name)
		}
	}

	for _, pane := range r.Panes {
		if err := claim(KindPane, pane.Metadata.UID); err != nil {
			return err
		}
		if pane.Kind != KindPane || pane.APIVersion != APIVersion {
			return stateErr(op, ErrInvalidRegistry, "pane %q has envelope %s/%s", pane.Metadata.UID, pane.APIVersion, pane.Kind)
		}
		if err := ValidateName(pane.Metadata.Name); err != nil {
			return err
		}
		if pane.Metadata.removedDisplayName.present || pane.Status.removedDisplayTitle.present {
			return stateErr(op, ErrInvalidRegistry, "schemaVersion 4 Pane %q contains removed presentation fields", pane.Metadata.Name)
		}
		switch pane.Spec.Role {
		case PaneRoleShell:
			if err := r.requireOwner(op, KindPane, pane.Metadata, KindWindow); err != nil {
				return err
			}
		case PaneRoleAgent:
			if err := r.requireOwner(op, KindPane, pane.Metadata, KindAgent); err != nil {
				return err
			}
		default:
			return stateErr(op, ErrInvalidRegistry, "pane %q has unsupported role %q", pane.Metadata.Name, pane.Spec.Role)
		}
		if activation := pane.Status.Activation; !activation.IsZero() && strings.TrimSpace(activation.Generation) == "" {
			return stateErr(op, ErrInvalidRegistry, "pane %q has an activation record without a generation", pane.Metadata.Name)
		}
		if binding := pane.Status.Activation.Codex; binding != nil {
			if pane.Spec.Role != PaneRoleAgent || strings.TrimSpace(pane.Status.Activation.AgentUID) == "" || strings.TrimSpace(binding.ThreadID) == "" {
				return stateErr(op, ErrInvalidRegistry, "pane %q has an invalid native Codex activation binding", pane.Metadata.Name)
			}
			if binding.Authority != nil && !binding.Authority.Valid() {
				return stateErr(op, ErrInvalidRegistry, "pane %q has an incomplete native Codex authority", pane.Metadata.Name)
			}
		}
		if err := validateTermination(op, "pane "+pane.Metadata.Name, pane.Status.LastTermination); err != nil {
			return err
		}
		if err := r.validatePaneTeardown(op, pane); err != nil {
			return err
		}
	}

	for _, agent := range r.Agents {
		if err := claim(KindAgent, agent.Metadata.UID); err != nil {
			return err
		}
		if agent.Kind != KindAgent || agent.APIVersion != APIVersion {
			return stateErr(op, ErrInvalidRegistry, "agent %q has envelope %s/%s", agent.Metadata.UID, agent.APIVersion, agent.Kind)
		}
		if err := ValidateName(agent.Metadata.Name); err != nil {
			return err
		}
		if agent.Metadata.removedDisplayName.present {
			return stateErr(op, ErrInvalidRegistry, "schemaVersion 4 Agent %q contains removed metadata.displayName", agent.Metadata.Name)
		}
		if err := r.requireOwner(op, KindAgent, agent.Metadata, KindWindow); err != nil {
			return err
		}
		if !ValidAgentPhase(agent.Status.Phase) {
			return stateErr(op, ErrInvalidPhase, "agent %q has unsupported phase %q", agent.Metadata.Name, agent.Status.Phase)
		}
		if agent.Status.Interaction.Kind != "" && !ValidAgentInteractionKind(agent.Status.Interaction.Kind) {
			return stateErr(op, ErrInvalidRegistry, "agent %q has unsupported interaction kind %q", agent.Metadata.Name, agent.Status.Interaction.Kind)
		}
		if source := strings.TrimSpace(agent.Status.Interaction.Source); source != "" && !ValidAgentInteractionSource(source) {
			return stateErr(op, ErrInvalidRegistry, "agent %q has unsupported interaction source %q", agent.Metadata.Name, source)
		}
		switch agent.Status.Activation.State {
		case "", ActivationNotRequested, ActivationPending, ActivationAcknowledged, ActivationUnconfirmed:
		default:
			return stateErr(op, ErrInvalidRegistry, "agent %q has unsupported activation state %q", agent.Metadata.Name, agent.Status.Activation.State)
		}
		if source := strings.TrimSpace(agent.Status.Activation.Source); source != "" && source != string(InteractionSourceProviderHook) && source != string(InteractionSourceProviderControl) {
			return stateErr(op, ErrInvalidRegistry, "agent %q has unsupported activation source %q", agent.Metadata.Name, source)
		}
		if !ValidAgentActivationReason(agent.Status.Activation.Reason) {
			return stateErr(op, ErrInvalidRegistry, "agent %q has unsupported activation reason %q", agent.Metadata.Name, agent.Status.Activation.Reason)
		}
		if workspace := agent.Spec.Workspace; !workspace.IsZero() {
			if workspace.CWD == "" || !filepath.IsAbs(workspace.CWD) || filepath.Clean(workspace.CWD) != workspace.CWD {
				return stateErr(op, ErrInvalidRegistry, "agent %q workspace cwd %q must be absolute", agent.Metadata.Name, workspace.CWD)
			}
			seenRoots := map[string]bool{workspace.CWD: true}
			for _, root := range workspace.AdditionalWritableRoots {
				if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
					return stateErr(op, ErrInvalidRegistry, "agent %q additional writable root %q must be absolute", agent.Metadata.Name, root)
				}
				if seenRoots[root] {
					return stateErr(op, ErrInvalidRegistry, "agent %q repeats workspace root %q", agent.Metadata.Name, root)
				}
				seenRoots[root] = true
			}
		}
		if agent.Status.PaneRef != "" {
			pane, ok := r.Pane(agent.Status.PaneRef)
			if !ok {
				return stateErr(op, ErrInvalidRegistry, "agent %q paneRef %q does not exist", agent.Metadata.Name, agent.Status.PaneRef)
			}
			if pane.Metadata.OwnerUID() != agent.Metadata.UID {
				return stateErr(op, ErrInvalidRegistry, "agent %q paneRef %q is owned by %q", agent.Metadata.Name, agent.Status.PaneRef, pane.Metadata.OwnerUID())
			}
		}
		if agent.Status.Phase == PhaseRunning && agent.Status.PaneRef == "" {
			return stateErr(op, ErrInvalidRegistry, "agent %q is Running without a managed pane", agent.Metadata.Name)
		}
		if err := validateAgentProgress(op, agent); err != nil {
			return err
		}
		if !agent.Status.Progress.IsZero() {
			pane, ok := r.Pane(agent.Status.PaneRef)
			if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.TurnID != agent.Status.Progress.TurnRef {
				return stateErr(op, ErrInvalidRegistry, "agent %q progress turn is not its current exact activation", agent.Metadata.Name)
			}
		}
		if err := validateSessionRef(op, agent); err != nil {
			return err
		}
		if err := validateTermination(op, "agent "+agent.Metadata.Name, agent.Status.LastTermination); err != nil {
			return err
		}
	}

	for _, window := range r.Windows {
		if window.Spec.sourceShape == windowSpecSourceLegacy || window.Spec.sourceShape == windowSpecSourceMixed ||
			window.Spec.sourceShape == windowSpecSourceUnknown || strings.TrimSpace(window.Spec.legacyPrimaryPaneRef) != "" {
			return stateErr(op, ErrInvalidRegistry, "window %q is not normalized to final-v2 anchor authority", window.Metadata.Name)
		}
		if strings.TrimSpace(window.Spec.AnchorPaneRef) == "" {
			return stateErr(op, ErrInvalidRegistry, "window %q has no anchorPaneRef", window.Metadata.Name)
		}
		anchor, ok := r.Pane(window.Spec.AnchorPaneRef)
		if !ok {
			return stateErr(op, ErrInvalidRegistry, "window %q anchorPaneRef %q does not exist", window.Metadata.Name, window.Spec.AnchorPaneRef)
		}
		anchorWindowUID, owned := paneWindowOwnerUID(r, *anchor)
		if !owned || anchorWindowUID != window.Metadata.UID ||
			(anchor.Spec.Role != PaneRoleShell && anchor.Spec.Role != PaneRoleAgent) {
			return stateErr(op, ErrInvalidRegistry, "window %q anchorPaneRef %q is not a same-Window shell or Agent Pane", window.Metadata.Name, window.Spec.AnchorPaneRef)
		}
		if anchor.Spec.Role == PaneRoleAgent {
			agent, _ := r.Agent(anchor.Metadata.OwnerUID())
			if agent == nil || agent.Status.PaneRef != anchor.Metadata.UID {
				return stateErr(op, ErrInvalidRegistry, "window %q anchorPaneRef %q is not its Agent owner's managed Pane", window.Metadata.Name, window.Spec.AnchorPaneRef)
			}
		}
		if shellRef := strings.TrimSpace(window.Spec.DefaultShellPaneRef); shellRef != "" {
			shell, ok := r.Pane(shellRef)
			if !ok {
				return stateErr(op, ErrInvalidRegistry, "window %q defaultShellPaneRef %q does not exist", window.Metadata.Name, window.Spec.DefaultShellPaneRef)
			}
			if shell.Metadata.OwnerRef == nil || shell.Metadata.OwnerRef.Kind != KindWindow ||
				shell.Metadata.OwnerRef.UID != window.Metadata.UID || shell.Spec.Role != PaneRoleShell {
				return stateErr(op, ErrInvalidRegistry, "window %q defaultShellPaneRef %q is not a direct Window-owned shell Pane", window.Metadata.Name, window.Spec.DefaultShellPaneRef)
			}
		}
	}

	for _, project := range r.Projects {
		windows := r.WindowsOf(project.Metadata.UID)
		if strings.TrimSpace(project.Spec.PrimaryWindowRef) == "" {
			if len(windows) == 0 {
				continue
			}
			return stateErr(op, ErrInvalidRegistry, "project %q has %d Windows but no primaryWindowRef", project.Metadata.Name, len(windows))
		}
		window, ok := r.Window(project.Spec.PrimaryWindowRef)
		if !ok {
			return stateErr(op, ErrInvalidRegistry, "project %q primaryWindowRef %q does not exist", project.Metadata.Name, project.Spec.PrimaryWindowRef)
		}
		if window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != KindProject || window.Metadata.OwnerRef.UID != project.Metadata.UID {
			return stateErr(op, ErrInvalidRegistry, "project %q primaryWindowRef %q is not owned by the Project", project.Metadata.Name, project.Spec.PrimaryWindowRef)
		}
	}

	return r.validateReservations(op, uids)
}

func (r Registry) validatePaneTeardown(op string, pane Pane) error {
	evidence := pane.Status.Teardown
	if evidence == nil {
		return nil
	}
	if strings.TrimSpace(evidence.SocketIdentity) == "" ||
		strings.TrimSpace(evidence.RuntimeSessionID) == "" ||
		strings.TrimSpace(evidence.RuntimePaneID) == "" ||
		strings.TrimSpace(evidence.RuntimeWindowID) == "" ||
		strings.TrimSpace(evidence.WindowUID) == "" ||
		strings.TrimSpace(evidence.RootUID) == "" ||
		strings.TrimSpace(evidence.Generation) == "" || evidence.ObservedAt.IsZero() {
		return stateErr(op, ErrInvalidRegistry, "pane %q has incomplete teardown evidence", pane.Metadata.Name)
	}
	if evidence.Generation != pane.Status.Activation.Generation {
		return stateErr(op, ErrInvalidRegistry, "pane %q teardown evidence is stale", pane.Metadata.Name)
	}
	if evidence.Classification != TerminationNormal && evidence.Classification != TerminationIntentional {
		return stateErr(op, ErrInvalidRegistry, "pane %q teardown evidence has non-causal classification %q", pane.Metadata.Name, evidence.Classification)
	}
	windowUID, ok := paneWindowOwnerUID(r, pane)
	if !ok || windowUID != evidence.WindowUID {
		return stateErr(op, ErrInvalidRegistry, "pane %q teardown evidence names a foreign Window", pane.Metadata.Name)
	}
	window, ok := r.Window(windowUID)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != evidence.RootKind ||
		window.Metadata.OwnerRef.UID != evidence.RootUID {
		return stateErr(op, ErrInvalidRegistry, "pane %q teardown evidence names a stale root", pane.Metadata.Name)
	}
	return nil
}

func validRuntimeHandle(value string, prefix byte) bool {
	if len(value) < 2 || value[0] != prefix {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func paneWindowOwnerUID(r Registry, pane Pane) (string, bool) {
	owner := pane.Metadata.OwnerRef
	if owner == nil {
		return "", false
	}
	if owner.Kind == KindWindow {
		_, ok := r.Window(owner.UID)
		return owner.UID, ok
	}
	if owner.Kind != KindAgent {
		return "", false
	}
	agent, ok := r.Agent(owner.UID)
	if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != KindWindow {
		return "", false
	}
	_, ok = r.Window(agent.Metadata.OwnerRef.UID)
	return agent.Metadata.OwnerRef.UID, ok
}

// validateTermination enforces the closed receipt vocabularies. A nil receipt
// is the normal state and is always valid: absence of evidence is a legal
// document, and the whole point of this transport is that it is never confused
// with evidence of normality.
func validateTermination(op, subject string, receipt *TerminationEvidence) error {
	if receipt == nil {
		return nil
	}
	if !ValidTerminationSource(receipt.Source) {
		return stateErr(op, ErrInvalidRegistry, "%s has unsupported termination source %q", subject, receipt.Source)
	}
	if !ValidTerminationClassification(receipt.Classification) {
		return stateErr(op, ErrInvalidRegistry, "%s has unsupported termination classification %q", subject, receipt.Classification)
	}
	if !validTerminationPairing(receipt.Source, receipt.Classification) {
		return stateErr(op, ErrInvalidRegistry, "%s records %q termination from source %q",
			subject, receipt.Classification, receipt.Source)
	}
	if !validTerminationEvidenceShape(*receipt) {
		return stateErr(op, ErrInvalidRegistry, "%s records killed termination without a supervisor SIGHUP wait status", subject)
	}
	if strings.TrimSpace(receipt.PaneUID) == "" {
		return stateErr(op, ErrInvalidRegistry, "%s has a termination receipt naming no pane", subject)
	}
	return nil
}

// requireOwner enforces that meta names an existing owner of one of wantKinds.
//
// The allowed *set* replaced a single wantKind when a Window became ownable by
// either a Project or a ControlSession. It is a set rather than a second
// call-site branch on purpose: the caller states which owner kinds are legal for
// its kind in one place, so there is exactly one refusal to keep in step, and a
// kind that is not in the set is refused rather than falling through to a
// lookup that would answer false for a reason the message cannot name.
//
// The refusal wording is preserved byte-for-byte for the single-kind case, which
// is every caller except Window. A Window's message lists its allowed kinds in
// declaration order; there is no other spelling that can name two legal owners
// without lying about one of them.
func (r Registry) requireOwner(op string, kind Kind, meta ObjectMeta, wantKinds ...Kind) error {
	if len(wantKinds) == 0 {
		return stateErr(op, ErrInvalidRegistry, "%s %q has no allowed owner kind", kind, meta.Name)
	}
	if meta.OwnerRef == nil {
		return stateErr(op, ErrInvalidRegistry, "%s %q has no ownerRef", kind, meta.Name)
	}
	if !slices.Contains(wantKinds, meta.OwnerRef.Kind) {
		return stateErr(op, ErrInvalidRegistry, "%s %q ownerRef kind %q is not %s", kind, meta.Name, meta.OwnerRef.Kind, quotedKinds(wantKinds))
	}
	var exists bool
	switch meta.OwnerRef.Kind {
	case KindProject:
		_, exists = r.Project(meta.OwnerRef.UID)
	case KindControlSession:
		_, exists = r.ControlSession(meta.OwnerRef.UID)
	case KindWindow:
		_, exists = r.Window(meta.OwnerRef.UID)
	case KindAgent:
		_, exists = r.Agent(meta.OwnerRef.UID)
	}
	if !exists {
		return stateErr(op, ErrInvalidRegistry, "%s %q ownerRef %q does not exist", kind, meta.Name, meta.OwnerRef.UID)
	}
	return nil
}

// quotedKinds renders an allowed owner set the way the single-kind refusal
// rendered one kind, so a one-element set produces the exact pre-existing text.
func quotedKinds(kinds []Kind) string {
	quoted := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		quoted = append(quoted, `"`+string(kind)+`"`)
	}
	return strings.Join(quoted, " or ")
}

// validateReservations proves the reservation table and the resource names
// agree in both directions, so name allocation can trust the table alone.
func (r Registry) validateReservations(op string, uids map[string]Kind) error {
	seen := map[nameKey]string{}
	seenUID := map[string]bool{}
	for _, reservation := range r.NameReservations {
		key := nameKey{Scope: reservation.Scope, Kind: reservation.Kind, Name: reservation.Name}
		if owner, ok := seen[key]; ok {
			return stateErr(op, ErrInvalidRegistry, "duplicate %s name reservation %q in scope %q (%s and %s)", reservation.Kind, reservation.Name, reservation.Scope, owner, reservation.UID)
		}
		uidKind, ok := uids[reservation.UID]
		if !ok {
			return stateErr(op, ErrInvalidRegistry, "%s name reservation %q refers to unknown uid %q", reservation.Kind, reservation.Name, reservation.UID)
		}
		if uidKind != reservation.Kind {
			return stateErr(op, ErrInvalidRegistry, "%s name reservation %q refers to %s uid %q", reservation.Kind, reservation.Name, uidKind, reservation.UID)
		}
		if seenUID[reservation.UID] {
			return stateErr(op, ErrInvalidRegistry, "%s uid %q has more than one name reservation", reservation.Kind, reservation.UID)
		}
		seenUID[reservation.UID] = true
		seen[key] = reservation.UID
	}

	require := func(scope string, kind Kind, name, uid string) error {
		key := nameKey{Scope: scope, Kind: kind, Name: name}
		owner, ok := seen[key]
		if !ok {
			return stateErr(op, ErrInvalidRegistry, "%s %q has no name reservation in scope %q", kind, name, scope)
		}
		if owner != uid {
			return stateErr(op, ErrInvalidRegistry, "%s name %q in scope %q is reserved by %q, not %q", kind, name, scope, owner, uid)
		}
		return nil
	}
	for _, project := range r.Projects {
		if err := require("", KindProject, project.Metadata.Name, project.Metadata.UID); err != nil {
			return err
		}
	}
	for _, control := range r.ControlSessions {
		if err := require("", KindControlSession, control.Metadata.Name, control.Metadata.UID); err != nil {
			return err
		}
	}
	for _, window := range r.Windows {
		scope, err := r.scopeFor(KindWindow, window.Metadata.OwnerUID())
		if err != nil {
			return err
		}
		if err := require(scope, KindWindow, window.Metadata.Name, window.Metadata.UID); err != nil {
			return err
		}
	}
	for _, pane := range r.Panes {
		scope, err := r.scopeFor(KindPane, pane.Metadata.OwnerUID())
		if err != nil {
			return err
		}
		if err := require(scope, KindPane, pane.Metadata.Name, pane.Metadata.UID); err != nil {
			return err
		}
	}
	for _, agent := range r.Agents {
		scope, err := r.scopeFor(KindAgent, agent.Metadata.OwnerUID())
		if err != nil {
			return err
		}
		if err := require(scope, KindAgent, agent.Metadata.Name, agent.Metadata.UID); err != nil {
			return err
		}
	}
	return nil
}
