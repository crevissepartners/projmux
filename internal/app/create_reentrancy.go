package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// createOperationEnvironment is a session-scoped lease carried by canonical
// create mutations. Generated tmux lifecycle hooks inspect it before opening
// the registry: a hook caused by the create that already owns the registry lock
// returns immediately, while an ordinary hook keeps its synchronous contract.
const createOperationEnvironment = "__projmux_create_operation"

const createOperationMarkerMaxAge = 5 * time.Minute

func newCreateOperationMarker(operationID string) string {
	return fmt.Sprintf("v1:%d:%d:%s", os.Getpid(), time.Now().Unix(), strings.TrimSpace(operationID))
}

func activeCreateOperationMarker(marker string, now time.Time) bool {
	parts := strings.SplitN(strings.TrimSpace(marker), ":", 4)
	if len(parts) != 4 || parts[0] != "v1" || strings.TrimSpace(parts[3]) == "" {
		return false
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || !notifyQueueEventProcessAlive(pid) {
		return false
	}
	started, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(started, 0))
	return age >= -time.Minute && age <= createOperationMarkerMaxAge
}

func sessionEnvironmentValue(output, name string) string {
	prefix := strings.TrimSpace(name) + "="
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, prefix); ok {
			return after
		}
	}
	return ""
}

// deferBindingConvergence reports whether session is owned by a live canonical
// create. Invalid and stale leases are removed before normal convergence so a
// crashed creator cannot disable lifecycle repair permanently.
func deferBindingConvergence(ctx context.Context, runner tmuxCommandRunner, target tmuxTransport, session string) (bool, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return false, fmt.Errorf("binding convergence requires an explicit hook session")
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	out, err := routed.Run(ctx, "tmux", "show-environment", "-t", session)
	if err != nil {
		return false, fmt.Errorf("read create-operation lease for tmux session %q: %w", session, err)
	}
	marker := sessionEnvironmentValue(string(out), createOperationEnvironment)
	if marker == "" {
		return false, nil
	}
	if activeCreateOperationMarker(marker, time.Now()) {
		return true, nil
	}
	if _, err := routed.Run(ctx, "tmux", "set-environment", "-u", "-t", session, createOperationEnvironment); err != nil {
		return false, fmt.Errorf("clear stale create-operation lease for tmux session %q: %w", session, err)
	}
	return false, nil
}
