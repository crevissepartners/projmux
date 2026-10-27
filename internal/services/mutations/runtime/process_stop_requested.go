package runtime

import (
	"context"
	"reflect"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// recordStopRequested runs only after successful Stop/claim retirement. Its
// transaction cannot retarget a new generation or supply any termination proof.
func (s *ProcessAgentStopper) recordStopRequested(ctx context.Context, retired coremetadata.Pane) error {
	update := s.Update
	if update == nil {
		update = intmetadata.NewStore(s.RegistryPath).UpdateConvergent
	}
	_, _, err := update(func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		record := retired.Status.ProcessSession
		current, ok := reg.Pane(retired.Metadata.UID)
		target, found := reg.Agent(retired.Metadata.OwnerUID())
		exact, ambiguous := ProcessAgentPane(*reg, retired.Metadata.OwnerUID())
		if record == nil || !ok || !found || ambiguous || exact == nil || exact.Metadata.UID != retired.Metadata.UID ||
			current.Metadata.OwnerUID() != retired.Metadata.OwnerUID() || target.Status.PaneRef != retired.Metadata.UID ||
			target.Status.Phase != coremetadata.PhaseOffline || !current.Status.Activation.IsZero() ||
			current.Status.ProcessSession == nil || current.Status.ProcessSession.Binding != record.Binding ||
			!reflect.DeepEqual(current.Status.LastTermination, retired.Status.LastTermination) ||
			!reflect.DeepEqual(target.Status.LastTermination, retired.Status.LastTermination) {
			return processhost.ErrStale
		}
		current.Status.ProcessSession.StopRequested = true
		return nil
	})
	return err
}
