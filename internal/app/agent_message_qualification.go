package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

const claudeQualificationCommandTimeout = claudeQualificationStopWindow + 5*time.Second

type agentMessageQualificationReceipt struct {
	Version          int       `json:"version"`
	State            string    `json:"state"`
	QualificationRef string    `json:"qualificationRef"`
	ProviderVersion  string    `json:"providerVersion"`
	AgentUID         string    `json:"agentUID"`
	PaneUID          string    `json:"paneUID"`
	Generation       string    `json:"generation"`
	RouteIncarnation string    `json:"routeIncarnation"`
	Evidence         string    `json:"evidence"`
	Reason           string    `json:"reason"`
	ObservedAt       time.Time `json:"observedAt"`
	Ambiguous        bool      `json:"ambiguous"`
	AutoResend       bool      `json:"autoResend"`
}

func readClaudeQualificationEvidence(path string) (claudeQualificationEvidence, error) {
	refused := errors.New("claude qualification evidence must be an exact owned private regular file")
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return claudeQualificationEvidence{}, refused
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || !localipc.OwnedByCurrentUser(before) {
		return claudeQualificationEvidence{}, refused
	}
	file, err := os.Open(path) // #nosec G304 -- exact absolute owned private file validated above.
	if err != nil {
		return claudeQualificationEvidence{}, refused
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm()&0o077 != 0 || !localipc.OwnedByCurrentUser(after) || !os.SameFile(before, after) {
		return claudeQualificationEvidence{}, refused
	}
	data, err := io.ReadAll(io.LimitReader(file, localipc.MaxFrameBytes+1))
	if err != nil || len(data) > localipc.MaxFrameBytes {
		return claudeQualificationEvidence{}, refused
	}
	var evidence claudeQualificationEvidence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&evidence) != nil {
		return claudeQualificationEvidence{}, refused
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return claudeQualificationEvidence{}, refused
	}
	return evidence, nil
}

func (c *agentCommand) runMessageQualify(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent message qualify"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var evidencePath, output string
	var timeout time.Duration
	var confirmed bool
	fs.StringVar(&evidencePath, "evidence", "", "owned sanitized public-init evidence JSON")
	fs.StringVar(&output, "o", "", "output mode: json")
	fs.DurationVar(&timeout, "timeout", claudeQualificationCommandTimeout, "maximum Stop marker wait")
	fs.BoolVar(&confirmed, "confirm-isolated-provider-push", false, "confirm this opt-in command sends one qualification frame")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		return err
	}
	if len(refs) != 1 || evidencePath == "" || output != "json" || timeout <= 0 || timeout > 5*time.Minute || !confirmed {
		return usageError(spelling + " requires <claude-agent-ref> --evidence <absolute-private-json> --confirm-isolated-provider-push -o json [--timeout <duration>]")
	}
	registry, err := c.readMessageRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	target, err := c.resolveMessageAgent(registry, refs[0], spelling)
	if err != nil {
		return err
	}
	if target.Spec.Provider != string(aiprovider.Claude) {
		return fmt.Errorf("%s: target Agent must use Claude", spelling)
	}
	if err := requireAgentMessageCapability("message.send", target); err != nil {
		return err
	}
	route, reason := coremetadata.ResolveAgentRoute(registry, target.Metadata.UID)
	if reason != "" || !probeClaudeRegistrationLease(c.messagePaths.registryPath, route) {
		return fmt.Errorf("%s: exact Claude registration lease is unavailable", spelling)
	}
	evidence, err := readClaudeQualificationEvidence(evidencePath)
	if err != nil || !evidence.valid(c.messageClock(), route) {
		return fmt.Errorf("%s: public-init evidence is invalid for the exact current activation", spelling)
	}
	coordinationTarget, ok := claudeTargetForRoute(route)
	if !ok {
		return fmt.Errorf("%s: exact Claude route is unavailable", spelling)
	}
	writeReceipt := func(response claudeCoordinationResponse) error {
		evidenceKind := "owned-public-init-only"
		if response.Kind == "qualification-qualified" {
			evidenceKind = "owned-public-init-plus-exact-stop-marker"
		}
		receipt := agentMessageQualificationReceipt{Version: claudeQualificationEvidenceVersion,
			State: response.Kind, QualificationRef: response.QualificationRef, ProviderVersion: response.ProviderVersion,
			AgentUID: route.AgentUID, PaneUID: route.PaneUID, Generation: route.Generation, RouteIncarnation: route.Incarnation(),
			Evidence: evidenceKind, Reason: response.Reason, ObservedAt: c.messageClock(),
			Ambiguous: response.Ambiguous, AutoResend: false}
		return json.NewEncoder(stdout).Encode(receipt)
	}
	deadline := time.Now().Add(timeout)
	callCtx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
	response, callErr := callClaudeCoordination(callCtx, c.messagePaths.registryPath, route, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "qualify", Target: coordinationTarget,
		Qualification: &evidence, ExplicitOptIn: true,
	})
	cancel()
	if valid := callErr == nil; valid {
		response, valid = validateClaudeQualificationResponse(response, "", "")
		if !valid {
			callErr = claudeCoordinationCallError{possiblyDispatched: true}
		}
	}
	if callErr != nil {
		response = claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-failed",
			ProviderVersion: claudeFrozenFrameProviderVersion, Reason: "qualification-response-missing",
			Ambiguous: claudeCoordinationCallPossiblyDispatched(callErr), AutoResend: false}
		if err := writeReceipt(response); err != nil {
			return err
		}
		return fmt.Errorf("%s: qualification push was refused", spelling)
	}
	for response.Kind == "qualification-pending" || response.Kind == "qualification-writing" {
		if !time.Now().Before(deadline) {
			response.Kind = "qualification-failed"
			response.Reason = "qualification-client-timeout"
			response.Ambiguous = true
			response.AutoResend = false
			break
		}
		if err := c.sleepMessage(context.Background(), 50*time.Millisecond); err != nil {
			response.Kind = "qualification-failed"
			response.Reason = "qualification-client-interrupted"
			response.Ambiguous = true
			response.AutoResend = false
			if receiptErr := writeReceipt(response); receiptErr != nil {
				return receiptErr
			}
			return err
		}
		callCtx, cancel = context.WithTimeout(context.Background(), localipc.Deadline)
		previousKind := response.Kind
		expectedRef := response.QualificationRef
		response, callErr = callClaudeCoordination(callCtx, c.messagePaths.registryPath, route, claudeCoordinationRequest{
			Version: claudeCoordinationVersion, Operation: "qualification-status", Target: coordinationTarget,
			QualificationRef: expectedRef,
		})
		cancel()
		if valid := callErr == nil; valid {
			response, valid = validateClaudeQualificationResponse(response, expectedRef, previousKind)
			if !valid {
				callErr = claudeCoordinationCallError{possiblyDispatched: true}
			}
		}
		if callErr != nil {
			response = claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-failed",
				QualificationRef: expectedRef, ProviderVersion: claudeFrozenFrameProviderVersion,
				Reason: "qualification-status-missing", Ambiguous: true, AutoResend: false}
			if err := writeReceipt(response); err != nil {
				return err
			}
			return fmt.Errorf("%s: qualification helper became unavailable", spelling)
		}
	}
	if err := writeReceipt(response); err != nil {
		return err
	}
	if response.Kind != "qualification-qualified" {
		return fmt.Errorf("%s: qualification failed closed: %s", spelling, response.Reason)
	}
	return nil
}

func validateClaudeQualificationResponse(response claudeCoordinationResponse, expectedRef, previousKind string) (claudeCoordinationResponse, bool) {
	if response.Version != claudeCoordinationVersion || response.ProviderVersion != claudeFrozenFrameProviderVersion ||
		response.AutoResend || response.ReplyRef != "" || response.Delivery.MessageRef != "" || response.Delivery.State != "" ||
		!validCoordinationRef(response.QualificationRef) || (expectedRef != "" && response.QualificationRef != expectedRef) {
		return claudeCoordinationResponse{}, false
	}
	allowedTransition := previousKind == "" ||
		(previousKind == "qualification-writing" && response.Kind != "qualification-refused") ||
		(previousKind == "qualification-pending" && response.Kind != "qualification-writing" && response.Kind != "qualification-refused")
	if !allowedTransition {
		return claudeCoordinationResponse{}, false
	}
	switch response.Kind {
	case "qualification-writing", "qualification-pending":
		if response.Reason != "" || response.Ambiguous {
			return claudeCoordinationResponse{}, false
		}
	case "qualification-qualified":
		if response.Reason != "exact-public-init-and-stop-marker" || response.Ambiguous {
			return claudeCoordinationResponse{}, false
		}
	case "qualification-failed":
		switch response.Reason {
		case "qualification-provider-write-zero", "qualification-write-not-complete":
			if response.Ambiguous {
				return claudeCoordinationResponse{}, false
			}
		case "qualification-provider-outcome-unknown", "qualification-stop-recursion",
			"qualification-concurrent-user-turn", "qualification-marker-mismatch", "qualification-stop-timeout":
			if !response.Ambiguous {
				return claudeCoordinationResponse{}, false
			}
		case "helper-restart":
			// Helper closure may race either before or after the sole write.
		default:
			return claudeCoordinationResponse{}, false
		}
	default:
		return claudeCoordinationResponse{}, false
	}
	return response, true
}
