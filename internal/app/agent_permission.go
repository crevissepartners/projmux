package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
)

// Refusal reason tokens of `agent approval list|answer`. Like the question
// tokens they are stable strings, carried verbatim in the error text of a
// usage error.
const (
	// permissionReasonNotFound: no request with that id belongs to the Agent.
	permissionReasonNotFound = "permission-not-found"
	// permissionReasonNotPending: the request was already answered, and the
	// first answer wins. That is an allow or deny from projmux, and equally a
	// request closed because it was answered in Claude Code's own prompt (or
	// its hook was canceled) before a projmux answer arrived.
	permissionReasonNotPending = "permission-not-pending"
	// permissionReasonExpired: the answer window ended; Claude Code's own
	// prompt decides.
	permissionReasonExpired = "permission-expired"
	// permissionReasonAnsweringOff: agent-approval-answering is not projmux.
	permissionReasonAnsweringOff = "permission-answering-off"
	// permissionReasonProviderUnsupported: the Agent is not a Claude Agent.
	permissionReasonProviderUnsupported = "permission-provider-unsupported"
)

// agentApprovalActions are the `agent approval` subcommands in help order.
var agentApprovalActions = []string{"review", "list", "answer"}

// agentPermissionRequest is one parsed `agent approval list|answer` argv.
type agentPermissionRequest struct {
	action    string
	spelling  string
	flags     resourceQueryFlags
	requestID string
	json      bool
	allow     bool
	via       string
}

// runPermissionApproval lists and answers the Claude permission requests the
// PermissionRequest hook `agent integrate claude` installs holds open while the
// central agent-approval-answering setting is `projmux`.
//
// An answer allows or denies that one tool call once. It never changes a
// permission rule, and the operator may already have answered the same request
// in Claude Code's own prompt, which stays usable: the first answer wins, and
// a later one is refused as permission-not-pending. A Codex Agent is refused without any write:
// its approvals go through `agent approval review`.
func (c *agentCommand) runPermissionApproval(args []string, stdout, stderr io.Writer) error {
	request, err := parseAgentPermissionArgs(args, stderr)
	if err != nil {
		return err
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := request.flags.resolve(selector.VerbTopic, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	found, ok := registry.Agent(resolution.Matches[0].UID)
	if !ok {
		return fmt.Errorf("%s: resolved uid %q is no longer in the registry", request.spelling, resolution.Matches[0].UID)
	}
	agent := found.Clone()
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", request.spelling, agent.Metadata.Name, detail, reason))
	}
	switch provider := coremetadata.NormalizeProvider(agent.Spec.Provider); provider {
	case aiModeClaude:
	case aiModeCodex:
		return refuse(permissionReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; Codex approvals are answered with `projmux agent approval review %s`", agent.Spec.Provider, agent.Metadata.Name))
	default:
		return refuse(permissionReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; captured permission requests apply only to --provider %s", agent.Spec.Provider, aiModeClaude))
	}
	answering := c.permissionAnswering()
	if request.action == "list" {
		return c.listPermissionRequests(request, agent, answering, stdout)
	}
	if answering != config.AgentApprovalAnsweringProjmux {
		return refuse(permissionReasonAnsweringOff, "has no captured permission requests while agent-approval-answering is claude; run `projmux config agent-approvals --answering projmux` first")
	}
	return c.answerPermissionRequest(request, agent, refuse, stdout)
}

// parseAgentPermissionArgs parses one `agent approval list|answer` argv. The
// Agent reference is required: the command addresses another Agent's prompt,
// so it never falls back to the active Pane.
func parseAgentPermissionArgs(args []string, stderr io.Writer) (agentPermissionRequest, error) {
	if len(args) == 0 || !slices.Contains([]string{"list", "answer"}, args[0]) {
		return agentPermissionRequest{}, usageError("agent approval requires " + strings.Join(agentApprovalActions, ", "))
	}
	request := agentPermissionRequest{action: args[0], spelling: "agent approval " + args[0]}
	fs := flag.NewFlagSet(request.spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	request.flags = resourceQueryFlags{kind: coremetadata.KindAgent}
	request.flags.register(fs)
	var output string
	var allow, deny bool
	if request.action == "list" {
		fs.StringVar(&output, "output", "", "result projection: json")
		fs.StringVar(&output, "o", "", "result projection: json (alias of --output)")
	}
	if request.action == "answer" {
		fs.BoolVar(&allow, "allow", false, "allow this one tool call once")
		fs.BoolVar(&deny, "deny", false, "deny this one tool call")
		fs.StringVar(&request.via, "via", agentapproval.ViaCLI, "self-reported answer channel: popup, cli, or web (unverified)")
	}
	positionals, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return agentPermissionRequest{}, err
		}
		return agentPermissionRequest{}, flagParseError(err)
	}
	want, shape := 1, "<agent-ref>"
	if request.action == "answer" {
		want, shape = 2, "<agent-ref> <request-id>"
	}
	if len(positionals) != want {
		return agentPermissionRequest{}, usageError(fmt.Sprintf("%s requires %s", request.spelling, shape))
	}
	if output != "" && output != "json" {
		return agentPermissionRequest{}, usageError(fmt.Sprintf("%s: unsupported output %q; want json", request.spelling, output))
	}
	request.json = output == "json"
	request.flags.addPositionalRef(positionals[0])
	if request.action == "answer" {
		request.requestID = strings.TrimSpace(positionals[1])
		if allow == deny {
			return agentPermissionRequest{}, usageError(request.spelling + " requires exactly one of --allow or --deny")
		}
		request.allow = allow
		if !agentapproval.ValidVia(request.via) {
			return agentPermissionRequest{}, usageError(fmt.Sprintf("%s: unknown --via %q; want %s, %s, or %s", request.spelling, request.via, agentapproval.ViaPopup, agentapproval.ViaCLI, agentapproval.ViaWeb))
		}
	}
	return request, nil
}

func (c *agentCommand) openApprovalStore() (*agentapproval.Store, error) {
	if c.approvalStore == nil {
		return nil, errors.New("the agent approval store is not configured")
	}
	store, err := c.approvalStore()
	if err == nil && store == nil {
		return nil, errors.New("the agent approval store is not configured")
	}
	return store, err
}

func (c *agentCommand) permissionAnswering() config.AgentApprovalAnswering {
	if c.approvalAnswering == nil {
		return config.AgentApprovalAnsweringClaude
	}
	return c.approvalAnswering()
}

// agentPermissionList is the `agent approval list -o json` projection.
type agentPermissionList struct {
	AgentUID  string                  `json:"agentUID"`
	AgentName string                  `json:"agentName"`
	Answering string                  `json:"answering"`
	Requests  []agentPermissionRecord `json:"requests"`
}

// agentPermissionRecord is one waiting request with its full tool input.
type agentPermissionRecord struct {
	ID        string              `json:"id"`
	State     agentapproval.State `json:"state"`
	ToolName  string              `json:"toolName"`
	AgentType string              `json:"agentType,omitempty"`
	ToolInput json.RawMessage     `json:"toolInput"`
	CreatedAt time.Time           `json:"createdAt"`
	Deadline  time.Time           `json:"deadline"`
}

// agentPermissionListNote is the caveat every text list ends with.
const agentPermissionListNote = "note: a request may already have been answered in Claude Code's own prompt; the first answer wins\n"

func (c *agentCommand) listPermissionRequests(request agentPermissionRequest, agent coremetadata.Agent, answering config.AgentApprovalAnswering, stdout io.Writer) error {
	store, err := c.openApprovalStore()
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	records, err := store.List(agent.Metadata.UID)
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	result := agentPermissionList{AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Answering: string(answering), Requests: []agentPermissionRecord{}}
	for _, record := range records {
		if record.State != agentapproval.StateWaiting {
			continue
		}
		result.Requests = append(result.Requests, agentPermissionRecord{
			ID: record.ID, State: record.State, ToolName: record.ToolName, AgentType: record.AgentType,
			ToolInput: record.ToolInput, CreatedAt: record.CreatedAt, Deadline: record.Deadline,
		})
	}
	if request.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	return writeAgentPermissionList(stdout, result, c.clock())
}

func writeAgentPermissionList(out io.Writer, result agentPermissionList, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "agent/%s approvals answering %s\n", result.AgentName, result.Answering)
	if len(result.Requests) == 0 {
		b.WriteString("no waiting permission requests\n")
	}
	for _, view := range result.Requests {
		fmt.Fprintf(&b, "%s\t%s\ttool %s", view.ID, view.State, view.ToolName)
		if view.AgentType != "" {
			fmt.Fprintf(&b, "\tsubagent %s", view.AgentType)
		}
		fmt.Fprintf(&b, "\tcreated %s\tdeadline %s (%s left)\n", view.CreatedAt.UTC().Format(time.RFC3339),
			view.Deadline.UTC().Format(time.RFC3339), view.Deadline.Sub(now).Round(time.Second))
		fmt.Fprintf(&b, "  input: %s\n", view.ToolInput)
	}
	b.WriteString(agentPermissionListNote)
	_, err := io.WriteString(out, b.String())
	return err
}

// answerPermissionRequest settles one request under the store lock. The store
// judges the state again under its lock, so of two racing answers exactly one
// lands, and a refusal changes nothing.
func (c *agentCommand) answerPermissionRequest(request agentPermissionRequest, agent coremetadata.Agent, refuse func(string, string) error, stdout io.Writer) error {
	store, err := c.openApprovalStore()
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	answered, err := store.Answer(request.requestID, agent.Metadata.UID, request.allow, request.via)
	if err != nil {
		if reason, detail := permissionStoreRefusal(err); reason != "" {
			return refuse(reason, fmt.Sprintf("permission request %q %s", request.requestID, detail))
		}
		if errors.Is(err, agentapproval.ErrAudit) {
			return fmt.Errorf("%s: %w; permission request %q is still waiting", request.spelling, err, request.requestID)
		}
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	_, err = fmt.Fprintf(stdout, "%s %s for agent/%s\n", answered.ID, answered.State, agent.Metadata.Name)
	return err
}

// permissionStoreRefusal maps a store refusal onto its reason token.
func permissionStoreRefusal(err error) (string, string) {
	switch {
	case errors.Is(err, agentapproval.ErrNotFound):
		return permissionReasonNotFound, "does not exist"
	case errors.Is(err, agentapproval.ErrNotPending):
		return permissionReasonNotPending, "is already answered; the first answer wins"
	case errors.Is(err, agentapproval.ErrExpired):
		return permissionReasonExpired, "expired; Claude Code's own prompt decides it"
	case errors.Is(err, agentapproval.ErrClosed):
		return permissionReasonNotPending, "was closed: answered in Claude Code's own prompt, or its hook ended"
	default:
		return "", ""
	}
}
