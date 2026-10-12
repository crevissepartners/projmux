package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runtimeMutationSocketNameOption records the logical -L name that config
// apply used for one app-owned server. #{socket_path} alone is insufficient:
// an arbitrary -S path and an -L name may share a basename, while only the
// latter can preserve the name-only PROJMUX_SOCKET hook contract.
const RuntimeMutationSocketNameOption = "@projmux_socket_name"

type RuntimeMutationMarkerDiagnosis string

const RuntimeMutationMarkerMissing RuntimeMutationMarkerDiagnosis = "missing-logical-marker"

// runtimeMutationMarkerError is the typed, zero-write diagnosis returned when
// an app-owned server has only the pre-0.13 ownership marker. Ordinary
// mutation routes never repair this state. The only recovery is the explicit
// config-apply writer, and the command is included only after the logical -L
// name has been matched back to the observed physical socket.
type RuntimeMutationMarkerError struct {
	Diagnosis     RuntimeMutationMarkerDiagnosis
	LogicalSocket string
}

func (e *RuntimeMutationMarkerError) Error() string {
	if e == nil {
		return "runtime mutation route: app-owned server has an invalid marker diagnosis"
	}
	if e.LogicalSocket == "" {
		return "runtime mutation route: app-owned server has no logical socket marker (partial marker state); run `projmux doctor --section runtime` to identify the exact recovery route"
	}
	return fmt.Sprintf("runtime mutation route: app-owned server has no logical socket marker (partial marker state); recovery: run `projmux config apply --socket %s`, then retry", e.LogicalSocket)
}

func MissingRuntimeMutationMarker(socketName string) error {
	return &RuntimeMutationMarkerError{Diagnosis: RuntimeMutationMarkerMissing, LogicalSocket: socketName}
}

// runtimeMutationAnchorPaneEnv is private transport between a generated,
// authority-checked popup launcher and its child process. It is deliberately
// distinct from TMUX_PANE: display-popup does not promise that inherited value
// names the originating Pane, while the generated --anchor operand does.
const RuntimeMutationAnchorPaneEnv = "__PROJMUX_RUNTIME_ANCHOR_PANE"

type RuntimeMutationRoute struct {
	Target             TmuxTransport
	ExpectedSocketPath string
	SocketName         string
	Authority          *RuntimeMutationRouteAuthority
}

const (
	RuntimeMutationRouteApp                = "app"
	RuntimeMutationRouteStandalone         = "standalone"
	RuntimeMutationRouteStandaloneExplicit = "standalone-explicit"
)

type RuntimeMutationRouteAuthority struct {
	Class     string
	ServerPID string
	SessionID string
	WindowID  string
	PaneID    string
}

func (a *RuntimeMutationRouteAuthority) Printable() string {
	if a == nil {
		return ""
	}
	return a.Class + ":pid=" + a.ServerPID + "/session=" + a.SessionID + "/window=" + a.WindowID + "/pane=" + a.PaneID
}

func ParseRuntimeMutationRouteAuthority(value string) (RuntimeMutationRouteAuthority, error) {
	class, rest, ok := strings.Cut(strings.TrimSpace(value), ":pid=")
	if !ok || (class != RuntimeMutationRouteApp && class != RuntimeMutationRouteStandalone && class != RuntimeMutationRouteStandaloneExplicit) {
		return RuntimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has an unknown class")
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return RuntimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has incomplete containment")
	}
	pid := parts[0]
	session, okSession := strings.CutPrefix(parts[1], "session=")
	window, okWindow := strings.CutPrefix(parts[2], "window=")
	pane, okPane := strings.CutPrefix(parts[3], "pane=")
	parsedPID, err := strconv.Atoi(pid)
	if err != nil || parsedPID <= 0 || !okSession || !okWindow || !okPane {
		return RuntimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has an invalid server generation")
	}
	hasReceipt := session != "" || window != "" || pane != ""
	if hasReceipt && (ExactTmuxHandle(session, "$") == "" || ExactTmuxHandle(window, "@") == "" || ExactTmuxHandle(pane, "%") == "") {
		return RuntimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has a partial or invalid $/@/% receipt")
	}
	if class == RuntimeMutationRouteStandalone && !hasReceipt {
		return RuntimeMutationRouteAuthority{}, errors.New("runtime mutation standalone authority requires an exact $/@/% receipt")
	}
	if class == RuntimeMutationRouteStandaloneExplicit && hasReceipt {
		return RuntimeMutationRouteAuthority{}, errors.New("runtime mutation explicit standalone authority cannot claim an inferred $/@/% receipt")
	}
	return RuntimeMutationRouteAuthority{Class: class, ServerPID: pid, SessionID: session, WindowID: window, PaneID: pane}, nil
}

type InheritedTmuxReceipt struct {
	SocketPath string
	ServerPID  string
	ClientID   string
}

func ParseInheritedTmuxReceipt(value string) (InheritedTmuxReceipt, error) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) != 3 {
		return InheritedTmuxReceipt{}, errors.New("runtime mutation route: inherited TMUX must contain exactly path,pid,index")
	}
	pathTarget, err := TmuxSocketPathTarget(parts[0])
	if err != nil || pathTarget.Value != parts[0] {
		return InheritedTmuxReceipt{}, errors.New("runtime mutation route: inherited TMUX socket path is not absolute and clean")
	}
	pid, pidErr := strconv.Atoi(parts[1])
	index, indexErr := strconv.Atoi(parts[2])
	if pidErr != nil || pid <= 0 || indexErr != nil || index < 0 {
		return InheritedTmuxReceipt{}, errors.New("runtime mutation route: inherited TMUX pid or client index is invalid")
	}
	return InheritedTmuxReceipt{SocketPath: pathTarget.Value, ServerPID: parts[1], ClientID: parts[2]}, nil
}

// runtimeMutationAnchorScope names the invocation shape one anchor row is
// reobserved for. It selects only the wording of a refusal: both routes prove
// the same containment.
type RuntimeMutationAnchorScope struct {
	// observation names the read in a transport failure, keeping the existing
	// `reobserve <observation>` wording of each route.
	Observation string
	// subject names the anchor in a containment refusal.
	Subject string
}

var (
	InheritedRuntimeMutationAnchorScope = RuntimeMutationAnchorScope{
		Observation: "inherited anchor", Subject: "inherited invocation",
	}
	ExplicitRuntimeMutationAnchorScope = RuntimeMutationAnchorScope{
		Observation: "explicit anchor", Subject: "explicit anchor",
	}
)

// observeRuntimeMutationAnchorRow reobserves one anchor pane on an already
// proven server and either returns its exact $/@/% receipt or names why there
// is none.
//
// The proof is unchanged: authority still requires the observed socket path,
// the observed server generation, and an exact $/@/% receipt naming the same
// pane. What is split is the refusal. tmux answers `display-message -t %N` for
// a pane that no longer exists with rc=0, filling the server columns and
// leaving $/@/% blank, so a single equality chain reports the commonest cause
// -- the anchor pane is gone -- with the wording of the rarest one, containment
// drift, and sends the reader to inspect a socket and a server generation that
// are both correct. The three causes the row can actually tell apart now say
// which one happened: a foreign socket, a replaced server generation, and an
// absent anchor pane.
func ObserveRuntimeMutationAnchorRow(
	ctx context.Context,
	routed TmuxCommandRunner,
	scope RuntimeMutationAnchorScope,
	expectedSocketPath, serverPID, paneID string,
) (sessionID, windowID string, err error) {
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", TmuxRowFormat(
		"#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}"))
	if err != nil {
		return "", "", fmt.Errorf("runtime mutation route: reobserve %s: %w", scope.Observation, err)
	}
	rows := SplitTmuxRows(string(out), 5)
	if len(rows) != 1 {
		return "", "", fmt.Errorf(
			"runtime mutation route: %s anchor pane %s returned no single containment row", scope.Subject, paneID)
	}
	row := rows[0]
	// No shell invocation reaches this branch. Every caller hands in a runner
	// already routed with -S to the socket path it has just proven equal to
	// expectedSocketPath -- the default-socket detached `--anchor` branch, and
	// the inherited standalone and app branches of both the invocation and the
	// existing-route resolvers -- so tmux answers #{socket_path} for that same
	// server. The branch stays as a defensive check, and its refusal text is
	// pinned by TestRuntimeMutationAnchorRefusalNamesItsCause. This stops being
	// true once a caller reobserves the anchor through a runner not routed to
	// the proven path (a logical -L alias, an unproven socket): then the branch
	// is reachable and needs a real-tmux test.
	if row[0] != expectedSocketPath {
		return "", "", fmt.Errorf(
			"runtime mutation route: %s anchor pane %s answers on socket %q, not the proven socket %q",
			scope.Subject, paneID, row[0], expectedSocketPath)
	}
	if row[1] != serverPID {
		return "", "", fmt.Errorf(
			"runtime mutation route: %s server generation was replaced: anchor pane %s answers pid %s, the proven generation is pid %s",
			scope.Subject, paneID, row[1], serverPID)
	}
	// An absent pane is the one answer tmux gives without failing: the server
	// columns above already matched, and every object column is blank.
	if row[2] == "" && row[3] == "" && row[4] == "" {
		return "", "", fmt.Errorf(
			"runtime mutation route: %s anchor pane %s no longer exists on the proven server", scope.Subject, paneID)
	}
	if ExactTmuxHandle(row[2], "$") == "" || ExactTmuxHandle(row[3], "@") == "" || row[4] != paneID {
		return "", "", fmt.Errorf(
			"runtime mutation route: %s socket/pid/pane containment drifted", scope.Subject)
	}
	return row[2], row[3], nil
}

func ObserveInheritedRuntimeMutationAuthority(
	ctx context.Context,
	routed TmuxCommandRunner,
	receipt InheritedTmuxReceipt,
	paneID, class string,
) (*RuntimeMutationRouteAuthority, error) {
	if ExactTmuxHandle(paneID, "%") == "" {
		return nil, errors.New("runtime mutation route: inherited invocation requires exact TMUX_PANE authority")
	}
	sessionID, windowID, err := ObserveRuntimeMutationAnchorRow(
		ctx, routed, InheritedRuntimeMutationAnchorScope, receipt.SocketPath, receipt.ServerPID, paneID)
	if err != nil {
		return nil, err
	}
	return &RuntimeMutationRouteAuthority{
		Class: class, ServerPID: receipt.ServerPID, SessionID: sessionID, WindowID: windowID, PaneID: paneID,
	}, nil
}

// resolveExistingRuntimeMutationRoute binds an already-running explicit route
// to one physical socket and server generation. Blank ownership markers are
// accepted only when exact inherited TMUX+TMUX_PANE containment proves an
// operator-owned standalone invocation; explicit foreign routes never acquire
// mutation authority by absence alone.
func ResolveExistingRuntimeMutationRoute(
	ctx context.Context,
	runner TmuxCommandRunner,
	target TmuxTransport,
	lookupEnv func(string) string,
) (RuntimeMutationRoute, error) {
	return ResolveExistingRuntimeMutationRouteWithAnchor(ctx, runner, target, lookupEnv, "")
}

func ResolveExistingRuntimeMutationRouteWithAnchor(
	ctx context.Context,
	runner TmuxCommandRunner,
	target TmuxTransport,
	lookupEnv func(string) string,
	anchorPaneID string,
) (RuntimeMutationRoute, error) {
	paneID, err := ResolveRuntimeMutationAnchorPane(lookupEnv, anchorPaneID)
	if err != nil {
		return RuntimeMutationRoute{}, err
	}
	routed := ExplicitTmuxRunner{Runner: runner, Target: target}
	pathOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return RuntimeMutationRoute{}, err
	}
	path := strings.TrimSpace(string(pathOut))
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return RuntimeMutationRoute{}, errors.New("runtime mutation route: observed socket is not absolute and clean")
	}
	physical := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: path, Source: TmuxSocketPathSource}}
	appOut, appErr := physical.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	logicalOut, logicalErr := physical.Run(ctx, "tmux", "show-options", "-gqv", RuntimeMutationSocketNameOption)
	if appErr != nil || logicalErr != nil {
		return RuntimeMutationRoute{}, fmt.Errorf("runtime mutation route: ownership markers are unreadable (app=%v logical=%v)", appErr, logicalErr)
	}
	appMarker, logicalMarker := strings.TrimSpace(string(appOut)), strings.TrimSpace(string(logicalOut))
	var inherited *InheritedTmuxReceipt
	if lookupEnv != nil && strings.TrimSpace(lookupEnv("TMUX")) != "" {
		receipt, parseErr := ParseInheritedTmuxReceipt(lookupEnv("TMUX"))
		if parseErr != nil {
			return RuntimeMutationRoute{}, parseErr
		}
		if receipt.SocketPath == path {
			inherited = &receipt
		}
	}
	if appMarker == "" && logicalMarker == "" {
		if inherited == nil {
			return RuntimeMutationRoute{}, errors.New("runtime mutation route: standalone authority requires exact inherited TMUX receipt")
		}
		authority, err := ObserveInheritedRuntimeMutationAuthority(ctx, physical, *inherited, paneID, RuntimeMutationRouteStandalone)
		if err != nil {
			return RuntimeMutationRoute{}, err
		}
		return RuntimeMutationRoute{
			Target: TmuxTransport{Kind: TmuxSocketPath, Value: path, Source: TmuxSocketPathSource}, ExpectedSocketPath: path,
			SocketName: DefaultAppSocket, Authority: authority,
		}, nil
	}
	if appMarker == "1" && logicalMarker == "" {
		return RuntimeMutationRoute{}, MissingRuntimeMutationMarker(ObserveMissingMarkerLogicalSocket(ctx, runner, target, path, lookupEnv))
	}
	if appMarker != "1" || logicalMarker == "" {
		return RuntimeMutationRoute{}, errors.New("runtime mutation route: exact server is not app-owned; partial or foreign markers refuse mutation authority")
	}
	logicalTarget, err := TmuxSocketNameTarget(logicalMarker)
	if err != nil {
		return RuntimeMutationRoute{}, errors.New("runtime mutation route: app server logical marker is invalid")
	}
	routeTarget := target
	if target.Flag() == "-L" {
		byName, err := (ExplicitTmuxRunner{Runner: runner, Target: logicalTarget}).Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
		if err != nil || strings.TrimSpace(string(byName)) != path {
			return RuntimeMutationRoute{}, errors.New("runtime mutation route: app logical alias no longer names the exact server")
		}
		routeTarget = logicalTarget
	}
	var authority *RuntimeMutationRouteAuthority
	if inherited != nil {
		authority, err = ObserveInheritedRuntimeMutationAuthority(ctx, physical, *inherited, paneID, RuntimeMutationRouteApp)
	} else {
		pidOut, pidErr := physical.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		pid := strings.TrimSpace(string(pidOut))
		parsedPID, parseErr := strconv.Atoi(pid)
		if pidErr != nil || parseErr != nil || parsedPID <= 0 {
			return RuntimeMutationRoute{}, errors.New("runtime mutation route: app server generation is unreadable")
		}
		authority = &RuntimeMutationRouteAuthority{Class: RuntimeMutationRouteApp, ServerPID: pid}
	}
	if err != nil {
		return RuntimeMutationRoute{}, err
	}
	return RuntimeMutationRoute{
		Target: routeTarget, ExpectedSocketPath: path, SocketName: logicalMarker, Authority: authority,
	}, nil
}

func ResolveRuntimeMutationAnchorPane(lookupEnv func(string) string, explicit string) (string, error) {
	if raw := strings.TrimSpace(explicit); raw != "" {
		if pane := ExactTmuxHandle(raw, "%"); pane != "" {
			return pane, nil
		}
		return "", errors.New("runtime mutation route: explicit anchor is not an exact TMUX Pane %N")
	}
	if lookupEnv == nil {
		return "", nil
	}
	if raw := strings.TrimSpace(lookupEnv(RuntimeMutationAnchorPaneEnv)); raw != "" {
		if pane := ExactTmuxHandle(raw, "%"); pane != "" {
			return pane, nil
		}
		return "", errors.New("runtime mutation route: private producer anchor is not an exact TMUX Pane %N")
	}
	if raw := strings.TrimSpace(lookupEnv("TMUX_PANE")); raw != "" {
		if pane := ExactTmuxHandle(raw, "%"); pane != "" {
			return pane, nil
		}
		return "", errors.New("runtime mutation route: inherited TMUX_PANE is not an exact Pane %N")
	}
	return "", nil
}

// routeRead is the argv of one tmux read a route proof issues.
type RouteRead []string

var (
	RouteReadSocketPath       = RouteRead{"display-message", "-p", "-F", "#{socket_path}"}
	RouteReadServerPID        = RouteRead{"display-message", "-p", "-F", "#{pid}"}
	RouteReadAppMarker        = RouteRead{"show-options", "-gqv", tmuxopts.AppGlobal}
	RouteReadSocketNameMarker = RouteRead{"show-options", "-gqv", RuntimeMutationSocketNameOption}
)

// routeReadBoundary is the literal line printed between two reads of one
// sequence. It carries no format escape, so display-message prints it
// verbatim.
const RouteReadBoundary = "projmux-route-read-boundary-5f0c"

// readTmuxSequence issues reads as ONE tmux invocation, a command sequence
// joined by tmux's ";" argument (exec'd directly, no shell), and returns each
// read's own output in order. A single read is issued unchanged.
//
// One route proof reads the socket path, the server generation, and both
// ownership markers under the Registry lock; each separate exec is a full
// tmux client round trip. The sequence keeps every read's own semantics:
// show-options -gqv still reads the GLOBAL option (a #{@option} format would
// let a session or window scoped option shadow it), an unset option still
// prints no line, and a multi-line value stays whole within its section.
//
// The caller maps an error to the text its FIRST read in the sequence gives
// today: tmux stops at a failing command, so which read failed is not known.
// Output that does not split into exactly one section per read -- a value
// that itself prints the boundary line, or a truncated sequence -- is refused
// the same way instead of being assigned to the wrong read.
func ReadTmuxSequence(ctx context.Context, runner TmuxCommandRunner, reads ...RouteRead) ([]string, error) {
	if len(reads) == 0 {
		return nil, nil
	}
	if len(reads) == 1 {
		out, err := runner.Run(ctx, "tmux", reads[0]...)
		if err != nil {
			return nil, err
		}
		return []string{string(out)}, nil
	}
	args := make([]string, 0, len(reads)*9)
	for i, read := range reads {
		if i > 0 {
			args = append(args, ";", "display-message", "-p", "-F", RouteReadBoundary, ";")
		}
		args = append(args, read...)
	}
	out, err := runner.Run(ctx, "tmux", args...)
	if err != nil {
		return nil, err
	}
	sections := SplitTmuxSequenceOutput(string(out))
	if len(sections) != len(reads) {
		return nil, fmt.Errorf("tmux read sequence returned %d sections, want %d", len(sections), len(reads))
	}
	return sections, nil
}

// splitTmuxSequenceOutput splits a sequence's output at the boundary lines.
// Every command's output ends in a newline, so exactly one trailing newline
// belongs to the last read and is dropped before splitting.
func SplitTmuxSequenceOutput(out string) []string {
	out = strings.TrimSuffix(out, "\n")
	sections := []string{}
	current := []string{}
	for line := range strings.SplitSeq(out, "\n") {
		if line == RouteReadBoundary {
			sections = append(sections, strings.Join(current, "\n"))
			current = current[:0]
			continue
		}
		current = append(current, line)
	}
	return append(sections, strings.Join(current, "\n"))
}

// observeMissingMarkerLogicalSocket finds a recovery-only logical name. It
// never grants mutation authority: each candidate must independently resolve
// through -L to the already observed absolute socket path, and the caller
// still returns the typed refusal unconditionally.
func ObserveMissingMarkerLogicalSocket(
	ctx context.Context,
	runner TmuxCommandRunner,
	target TmuxTransport,
	expectedPath string,
	lookupEnv func(string) string,
) string {
	candidates := make([]string, 0, 3)
	if target.Flag() == "-L" {
		candidates = append(candidates, target.Value)
	}
	if lookupEnv != nil {
		candidates = append(candidates, strings.TrimSpace(lookupEnv("PROJMUX_SOCKET")))
	}
	candidates = append(candidates, DefaultAppSocket)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		logical, err := TmuxSocketNameTarget(candidate)
		if err != nil {
			continue
		}
		out, err := (ExplicitTmuxRunner{Runner: runner, Target: logical}).Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
		if err == nil && filepath.Clean(strings.TrimSpace(string(out))) == expectedPath {
			return candidate
		}
	}
	return ""
}
