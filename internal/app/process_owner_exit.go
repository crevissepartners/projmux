package app

import (
	"errors"
	"fmt"
	"io"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// processOwnerEndNotice decides, after a foreground owner's generation ended
// with an error, whether that owner still has anything to clean up.
//
// Another process can end a generation: a delete stops and removes the Agent,
// and a relaunch or resume stops it and continues the same Agent uid as a new
// generation. The old owner then sees its closed host as an error. Printing its
// usual `delete agent` cleanup command at that point is destructive: following
// it later would delete the continued Agent. The Registry decides instead:
//
//   - the Agent or Pane is gone: another process deleted it;
//   - the Pane's process binding is another generation: it continued there;
//   - this generation's exact Wait is recorded and the only errors are the
//     closed or stale host it left behind: it was stopped elsewhere and is a
//     valid offline Agent.
//
// Anything else keeps the owner's existing failure guidance.
func processOwnerEndNotice(registry coremetadata.Registry, binding processhost.Binding, waitRecorded bool, cause error) (string, bool) {
	pane, paneOK := registry.Pane(binding.Pane)
	agent, agentOK := registry.Agent(binding.Agent)
	if !paneOK || !agentOK {
		return fmt.Sprintf("agent uid:%s was deleted by another process; nothing to clean up", binding.Agent), true
	}
	record := pane.Status.ProcessSession
	own := metadataProcessBinding(binding)
	if record != nil && record.Binding.PaneUID == own.PaneUID && record.Binding.AgentUID == own.AgentUID && record.Binding.Generation != own.Generation {
		return fmt.Sprintf("agent uid:%s pane uid:%s continued as generation %s in another process; this owner's generation %s ended; nothing to clean up",
			binding.Agent, binding.Pane, record.Binding.Generation, binding.Generation), true
	}
	if waitRecorded && record != nil && record.Binding == own && pane.Status.Activation.IsZero() &&
		agent.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(own, pane.Status.LastTermination) &&
		coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) && processHostClosedOnly(cause) {
		return fmt.Sprintf("agent uid:%s generation %s was stopped by another process; it is offline with its recorded Wait; nothing to clean up",
			binding.Agent, binding.Generation), true
	}
	return "", false
}

// processHostClosedOnly reports whether every joined error is the closed or
// stale host an external Stop leaves behind.
func processHostClosedOnly(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, part := range joined.Unwrap() {
			if !processHostClosedOnly(part) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, processhost.ErrClosed) || errors.Is(err, processhost.ErrStale)
}

// processOwnerEnded re-reads the Registry for a failed owner end. When another
// process already took the generation over, it prints the notice and returns
// the generation's actual Wait exit, or a plain error naming the notice when no
// Wait was observed; ok is false when the caller's own guidance still applies.
func processOwnerEnded(registryPath string, binding processhost.Binding, waitRecorded bool, snapshot processhost.Snapshot, cause error, stderr io.Writer) (error, bool) {
	if registryPath == "" || binding.Agent == "" {
		return nil, false
	}
	registry, err := intmetadata.NewStore(registryPath).LoadDegradedReadOnly()
	if err != nil {
		return nil, false
	}
	notice, ok := processOwnerEndNotice(registry, binding, waitRecorded, cause)
	if !ok {
		return nil, false
	}
	if snapshot.Exit == nil {
		return errors.New(notice), true
	}
	if stderr != nil {
		_, _ = fmt.Fprintln(stderr, notice)
	}
	return processWaitExit(snapshot), true
}
