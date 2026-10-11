package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/agentcatalog"
)

type agentCapabilityProjectionEntry = agentcatalog.Capability
type agentCapabilityProjection = agentcatalog.Capabilities
type agentCapabilityCoordination = agentcatalog.Coordination

func (c *agentCommand) runCapabilities(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent capabilities"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	var provider, output string
	var jsonOutput bool
	fs.StringVar(&provider, "provider", "", "provider id: codex, claude, or antigravity")
	fs.StringVar(&output, "o", "", "output mode: json")
	fs.BoolVar(&jsonOutput, "json", false, "emit JSON")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if len(refs) > 1 {
		return usageError(spelling + " accepts at most one Agent reference")
	}
	if len(refs) == 1 && strings.TrimSpace(provider) != "" {
		return usageError(spelling + " accepts an Agent reference or --provider, not both")
	}
	if output != "" && output != "json" {
		return usageError(fmt.Sprintf("%s: unsupported output mode %q", spelling, output))
	}
	jsonOutput = jsonOutput || output == "json"

	var projection agentCapabilityProjection
	if strings.TrimSpace(provider) != "" {
		id, err := agentcatalog.Provider(spelling, provider)
		if err != nil {
			return MapMetadataError(err)
		}
		projection = projectStaticAgentCapabilities(id)
	} else {
		ref := ""
		if len(refs) == 1 {
			ref = refs[0]
		}
		var registry coremetadata.Registry
		var agent coremetadata.Agent
		var resolveErr error
		if strings.TrimSpace(ref) == "" {
			registry, agent, resolveErr = c.resolveOneAgent(spelling, ref, selector.VerbStatus)
		} else {
			registry, resolveErr = c.loadRegistry()
			if resolveErr == nil {
				agent, resolveErr = agentcatalog.ResolveAgent(registry, ref)
			}
		}
		if resolveErr != nil {
			return MapMetadataError(resolveErr)
		}
		id, err := agentcatalog.AgentProvider(agent)
		if err != nil {
			return MapMetadataError(err)
		}
		registryPath := ""
		if paths, err := config.DefaultPathsFromEnv(); err == nil {
			registryPath = intmetadata.PathFor(paths.StateDir)
		}
		projection = capabilityProbes().Project(registry, agent, id, registryPath)
	}
	return writeAgentCapabilityProjection(stdout, projection, jsonOutput)
}

func capabilityProbes() agentcatalog.Probes {
	return agentcatalog.Probes{Lease: probeClaudeLease, Qualification: probeClaudeQualification, ProcessAlive: claudeActivationProcessAlive}
}
func projectClaudeCoordinationEligibilityAt(registry coremetadata.Registry, agent coremetadata.Agent, registryPath string) *agentCapabilityCoordination {
	return capabilityProbes().CoordinationAt(registry, agent, registryPath)
}

// probeClaudeLease and probeClaudeQualification are the two live probes the
// coordination projection consults. They are variables for the same reason
// claudeActivationProcessAlive is one: a table case can state the host truth of
// each branch instead of standing up a socket per row, and no branch can be
// classified while consulting nothing. The real dial stays covered by the Linux
// socket fixture in claude_dialogue_tool_evidence_linux_test.go.
var (
	probeClaudeLease         = probeClaudeRegistrationLease
	probeClaudeQualification = probeClaudeCoordinationEligibility
)

func projectStaticAgentCapabilities(provider aiprovider.ID) agentCapabilityProjection {
	return agentcatalog.Static(provider)
}

func exactAgentActivationReason(registry coremetadata.Registry, agent coremetadata.Agent) string {
	return agentcatalog.ActivationReason(registry, agent)
}

func writeAgentCapabilityProjection(stdout io.Writer, projection agentCapabilityProjection, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(projection)
	}
	if projection.Agent == nil {
		if _, err := fmt.Fprintf(stdout, "provider=%s static=true\n", projection.Provider); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(stdout, "ACTION\tMODE\tCOMPLETION"); err != nil {
			return err
		}
		for _, entry := range projection.Capabilities {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", entry.Action, entry.Mode, entry.CompletionPrecision); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "agent=%s uid=%s provider=%s phase=%s registryReady=%t liveVerified=%t generation=%s connectionEpoch=%d bindingEpoch=%d\n",
		projection.Agent.Name, projection.Agent.UID, projection.Provider, projection.Agent.Phase, projection.Runtime.Ready,
		projection.Runtime.LiveVerified, projection.Runtime.ActivationGeneration, projection.Runtime.ConnectionEpoch, projection.Runtime.BindingEpoch); err != nil {
		return err
	}
	if coordination := projection.Runtime.Coordination; coordination != nil {
		if _, err := fmt.Fprintf(stdout, "coordination eligible=%t evidence=%s reason=%s recovery=%s\n", coordination.Eligible, coordination.Evidence, coordination.Reason, coordination.Recovery); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, "ACTION\tMODE\tCOMPLETION\tAVAILABLE\tEVIDENCE\tREASON"); err != nil {
		return err
	}
	for _, entry := range projection.Capabilities {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%t\t%s\t%s\n", entry.Action, entry.Mode, entry.CompletionPrecision, *entry.Available, entry.Evidence, entry.Reason); err != nil {
			return err
		}
	}
	return nil
}

func requireStaticNativeAgentCapability(spelling string, agent coremetadata.Agent) error {
	action := strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(spelling), "agent "), " ", ".")
	metadata, ok := aiprovider.Lookup(agent.Spec.Provider)
	if !ok {
		return fmt.Errorf("%s unavailable: agent/%s has unsupported provider %q", spelling, agent.Metadata.Name, agent.Spec.Provider)
	}
	_, cell, ok := aiprovider.LookupAgentCapability(action, metadata.ID)
	if !ok || cell.Mode != aiprovider.SupportNativeExact {
		return fmt.Errorf("%s unavailable: agent/%s provider %s does not support native exact control", spelling, agent.Metadata.Name, metadata.ID)
	}
	return nil
}
