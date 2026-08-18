package metadata

import (
	"path/filepath"
	"strings"
)

// Validate checks every structural invariant of the registry: envelope
// spelling, uid uniqueness, in-scope name uniqueness, ownerRef integrity,
// primaryPaneRef validity, Agent phase membership, and reservation
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
		if err := r.requireOwner(op, KindWindow, window.Metadata, KindProject); err != nil {
			return err
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
		if err := validateTermination(op, "pane "+pane.Metadata.Name, pane.Status.LastTermination); err != nil {
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
		if source := strings.TrimSpace(agent.Status.Activation.Source); source != "" && source != string(InteractionSourceProviderHook) {
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
		if err := validateSessionRef(op, agent); err != nil {
			return err
		}
		if err := validateTermination(op, "agent "+agent.Metadata.Name, agent.Status.LastTermination); err != nil {
			return err
		}
	}

	for _, window := range r.Windows {
		owned := r.windowPaneUIDs(window.Metadata.UID)
		if window.Spec.PrimaryPaneRef == "" {
			if len(owned) > 0 {
				return stateErr(op, ErrInvalidRegistry, "window %q has panes but no primaryPaneRef", window.Metadata.Name)
			}
			continue
		}
		if _, ok := r.Pane(window.Spec.PrimaryPaneRef); !ok {
			return stateErr(op, ErrInvalidRegistry, "window %q primaryPaneRef %q does not exist", window.Metadata.Name, window.Spec.PrimaryPaneRef)
		}
		if !owned[window.Spec.PrimaryPaneRef] {
			return stateErr(op, ErrInvalidRegistry, "window %q primaryPaneRef %q is not owned by the window or one of its agents", window.Metadata.Name, window.Spec.PrimaryPaneRef)
		}
	}

	return r.validateReservations(op, uids)
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
	if strings.TrimSpace(receipt.PaneUID) == "" {
		return stateErr(op, ErrInvalidRegistry, "%s has a termination receipt naming no pane", subject)
	}
	return nil
}

// windowPaneUIDs returns every Pane uid transitively owned by a Window: its
// own shell Panes plus the managed Panes of the Agents it owns.
func (r Registry) windowPaneUIDs(windowUID string) map[string]bool {
	owners := map[string]bool{windowUID: true}
	for _, agent := range r.Agents {
		if agent.Metadata.OwnerUID() == windowUID {
			owners[agent.Metadata.UID] = true
		}
	}
	out := map[string]bool{}
	for _, pane := range r.Panes {
		if owners[pane.Metadata.OwnerUID()] {
			out[pane.Metadata.UID] = true
		}
	}
	return out
}

func (r Registry) requireOwner(op string, kind Kind, meta ObjectMeta, wantKind Kind) error {
	if meta.OwnerRef == nil {
		return stateErr(op, ErrInvalidRegistry, "%s %q has no ownerRef", kind, meta.Name)
	}
	if meta.OwnerRef.Kind != wantKind {
		return stateErr(op, ErrInvalidRegistry, "%s %q ownerRef kind %q is not %q", kind, meta.Name, meta.OwnerRef.Kind, wantKind)
	}
	var exists bool
	switch wantKind {
	case KindProject:
		_, exists = r.Project(meta.OwnerRef.UID)
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

// validateReservations proves the reservation table and the resource names
// agree in both directions, so name allocation can trust the table alone.
func (r Registry) validateReservations(op string, uids map[string]Kind) error {
	seen := map[nameKey]string{}
	for _, reservation := range r.NameReservations {
		key := nameKey{Scope: reservation.Scope, Kind: reservation.Kind, Name: reservation.Name}
		if owner, ok := seen[key]; ok {
			return stateErr(op, ErrInvalidRegistry, "duplicate %s name reservation %q in scope %q (%s and %s)", reservation.Kind, reservation.Name, reservation.Scope, owner, reservation.UID)
		}
		if _, ok := uids[reservation.UID]; !ok {
			return stateErr(op, ErrInvalidRegistry, "%s name reservation %q refers to unknown uid %q", reservation.Kind, reservation.Name, reservation.UID)
		}
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
	for _, window := range r.Windows {
		if err := require(window.Metadata.OwnerUID(), KindWindow, window.Metadata.Name, window.Metadata.UID); err != nil {
			return err
		}
	}
	for _, pane := range r.Panes {
		if err := require(pane.Metadata.OwnerUID(), KindPane, pane.Metadata.Name, pane.Metadata.UID); err != nil {
			return err
		}
	}
	for _, agent := range r.Agents {
		if err := require(agent.Metadata.OwnerUID(), KindAgent, agent.Metadata.Name, agent.Metadata.UID); err != nil {
			return err
		}
	}
	return nil
}
