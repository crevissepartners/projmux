package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"unicode"
)

const installedCommandStderrLimit = 4096

// This is a projection of one failed invocation, not a provider receipt.
// Even a recognized CLI refusal never proves whether a provider write happened.
type installedRecoveryCommandFailure struct {
	Operation   string `json:"operation"`
	ExitCode    int    `json:"exitCode"`
	Cause       string `json:"cause"`
	StderrState string `json:"stderrState"`
	Stage       string `json:"stage"`
	Code        string `json:"code"`
	Outcome     string `json:"outcome"`
}

type installedCommandCapture struct {
	data     []byte
	overflow bool
}

func (capture *installedCommandCapture) Write(raw []byte) (int, error) {
	n := len(raw)
	remaining := installedCommandStderrLimit - len(capture.data)
	if len(raw) > remaining {
		capture.overflow = true
		raw = raw[:remaining]
	}
	capture.data = append(capture.data, raw...)
	return n, nil
}

func installedCommandOperation(args []string) string {
	for _, route := range [][]string{{"agent", "turn", "start"}, {"create", "agent"}, {"create", "project"}, {"get", "windows"}, {"delete", "agent"}, {"config", "apply"}, {"reconcile", "resources"}} {
		if len(args) >= len(route) && slices.Equal(args[:len(route)], route) {
			return strings.Join(route, "-")
		}
	}
	return "unclassified"
}

// Output is returned only on success. Failure consumes stderr once in memory,
// retains closed metadata, and cannot retry or turn a partial receipt into success.
func executeInstalledRecoveryCommand(ctx context.Context, executable string, args ...string) ([]byte, *installedRecoveryCommandFailure, error) {
	command := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- explicit fixture executable and argv, never a shell composition.
	command.Env = withoutInheritedTmuxEnvironment(os.Environ())
	var output bytes.Buffer
	var stderr installedCommandCapture
	command.Stdout, command.Stderr = &output, &stderr
	if err := command.Run(); err != nil {
		return nil, projectInstalledCommandFailure(args, ctx.Err(), err, stderr), err
	}
	return output.Bytes(), nil, nil
}

func projectInstalledCommandFailure(args []string, contextErr, err error, stderr installedCommandCapture) *installedRecoveryCommandFailure {
	if err == nil {
		return nil
	}
	out := &installedRecoveryCommandFailure{Operation: installedCommandOperation(args), ExitCode: -1, Cause: "unclassified", StderrState: "empty", Stage: "unclassified", Code: "unclassified", Outcome: "unknown"}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ProcessState != nil {
		out.ExitCode = exit.ExitCode()
		out.Cause = "process-exit"
	}
	switch {
	case errors.Is(contextErr, context.DeadlineExceeded):
		out.Cause = "timeout"
	case errors.Is(contextErr, context.Canceled):
		out.Cause = "cancelled"
	case errors.Is(err, os.ErrPermission):
		out.Cause = "permission"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		out.Cause = "missing-executable"
	}
	if stderr.overflow || len(stderr.data) > installedCommandStderrLimit {
		out.StderrState = "oversized"
		return out
	}
	if len(stderr.data) == 0 {
		return out
	}
	out.StderrState = "unrecognized"
	if out.Operation != "agent-turn-start" {
		return out
	}
	line := strings.TrimSuffix(string(stderr.data), "\n")
	if strings.IndexFunc(line, unicode.IsControl) >= 0 {
		return out
	}
	// A known recovery suffix contains only exact resource selectors. Validate
	// its structure, then discard all UIDs rather than emitting argv or paths.
	if before, after, ok := strings.Cut(line, "; Open Codex: `"); ok {
		if !validInstalledCommandRecoverySuffix(after) {
			return out
		}
		line = before
	}
	stage, code := projectInstalledTurnStartError(line)
	if stage != "" {
		out.StderrState = "recognized"
		out.Stage, out.Code = stage, code
	}
	return out
}

func validInstalledCommandRecoverySuffix(suffix string) bool {
	if !strings.HasSuffix(suffix, "`") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(suffix, "`"), " ")
	if len(parts) != 8 || !slices.Equal(parts[:3], []string{"projmux", "focus", "pane"}) || parts[4] != "--project" || parts[6] != "--window" {
		return false
	}
	for _, index := range []int{3, 5, 7} {
		uid, ok := strings.CutPrefix(parts[index], "uid:")
		if !ok || uid == "" || len(uid) > 128 {
			return false
		}
		for _, char := range uid {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

func projectInstalledTurnStartError(line string) (string, string) {
	// Full public response messages must agree with their closed codes. A
	// provider payload that merely mentions a code cannot acquire a stage.
	responses := map[string]string{
		"exact Agent connection epoch is no longer active (stale-epoch)":                         "stale-epoch",
		"exact Agent binding or activation generation changed (stale-binding)":                   "stale-binding",
		"native Codex control connection is unavailable (unavailable)":                           "unavailable",
		"thread is not idle; new turn write refused (stale-turn)":                                "stale-turn",
		"fresh exact turn state is unavailable; new turn write refused (turn-state-unavailable)": "turn-state-unavailable",
		"exact thread already has a turn in progress (turn-in-progress)":                         "turn-in-progress",
		"native Codex control refused the exact request (turn-start-failed)":                     "turn-start-failed",
		"native Codex control timed out (timeout)":                                               "timeout",
		"turn/start returned a different or incomplete identity (protocol-error)":                "protocol-error",
	}
	if code, ok := responses[line]; ok {
		return "control-response", code
	}
	switch line {
	case "agent turn start|steer requires <agent-ref> -- <text>; quote text as one argument":
		return "cli-input", "invalid-turn-arguments"
	case "exact Agent native control unavailable: turn response did not preserve the exact thread identity":
		return "turn-recording", "response-identity"
	case "exact Agent native control unavailable: first-turn evidence no longer matches the exact native binding":
		return "turn-recording", "binding-changed"
	case "exact Agent native control unavailable: canonical generation consumer fence is stale":
		return "control-binding", "stale-consumer-fence"
	case "exact Agent control socket path exceeds the platform-safe bound":
		return "control-transport", "socket-path-bound"
	case "exact Agent control requires an absolute state directory and durable endpoint/thread identity":
		return "control-transport", "invalid-address-identity"
	}
	// Only established CLI wrapper prefixes are classified; nested error text
	// remains opaque. A stage names that wrapper, not the underlying cause.
	for _, known := range []struct{ prefix, stage, code string }{
		{"exact Agent native control unavailable: resolve logical tmux route: ", "control-binding", "logical-route"},
		{"exact Agent native control unavailable: resolve private control path: ", "control-binding", "private-path"},
		{"exact Agent native control unavailable: read live binding: ", "control-binding", "live-binding-read"},
		{"exact Agent native control unavailable: native Codex control endpoint unavailable: ", "control-transport", "endpoint-unavailable"},
		{"exact Agent native control unavailable: write native Codex control request: ", "control-transport", "request-write"},
		{"exact Agent native control unavailable: read native Codex control response: ", "control-transport", "response-read"},
	} {
		if strings.HasPrefix(line, known.prefix) && len(line) > len(known.prefix) {
			return known.stage, known.code
		}
	}
	return "", ""
}
