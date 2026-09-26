package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// sessionsReasonProviderUnsupported: the Agent is not a Claude Agent, and only
// Claude conversation changes are recorded.
const sessionsReasonProviderUnsupported = "sessions-provider-unsupported"

// agentSessionsActions are the `agent sessions` subcommands in help order.
var agentSessionsActions = []string{"list", "backfill"}

// agentSessionsList is the `agent sessions list -o json` projection. Each
// session row carries the sessionhistory.Record keys verbatim.
type agentSessionsList struct {
	AgentUID     string                  `json:"agentUID"`
	AgentName    string                  `json:"agentName"`
	Sessions     []sessionhistory.Record `json:"sessions"`
	CorruptLines int                     `json:"corruptLines"`
}

// runSessions lists the Claude conversations one Agent has moved through: the
// append-only session history joined with the conversation its
// `status.sessionRef` records now (sessionhistory.List). It is read-only.
func (c *agentCommand) runSessions(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "backfill" {
		return c.runSessionsBackfill(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "list" {
		return usageError("agent sessions requires " + strings.Join(agentSessionsActions, ", "))
	}
	const spelling = "agent sessions list"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	flags := resourceQueryFlags{kind: coremetadata.KindAgent}
	flags.register(fs)
	var output string
	fs.StringVar(&output, "output", "", "result projection: json")
	fs.StringVar(&output, "o", "", "result projection: json (alias of --output)")
	positionals, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if len(positionals) != 1 {
		return usageError(spelling + " requires <agent-ref>")
	}
	if output != "" && output != "json" {
		return usageError(fmt.Sprintf("%s: unsupported output %q; want json", spelling, output))
	}
	flags.addPositionalRef(positionals[0])

	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbTopic, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	found, ok := registry.Agent(resolution.Matches[0].UID)
	if !ok {
		return fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, resolution.Matches[0].UID)
	}
	agent := found.Clone()
	if coremetadata.NormalizeProvider(agent.Spec.Provider) != aiModeClaude {
		return usageError(fmt.Sprintf("%s: agent/%s is a %q Agent; session history is recorded only for --provider %s (%s)",
			spelling, agent.Metadata.Name, agent.Spec.Provider, aiModeClaude, sessionsReasonProviderUnsupported))
	}
	if c.store == nil || c.store.stateDir == nil {
		return fmt.Errorf("%s: the projmux state directory is not configured", spelling)
	}
	stateDir, err := c.store.stateDir()
	if err != nil {
		return fmt.Errorf("%s: %w", spelling, err)
	}
	listed, err := sessionhistory.List(stateDir, agent)
	if err != nil {
		return fmt.Errorf("%s: %w", spelling, err)
	}
	result := agentSessionsList{AgentUID: listed.AgentUID, AgentName: agent.Metadata.Name, Sessions: listed.Sessions, CorruptLines: listed.CorruptLines}
	if output == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	if result.CorruptLines > 0 && stderr != nil {
		_, _ = fmt.Fprintf(stderr, "%s: skipped %d unreadable line(s) in %s\n", spelling, result.CorruptLines, sessionhistory.FileName)
	}
	return writeAgentSessionsList(stdout, result)
}

func writeAgentSessionsList(out io.Writer, result agentSessionsList) error {
	if len(result.Sessions) == 0 {
		_, err := fmt.Fprintf(out, "agent/%s has no recorded sessions\n", result.AgentName)
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tSOURCE\tOBSERVED\tTRANSCRIPT")
	for _, row := range result.Sessions {
		transcript := row.TranscriptPath
		if transcript == "" {
			transcript = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.SessionID, row.Source, row.ObservedAt.UTC().Format(time.RFC3339), transcript)
	}
	return tw.Flush()
}

// runSessionsBackfill is `agent sessions backfill`: the one explicit,
// user-run reader of Claude transcript contents. It scans the top-level
// transcripts read-only and appends one `estimated` history row per session
// whose delivered coordination frames name exactly one target Agent
// (sessionhistory.Backfill). The Registry is only read, for the current refs
// an estimate must never duplicate.
func (c *agentCommand) runSessionsBackfill(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent sessions backfill"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	var dryRun bool
	var output string
	fs.BoolVar(&dryRun, "dry-run", false, "report what would be appended and write nothing")
	fs.StringVar(&output, "output", "", "result projection: json")
	fs.StringVar(&output, "o", "", "result projection: json (alias of --output)")
	positionals, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if len(positionals) != 0 {
		return usageError(fmt.Sprintf("%s takes no arguments; got %q", spelling, positionals[0]))
	}
	if output != "" && output != "json" {
		return usageError(fmt.Sprintf("%s: unsupported output %q; want json", spelling, output))
	}

	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	var current []sessionhistory.Record
	for _, agent := range registry.Agents {
		if row, ok := sessionhistory.RecordFor(agent.Metadata.UID, agent.Status.SessionRef, sessionhistory.SourceCurrent); ok {
			current = append(current, row)
		}
	}
	if c.store == nil || c.store.stateDir == nil {
		return fmt.Errorf("%s: the projmux state directory is not configured", spelling)
	}
	stateDir, err := c.store.stateDir()
	if err != nil {
		return fmt.Errorf("%s: %w", spelling, err)
	}
	projectsDir, err := c.resolveClaudeProjectsDir()
	if err != nil {
		return fmt.Errorf("%s: %w", spelling, err)
	}
	report, err := sessionhistory.Backfill(sessionhistory.BackfillOptions{
		ProjectsDir: projectsDir, StateDir: stateDir, Current: current, DryRun: dryRun,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", spelling, err)
	}
	if output == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	return writeAgentSessionsBackfill(stdout, report)
}

// resolveClaudeProjectsDir is `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects`,
// or the claudeProjectsDir seam when set.
func (c *agentCommand) resolveClaudeProjectsDir() (string, error) {
	if c.claudeProjectsDir != nil {
		return c.claudeProjectsDir()
	}
	lookup := c.lookupEnv
	if lookup == nil {
		lookup = os.Getenv
	}
	homeDir, err := os.UserHomeDir()
	if err != nil && strings.TrimSpace(lookup("CLAUDE_CONFIG_DIR")) == "" {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return sessionhistory.ClaudeProjectsDir(lookup("CLAUDE_CONFIG_DIR"), homeDir)
}

func writeAgentSessionsBackfill(out io.Writer, report sessionhistory.BackfillReport) error {
	verb := "appended"
	if report.DryRun {
		verb = "would append (dry run)"
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "projects\t%s\n", report.ProjectsDir)
	fmt.Fprintf(tw, "history\t%s\n", report.HistoryPath)
	for _, row := range []struct {
		label string
		count int
	}{
		{"scanned", report.Scanned},
		{"attributed", report.Attributed},
		{"alreadyEstimated", report.AlreadyEstimated},
		{"skippedObserved", report.SkippedObserved},
		{"ambiguous", report.Ambiguous},
		{"noFrame", report.NoFrame},
		{"unreadable", report.Unreadable},
		{"malformedLines", report.MalformedLines},
		{"malformedFrames", report.MalformedFrames},
	} {
		fmt.Fprintf(tw, "%s\t%d\n", row.label, row.count)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(report.Rows) == 0 {
		_, err := fmt.Fprintf(out, "%s 0 estimated sessions\n", verb)
		return err
	}
	if _, err := fmt.Fprintf(out, "%s %d estimated session(s):\n", verb, len(report.Rows)); err != nil {
		return err
	}
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSESSION\tFIRST\tLAST\tTRANSCRIPT")
	for _, row := range report.Rows {
		last := "-"
		if row.LastRecordAt != nil {
			last = row.LastRecordAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", row.AgentUID, row.SessionID, row.ObservedAt.UTC().Format(time.RFC3339), last, row.TranscriptPath)
	}
	return tw.Flush()
}
