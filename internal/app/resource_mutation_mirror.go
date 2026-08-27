package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// resourceMutationMirror is the narrow live-transport seam shared by rename
// and rebind. Lookups distinguish an offline resource from an exact live UID;
// writes touch only the field owned by the invoking verb.
type resourceMutationMirror interface {
	FindSessionForProjectUID(context.Context, string) (string, bool, error)
	FindWindowTargetForUID(context.Context, string) (string, bool, error)
	FindPaneTargetForUID(context.Context, string) (string, bool, error)
	RenameProject(context.Context, string, string) error
	RenameWindow(context.Context, string, string) error
	RenamePane(context.Context, string, string) error
	RebindProject(context.Context, string, string) error
}

// defaultResourceMutationMirror enables the best-effort immediate projection
// only when this invocation inherited an exact absolute tmux socket path. A
// command run outside tmux remains Registry-only; it must never probe or mutate
// whichever server a bare `tmux` command would happen to select.
func defaultResourceMutationMirror() resourceMutationMirror {
	return inheritedResourceMutationMirror(os.Getenv, inttmux.ExecRunner{})
}

func inheritedResourceMutationMirror(lookupEnv func(string) string, runner tmuxCommandRunner) resourceMutationMirror {
	if lookupEnv == nil || runner == nil {
		return nil
	}
	target, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{InheritedTMUX: lookupEnv("TMUX")})
	if err != nil || !target.Present() {
		return nil
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	return intmetadata.NewMirror(routed)
}

func committedMirrorError(verb string, kind coremetadata.Kind, uid string, err error) error {
	// Keep this as an app-level operational error instead of unwrapping the
	// subprocess error. An exec.ExitError implements ExitCode; exposing it would
	// make cmd/projmux treat the failure as a command that already printed its
	// own diagnostic and suppress this required recovery message on stderr.
	return fmt.Errorf("%s %s %q committed Registry state but could not converge its exact live tmux mirror: %v; retry on the same exact socket with `projmux reconcile resources`",
		verb, strings.ToLower(string(kind)), uid, err)
}
