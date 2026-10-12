package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

import (
	"context"
	"fmt"

	"strconv"
	"strings"
	"time"
)

const createOperationMarkerMaxAge = 5 * time.Minute

func newCreateOperationMarker(operationID string) string {
	return newCreateOperationMarkerAt(operationID, time.Now())
}

// newCreateOperationMarkerAt stamps the lease with an explicit clock reading so
// a transaction whose clock is injected produces the same marker on every run.
// The format is the hook-visible contract; only the source of the instant
// differs from newCreateOperationMarker.
var newCreateOperationMarkerAt = mutationruntime.NewCreateOperationMarkerAt

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

// deferBindingConvergence reports whether session is owned by a live canonical
// create. Invalid and stale leases are removed before normal convergence so a
// crashed creator cannot disable lifecycle repair permanently.
func deferBindingConvergence(ctx context.Context, runner tmuxCommandRunner, target tmuxTransport, session string) (bool, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return false, fmt.Errorf("binding convergence requires an explicit hook session")
	}
	routed := explicitTmuxRunner{Runner: runner, Target: target}
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
