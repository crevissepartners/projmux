package app

import (
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// deleteRootOwner is the exact Registry root authority a live delete follows.
//
// Project and ControlSession are peers in the ownership graph, but they do not
// carry the same session projection. A Project names its runtime through
// status.session and may mirror its uid on that session. A ControlSession names
// its runtime through spec.session and must not carry a Project uid mirror.
// Keeping the kind beside the uid prevents the live preflight from flattening a
// control-owned descendant into the old Project-only transport policy.
type deleteRootOwner struct {
	Kind    coremetadata.Kind
	UID     string
	Session string
}

func deleteRootForWindow(registry coremetadata.Registry, window coremetadata.Window) (deleteRootOwner, error) {
	owner := window.Metadata.OwnerRef
	if owner == nil {
		return deleteRootOwner{}, fmt.Errorf("registry Window uid %q has no managed root owner", window.Metadata.UID)
	}
	switch owner.Kind {
	case coremetadata.KindProject:
		project, ok := registry.Project(owner.UID)
		if !ok {
			return deleteRootOwner{}, fmt.Errorf("registry Window uid %q has no owning Project %q", window.Metadata.UID, owner.UID)
		}
		session := ""
		if project.Status.Session != nil {
			session = strings.TrimSpace(project.Status.Session.Name)
		}
		return deleteRootOwner{Kind: owner.Kind, UID: owner.UID, Session: session}, nil
	case coremetadata.KindControlSession:
		control, ok := registry.ControlSession(owner.UID)
		if !ok {
			return deleteRootOwner{}, fmt.Errorf("registry Window uid %q has no owning ControlSession %q", window.Metadata.UID, owner.UID)
		}
		return deleteRootOwner{
			Kind: owner.Kind, UID: owner.UID, Session: strings.TrimSpace(control.Spec.Session),
		}, nil
	default:
		return deleteRootOwner{}, fmt.Errorf("registry Window uid %q has unsupported root owner kind %q uid %q",
			window.Metadata.UID, owner.Kind, owner.UID)
	}
}

func (o deleteRootOwner) validateLiveSession(spelling, liveKind, liveID, resourceUID, projectMirror, sessionName string) error {
	switch o.Kind {
	case coremetadata.KindProject:
		if projectMirror != "" && projectMirror != o.UID {
			return fmt.Errorf("%s: live tmux %s %s mirrors registry uid %q under foreign Project uid %q, want %q; nothing was changed",
				spelling, liveKind, liveID, resourceUID, projectMirror, o.UID)
		}
		if o.Session == "" {
			return fmt.Errorf("%s: owning Project uid %q has no registry session projection for live tmux %s %s; nothing was changed",
				spelling, o.UID, liveKind, liveID)
		}
		if sessionName != o.Session {
			return fmt.Errorf("%s: live tmux %s %s is in stale session %q, registry Project uid %q projects session %q; nothing was changed",
				spelling, liveKind, liveID, sessionName, o.UID, o.Session)
		}
	case coremetadata.KindControlSession:
		if projectMirror != "" {
			return fmt.Errorf("%s: live tmux %s %s mirrors registry uid %q in ControlSession owner scope %q but carries conflicting Project uid %q; nothing was changed",
				spelling, liveKind, liveID, resourceUID, o.UID, projectMirror)
		}
		if o.Session == "" {
			return fmt.Errorf("%s: owning ControlSession uid %q has no exact registry session identity for live tmux %s %s; nothing was changed",
				spelling, o.UID, liveKind, liveID)
		}
		if sessionName != o.Session {
			return fmt.Errorf("%s: live tmux %s %s is in stale session %q, registry ControlSession uid %q names session %q; nothing was changed",
				spelling, liveKind, liveID, sessionName, o.UID, o.Session)
		}
	default:
		return fmt.Errorf("%s: live tmux %s %s has unsupported Registry root kind %q; nothing was changed",
			spelling, liveKind, liveID, o.Kind)
	}
	return nil
}
