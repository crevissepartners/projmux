package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runtimeMutationVerb is the closed lifecycle/topology mutation vocabulary.
// Keep this list deliberately transport-shaped: adding a verb requires adding
// its guard and expected-effect contract to runtimeMutationInventory.
type runtimeMutationVerb string

const (
	mutationCreateSession           runtimeMutationVerb = "create-session"
	mutationFinalizeSession         runtimeMutationVerb = "finalize-session-startup"
	mutationCreateWindow            runtimeMutationVerb = "create-window"
	mutationCreatePane              runtimeMutationVerb = "create-pane"
	mutationWriteIdentity           runtimeMutationVerb = "write-identity"
	mutationWriteLease              runtimeMutationVerb = "write-lease"
	mutationClearLease              runtimeMutationVerb = "clear-lease"
	mutationWriteLayout             runtimeMutationVerb = "write-layout"
	mutationKillOwned               runtimeMutationVerb = "kill-owned"
	mutationKillPane                runtimeMutationVerb = "kill-pane"
	mutationTombstonePane           runtimeMutationVerb = "tombstone-pane"
	mutationRestorePane             runtimeMutationVerb = "restore-pane"
	mutationQueuePaneKill           runtimeMutationVerb = "queue-pane-kill"
	mutationKillWindow              runtimeMutationVerb = "kill-window"
	mutationQueueWindowKill         runtimeMutationVerb = "queue-window-kill"
	mutationStopManagedSession      runtimeMutationVerb = "stop-managed-session"
	mutationStopUnmanagedSession    runtimeMutationVerb = "stop-unmanaged-session"
	mutationBootstrapControlSession runtimeMutationVerb = "bootstrap-control-session"
	mutationRenameWindow            runtimeMutationVerb = "rename-window"
	mutationWriteRouteMarker        runtimeMutationVerb = "write-route-marker"
	mutationWriteProjectAnchor      runtimeMutationVerb = "write-project-anchor"
	mutationConvergeControlIdentity runtimeMutationVerb = "converge-control-identity"
)

type runtimeMutationContract struct {
	GuardKind string
	Effect    string
}

// runtimeMutationGuard is the printable evidence contract for one action.
// Kind closes the vocabulary while Expect binds that kind to the exact fact
// observed for this target when the plan was built.
type runtimeMutationGuard struct {
	Kind   string `json:"kind"`
	Expect string `json:"expect"`
}

// runtimeMutationInventory is the reviewable, printable closure proof. The
// executor rejects every action absent from this table before running guards.
var runtimeMutationInventory = map[runtimeMutationVerb]runtimeMutationContract{
	mutationCreateSession:           {GuardKind: "session-name-and-project-identity", Effect: "one detached owned session exists"},
	mutationFinalizeSession:         {GuardKind: "create-operation-lease", Effect: "startup hooks are finalized once"},
	mutationCreateWindow:            {GuardKind: "session-containment-and-owner-inventory", Effect: "one detached owned window and primary pane exist"},
	mutationCreatePane:              {GuardKind: "anchor-containment-and-pane-inventory", Effect: "one detached owned pane exists"},
	mutationWriteIdentity:           {GuardKind: "exact-handle-owner-chain", Effect: "exact uid identity mirror equals desired value"},
	mutationWriteLease:              {GuardKind: "exact-owned-session", Effect: "create-operation lease equals marker"},
	mutationClearLease:              {GuardKind: "create-operation-lease", Effect: "create-operation lease is absent"},
	mutationWriteLayout:             {GuardKind: "exact-anchor-containment", Effect: "best-effort even split layout is selected"},
	mutationKillOwned:               {GuardKind: "operation-owned-uid", Effect: "only the owned created object is absent"},
	mutationKillPane:                {GuardKind: "exact-pane-evidence", Effect: "exact live pane is absent"},
	mutationTombstonePane:           {GuardKind: "exact-pane-evidence", Effect: "pane uid mirror carries the delete tombstone"},
	mutationRestorePane:             {GuardKind: "exact-pane-evidence", Effect: "pane uid mirror is restored"},
	mutationQueuePaneKill:           {GuardKind: "exact-pane-evidence", Effect: "exact pane kill is queued after result flush"},
	mutationKillWindow:              {GuardKind: "exact-window-evidence", Effect: "exact live window is absent"},
	mutationQueueWindowKill:         {GuardKind: "exact-window-evidence", Effect: "exact window kill is queued after result flush"},
	mutationStopManagedSession:      {GuardKind: "exact-managed-root-session", Effect: "exact managed root session is absent"},
	mutationStopUnmanagedSession:    {GuardKind: "exact-unmanaged-session", Effect: "exact unowned or ephemeral session is absent"},
	mutationBootstrapControlSession: {GuardKind: "exact-control-session-absence", Effect: "one detached app-owned control session exists"},
	mutationRenameWindow:            {GuardKind: "exact-window-evidence", Effect: "exact owned window name equals desired value"},
	mutationWriteRouteMarker:        {GuardKind: "exact-app-owned-session", Effect: "server logical route marker equals invocation route"},
	mutationWriteProjectAnchor:      {GuardKind: "create-operation-lease", Effect: "exact created session Project path anchor equals Registry root"},
	mutationConvergeControlIdentity: {GuardKind: "exact-control-session-containment", Effect: "control root UID and exact Window/Pane mirrors equal desired identity"},
}

// runtimeMutationTarget is stable across formatting and independent of tmux
// indexes. ID is an exact tmux handle when one exists; UID is the desired or
// observed Projmux identity. Parent pins containment for pane/window actions.
type runtimeMutationTarget struct {
	Socket         string `json:"socket"`
	PhysicalSocket string `json:"physicalSocket"`
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	UID            string `json:"uid"`
	Parent         string `json:"parent"`
}

const runtimeMutationSocketAbsentBeforeCreate = "absent-before-create"

func printableRuntimeMutationSocket(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return runtimeMutationSocketAbsentBeforeCreate
	}
	return path
}

type plannedRuntimeMutation struct {
	Order  int                   `json:"order"`
	Verb   runtimeMutationVerb   `json:"action"`
	Target runtimeMutationTarget `json:"target"`
	Guard  runtimeMutationGuard  `json:"guard"`
	Effect string                `json:"expectedEffect"`
	// Operands are printable typed step data. The managed tmux verb itself is
	// selected only by runtimeMutationArgv, which also binds any executable -t
	// operand back to Target.ID before returning argv.
	Operands []string                   `json:"operands,omitempty"`
	Command  []string                   `json:"command,omitempty"`
	Queue    *runtimeMutationQueuedKill `json:"queue,omitempty"`
}

type runtimeMutationQueuedKill struct {
	PhysicalSocket string `json:"physicalSocket"`
	LogicalSocket  string `json:"logicalSocket"`
	ExpectedUID    string `json:"expectedUid"`
	SessionID      string `json:"sessionId"`
	WindowID       string `json:"windowId"`
	Marker         string `json:"marker"`
}

// runtimeMutationArgv is the sole assembler for managed lifecycle/topology
// tmux verbs in this slice. Producers choose a closed typed intent and provide
// only its operands; they cannot smuggle a second lifecycle verb through the
// printable plan.
func runtimeMutationArgv(action plannedRuntimeMutation) ([]string, error) {
	if err := validateRuntimeMutationOperandTarget(action); err != nil {
		return nil, err
	}
	for _, operand := range action.Operands {
		if closedTmuxTopologyMutationVerbs[operand] {
			return nil, fmt.Errorf("runtime mutation plan: action %q carries hidden managed verb operand %q", action.Verb, operand)
		}
	}
	if len(action.Command) > 0 && action.Verb != mutationCreateWindow && action.Verb != mutationCreatePane {
		return nil, fmt.Errorf("runtime mutation plan: action %q carries an unexpected child command", action.Verb)
	}
	var verb string
	switch action.Verb {
	case mutationCreateSession:
		verb = "new-session"
	case mutationCreateWindow:
		verb = "new-window"
	case mutationCreatePane:
		verb = "split-window"
	case mutationFinalizeSession:
		verb = "set-environment"
	case mutationWriteLayout:
		verb = "resize-pane"
	case mutationKillPane:
		verb = "kill-pane"
	case mutationKillWindow:
		verb = "kill-window"
	case mutationStopManagedSession, mutationStopUnmanagedSession:
		verb = "kill-session"
	case mutationKillOwned:
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
	case mutationTombstonePane, mutationRestorePane, mutationWriteIdentity, mutationWriteProjectAnchor:
		verb = "set-option"
	case mutationQueuePaneKill, mutationQueueWindowKill:
		if action.Queue == nil || !filepath.IsAbs(action.Queue.PhysicalSocket) || filepath.Clean(action.Queue.PhysicalSocket) != action.Queue.PhysicalSocket ||
			action.Queue.PhysicalSocket != action.Target.PhysicalSocket || strings.TrimSpace(action.Queue.LogicalSocket) == "" ||
			strings.TrimSpace(action.Queue.ExpectedUID) == "" || action.Queue.ExpectedUID != action.Target.UID ||
			strings.TrimSpace(action.Target.ID) == "" || exactTmuxHandle(action.Queue.SessionID, "$") == "" {
			return nil, errors.New("runtime mutation plan: queued kill route or exact target is incomplete")
		}
		if action.Queue.Marker == "" || action.Queue.Marker != runtimeMutationQueueMarker(action) {
			return nil, errors.New("runtime mutation plan: queued kill durable marker disagrees with printable target")
		}
		logical, ok := strings.CutPrefix(action.Target.Socket, "-L=")
		if !ok || logical != action.Queue.LogicalSocket {
			return nil, errors.New("runtime mutation plan: queued kill logical route disagrees with printable target")
		}
		if _, err := tmuxSocketNameTarget(action.Queue.LogicalSocket); err != nil {
			return nil, errors.New("runtime mutation plan: queued kill logical route is invalid")
		}
		killVerb := "kill-pane"
		uidOption := tmuxopts.PaneUID
		identity := "#{&&:#{==:#{session_id}," + action.Queue.SessionID + "},#{&&:#{==:#{window_id}," + action.Queue.WindowID + "},#{==:#{pane_id}," + action.Target.ID + "}}}"
		if action.Verb == mutationQueueWindowKill {
			killVerb = "kill-window"
			uidOption = tmuxopts.WindowUID
			identity = "#{&&:#{==:#{session_id}," + action.Queue.SessionID + "},#{==:#{window_id}," + action.Target.ID + "}}"
		} else if exactTmuxHandle(action.Queue.WindowID, "@") == "" {
			return nil, errors.New("runtime mutation plan: queued Pane kill has no exact Window containment")
		}
		if !strings.HasPrefix(action.Target.Parent, action.Queue.SessionID+"/") ||
			(action.Verb == mutationQueuePaneKill && !strings.HasPrefix(action.Target.Parent, action.Queue.SessionID+"/"+action.Queue.WindowID+"/")) {
			return nil, errors.New("runtime mutation plan: queued kill containment disagrees with printable target")
		}
		routeCondition := "#{&&:#{==:#{" + tmuxopts.AppGlobal + "},1},#{==:#{" + runtimeMutationSocketNameOption + "}," + action.Queue.LogicalSocket + "}}"
		leaseCondition := "#{==:#{E:" + action.Queue.Marker + "}," + action.Queue.ExpectedUID + "}"
		condition := "#{&&:" + routeCondition + ",#{&&:" + leaseCondition + ",#{&&:" + identity + ",#{==:#{" + uidOption + "}," + action.Queue.ExpectedUID + "}}}}"
		clearCondition := "#{&&:" + routeCondition + "," + leaseCondition + "}"
		deferredKill := killVerb + " -t " + action.Target.ID
		deferredClear := "set-environment -gu " + action.Queue.Marker
		command := shellQuote("tmux") + " -S " + shellQuote(action.Queue.PhysicalSocket) +
			" if-shell -F -t " + shellQuote(action.Target.ID) + " " + shellQuote(condition) + " " + shellQuote(deferredKill) + " ''; " +
			"status=$?; " + shellQuote("tmux") + " -S " + shellQuote(action.Queue.PhysicalSocket) +
			" if-shell -F " + shellQuote(clearCondition) + " " + shellQuote(deferredClear) + " '' >/dev/null 2>&1 || true; exit $status"
		return []string{"set-environment", "-g", action.Queue.Marker, action.Queue.ExpectedUID, ";", "run-shell", "-b", command}, nil
	case mutationWriteLease, mutationClearLease:
		verb = "set-environment"
	case mutationRenameWindow:
		verb = "rename-window"
	case mutationBootstrapControlSession:
		// Bootstrap needs global route/config flags before the managed verb.
		if len(action.Operands) < 4 {
			return nil, errors.New("runtime mutation plan: control bootstrap operands are incomplete")
		}
		return append(append([]string(nil), action.Operands[:4]...), append([]string{"new-session"}, action.Operands[4:]...)...), nil
	case mutationWriteRouteMarker:
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

func runtimeMutationQueueMarker(action plannedRuntimeMutation) string {
	id := strings.NewReplacer("$", "s", "@", "w", "%", "p", "-", "_").Replace(action.Target.ID)
	return "__projmux_delete_queue_" + id
}

func observeRuntimeMutationQueueMarker(ctx context.Context, runner tmuxCommandRunner, action plannedRuntimeMutation) (bool, error) {
	if action.Queue == nil {
		return false, errors.New("runtime mutation queue observer has no printable queue declaration")
	}
	if err := guardRuntimeMutationQueueRoute(ctx, runner, action); err != nil {
		return false, err
	}
	out, err := runner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "show-environment", "-g")
	if err != nil {
		return false, err
	}
	return sessionEnvironmentValue(string(out), runtimeMutationQueueMarker(action)) == action.Queue.ExpectedUID, nil
}

func clearRuntimeMutationQueueMarker(ctx context.Context, runner tmuxCommandRunner, action plannedRuntimeMutation) error {
	if err := guardRuntimeMutationQueueRoute(ctx, runner, action); err != nil {
		return err
	}
	out, err := runner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "show-environment", "-g")
	if err != nil {
		return err
	}
	marker := runtimeMutationQueueMarker(action)
	current := sessionEnvironmentValue(string(out), marker)
	if current == "" {
		return nil
	}
	if current != action.Queue.ExpectedUID {
		return fmt.Errorf("runtime mutation queue rollback refuses marker %s owned by %q", marker, current)
	}
	_, err = runner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "set-environment", "-gu", marker)
	return err
}

func guardRuntimeMutationQueueRoute(ctx context.Context, runner tmuxCommandRunner, action plannedRuntimeMutation) error {
	if action.Queue == nil {
		return errors.New("runtime mutation queue route has no printable declaration")
	}
	route := runtimeMutationRoute{
		target:             explicitTmuxTarget{flag: "-S", value: action.Queue.PhysicalSocket},
		expectedSocketPath: action.Queue.PhysicalSocket,
		socketName:         action.Queue.LogicalSocket,
	}
	return guardResolvedRuntimeMutationRoute(ctx, runner, route)
}

func validateRuntimeMutationOperandTarget(action plannedRuntimeMutation) error {
	if len(action.Command) > 0 {
		if action.Verb != mutationCreateWindow && action.Verb != mutationCreatePane {
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
	for index, operand := range action.Operands {
		if operand == ";" || operand == "\\;" {
			return fmt.Errorf("runtime mutation plan: action %q carries a tmux command separator", action.Verb)
		}
		if action.Verb != mutationBootstrapControlSession && action.Verb != mutationWriteRouteMarker && (operand == "-L" || operand == "-S") {
			return fmt.Errorf("runtime mutation plan: action %q carries an embedded route selector", action.Verb)
		}
		if runtimeMutationOperandIsValue(action.Operands, index) {
			continue
		}
		for _, prefix := range []string{"-t", "-s", "-L", "-S"} {
			if strings.HasPrefix(operand, prefix) && operand != prefix {
				return fmt.Errorf("runtime mutation plan: action %q carries attached control operand %q", action.Verb, operand)
			}
		}
	}
	if action.Verb == mutationBootstrapControlSession || action.Verb == mutationWriteRouteMarker {
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
		if action.Verb == mutationWriteRouteMarker {
			logical, ok := strings.CutPrefix(action.Target.UID, "logical:")
			if !ok || logical != value || len(action.Operands) < 2 || action.Operands[len(action.Operands)-2] != runtimeMutationSocketNameOption || action.Operands[len(action.Operands)-1] != logical {
				return errors.New("runtime mutation plan: route-marker operands do not match printable logical identity")
			}
		}
	}
	if action.Verb == mutationCreateSession || action.Verb == mutationBootstrapControlSession {
		want := action.Target.ID
		if action.Verb == mutationBootstrapControlSession {
			var ok bool
			_, want, ok = strings.Cut(action.Target.ID, "/session=")
			if !ok || strings.TrimSpace(want) == "" {
				return errors.New("runtime mutation plan: control bootstrap has no stable session declaration")
			}
		}
		matches := 0
		for i := 0; i+1 < len(action.Operands); i++ {
			if action.Operands[i] != "-s" {
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
	case mutationCreateWindow:
		want = action.Target.ID + ":"
		exactPrefix = "$"
	case mutationCreatePane, mutationWriteLayout, mutationKillPane, mutationKillWindow, mutationStopManagedSession, mutationStopUnmanagedSession,
		mutationKillOwned, mutationTombstonePane, mutationRestorePane, mutationWriteIdentity,
		mutationWriteLease, mutationClearLease, mutationRenameWindow, mutationWriteProjectAnchor, mutationFinalizeSession:
		want = action.Target.ID
		switch action.Verb {
		case mutationCreatePane, mutationWriteLayout, mutationKillPane, mutationTombstonePane, mutationRestorePane:
			exactPrefix = "%"
		case mutationKillWindow, mutationRenameWindow:
			exactPrefix = "@"
		case mutationStopManagedSession, mutationStopUnmanagedSession, mutationWriteLease, mutationClearLease, mutationWriteProjectAnchor, mutationFinalizeSession:
			exactPrefix = "$"
		case mutationKillOwned:
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
		case mutationWriteIdentity:
			switch action.Target.Kind {
			case "session", "control-session":
				exactPrefix = "$"
			case "window":
				exactPrefix = "@"
			case "pane":
				exactPrefix = "%"
			default:
				return fmt.Errorf("runtime mutation plan: identity write has unknown target kind %q", action.Target.Kind)
			}
		}
	case mutationQueuePaneKill, mutationQueueWindowKill:
		if len(action.Operands) != 0 {
			return fmt.Errorf("runtime mutation plan: queued action %q carries hidden operands", action.Verb)
		}
		prefix := "%"
		if action.Verb == mutationQueueWindowKill {
			prefix = "@"
		}
		if exactTmuxHandle(action.Target.ID, prefix) == "" {
			return fmt.Errorf("runtime mutation plan: queued action %q target %q is not an exact %s handle", action.Verb, action.Target.ID, prefix)
		}
		return nil
	default:
		return nil
	}
	if exactPrefix != "" && exactTmuxHandle(action.Target.ID, exactPrefix) == "" {
		return fmt.Errorf("runtime mutation plan: action %q target %q is not an exact %s handle", action.Verb, action.Target.ID, exactPrefix)
	}
	matches := 0
	for i := 0; i+1 < len(action.Operands); i++ {
		if action.Operands[i] != "-t" {
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

func runtimeMutationOperandIsValue(operands []string, index int) bool {
	if index <= 0 {
		return false
	}
	switch operands[index-1] {
	case "-t", "-s", "-L", "-S", "-f", "-F", "-c", "-e", "-n", "-x", "-y":
		return true
	default:
		return false
	}
}

func runRuntimeMutationCommand(ctx context.Context, runner tmuxCommandRunner, action plannedRuntimeMutation) ([]byte, error) {
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		return nil, err
	}
	physical := strings.TrimSpace(action.Target.PhysicalSocket)
	if physical != runtimeMutationSocketAbsentBeforeCreate {
		base := runner
		switch routed := runner.(type) {
		case explicitTmuxRunner:
			base = routed.runner
		case *explicitTmuxRunner:
			base = routed.runner
		}
		if len(argv) >= 2 && (argv[0] == "-L" || argv[0] == "-S") {
			argv = argv[2:]
		}
		return base.Run(ctx, "tmux", append([]string{"-S", physical}, argv...)...)
	}
	return runner.Run(ctx, "tmux", argv...)
}

type runtimeMutationPlan struct {
	Version int                      `json:"version"`
	Actions []plannedRuntimeMutation `json:"actions"`
}

func newRuntimeMutation(order int, verb runtimeMutationVerb, target runtimeMutationTarget) plannedRuntimeMutation {
	contract := runtimeMutationInventory[verb]
	return plannedRuntimeMutation{
		Order: order, Verb: verb, Target: target,
		Guard:  runtimeMutationGuard{Kind: contract.GuardKind, Expect: "stable target=" + runtimeMutationTargetKey(target)},
		Effect: contract.Effect,
	}
}

func bindRuntimeMutationGuard(action *plannedRuntimeMutation, detail string) {
	if action == nil {
		return
	}
	action.Guard.Expect = "stable target=" + runtimeMutationTargetKey(action.Target)
	if detail = strings.TrimSpace(detail); detail != "" {
		action.Guard.Expect += ";" + detail
	}
}

func newRuntimeMutationPlan(actions ...plannedRuntimeMutation) runtimeMutationPlan {
	plan := runtimeMutationPlan{Version: 1, Actions: append([]plannedRuntimeMutation(nil), actions...)}
	slices.SortStableFunc(plan.Actions, func(a, b plannedRuntimeMutation) int { return a.Order - b.Order })
	return plan
}

func (p runtimeMutationPlan) validate() error {
	if p.Version != 1 {
		return fmt.Errorf("runtime mutation plan: unsupported version %d", p.Version)
	}
	for i, action := range p.Actions {
		contract, ok := runtimeMutationInventory[action.Verb]
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
		if physical == runtimeMutationSocketAbsentBeforeCreate {
			if action.Verb != mutationCreateSession && action.Verb != mutationBootstrapControlSession {
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
			if _, err := tmuxSocketNameTarget(routeValue); err != nil {
				return fmt.Errorf("runtime mutation plan: action %q has invalid logical route: %w", action.Verb, err)
			}
		case "-S":
			if !filepath.IsAbs(routeValue) || filepath.Clean(routeValue) != routeValue || physical != routeValue {
				return fmt.Errorf("runtime mutation plan: action %q physical route disagrees with printable socket identity", action.Verb)
			}
		default:
			return fmt.Errorf("runtime mutation plan: action %q has unsupported printable route %q", action.Verb, action.Target.Socket)
		}
		if action.Guard.Kind != contract.GuardKind || strings.TrimSpace(action.Guard.Expect) == "" {
			return fmt.Errorf("runtime mutation plan: action %q has a non-canonical guard", action.Verb)
		}
		if !strings.Contains(action.Guard.Expect, "stable target="+runtimeMutationTargetKey(action.Target)) {
			return fmt.Errorf("runtime mutation plan: action %q guard is not bound to its full printable target", action.Verb)
		}
		if action.Effect != contract.Effect || strings.TrimSpace(action.Effect) == "" {
			return fmt.Errorf("runtime mutation plan: action %q has a non-canonical expected effect", action.Verb)
		}
		if err := validateRuntimeMutationActionShape(action); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeMutationActionShape(action plannedRuntimeMutation) error {
	if action.Verb == mutationConvergeControlIdentity {
		if len(action.Operands) != 0 || len(action.Command) != 0 || action.Queue != nil {
			return errors.New("runtime mutation plan: ControlSession identity convergence carries hidden argv")
		}
		if exactTmuxHandle(action.Target.ID, "$") == "" {
			return errors.New("runtime mutation plan: ControlSession identity convergence has no exact root session handle")
		}
		return nil
	}
	_, err := runtimeMutationArgv(action)
	return err
}

// printableBytes is deterministic: plans contain structs (not maps), actions
// are sorted into their total order, and encoding/json emits struct fields in
// declaration order.
func (p runtimeMutationPlan) printableBytes() ([]byte, error) {
	p = newRuntimeMutationPlan(p.Actions...)
	if err := p.validate(); err != nil {
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
func (p runtimeMutationPlan) withoutEffects(observed map[string]bool) (runtimeMutationPlan, error) {
	if observed == nil {
		return runtimeMutationPlan{}, errors.New("runtime mutation plan: observation is unknown")
	}
	var pending []plannedRuntimeMutation
	for _, action := range p.Actions {
		key := runtimeMutationEffectKey(action)
		if !observed[key] {
			action.Order = len(pending) + 1
			pending = append(pending, action)
		}
	}
	return newRuntimeMutationPlan(pending...), nil
}

func runtimeMutationEffectKey(action plannedRuntimeMutation) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s", action.Verb, action.Target.Socket, action.Target.PhysicalSocket,
		action.Target.Kind, action.Target.ID, action.Target.UID, action.Target.Parent, strings.Join(action.Operands, "\x1f"), strings.Join(action.Command, "\x1f"))
}

func runtimeMutationTargetKey(target runtimeMutationTarget) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", target.Socket, target.PhysicalSocket, target.Kind, target.ID, target.UID, target.Parent)
}

type runtimeMutationStep struct {
	Action plannedRuntimeMutation
	Guard  func(context.Context) error
	Apply  func(context.Context) error
	// Reobserve reports whether this exact action's expected effect is present.
	// It runs once before guards (repeat-empty/replan) and once after execution.
	// An observation error is unknown authority and therefore permits no write.
	Reobserve func(context.Context) (bool, error)
	// Undo is present only when this operation owns the thing Apply produced.
	// The executor calls it in reverse apply order after a partial failure.
	Undo func(context.Context) error
}

// executeRuntimeMutationPlan is the sole
// plan -> reobserve/replan -> guard -> execute -> reobserve/replan boundary.
// It validates every printable action, refuses unknown pre-write observation,
// runs all guards before the first write, and returns only after every expected
// effect replans to empty. A partial failure unwinds only steps with explicit
// ownership-backed Undo functions, in reverse order.
func executeRuntimeMutationPlan(ctx context.Context, steps []runtimeMutationStep) error {
	steps = slices.Clone(steps)
	slices.SortStableFunc(steps, func(a, b runtimeMutationStep) int { return a.Action.Order - b.Action.Order })
	actions := make([]plannedRuntimeMutation, 0, len(steps))
	for _, step := range steps {
		actions = append(actions, step.Action)
	}
	plan := newRuntimeMutationPlan(actions...)
	// The bytes validated here are the same value a dry-run or diagnostic
	// consumer prints. Execution cannot accept a shape the printable contract
	// rejects.
	if _, err := plan.printableBytes(); err != nil {
		return err
	}
	pending := make([]runtimeMutationStep, 0, len(steps))
	for i := range steps {
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
		step := steps[i]
		step.Action.Order = len(pending) + 1
		pending = append(pending, step)
	}
	if len(pending) == 0 {
		return nil
	}
	steps = pending
	actions = actions[:0]
	for _, step := range steps {
		actions = append(actions, step.Action)
	}
	plan = newRuntimeMutationPlan(actions...)
	if _, err := plan.printableBytes(); err != nil {
		return err
	}
	for i := range steps {
		if steps[i].Guard == nil {
			return fmt.Errorf("runtime mutation plan: action %q has no executable guard", steps[i].Action.Verb)
		}
		if err := steps[i].Guard(ctx); err != nil {
			return fmt.Errorf("runtime mutation plan: guard refused action %q before first write: %w", steps[i].Action.Verb, err)
		}
	}
	var applied []runtimeMutationStep
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
	var residual []plannedRuntimeMutation
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
	residualBytes, _ := newRuntimeMutationPlan(residual...).printableBytes()
	resultErr := fmt.Errorf("runtime mutation plan: expected effects were not fully observed; residual plan:\n%s", residualBytes)
	if len(observeErrors) > 0 {
		resultErr = errors.Join(resultErr, errors.Join(observeErrors...))
	}
	if len(rollbackErrors) > 0 {
		resultErr = errors.Join(resultErr, fmt.Errorf("runtime mutation plan: owned reverse rollback incomplete: %w", errors.Join(rollbackErrors...)))
	}
	return resultErr
}
