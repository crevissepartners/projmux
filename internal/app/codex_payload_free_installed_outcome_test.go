package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type installedPayloadFreeCreateOutcome struct {
	DurableReady bool
	AgentUID     string
	ThreadID     string
	Endpoint     coremetadata.CodexEndpointRef
	Readiness    codexappserver.DurableResumeOutcome
}

var installedPayloadFreeCreateFailurePattern = regexp.MustCompile(
	"^Codex payload-free create preserved a content-free failed outcome: agent uid:([^[:space:]]+) thread ([^[:space:]]+) endpoint ([^/[:space:]]+)/([^[:space:]]+) readiness ([a-z-]+); TUI was not launched and retry must use `projmux agent resume uid:([^[:space:]]+)`$",
)

var installedPayloadFreeResumeFailurePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^agent resume: native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: codex app-server response refused: (thread-not-durable|thread-absent|protocol-error) \(code -?[0-9]+\)$`),
	regexp.MustCompile(`^agent resume: the stored Codex thread cannot be resumed natively right now \((generation-unavailable|legacy-generation-unavailable|handover-required)\); refusing to rebind it onto a lane with no native turn control: Codex generation route: (generation-unavailable|legacy-generation-unavailable|handover-required)$`),
}

func runInstalledPayloadFreeCreate(
	ctx context.Context,
	executable string,
	environment []string,
	args ...string,
) (installedPayloadFreeCreateOutcome, error) {
	command := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- installed executable and structured test argv.
	command.Env = environment
	output, runErr := command.CombinedOutput()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return installedPayloadFreeCreateOutcome{}, fmt.Errorf("payload-free create did not return a process exit: %w", runErr)
		}
		exitCode = exitErr.ExitCode()
	}
	return classifyInstalledPayloadFreeCreateOutput(string(output), exitCode)
}

func classifyInstalledPayloadFreeCreateOutput(output string, exitCode int) (installedPayloadFreeCreateOutcome, error) {
	output = strings.TrimSpace(output)
	if exitCode == 0 {
		if fields := strings.Fields(output); len(fields) == 1 {
			if kind, ok := coremetadata.UIDKind(fields[0]); ok && kind == coremetadata.KindAgent {
				return installedPayloadFreeCreateOutcome{DurableReady: true, AgentUID: fields[0]}, nil
			}
		}
		return installedPayloadFreeCreateOutcome{}, fmt.Errorf("successful payload-free create output is not one exact Agent uid: %s", installedOutputReceipt(output))
	}
	if exitCode != 1 {
		return installedPayloadFreeCreateOutcome{}, fmt.Errorf("payload-free create exit=%d %s, want exact success or typed readiness exit 1", exitCode, installedOutputReceipt(output))
	}
	match := installedPayloadFreeCreateFailurePattern.FindStringSubmatch(output)
	agentKind, agentUIDOK := coremetadata.UIDKind(matchValue(match, 1))
	retryKind, retryUIDOK := coremetadata.UIDKind(matchValue(match, 6))
	if len(match) != 7 || !agentUIDOK || agentKind != coremetadata.KindAgent || !retryUIDOK || retryKind != coremetadata.KindAgent || match[1] != match[6] {
		return installedPayloadFreeCreateOutcome{}, fmt.Errorf("payload-free create failure is not the closed content-free readiness outcome: %s", installedOutputReceipt(output))
	}
	readiness := codexappserver.DurableResumeOutcome(match[5])
	switch readiness {
	case codexappserver.DurableResumeTimeout,
		codexappserver.DurableResumeThreadAbsent,
		codexappserver.DurableResumeConnectionClose,
		codexappserver.DurableResumeEndpointChanged,
		codexappserver.DurableResumeProtocolRefusal:
	default:
		return installedPayloadFreeCreateOutcome{}, fmt.Errorf("payload-free create readiness outcome is outside the closed table: %s", installedOutputReceipt(output))
	}
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: match[3], EndpointGenerationID: match[4]}
	if !endpoint.Valid() {
		return installedPayloadFreeCreateOutcome{}, fmt.Errorf("payload-free create failure returned an invalid endpoint identity")
	}
	return installedPayloadFreeCreateOutcome{
		AgentUID: match[1], ThreadID: match[2], Endpoint: endpoint, Readiness: readiness,
	}, nil
}

func matchValue(match []string, index int) string {
	if index < 0 || index >= len(match) {
		return ""
	}
	return match[index]
}

func requireInstalledPayloadFreeResumeRefusal(
	ctx context.Context,
	executable string,
	environment []string,
	agentUID string,
) error {
	if kind, ok := coremetadata.UIDKind(agentUID); !ok || kind != coremetadata.KindAgent {
		return errors.New("exact Agent retry identity is not an Agent uid")
	}
	command := exec.CommandContext(ctx, executable, "agent", "resume", "uid:"+agentUID) // #nosec G204 -- installed executable and exact Agent uid.
	command.Env = environment
	output, runErr := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return fmt.Errorf("exact Agent retry exit is not the closed refusal: err=%v %s", runErr, installedOutputReceipt(string(output)))
	}
	return classifyInstalledPayloadFreeResumeOutput(string(output), exitErr.ExitCode())
}

func classifyInstalledPayloadFreeResumeOutput(output string, exitCode int) error {
	if exitCode != 1 {
		return fmt.Errorf("exact Agent retry exit=%d, want content-free refusal exit 1", exitCode)
	}
	trimmed := strings.TrimSpace(string(output))
	for _, pattern := range installedPayloadFreeResumeFailurePatterns {
		if pattern.MatchString(trimmed) {
			return nil
		}
	}
	return fmt.Errorf("exact Agent retry output is not a content-free no-second-lane refusal: %s", installedOutputReceipt(trimmed))
}

func installedOutputReceipt(output string) string {
	digest := sha256.Sum256([]byte(output))
	return fmt.Sprintf("output-bytes=%d output-sha256=%x", len(output), digest)
}

func installedCatalogThreadIDs(ctx context.Context, socketPath, cwd string) ([]string, error) {
	client, err := codexappserver.OpenPrivateUnix(ctx, socketPath, 10*codexappserver.DefaultProbeTimeout, "installed-phase7-observation", true)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	page, err := client.ListCatalogThreads(ctx, codexappserver.CatalogQuery{CWD: cwd})
	if err != nil {
		return nil, err
	}
	if page.NextCursor != nil {
		return nil, errors.New("isolated payload-free catalog unexpectedly exceeded one page")
	}
	ids := make([]string, 0, len(page.Threads))
	for _, thread := range page.Threads {
		ids = append(ids, thread.ID)
	}
	return ids, nil
}

func TestInstalledPayloadFreeCreateOutputClassificationIsStrictAndContentFree(t *testing.T) {
	t.Parallel()
	valid := "Codex payload-free create preserved a content-free failed outcome: agent uid:agent-one thread thread-one endpoint state-one/generation-one readiness deadline; TUI was not launched and retry must use `projmux agent resume uid:agent-one`"
	outcome, err := classifyInstalledPayloadFreeCreateOutput(valid, 1)
	if err != nil || outcome.DurableReady || outcome.AgentUID != "agent-one" || outcome.ThreadID != "thread-one" ||
		outcome.Readiness != codexappserver.DurableResumeTimeout || outcome.Endpoint.StateDomainID != "state-one" ||
		outcome.Endpoint.EndpointGenerationID != "generation-one" {
		t.Fatalf("typed classification=%+v err=%v", outcome, err)
	}
	for _, test := range []struct {
		name     string
		output   string
		exitCode int
	}{
		{name: "arbitrary failure", output: "provider said secret conversation content", exitCode: 1},
		{name: "unknown readiness", output: strings.Replace(valid, "readiness deadline", "readiness provider-secret", 1), exitCode: 1},
		{name: "different retry identity", output: strings.Replace(valid, "uid:agent-one`", "uid:agent-two`", 1), exitCode: 1},
		{name: "typed non-Agent identity", output: strings.ReplaceAll(valid, "agent-one", "pane-one"), exitCode: 1},
		{name: "usage exit", output: valid, exitCode: 2},
		{name: "success with diagnostic", output: "agent-one provider-secret", exitCode: 0},
		{name: "success Pane uid", output: "pane-one", exitCode: 0},
		{name: "success garbage token", output: "garbage", exitCode: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := classifyInstalledPayloadFreeCreateOutput(test.output, test.exitCode); err == nil {
				t.Fatalf("classification accepted non-contract output: %+v", got)
			}
		})
	}
}

func TestInstalledPayloadFreeResumeOutputClassificationIsStrictAndContentFree(t *testing.T) {
	t.Parallel()
	for _, output := range []string{
		"agent resume: native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: codex app-server response refused: thread-not-durable (code -32600)",
		"agent resume: the stored Codex thread cannot be resumed natively right now (generation-unavailable); refusing to rebind it onto a lane with no native turn control: Codex generation route: generation-unavailable",
	} {
		if err := classifyInstalledPayloadFreeResumeOutput(output, 1); err != nil {
			t.Fatalf("content-free retry refusal rejected: %v", err)
		}
	}
	for _, test := range []struct {
		output   string
		exitCode int
	}{
		{output: "provider secret conversation content", exitCode: 1},
		{output: "agent resume: native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: no rollout found for private title", exitCode: 1},
		{output: "", exitCode: 0},
		{output: "usage", exitCode: 2},
	} {
		if err := classifyInstalledPayloadFreeResumeOutput(test.output, test.exitCode); err == nil {
			t.Fatalf("retry classifier accepted a non-contract output at exit=%d", test.exitCode)
		}
	}
}
