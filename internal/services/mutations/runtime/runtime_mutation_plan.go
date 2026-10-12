package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runtimeMutationVerb is the closed lifecycle/topology mutation vocabulary.
// Keep this list deliberately transport-shaped: adding a verb requires adding
// its guard and expected-effect contract to runtimeMutationInventory.
type RuntimeMutationVerb string

const (
	MutationCreateSession           RuntimeMutationVerb = "create-session"
	MutationFinalizeSession         RuntimeMutationVerb = "finalize-session-startup"
	MutationCreateWindow            RuntimeMutationVerb = "create-window"
	MutationCreatePane              RuntimeMutationVerb = "create-pane"
	MutationWriteIdentity           RuntimeMutationVerb = "write-identity"
	MutationWriteOption             RuntimeMutationVerb = "write-option"
	MutationWritePresentationOption RuntimeMutationVerb = "write-presentation-option"
	MutationWriteStableName         RuntimeMutationVerb = "write-stable-name"
	MutationWriteLease              RuntimeMutationVerb = "write-lease"
	MutationClearLease              RuntimeMutationVerb = "clear-lease"
	MutationWriteLayout             RuntimeMutationVerb = "write-layout"
	MutationKillOwned               RuntimeMutationVerb = "kill-owned"
	MutationKillPane                RuntimeMutationVerb = "kill-pane"
	MutationTombstonePane           RuntimeMutationVerb = "tombstone-pane"
	MutationRestorePane             RuntimeMutationVerb = "restore-pane"
	MutationQueuePaneKill           RuntimeMutationVerb = "queue-pane-kill"
	MutationKillWindow              RuntimeMutationVerb = "kill-window"
	MutationQueueWindowKill         RuntimeMutationVerb = "queue-window-kill"
	MutationStopManagedSession      RuntimeMutationVerb = "stop-managed-session"
	MutationStopUnmanagedSession    RuntimeMutationVerb = "stop-unmanaged-session"
	MutationBootstrapControlSession RuntimeMutationVerb = "bootstrap-control-session"
	MutationRenameWindow            RuntimeMutationVerb = "rename-window"
	MutationWriteRouteMarker        RuntimeMutationVerb = "write-route-marker"
	MutationWriteProjectAnchor      RuntimeMutationVerb = "write-project-anchor"
	MutationConvergeControlIdentity RuntimeMutationVerb = "converge-control-identity"
)

type RuntimeMutationContract struct {
	GuardKind string
	Effect    string
}

// runtimeMutationGuard is the printable evidence contract for one action.
// Kind closes the vocabulary while Expect binds that kind to the exact fact
// observed for this target when the plan was built.
type RuntimeMutationGuard struct {
	Kind   string `json:"kind"`
	Expect string `json:"expect"`
}

// runtimeMutationInventory is the reviewable, printable closure proof. The
// executor rejects every action absent from this table before running guards.
var RuntimeMutationInventory = map[RuntimeMutationVerb]RuntimeMutationContract{
	MutationCreateSession:           {GuardKind: "session-name-and-project-identity", Effect: "one detached owned session exists"},
	MutationFinalizeSession:         {GuardKind: "create-operation-lease", Effect: "startup hooks are finalized once"},
	MutationCreateWindow:            {GuardKind: "session-containment-and-owner-inventory", Effect: "one detached owned window and primary pane exist"},
	MutationCreatePane:              {GuardKind: "anchor-containment-and-pane-inventory", Effect: "one detached owned pane exists"},
	MutationWriteIdentity:           {GuardKind: "exact-handle-owner-chain", Effect: "exact uid identity mirror equals desired value"},
	MutationWriteOption:             {GuardKind: "exact-handle-owner-chain", Effect: "exact typed tmux option equals desired value"},
	MutationWritePresentationOption: {GuardKind: "exact-handle-owner-chain", Effect: "exact exempt presentation option equals desired value"},
	MutationWriteStableName:         {GuardKind: "exact-window-evidence", Effect: "exact Window stable-name option equals desired value"},
	MutationWriteLease:              {GuardKind: "exact-owned-session", Effect: "create-operation lease equals marker"},
	MutationClearLease:              {GuardKind: "create-operation-lease", Effect: "create-operation lease is absent"},
	MutationWriteLayout:             {GuardKind: "exact-anchor-containment", Effect: "best-effort even split layout is selected"},
	MutationKillOwned:               {GuardKind: "operation-owned-uid", Effect: "only the owned created object is absent"},
	MutationKillPane:                {GuardKind: "exact-pane-evidence", Effect: "exact live pane is absent"},
	MutationTombstonePane:           {GuardKind: "exact-pane-evidence", Effect: "pane uid mirror carries the delete tombstone"},
	MutationRestorePane:             {GuardKind: "exact-pane-evidence", Effect: "pane uid mirror is restored"},
	MutationQueuePaneKill:           {GuardKind: "exact-pane-evidence", Effect: "exact pane kill is queued after result flush"},
	MutationKillWindow:              {GuardKind: "exact-window-evidence", Effect: "exact live window is absent"},
	MutationQueueWindowKill:         {GuardKind: "exact-window-evidence", Effect: "exact window kill is queued after result flush"},
	MutationStopManagedSession:      {GuardKind: "exact-managed-root-session", Effect: "exact managed root session is absent"},
	MutationStopUnmanagedSession:    {GuardKind: "exact-unmanaged-session", Effect: "exact unowned or ephemeral session is absent"},
	MutationBootstrapControlSession: {GuardKind: "exact-control-session-absence", Effect: "one detached app-owned control session exists"},
	MutationRenameWindow:            {GuardKind: "exact-window-evidence", Effect: "exact owned window name equals desired value"},
	MutationWriteRouteMarker:        {GuardKind: "exact-app-owned-session", Effect: "server logical route marker equals invocation route"},
	MutationWriteProjectAnchor:      {GuardKind: "create-operation-lease", Effect: "exact created session Project path anchor equals Registry root"},
	MutationConvergeControlIdentity: {GuardKind: "exact-control-session-containment", Effect: "control root UID and exact Window/Pane mirrors equal desired identity"},
}

// runtimeMutationTarget is stable across formatting and independent of tmux
// indexes. ID is an exact tmux handle when one exists; UID is the desired or
// observed Projmux identity. Parent pins containment for pane/window actions.
type RuntimeMutationTarget struct {
	Socket         string `json:"socket"`
	PhysicalSocket string `json:"physicalSocket"`
	RouteAuthority string `json:"routeAuthority,omitempty"`
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	UID            string `json:"uid"`
	Parent         string `json:"parent"`
}

const RuntimeMutationSocketAbsentBeforeCreate = "absent-before-create"

func PrintableRuntimeMutationSocket(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return RuntimeMutationSocketAbsentBeforeCreate
	}
	return path
}

type PlannedRuntimeMutation struct {
	Order  int                   `json:"order"`
	Verb   RuntimeMutationVerb   `json:"action"`
	Target RuntimeMutationTarget `json:"target"`
	Guard  RuntimeMutationGuard  `json:"guard"`
	Effect string                `json:"expectedEffect"`
	// Operands are printable typed step data. The managed tmux verb itself is
	// selected only by runtimeMutationArgv, which also binds any executable -t
	// operand back to Target.ID before returning argv.
	Operands []string                   `json:"operands,omitempty"`
	Command  []string                   `json:"command,omitempty"`
	Queue    *RuntimeMutationQueuedKill `json:"queue,omitempty"`
	// LeaseMarker makes a clear-lease a compare-and-unset: tmux unsets the
	// variable only while it still holds exactly this marker, in one server
	// command. Empty keeps the unconditional unset.
	LeaseMarker string `json:"leaseMarker,omitempty"`
	// Controller is an app-internal, printable binding between a controller
	// declaration and its executable tmux argv. It is deliberately absent from
	// the public controller.Plan JSON; only the Phase 10 execution plan carries
	// this closed effect grammar.
	Controller *RuntimeMutationControllerEffect `json:"controller,omitempty"`
}

type RuntimeMutationControllerEffect struct {
	Class  string `json:"class"`
	Mode   string `json:"mode"`
	Scope  string `json:"scope"`
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type RuntimeMutationQueuedKill struct {
	PhysicalSocket string `json:"physicalSocket"`
	LogicalSocket  string `json:"logicalSocket"`
	RouteAuthority string `json:"routeAuthority,omitempty"`
	ExpectedUID    string `json:"expectedUid"`
	SessionID      string `json:"sessionId"`
	WindowID       string `json:"windowId"`
	Marker         string `json:"marker"`
}

// runtimeMutationArgv is the sole assembler for managed lifecycle/topology
// tmux verbs in this slice. Producers choose a closed typed intent and provide
// only its operands; they cannot smuggle a second lifecycle verb through the
// printable plan.
func RuntimeMutationArgv(action PlannedRuntimeMutation) ([]string, error) {
	if err := ValidateRuntimeMutationOperandTarget(action); err != nil {
		return nil, err
	}
	for _, operand := range action.Operands {
		if ClosedTmuxTopologyMutationVerbs[operand] {
			return nil, fmt.Errorf("runtime mutation plan: action %q carries hidden managed verb operand %q", action.Verb, operand)
		}
	}
	if len(action.Command) > 0 && !RuntimeMutationCarriesChildCommand(action.Verb) {
		return nil, fmt.Errorf("runtime mutation plan: action %q carries an unexpected child command", action.Verb)
	}
	var verb string
	switch action.Verb {
	case MutationCreateWindow:
		verb = "new-window"
	case MutationCreatePane:
		verb = "split-window"
	case MutationFinalizeSession:
		verb = "set-environment"
	case MutationWriteLayout:
		verb = "resize-pane"
	case MutationKillPane:
		verb = "kill-pane"
	case MutationKillWindow:
		verb = "kill-window"
	case MutationStopManagedSession, MutationStopUnmanagedSession:
		verb = "kill-session"
	case MutationKillOwned:
		switch action.Target.Kind {
		case "session":
			verb = "kill-session"
		case "window":
			verb = "kill-window"
		case "pane":
			verb = "kill-pane"
		default:
			return nil, fmt.Errorf("runtime mutation plan: kill-owned target kind %q has no typed executor", action.Target.Kind)
		}
	case MutationTombstonePane, MutationRestorePane, MutationWriteIdentity, MutationWriteOption, MutationWritePresentationOption, MutationWriteStableName, MutationWriteProjectAnchor:
		verb = "set-option"
	case MutationQueuePaneKill, MutationQueueWindowKill:
		if action.Queue == nil || !filepath.IsAbs(action.Queue.PhysicalSocket) || filepath.Clean(action.Queue.PhysicalSocket) != action.Queue.PhysicalSocket ||
			action.Queue.PhysicalSocket != action.Target.PhysicalSocket ||
			strings.TrimSpace(action.Queue.ExpectedUID) == "" || action.Queue.ExpectedUID != action.Target.UID ||
			strings.TrimSpace(action.Target.ID) == "" || ExactTmuxHandle(action.Queue.SessionID, "$") == "" {
			return nil, errors.New("runtime mutation plan: queued kill route or exact target is incomplete")
		}
		if action.Queue.Marker == "" || action.Queue.Marker != RuntimeMutationQueueMarker(action) {
			return nil, errors.New("runtime mutation plan: queued kill durable marker disagrees with printable target")
		}
		authority, authorityErr := ParseRuntimeMutationRouteAuthority(action.Target.RouteAuthority)
		if authorityErr != nil || action.Queue.RouteAuthority != action.Target.RouteAuthority {
			return nil, errors.New("runtime mutation plan: queued kill route authority disagrees with printable target")
		}
		isStandalone := authority.Class == RuntimeMutationRouteStandalone
		if isStandalone {
			if action.Queue.LogicalSocket != "" || action.Target.Socket != "-S="+action.Queue.PhysicalSocket {
				return nil, errors.New("runtime mutation plan: queued kill standalone authority disagrees with printable target")
			}
		} else {
			logical, logicalRoute := strings.CutPrefix(action.Target.Socket, "-L=")
			physical, physicalRoute := strings.CutPrefix(action.Target.Socket, "-S=")
			if (!logicalRoute || logical != action.Queue.LogicalSocket) &&
				(!physicalRoute || physical != action.Queue.PhysicalSocket) {
				return nil, errors.New("runtime mutation plan: queued kill logical route disagrees with printable target")
			}
			if _, err := TmuxSocketNameTarget(action.Queue.LogicalSocket); err != nil {
				return nil, errors.New("runtime mutation plan: queued kill logical route is invalid")
			}
		}
		killVerb := "kill-pane"
		uidOption := tmuxopts.PaneUID
		identity := "#{&&:#{==:#{session_id}," + action.Queue.SessionID + "},#{&&:#{==:#{window_id}," + action.Queue.WindowID + "},#{==:#{pane_id}," + action.Target.ID + "}}}"
		if action.Verb == MutationQueueWindowKill {
			killVerb = "kill-window"
			uidOption = tmuxopts.WindowUID
			identity = "#{&&:#{==:#{session_id}," + action.Queue.SessionID + "},#{==:#{window_id}," + action.Target.ID + "}}"
		} else if ExactTmuxHandle(action.Queue.WindowID, "@") == "" {
			return nil, errors.New("runtime mutation plan: queued Pane kill has no exact Window containment")
		}
		if !strings.HasPrefix(action.Target.Parent, action.Queue.SessionID+"/") ||
			(action.Verb == MutationQueuePaneKill && !strings.HasPrefix(action.Target.Parent, action.Queue.SessionID+"/"+action.Queue.WindowID+"/")) {
			return nil, errors.New("runtime mutation plan: queued kill containment disagrees with printable target")
		}
		routeCondition := "#{&&:#{==:#{pid}," + authority.ServerPID + "},#{&&:#{==:#{" + tmuxopts.AppGlobal + "},1},#{==:#{" + RuntimeMutationSocketNameOption + "}," + action.Queue.LogicalSocket + "}}}"
		if isStandalone {
			routeCondition = "#{&&:#{==:#{pid}," + authority.ServerPID + "},#{&&:#{==:#{" + tmuxopts.AppGlobal + "},},#{==:#{" + RuntimeMutationSocketNameOption + "},}}}"
		}
		leaseCondition := "#{==:#{E:" + action.Queue.Marker + "}," + action.Queue.ExpectedUID + "}"
		condition := "#{&&:" + routeCondition + ",#{&&:" + leaseCondition + ",#{&&:" + identity + ",#{==:#{" + uidOption + "}," + action.Queue.ExpectedUID + "}}}}"
		clearCondition := "#{&&:" + routeCondition + "," + leaseCondition + "}"
		deferredKill := killVerb + " -t " + action.Target.ID
		deferredClear := "set-environment -gu " + action.Queue.Marker
		// run-shell expands formats before starting its shell. Delay the inner
		// if-shell conditions by one tmux expansion so exact -S/-t observation
		// happens in the declared target context, not whichever Pane happened to
		// be current when the background job was queued.
		condition = strings.ReplaceAll(condition, "#", "##")
		clearCondition = strings.ReplaceAll(clearCondition, "#", "##")
		command := ShellQuote("tmux") + " -S " + ShellQuote(action.Queue.PhysicalSocket) +
			" if-shell -F -t " + ShellQuote(action.Target.ID) + " " + ShellQuote(condition) + " " + ShellQuote(deferredKill) + " ''; " +
			"status=$?; " + ShellQuote("tmux") + " -S " + ShellQuote(action.Queue.PhysicalSocket) +
			" if-shell -F " + ShellQuote(clearCondition) + " " + ShellQuote(deferredClear) + " '' >/dev/null 2>&1 || true; exit $status"
		return []string{"set-environment", "-g", action.Queue.Marker, action.Queue.ExpectedUID, ";", "run-shell", "-b", command}, nil
	case MutationClearLease:
		if action.LeaseMarker != "" {
			return RuntimeMutationClearLeaseIfOwnedArgv(action)
		}
		verb = "set-environment"
	case MutationWriteLease:
		verb = "set-environment"
	case MutationRenameWindow:
		verb = "rename-window"
	case MutationCreateSession:
		// A fresh managed Project server must load the generated app config
		// before new-session creates its first object. Keep the global tmux
		// option in printable typed data and assemble it before the verb.
		if len(action.Operands) >= 2 && action.Operands[0] == "-f" {
			argv := append(append([]string(nil), action.Operands[:2]...), append([]string{"new-session"}, action.Operands[2:]...)...)
			return append(argv, action.Command...), nil
		}
		verb = "new-session"
	case MutationBootstrapControlSession:
		// Bootstrap needs global route/config flags before the managed verb.
		if len(action.Operands) < 4 {
			return nil, errors.New("runtime mutation plan: control bootstrap operands are incomplete")
		}
		return append(append([]string(nil), action.Operands[:4]...), append([]string{"new-session"}, action.Operands[4:]...)...), nil
	case MutationWriteRouteMarker:
		if len(action.Operands) < 2 {
			return nil, errors.New("runtime mutation plan: route marker operands are incomplete")
		}
		return append(append([]string(nil), action.Operands[:2]...), append([]string{"set-option"}, action.Operands[2:]...)...), nil
	default:
		return nil, fmt.Errorf("runtime mutation plan: action %q executes through a typed non-argv seam", action.Verb)
	}
	argv := append([]string{verb}, action.Operands...)
	return append(argv, action.Command...), nil
}

// runtimeMutationLeaseMarkerPattern admits the create-operation marker grammar
// (v1:<pid>:<unix>:<operation id>) and nothing a tmux format or command parser
// would read: no '#', ',', '}', quote, '$', ';', or whitespace.
var RuntimeMutationLeaseMarkerPattern = regexp.MustCompile(`^[A-Za-z0-9:._-]+$`)

// runtimeMutationClearLeaseIfOwnedArgv assembles the owner-checked lease clear.
// The comparison and the unset run inside one if-shell, so tmux serializes them
// against every other client command: a later create that writes its own
// marker into the same variable between our observation and this command keeps
// it. A read followed by a separate unset would delete that marker.
func RuntimeMutationClearLeaseIfOwnedArgv(action PlannedRuntimeMutation) ([]string, error) {
	id := ExactTmuxHandle(action.Target.ID, "$")
	if id == "" || id != action.Target.ID || len(action.Operands) != 4 ||
		action.Operands[0] != "-u" || action.Operands[1] != "-t" || action.Operands[2] != id {
		return nil, errors.New("runtime mutation plan: owner-checked lease clear is not exactly -u -t <exact session> <lease>")
	}
	variable := action.Operands[3]
	if variable != CreateOperationEnvironment && variable != FinalizeOperationEnvironment {
		return nil, fmt.Errorf("runtime mutation plan: owner-checked lease clear targets unknown variable %q", variable)
	}
	if !RuntimeMutationLeaseMarkerPattern.MatchString(action.LeaseMarker) {
		return nil, errors.New("runtime mutation plan: owner-checked lease clear marker carries format or quote characters")
	}
	condition := "#{==:#{E:" + variable + "}," + action.LeaseMarker + "}"
	// The inner command is parsed again by tmux, which would expand an unquoted
	// $N as an environment variable; single quotes keep the exact handle.
	return []string{"if-shell", "-F", "-t", id, condition, "set-environment -u -t '" + id + "' " + variable}, nil
}

func RuntimeMutationQueueMarker(action PlannedRuntimeMutation) string {
	id := strings.NewReplacer("$", "s", "@", "w", "%", "p", "-", "_").Replace(action.Target.ID)
	return "__projmux_delete_queue_" + id
}

func ObserveRuntimeMutationQueueMarker(ctx context.Context, runner TmuxCommandRunner, action PlannedRuntimeMutation) (bool, error) {
	if action.Queue == nil {
		return false, errors.New("runtime mutation queue observer has no printable queue declaration")
	}
	if err := GuardRuntimeMutationQueueRoute(ctx, runner, action); err != nil {
		return false, err
	}
	out, err := runner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "show-environment", "-g")
	if err != nil {
		return false, err
	}
	return SessionEnvironmentValue(string(out), RuntimeMutationQueueMarker(action)) == action.Queue.ExpectedUID, nil
}

// observeQueuedRuntimeMutationEffect closes the only valid race in a deferred
// kill observation. The target may be present during the first inventory, then
// the background job may kill it and clear its durable queue marker before the
// marker read. A final absence observation distinguishes that completed effect
// from a queue command that neither retained its marker nor killed its target.
func ObserveQueuedRuntimeMutationEffect(
	ctx context.Context,
	observeAbsent func(context.Context) (bool, error),
	observeMarker func(context.Context) (bool, error),
) (bool, error) {
	absent, err := observeAbsent(ctx)
	if err != nil || absent {
		return absent, err
	}
	queued, markerErr := observeMarker(ctx)
	if markerErr == nil && queued {
		return true, nil
	}
	absent, absentErr := observeAbsent(ctx)
	if absentErr == nil && absent {
		return true, nil
	}
	if markerErr != nil {
		return false, markerErr
	}
	return false, absentErr
}

func ClearRuntimeMutationQueueMarker(ctx context.Context, runner TmuxCommandRunner, action PlannedRuntimeMutation) error {
	if err := GuardRuntimeMutationQueueRoute(ctx, runner, action); err != nil {
		return err
	}
	out, err := runner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "show-environment", "-g")
	if err != nil {
		return err
	}
	marker := RuntimeMutationQueueMarker(action)
	current := SessionEnvironmentValue(string(out), marker)
	if current == "" {
		return nil
	}
	if current != action.Queue.ExpectedUID {
		return fmt.Errorf("runtime mutation queue rollback refuses marker %s owned by %q", marker, current)
	}
	_, err = runner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "set-environment", "-gu", marker)
	return err
}

func GuardRuntimeMutationQueueRoute(ctx context.Context, runner TmuxCommandRunner, action PlannedRuntimeMutation) error {
	if action.Queue == nil {
		return errors.New("runtime mutation queue route has no printable declaration")
	}
	route := RuntimeMutationRoute{
		Target:             TmuxTransport{Kind: TmuxSocketPath, Value: action.Queue.PhysicalSocket, Source: TmuxSocketPathSource},
		ExpectedSocketPath: action.Queue.PhysicalSocket,
		SocketName:         action.Queue.LogicalSocket,
	}
	if action.Queue.RouteAuthority == "" {
		return errors.New("runtime mutation queue route has no server-generation authority")
	}
	authority, err := ParseRuntimeMutationRouteAuthority(action.Queue.RouteAuthority)
	if err != nil {
		return err
	}
	route.Authority = &authority
	return GuardResolvedRuntimeMutationRoute(ctx, runner, route)
}

// runtimeMutationCarriesChildCommand reports whether a verb starts a Pane and
// so may carry that Pane's launch argv. A new session's first Pane is launched
// exactly like a new Window's or split's Pane.
func RuntimeMutationCarriesChildCommand(verb RuntimeMutationVerb) bool {
	return verb == MutationCreateWindow || verb == MutationCreatePane || verb == MutationCreateSession
}

func ValidateRuntimeMutationOperandTarget(action PlannedRuntimeMutation) error {
	if len(action.Command) > 0 {
		if !RuntimeMutationCarriesChildCommand(action.Verb) {
			return fmt.Errorf("runtime mutation plan: action %q carries an unsupported child command", action.Verb)
		}
		if strings.HasPrefix(action.Command[0], "-") {
			return fmt.Errorf("runtime mutation plan: action %q child command starts with tmux control operand %q", action.Verb, action.Command[0])
		}
		for _, argument := range action.Command {
			if argument == ";" || argument == "\\;" {
				return fmt.Errorf("runtime mutation plan: action %q child command carries a tmux command separator", action.Verb)
			}
		}
	}
	values := RuntimeMutationOperandValueSlots(action)
	for index, operand := range action.Operands {
		if operand == ";" || operand == "\\;" {
			return fmt.Errorf("runtime mutation plan: action %q carries a tmux command separator", action.Verb)
		}
		if values[index] {
			continue
		}
		if action.Verb != MutationBootstrapControlSession && action.Verb != MutationWriteRouteMarker && (operand == "-L" || operand == "-S") {
			return fmt.Errorf("runtime mutation plan: action %q carries an embedded route selector", action.Verb)
		}
		for _, prefix := range []string{"-t", "-s", "-L", "-S"} {
			if strings.HasPrefix(operand, prefix) && operand != prefix {
				return fmt.Errorf("runtime mutation plan: action %q carries attached control operand %q", action.Verb, operand)
			}
		}
	}
	if action.Verb == MutationRenameWindow {
		// tmux reads a leading-dash new-name as a flag unless "--" ends options,
		// so the only executable shape is one flag-slot "--" and then exactly
		// one Window name.
		endOfOptions, at := 0, -1
		for index, operand := range action.Operands {
			if operand == "--" && !values[index] {
				endOfOptions++
				at = index
			}
		}
		if endOfOptions != 1 || at != len(action.Operands)-2 {
			return fmt.Errorf("runtime mutation plan: action %q must end options with -- before exactly one Window name", action.Verb)
		}
	}
	if action.Verb == MutationBootstrapControlSession || action.Verb == MutationWriteRouteMarker {
		flag, value, ok := strings.Cut(action.Target.Socket, "=")
		if !ok || (flag != "-L" && flag != "-S") || strings.TrimSpace(value) == "" {
			return fmt.Errorf("runtime mutation plan: action %q has no exact printable route", action.Verb)
		}
		matches := 0
		for i := 0; i+1 < len(action.Operands); i++ {
			if action.Operands[i] != "-L" && action.Operands[i] != "-S" {
				continue
			}
			matches++
			if action.Operands[i] != flag || action.Operands[i+1] != value {
				return fmt.Errorf("runtime mutation plan: action %q operand route %s=%s does not match printable route %s", action.Verb, action.Operands[i], action.Operands[i+1], action.Target.Socket)
			}
		}
		if matches != 1 {
			return fmt.Errorf("runtime mutation plan: action %q has %d executable routes, want exactly one", action.Verb, matches)
		}
		if action.Verb == MutationWriteRouteMarker {
			if len(action.Operands) < 2 || action.Operands[0] != flag || action.Operands[1] != value {
				return errors.New("runtime mutation plan: route-marker executable route must be the leading exact operand pair")
			}
			logical, ok := strings.CutPrefix(action.Target.UID, "logical:")
			if !ok || len(action.Operands) < 2 || action.Operands[len(action.Operands)-2] != RuntimeMutationSocketNameOption || action.Operands[len(action.Operands)-1] != logical {
				return errors.New("runtime mutation plan: route-marker operands do not match printable logical identity")
			}
		}
	}
	if action.Verb == MutationCreateSession || action.Verb == MutationBootstrapControlSession {
		if action.Verb == MutationCreateSession {
			configFlags := 0
			for i := 0; i < len(action.Operands); i++ {
				if values[i] || action.Operands[i] != "-f" {
					continue
				}
				configFlags++
				if i+1 >= len(action.Operands) {
					return errors.New("runtime mutation plan: create-session config flag has no path")
				}
				configPath := action.Operands[i+1]
				if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
					return errors.New("runtime mutation plan: create-session config path is not absolute and clean")
				}
				if i != 0 {
					return errors.New("runtime mutation plan: create-session config must precede the managed verb")
				}
			}
			if action.Target.PhysicalSocket == RuntimeMutationSocketAbsentBeforeCreate && configFlags != 1 {
				return fmt.Errorf("runtime mutation plan: absent-server create-session has %d generated configs, want exactly one", configFlags)
			}
			if action.Target.PhysicalSocket != RuntimeMutationSocketAbsentBeforeCreate && configFlags != 0 {
				return errors.New("runtime mutation plan: existing-server create-session unexpectedly carries a startup config")
			}
		}
		want := action.Target.ID
		if action.Verb == MutationBootstrapControlSession {
			var ok bool
			_, want, ok = strings.Cut(action.Target.ID, "/session=")
			if !ok || strings.TrimSpace(want) == "" {
				return errors.New("runtime mutation plan: control bootstrap has no stable session declaration")
			}
		}
		matches := 0
		for i := 0; i+1 < len(action.Operands); i++ {
			if values[i] || action.Operands[i] != "-s" {
				continue
			}
			matches++
			if action.Operands[i+1] != want {
				return fmt.Errorf("runtime mutation plan: action %q session operand %q does not match printable declaration %q", action.Verb, action.Operands[i+1], want)
			}
		}
		if matches != 1 {
			return fmt.Errorf("runtime mutation plan: action %q has %d executable session operands, want exactly one", action.Verb, matches)
		}
		return nil
	}
	var want, exactPrefix string
	switch action.Verb {
	case MutationCreateWindow:
		want = action.Target.ID + ":"
		exactPrefix = "$"
	case MutationCreatePane, MutationWriteLayout, MutationKillPane, MutationKillWindow, MutationStopManagedSession, MutationStopUnmanagedSession,
		MutationKillOwned, MutationTombstonePane, MutationRestorePane, MutationWriteIdentity, MutationWriteOption, MutationWritePresentationOption, MutationWriteStableName,
		MutationWriteLease, MutationClearLease, MutationRenameWindow, MutationWriteProjectAnchor, MutationFinalizeSession:
		want = action.Target.ID
		switch action.Verb {
		case MutationCreatePane, MutationWriteLayout, MutationKillPane, MutationTombstonePane, MutationRestorePane:
			exactPrefix = "%"
		case MutationKillWindow, MutationRenameWindow, MutationWriteStableName:
			exactPrefix = "@"
		case MutationStopManagedSession, MutationStopUnmanagedSession, MutationWriteLease, MutationClearLease, MutationWriteProjectAnchor, MutationFinalizeSession:
			exactPrefix = "$"
		case MutationKillOwned:
			switch action.Target.Kind {
			case "session":
				exactPrefix = "$"
			case "window":
				exactPrefix = "@"
			case "pane":
				exactPrefix = "%"
			default:
				return fmt.Errorf("runtime mutation plan: kill-owned has unknown target kind %q", action.Target.Kind)
			}
		case MutationWriteIdentity, MutationWriteOption, MutationWritePresentationOption:
			switch action.Target.Kind {
			case "session", "control-session":
				exactPrefix = "$"
			case "window":
				exactPrefix = "@"
			case "pane":
				exactPrefix = "%"
			default:
				return fmt.Errorf("runtime mutation plan: identity/option write has unknown target kind %q", action.Target.Kind)
			}
		}
	case MutationQueuePaneKill, MutationQueueWindowKill:
		if len(action.Operands) != 0 {
			return fmt.Errorf("runtime mutation plan: queued action %q carries hidden operands", action.Verb)
		}
		prefix := "%"
		if action.Verb == MutationQueueWindowKill {
			prefix = "@"
		}
		if ExactTmuxHandle(action.Target.ID, prefix) == "" {
			return fmt.Errorf("runtime mutation plan: queued action %q target %q is not an exact %s handle", action.Verb, action.Target.ID, prefix)
		}
		return nil
	default:
		return nil
	}
	if exactPrefix != "" && ExactTmuxHandle(action.Target.ID, exactPrefix) == "" {
		return fmt.Errorf("runtime mutation plan: action %q target %q is not an exact %s handle", action.Verb, action.Target.ID, exactPrefix)
	}
	matches := 0
	for i := 0; i+1 < len(action.Operands); i++ {
		if values[i] || action.Operands[i] != "-t" {
			continue
		}
		matches++
		if action.Operands[i+1] != want {
			return fmt.Errorf("runtime mutation plan: action %q operand target %q does not match printable target %q", action.Verb, action.Operands[i+1], want)
		}
	}
	if matches != 1 {
		return fmt.Errorf("runtime mutation plan: action %q has %d executable target operands, want exactly one for printable target %q", action.Verb, matches, want)
	}
	return nil
}

// runtimeMutationArgumentFlags names the flag letters that take a separate
// argument in the tmux command runtimeMutationArgv assembles for action, per
// `tmux list-commands`, checked on tmux 3.4 and 3.6 (the rows are identical):
//
//	new-window [-abdkPS] [-c] [-e] [-F] [-n] [-t] [shell-command]
//	split-window [-bdefhIPvZ] [-c] [-e] [-F] [-l] [-t] [shell-command]
//	new-session [-AdDEPX] [-c] [-e] [-F] [-f] [-n] [-s] [-t] [-x] [-y] [shell-command]
//	set-option [-aFgopqsuUw] [-t] option [value]
//	set-environment [-Fhgru] [-t] variable [value]
//	resize-pane [-DLMRTUZ] [-x] [-y] [-t] [adjustment]
//	kill-pane/kill-window [-a] [-t]; kill-session [-aC] [-t]
//	rename-window [-t] new-name
//
// The global prefix adds -f <config> (create-session, bootstrap) and -L/-S
// (bootstrap, route marker). A verb without a row has no value slots. Only
// rename-window ends its options with "--": every producer spells it
// `rename-window -t @N -- <name>`, so a Window name shaped like a flag reaches
// tmux as new-name.
func RuntimeMutationArgumentFlags(action PlannedRuntimeMutation) string {
	switch action.Verb {
	case MutationCreateWindow:
		return "ceFnt"
	case MutationCreatePane:
		return "ceFlt"
	case MutationCreateSession:
		return "ceFfnstxy"
	case MutationBootstrapControlSession:
		return "LSf" + "ceFfnstxy"
	case MutationTombstonePane, MutationRestorePane, MutationWriteIdentity, MutationWriteOption, MutationWritePresentationOption, MutationWriteStableName, MutationWriteProjectAnchor:
		return "t"
	case MutationWriteRouteMarker:
		return "LS" + "t"
	case MutationWriteLease, MutationClearLease, MutationFinalizeSession:
		return "t"
	case MutationWriteLayout:
		return "xyt"
	case MutationKillPane, MutationKillWindow, MutationStopManagedSession, MutationStopUnmanagedSession, MutationRenameWindow:
		return "t"
	case MutationKillOwned:
		switch action.Target.Kind {
		case "session", "window", "pane":
			return "t"
		}
	}
	return ""
}

// runtimeMutationOperandValueSlots reads operands left to right the way tmux
// getopt does. An exact argument-taking flag makes only the next token a
// value; any other dash token stays a flag; the first non-dash token is a
// positional and every later token is a value. "-" never ends options. "--"
// ends options only for rename-window, and only as the flag slot directly
// before the final operand, which makes that Window name a value slot. For
// every other verb, and anywhere else in rename-window, "--" stays a flag slot,
// so an unsure token is a flag, never a value.
func RuntimeMutationOperandValueSlots(action PlannedRuntimeMutation) []bool {
	argumentFlags := RuntimeMutationArgumentFlags(action)
	values := make([]bool, len(action.Operands))
	if argumentFlags == "" {
		return values
	}
	nextIsValue, positional := false, false
	for index, operand := range action.Operands {
		if positional || nextIsValue {
			values[index] = true
			nextIsValue = false
			continue
		}
		if !strings.HasPrefix(operand, "-") {
			positional = true
			continue
		}
		if operand == "--" && action.Verb == MutationRenameWindow && index == len(action.Operands)-2 {
			nextIsValue = true
			continue
		}
		nextIsValue = len(operand) == 2 && strings.IndexByte(argumentFlags, operand[1]) >= 0
	}
	return values
}

func RunRuntimeMutationCommand(ctx context.Context, runner TmuxCommandRunner, action PlannedRuntimeMutation) ([]byte, error) {
	argv, err := RuntimeMutationArgv(action)
	if err != nil {
		return nil, err
	}
	physical := strings.TrimSpace(action.Target.PhysicalSocket)
	if physical != RuntimeMutationSocketAbsentBeforeCreate {
		base := runner
		switch routed := runner.(type) {
		case ExplicitTmuxRunner:
			base = routed.Runner
		case *ExplicitTmuxRunner:
			base = routed.Runner
		}
		if len(argv) >= 2 && (argv[0] == "-L" || argv[0] == "-S") {
			argv = argv[2:]
		}
		return base.Run(ctx, "tmux", append([]string{"-S", physical}, argv...)...)
	}
	return runner.Run(ctx, "tmux", argv...)
}

type RuntimeMutationPlan struct {
	Version int                      `json:"version"`
	Actions []PlannedRuntimeMutation `json:"actions"`
}

func NewRuntimeMutation(order int, verb RuntimeMutationVerb, target RuntimeMutationTarget) PlannedRuntimeMutation {
	contract := RuntimeMutationInventory[verb]
	return PlannedRuntimeMutation{
		Order: order, Verb: verb, Target: target,
		Guard:  RuntimeMutationGuard{Kind: contract.GuardKind, Expect: "stable target=" + RuntimeMutationTargetKey(target)},
		Effect: contract.Effect,
	}
}

func BindRuntimeMutationGuard(action *PlannedRuntimeMutation, detail string) {
	if action == nil {
		return
	}
	action.Guard.Expect = "stable target=" + RuntimeMutationTargetKey(action.Target)
	if detail = strings.TrimSpace(detail); detail != "" {
		action.Guard.Expect += ";" + detail
	}
}

func NewRuntimeMutationPlan(actions ...PlannedRuntimeMutation) RuntimeMutationPlan {
	plan := RuntimeMutationPlan{Version: 1, Actions: append([]PlannedRuntimeMutation(nil), actions...)}
	slices.SortStableFunc(plan.Actions, func(a, b PlannedRuntimeMutation) int { return a.Order - b.Order })
	return plan
}

func (p RuntimeMutationPlan) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("runtime mutation plan: unsupported version %d", p.Version)
	}
	for i, action := range p.Actions {
		contract, ok := RuntimeMutationInventory[action.Verb]
		if !ok {
			return fmt.Errorf("runtime mutation plan: action %q is outside the closed inventory", action.Verb)
		}
		if action.Order != i+1 {
			return fmt.Errorf("runtime mutation plan: action %q order=%d, want total order %d", action.Verb, action.Order, i+1)
		}
		if strings.TrimSpace(action.Target.Socket) == "" || strings.TrimSpace(action.Target.PhysicalSocket) == "" || strings.TrimSpace(action.Target.Kind) == "" ||
			(strings.TrimSpace(action.Target.ID) == "" && strings.TrimSpace(action.Target.UID) == "") {
			return fmt.Errorf("runtime mutation plan: action %q has no stable target", action.Verb)
		}
		physical := strings.TrimSpace(action.Target.PhysicalSocket)
		if physical == RuntimeMutationSocketAbsentBeforeCreate {
			if action.Verb != MutationCreateSession && action.Verb != MutationBootstrapControlSession {
				return fmt.Errorf("runtime mutation plan: action %q cannot target an absent-before-create socket", action.Verb)
			}
		} else if !filepath.IsAbs(physical) || filepath.Clean(physical) != physical {
			return fmt.Errorf("runtime mutation plan: action %q physical socket %q is not an absolute clean path", action.Verb, physical)
		}
		routeFlag, routeValue, ok := strings.Cut(action.Target.Socket, "=")
		if !ok || strings.TrimSpace(routeValue) == "" {
			return fmt.Errorf("runtime mutation plan: action %q has malformed printable route %q", action.Verb, action.Target.Socket)
		}
		switch routeFlag {
		case "-L":
			if _, err := TmuxSocketNameTarget(routeValue); err != nil {
				return fmt.Errorf("runtime mutation plan: action %q has invalid logical route: %w", action.Verb, err)
			}
		case "-S":
			if !filepath.IsAbs(routeValue) || filepath.Clean(routeValue) != routeValue || physical != routeValue {
				return fmt.Errorf("runtime mutation plan: action %q physical route disagrees with printable socket identity", action.Verb)
			}
		default:
			return fmt.Errorf("runtime mutation plan: action %q has unsupported printable route %q", action.Verb, action.Target.Socket)
		}
		if printableAuthority := strings.TrimSpace(action.Target.RouteAuthority); printableAuthority != "" {
			authority, err := ParseRuntimeMutationRouteAuthority(printableAuthority)
			if err != nil {
				return fmt.Errorf("runtime mutation plan: action %q has invalid route authority: %w", action.Verb, err)
			}
			if physical == RuntimeMutationSocketAbsentBeforeCreate {
				return fmt.Errorf("runtime mutation plan: action %q authority has no physical server generation", action.Verb)
			}
			if (authority.Class == RuntimeMutationRouteStandalone || authority.Class == RuntimeMutationRouteStandaloneExplicit) && (routeFlag != "-S" || routeValue != physical) {
				return fmt.Errorf("runtime mutation plan: action %q standalone authority is not bound to its exact physical route", action.Verb)
			}
			if authority.Class == RuntimeMutationRouteStandaloneExplicit &&
				(action.Verb != MutationStopManagedSession || action.Target.Kind != "Project") &&
				((action.Verb != MutationWriteIdentity && action.Verb != MutationWriteOption && action.Verb != MutationWritePresentationOption && action.Verb != MutationRenameWindow) ||
					!strings.HasPrefix(action.Target.Parent, "controller.identity/") || action.Controller == nil) {
				return fmt.Errorf("runtime mutation plan: action %q cannot use controller-only explicit standalone authority", action.Verb)
			}
			if authority.Class == RuntimeMutationRouteApp && routeFlag != "-L" && !(routeFlag == "-S" && routeValue == physical) {
				return fmt.Errorf("runtime mutation plan: action %q app authority is not bound to its logical route", action.Verb)
			}
		} else if physical != RuntimeMutationSocketAbsentBeforeCreate {
			return fmt.Errorf("runtime mutation plan: action %q has no printable server-generation authority", action.Verb)
		}
		if action.Guard.Kind != contract.GuardKind || strings.TrimSpace(action.Guard.Expect) == "" {
			return fmt.Errorf("runtime mutation plan: action %q has a non-canonical guard", action.Verb)
		}
		if !strings.Contains(action.Guard.Expect, "stable target="+RuntimeMutationTargetKey(action.Target)) {
			return fmt.Errorf("runtime mutation plan: action %q guard is not bound to its full printable target", action.Verb)
		}
		if action.Effect != contract.Effect || strings.TrimSpace(action.Effect) == "" {
			return fmt.Errorf("runtime mutation plan: action %q has a non-canonical expected effect", action.Verb)
		}
		if err := ValidateRuntimeMutationActionShape(action); err != nil {
			return err
		}
	}
	return nil
}

func ValidateRuntimeMutationActionShape(action PlannedRuntimeMutation) error {
	requiresController := action.Verb == MutationWriteOption || action.Verb == MutationWritePresentationOption
	if requiresController && action.Controller == nil {
		return fmt.Errorf("runtime mutation plan: action %q requires a typed controller declaration", action.Verb)
	}
	if action.Controller != nil {
		if action.Verb != MutationWriteIdentity && action.Verb != MutationWriteOption &&
			action.Verb != MutationWritePresentationOption && action.Verb != MutationRenameWindow {
			return fmt.Errorf("runtime mutation plan: action %q cannot carry a controller declaration", action.Verb)
		}
		if !strings.HasPrefix(action.Target.Parent, "controller.identity/") {
			return fmt.Errorf("runtime mutation plan: controller action %q has no controller target namespace", action.Verb)
		}
		if err := ValidateControllerRuntimeMutationPlanAction(action); err != nil {
			return err
		}
	} else if strings.HasPrefix(action.Target.Parent, "controller.identity/") {
		return fmt.Errorf("runtime mutation plan: controller target %q has no typed declared effect", action.Target.ID)
	}
	if action.LeaseMarker != "" && action.Verb != MutationClearLease {
		return fmt.Errorf("runtime mutation plan: action %q cannot carry a lease marker", action.Verb)
	}
	if action.Verb == MutationWriteStableName {
		want := []string{"-w", "-t", action.Target.ID, "-q", tmuxopts.WindowName}
		if len(action.Operands) != len(want)+1 || !slices.Equal(action.Operands[:len(want)], want) || strings.TrimSpace(action.Operands[len(want)]) == "" {
			return errors.New("runtime mutation plan: Window stable-name declaration disagrees with executable argv")
		}
	}
	if action.Verb == MutationConvergeControlIdentity {
		if len(action.Operands) != 0 || len(action.Command) != 0 || action.Queue != nil {
			return errors.New("runtime mutation plan: ControlSession identity convergence carries hidden argv")
		}
		if ExactTmuxHandle(action.Target.ID, "$") == "" {
			return errors.New("runtime mutation plan: ControlSession identity convergence has no exact root session handle")
		}
		return nil
	}
	_, err := RuntimeMutationArgv(action)
	return err
}

// printableBytes is deterministic: plans contain structs (not maps), actions
// are sorted into their total order, and encoding/json emits struct fields in
// declaration order.
func (p RuntimeMutationPlan) PrintableBytes() ([]byte, error) {
	p = NewRuntimeMutationPlan(p.Actions...)
	if err := p.Validate(); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// withoutEffects is the reobserve/replan half of the seam. A successful
// observation names the expected effects now present; those rows disappear.
// Unknown observations are errors and never synthesize a destructive action.
func (p RuntimeMutationPlan) WithoutEffects(observed map[string]bool) (RuntimeMutationPlan, error) {
	if observed == nil {
		return RuntimeMutationPlan{}, errors.New("runtime mutation plan: observation is unknown")
	}
	var pending []PlannedRuntimeMutation
	for _, action := range p.Actions {
		key := RuntimeMutationEffectKey(action)
		if !observed[key] {
			action.Order = len(pending) + 1
			pending = append(pending, action)
		}
	}
	return NewRuntimeMutationPlan(pending...), nil
}

func RuntimeMutationEffectKey(action PlannedRuntimeMutation) string {
	controller := ""
	if action.Controller != nil {
		controller = action.Controller.Class + "\x1f" + action.Controller.Mode + "\x1f" + action.Controller.Scope + "\x1f" + action.Controller.Field + "\x1f" + action.Controller.Before + "\x1f" + action.Controller.After
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s", action.Verb, action.Target.Socket, action.Target.PhysicalSocket,
		action.Target.RouteAuthority, action.Target.Kind, action.Target.ID, action.Target.UID, action.Target.Parent, strings.Join(action.Operands, "\x1f"), strings.Join(action.Command, "\x1f"), controller)
}

func RuntimeMutationTargetKey(target RuntimeMutationTarget) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", target.Socket, target.PhysicalSocket, target.RouteAuthority, target.Kind, target.ID, target.UID, target.Parent)
}

type RuntimeMutationStep struct {
	Action PlannedRuntimeMutation
	// TargetRouteGuard binds the printable Socket/PhysicalSocket/RouteAuthority
	// tuple to the captured live server before pre-effect reobservation can
	// declare a repeat empty. It is distinct from the semantic Guard because
	// an already-satisfied effect may legitimately no longer have its pre-write
	// state, while it must still be observed on the same server generation.
	TargetRouteGuard func(context.Context) error
	Guard            func(context.Context) error
	Apply            func(context.Context) error
	// Reobserve reports whether this exact action's expected effect is present.
	// It runs once before guards (repeat-empty/replan) and once after execution.
	// An observation error is unknown authority and therefore permits no write.
	Reobserve func(context.Context) (bool, error)
	// Undo is present only when this operation owns the thing Apply produced.
	// The executor calls it in reverse apply order after a partial failure.
	Undo func(context.Context) error
}

// errRuntimeMutationEffectConverged is returned (possibly wrapped) by a step's
// semantic Guard when, on the same fresh read, the step's own write field
// already holds its planned value and every other condition the Guard checks
// still holds. A concurrent writer converged the target between pre-effect
// reobservation and the guard pass. For an absence-effect step (a kill, stop,
// or lease clear) the expected effect is the exact target's absence, so its
// Guard returns the sentinel only when the same absence predicate its
// Reobserve uses proves that exact target gone; any other drift stays a
// refusal. The executor drops such a step like a repeat-empty one. Only a
// Guard that opts in by returning this sentinel changes behaviour; the
// executor never decides convergence itself.
var ErrRuntimeMutationEffectConverged = errors.New("runtime mutation plan: expected effect already present at guard")

// runtimeMutationAbsenceConverged reclassifies an absence-effect Guard refusal
// as converged only when a fresh observeAbsent, the step's own absence
// predicate, proves the exact target gone. A nil guardErr, an observation
// error, or a still-present target returns guardErr unchanged, so every
// non-absence drift stays refused.
func RuntimeMutationAbsenceConverged(ctx context.Context, guardErr error, observeAbsent func(context.Context) (bool, error)) error {
	if guardErr == nil || observeAbsent == nil {
		return guardErr
	}
	absent, err := observeAbsent(ctx)
	if err != nil || !absent {
		return guardErr
	}
	return fmt.Errorf("exact target proven absent after guard refusal (%v): %w", guardErr, ErrRuntimeMutationEffectConverged)
}

// replanRuntimeMutationSteps renumbers the surviving steps of a replan and
// re-validates their printable plan. An empty replan needs no validation.
func ReplanRuntimeMutationSteps(steps []RuntimeMutationStep) ([]RuntimeMutationStep, error) {
	if len(steps) == 0 {
		return steps, nil
	}
	actions := make([]PlannedRuntimeMutation, 0, len(steps))
	for i := range steps {
		steps[i].Action.Order = i + 1
		actions = append(actions, steps[i].Action)
	}
	if _, err := NewRuntimeMutationPlan(actions...).PrintableBytes(); err != nil {
		return nil, err
	}
	return steps, nil
}

// executeRuntimeMutationPlan is the sole
// plan -> target/route guard -> reobserve/replan -> semantic guard -> execute
// -> reobserve/replan boundary.
// It validates every printable action, refuses unknown pre-write observation,
// runs all guards before the first write, and returns only after every expected
// effect replans to empty. A partial failure unwinds only steps with explicit
// ownership-backed Undo functions, in reverse order.
func ExecuteRuntimeMutationPlan(ctx context.Context, steps []RuntimeMutationStep) error {
	steps = slices.Clone(steps)
	slices.SortStableFunc(steps, func(a, b RuntimeMutationStep) int { return a.Action.Order - b.Action.Order })
	actions := make([]PlannedRuntimeMutation, 0, len(steps))
	for _, step := range steps {
		actions = append(actions, step.Action)
	}
	plan := NewRuntimeMutationPlan(actions...)
	// The bytes validated here are the same value a dry-run or diagnostic
	// consumer prints. Execution cannot accept a shape the printable contract
	// rejects.
	if _, err := plan.PrintableBytes(); err != nil {
		return err
	}
	pending := make([]RuntimeMutationStep, 0, len(steps))
	for i := range steps {
		if steps[i].Action.Target.PhysicalSocket != RuntimeMutationSocketAbsentBeforeCreate {
			if steps[i].TargetRouteGuard == nil {
				return fmt.Errorf("runtime mutation plan: action %q has no printable target/route guard", steps[i].Action.Verb)
			}
			if err := steps[i].TargetRouteGuard(ctx); err != nil {
				return fmt.Errorf("runtime mutation plan: target/route authority refused action %q before effect observation: %w", steps[i].Action.Verb, err)
			}
		} else if steps[i].TargetRouteGuard != nil {
			if err := steps[i].TargetRouteGuard(ctx); err != nil {
				return fmt.Errorf("runtime mutation plan: absence authority refused action %q before effect observation: %w", steps[i].Action.Verb, err)
			}
		}
		if steps[i].Reobserve == nil {
			return fmt.Errorf("runtime mutation plan: action %q has no effect observer", steps[i].Action.Verb)
		}
		observed, err := steps[i].Reobserve(ctx)
		if err != nil {
			return fmt.Errorf("runtime mutation plan: observation unknown before first write for action %q: %w", steps[i].Action.Verb, err)
		}
		if observed {
			continue
		}
		pending = append(pending, steps[i])
	}
	steps, err := ReplanRuntimeMutationSteps(pending)
	if err != nil || len(steps) == 0 {
		return err
	}
	guarded := make([]RuntimeMutationStep, 0, len(steps))
	for i := range steps {
		if steps[i].Guard == nil {
			return fmt.Errorf("runtime mutation plan: action %q has no executable guard", steps[i].Action.Verb)
		}
		if err := steps[i].Guard(ctx); err != nil {
			if errors.Is(err, ErrRuntimeMutationEffectConverged) {
				// Drop, never re-write: the step is not applied, so it is not in
				// applied and no rollback can revert the value another writer
				// put there. Undo cannot tell its own write from a concurrent
				// same-value write.
				continue
			}
			return fmt.Errorf("runtime mutation plan: guard refused action %q before first write: %w", steps[i].Action.Verb, err)
		}
		guarded = append(guarded, steps[i])
	}
	if len(guarded) != len(steps) {
		if steps, err = ReplanRuntimeMutationSteps(guarded); err != nil || len(steps) == 0 {
			return err
		}
	}
	var applied []RuntimeMutationStep
	for i := range steps {
		if steps[i].Apply == nil {
			return fmt.Errorf("runtime mutation plan: action %q has no executor", steps[i].Action.Verb)
		}
		if err := steps[i].Apply(ctx); err != nil {
			var rollbackErrors []error
			// tmux can report failure after applying a mutation. When the current
			// step owns an idempotent, guarded Undo, run it before unwinding the
			// earlier successful steps so an error-after-effect cannot escape.
			if steps[i].Undo != nil {
				if undoErr := steps[i].Undo(ctx); undoErr != nil {
					rollbackErrors = append(rollbackErrors, undoErr)
				}
			}
			for j := len(applied) - 1; j >= 0; j-- {
				if applied[j].Undo != nil {
					if undoErr := applied[j].Undo(ctx); undoErr != nil {
						rollbackErrors = append(rollbackErrors, undoErr)
					}
				}
			}
			if len(rollbackErrors) > 0 {
				return errors.Join(err, fmt.Errorf("runtime mutation plan: owned reverse rollback incomplete: %w", errors.Join(rollbackErrors...)))
			}
			return err
		}
		applied = append(applied, steps[i])
	}
	var residual []PlannedRuntimeMutation
	var observeErrors []error
	for i := range steps {
		observed, err := steps[i].Reobserve(ctx)
		if err != nil || !observed {
			action := steps[i].Action
			action.Order = len(residual) + 1
			residual = append(residual, action)
			if err != nil {
				observeErrors = append(observeErrors, fmt.Errorf("action %q: %w", action.Verb, err))
			}
		}
	}
	if len(residual) == 0 {
		return nil
	}
	var rollbackErrors []error
	for i := len(applied) - 1; i >= 0; i-- {
		if applied[i].Undo != nil {
			if err := applied[i].Undo(ctx); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
	}
	residualBytes, _ := NewRuntimeMutationPlan(residual...).PrintableBytes()
	resultErr := fmt.Errorf("runtime mutation plan: expected effects were not fully observed; residual plan:\n%s", residualBytes)
	if len(observeErrors) > 0 {
		resultErr = errors.Join(resultErr, errors.Join(observeErrors...))
	}
	if len(rollbackErrors) > 0 {
		resultErr = errors.Join(resultErr, fmt.Errorf("runtime mutation plan: owned reverse rollback incomplete: %w", errors.Join(rollbackErrors...)))
	}
	return resultErr
}
