package runtime

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
)

// superviseSpec is the identity one supervised launch quotes back when its
// child stops.
//
// Everything in it is minted by projmux before the pane exists. Nothing is read
// out of the pane, the shell, or the provider afterwards, which is what keeps
// the receipt an assertion about a process this build started rather than a
// guess about a process it found.
type superviseSpec struct {
	PaneUID     string
	AgentUID    string
	Generation  string
	OperationID string
	// RuntimeID is the exact raw tmux Pane handle observed by the activation
	// gate after tmux started this generation. It is intentionally absent from
	// the supervisor argv because no raw Pane exists when that argv is planned;
	// activation-exec fills it only after the committed Registry binding agrees
	// with its inherited TMUX_PANE.
	RuntimeID string
	// RegistryPath is the creator-resolved private authority for Agent
	// admission. Internal routes carry it explicitly; the admitted Claude
	// child also passes it to its hook in private PMX_INTERNAL context. It is
	// never part of the public PROJMUX_* hook contract.
	RegistryPath string
	// ClaudeRegistration is set only after exact gate admission identifies a
	// Claude child. Its registration hook inherits the creator-selected path
	// through private PMX_INTERNAL context, never the public PROJMUX hook API.
	ClaudeRegistration bool
	DialogueReplyOnly  bool
	// ServerDefaultCommand launches no planned child: the supervisor runs the
	// process tmux itself would have started, read from the server the Pane
	// runs in. Only a session's first Pane on a server its own new-session
	// starts uses it, because no server exists to read before that.
	ServerDefaultCommand bool
}

// superviseServerDefaultCommandFlag is the private flag that carries
// superviseSpec.ServerDefaultCommand.
const superviseServerDefaultCommandFlag = "server-default-command"

// valid reports whether the spec can identify a receipt at all.
func (s superviseSpec) valid() bool {
	return strings.TrimSpace(s.PaneUID) != "" && strings.TrimSpace(s.Generation) != ""
}
