package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	agentActionSendTurn       = "Send new turn"
	agentActionSteerTurn      = "Steer current turn"
	agentActionInterruptTurn  = "Interrupt current turn"
	agentActionReviewApproval = "Review pending approval"
	agentActionOpenCodex      = "Open Codex"
)

type agentControlLive struct {
	RuntimeID string
	PaneUID   string
	ThreadID  string
	Authority string
	Epoch     string
	Reason    string
}

type agentControlCaller func(context.Context, string, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error)
type agentControlPathResolver func() (config.Paths, error)
type agentControlPicker interface {
	Run(intpicker.Options) (intpicker.Result, error)
}

type agentControlBindingLookup interface {
	Live(context.Context, string) (agentControlLive, bool, error)
}

type tmuxAgentControlBindingLookup struct {
	lookup intmetadata.Mirror
	runner tmuxCommandRunner
}

func (l *tmuxAgentControlBindingLookup) Live(ctx context.Context, paneUID string) (agentControlLive, bool, error) {
	target, found, err := l.lookup.FindPaneTargetForUID(ctx, paneUID)
	if err != nil || !found {
		return agentControlLive{}, found, err
	}
	format := strings.Join([]string{"#{pane_id}", "#{@projmux_pane_uid}", "#{" + aiPaneThreadIDOption + "}", "#{" + aiPaneCodexAuthorityOption + "}", "#{" + aiPaneCodexEpochOption + "}", "#{" + aiPaneCodexReasonOption + "}"}, "\x1f")
	out, err := l.runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, format)
	if err != nil {
		return agentControlLive{}, false, err
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "\x1f")
	if len(fields) != 6 {
		return agentControlLive{}, false, errors.New("live Codex control binding is malformed")
	}
	return agentControlLive{RuntimeID: fields[0], PaneUID: fields[1], ThreadID: fields[2], Authority: fields[3], Epoch: fields[4], Reason: fields[5]}, true, nil
}

type exactAgentControlBinding struct {
	Identity   codexLifecycleIdentity
	Epoch      string
	StateDir   string
	ProjectUID string
	WindowUID  string
}

func resolveExactAgentControlBinding(registry coremetadata.Registry, agent coremetadata.Agent, live agentControlLive, observed bool, stateDir string) (exactAgentControlBinding, error) {
	refusal := func(reason string) (exactAgentControlBinding, error) {
		return exactAgentControlBinding{}, fmt.Errorf("exact Agent native control unavailable: %s", reason)
	}
	if agent.Spec.Provider != aiModeCodex {
		return refusal("the selected Agent is not Codex")
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
		return refusal("the selected Agent has no current Running Pane")
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerUID() != agent.Metadata.UID || pane.Status.Activation.AgentUID != agent.Metadata.UID {
		return refusal("Agent to Pane ownership is not exact")
	}
	threadID := ""
	if pane.Status.Activation.Codex != nil {
		threadID = strings.TrimSpace(pane.Status.Activation.Codex.ThreadID)
	}
	if threadID == "" || pane.Status.Activation.Generation == "" || pane.Status.Activation.RuntimeID == "" {
		return refusal("activation generation or Codex thread identity is missing")
	}
	if !observed || live.RuntimeID != pane.Status.Activation.RuntimeID || live.PaneUID != pane.Metadata.UID || live.ThreadID != threadID {
		return refusal("live Pane identity no longer matches the activation")
	}
	if live.Authority != codexAuthorityControlPlane || strings.TrimSpace(live.Epoch) == "" {
		reason := strings.TrimSpace(live.Reason)
		if reason == "" {
			reason = "not-ready"
		}
		return refusal("the native connection epoch is unavailable (" + reason + "); Open Codex")
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return refusal("owning Window is missing")
	}
	project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return refusal("owning Project is missing")
	}
	return exactAgentControlBinding{
		Identity: codexLifecycleIdentity{AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, RuntimeID: pane.Status.Activation.RuntimeID, Generation: pane.Status.Activation.Generation, ThreadID: threadID},
		Epoch:    live.Epoch, StateDir: stateDir, ProjectUID: project.Metadata.UID, WindowUID: window.Metadata.UID,
	}, nil
}

func (c *agentCommand) resolveControlBinding(spelling, ref string) (exactAgentControlBinding, error) {
	registry, agent, err := c.resolveOneAgent(spelling, ref, selector.VerbReview)
	if err != nil {
		return exactAgentControlBinding{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.controlTimeoutValue())
	defer cancel()
	lookup := c.controlBinding
	if lookup == nil {
		if c.controlRoute == nil {
			return exactAgentControlBinding{}, errors.New("exact Agent native control unavailable: logical tmux route resolver is not configured")
		}
		route, routeErr := c.controlRoute(ctx)
		if routeErr != nil {
			return exactAgentControlBinding{}, fmt.Errorf("exact Agent native control unavailable: resolve logical tmux route: %w", routeErr)
		}
		if c.controlRunner == nil {
			return exactAgentControlBinding{}, errors.New("exact Agent native control unavailable: exact tmux runner is not configured")
		}
		routed := explicitTmuxRunner{runner: c.controlRunner, target: route.target}
		lookup = &tmuxAgentControlBindingLookup{lookup: intmetadata.NewMirror(routed), runner: routed}
	}
	live, observed, err := lookup.Live(ctx, agent.Status.PaneRef)
	if err != nil {
		return exactAgentControlBinding{}, fmt.Errorf("exact Agent native control unavailable: read live binding: %w", err)
	}
	paths, err := c.controlPaths()
	if err != nil {
		return exactAgentControlBinding{}, fmt.Errorf("exact Agent native control unavailable: resolve private control path: %w", err)
	}
	binding, err := resolveExactAgentControlBinding(registry, agent, live, observed, paths.StateDir)
	if err != nil {
		return exactAgentControlBinding{}, addOpenCodexRecovery(err, registry, agent)
	}
	return binding, nil
}

func (c *agentCommand) runTurn(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent turn requires start, steer, or interrupt")
	}
	switch args[0] {
	case "start", "steer":
		before, text, err := splitAgentTurnText(args[1:])
		if err != nil {
			return err
		}
		binding, err := c.resolveControlBinding("agent turn "+args[0], before[0])
		if err != nil {
			return err
		}
		op := agentControlOpStart
		label := agentActionSendTurn
		if args[0] == "steer" {
			op, label = agentControlOpSteer, agentActionSteerTurn
		}
		response, err := c.callControl(binding, agentControlRequest{Operation: op, Text: text})
		if err != nil {
			return err
		}
		if err := response.Error(); err != nil {
			return addOpenCodexBindingRecovery(err, binding)
		}
		_, err = fmt.Fprintf(stdout, "%s thread=%s turn=%s\n", c.agentActionText(label), safeApprovalDetail(response.ThreadID), safeApprovalDetail(response.TurnID))
		return err
	case "interrupt":
		if len(args) != 2 {
			return usageError("agent turn interrupt requires exactly one <agent-ref>")
		}
		binding, err := c.resolveControlBinding("agent turn interrupt", args[1])
		if err != nil {
			return err
		}
		response, err := c.callControl(binding, agentControlRequest{Operation: agentControlOpInterrupt})
		if err != nil {
			return err
		}
		if err := response.Error(); err != nil {
			return addOpenCodexBindingRecovery(err, binding)
		}
		_, err = fmt.Fprintf(stdout, "%s thread=%s turn=%s\n", c.agentActionText(agentActionInterruptTurn), safeApprovalDetail(response.ThreadID), safeApprovalDetail(response.TurnID))
		return err
	default:
		return usageError("agent turn requires start, steer, or interrupt")
	}
}

func splitAgentTurnText(args []string) ([]string, string, error) {
	separator := slices.Index(args, "--")
	if separator != 1 || len(args) != 3 || strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[2]) == "" {
		return nil, "", usageError("agent turn start|steer requires <agent-ref> -- <text>; quote text as one argument")
	}
	return args[:1], args[2], nil
}

func (c *agentCommand) runApproval(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "review" {
		return usageError("agent approval requires review")
	}
	fs := flag.NewFlagSet("agent approval review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var requestID string
	fs.StringVar(&requestID, "request", "", "normalized pending request id")
	refs, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		return usageError(err.Error())
	}
	if len(refs) != 1 {
		return usageError("agent approval review requires exactly one <agent-ref>")
	}
	binding, err := c.resolveControlBinding("agent approval review", refs[0])
	if err != nil {
		return err
	}
	response, err := c.callControl(binding, agentControlRequest{Operation: agentControlOpApprovals})
	if err != nil {
		return err
	}
	if err := response.Error(); err != nil {
		return addOpenCodexBindingRecovery(err, binding)
	}
	pending, err := c.selectPendingApproval(response.Approvals, requestID, stderr)
	if err != nil {
		return addOpenCodexBindingRecovery(err, binding)
	}
	decision, open, err := c.selectApprovalDecision(pending, stderr)
	if err != nil {
		return err
	}
	if open {
		return c.focusExactCodex(binding, stdout, stderr)
	}
	response, err = c.callControl(binding, agentControlRequest{Operation: agentControlOpReview, RequestKey: pending.RequestID, Decision: string(decision)})
	if err != nil {
		return err
	}
	if err := response.Error(); err != nil {
		return addOpenCodexBindingRecovery(err, binding)
	}
	_, err = fmt.Fprintf(stdout, "approval resolved request=%s decision=%s\n", safeApprovalDetail(pending.RequestID), safeApprovalDetail(string(decision)))
	return err
}

func (c *agentCommand) callControl(binding exactAgentControlBinding, request agentControlRequest) (agentControlResponse, error) {
	request.Identity, request.Epoch = binding.Identity, binding.Epoch
	ctx, cancel := context.WithTimeout(context.Background(), c.controlTimeoutValue())
	defer cancel()
	call := c.controlCall
	if call == nil {
		call = callCodexControl
	}
	response, err := call(ctx, binding.StateDir, binding.Identity, request)
	if err != nil {
		return agentControlResponse{}, addOpenCodexBindingRecovery(fmt.Errorf("exact Agent native control unavailable: %w", err), binding)
	}
	return response, nil
}

func (c *agentCommand) controlTimeoutValue() time.Duration {
	if c.controlTimeout > 0 {
		return c.controlTimeout
	}
	return 10 * time.Second
}

func (c *agentCommand) selectPendingApproval(pending []agentPendingApproval, requestID string, stderr io.Writer) (agentPendingApproval, error) {
	if requestID != "" {
		pending = slices.DeleteFunc(pending, func(p agentPendingApproval) bool { return p.RequestID != requestID })
	}
	if len(pending) == 0 {
		return agentPendingApproval{}, errors.New("no exact unresolved approval request matches; response write refused")
	}
	counts := map[string]int{}
	for _, p := range pending {
		counts[p.RequestID]++
	}
	if requestID != "" && counts[requestID] != 1 {
		return agentPendingApproval{}, errors.New("normalized request id is ambiguous across raw scalar identities; response write refused")
	}
	if len(pending) == 1 && counts[pending[0].RequestID] == 1 {
		return pending[0], nil
	}
	entries := []intpickercompat.Entry{}
	for i, p := range pending {
		if counts[p.RequestID] > 1 {
			continue
		}
		entries = append(entries, intpickercompat.Entry{Label: boundApprovalLabel(fmt.Sprintf("%s request=%s item=%s", safeApprovalDetail(string(p.Kind)), safeApprovalDetail(p.RequestID), safeApprovalDetail(p.ItemID))), Value: fmt.Sprintf("%d", i)})
	}
	if len(entries) == 0 {
		return agentPendingApproval{}, errors.New("all pending request ids are ambiguous; Open Codex")
	}
	reviewLabel := c.agentActionText(agentActionReviewApproval)
	result, err := runNativePickerOption(os.UserHomeDir, os.Getenv, c.controlPicker, intpickercompat.Options{UI: switchUIPopup, Entries: entries, Title: reviewLabel, Prompt: reviewLabel + " > ", DisableSearch: false})
	if err != nil {
		return agentPendingApproval{}, err
	}
	var index int
	if _, err := fmt.Sscanf(result.Value, "%d", &index); err != nil || index < 0 || index >= len(pending) {
		return agentPendingApproval{}, errors.New("approval selection closed without an exact request")
	}
	return pending[index], nil
}

func (c *agentCommand) selectApprovalDecision(p agentPendingApproval, stderr io.Writer) (codexappserver.ApprovalDecision, bool, error) {
	entries := []intpickercompat.Entry{{Label: approvalDetailLabel(p), Value: settingsNoopValue}}
	for _, decision := range p.Decisions {
		entries = append(entries, intpickercompat.Entry{Label: approvalDecisionLabelLocale(appLocale(os.UserHomeDir, os.Getenv), p, decision), Value: "decision:" + string(decision)})
	}
	entries = append(entries, intpickercompat.Entry{Label: c.agentActionText(agentActionOpenCodex) + " — focus exact Agent; send no response", Value: "open"})
	reviewLabel := c.agentActionText(agentActionReviewApproval)
	result, err := runNativePickerOption(os.UserHomeDir, os.Getenv, c.controlPicker, intpickercompat.Options{UI: switchUIPopup, Entries: entries, Title: reviewLabel, Prompt: reviewLabel + " > ", DisableSearch: true})
	if err != nil {
		return "", false, err
	}
	if result.Value == "open" {
		return "", true, nil
	}
	if !strings.HasPrefix(result.Value, "decision:") {
		return "", false, errors.New("approval decision picker closed without a decision")
	}
	decision := codexappserver.ApprovalDecision(strings.TrimPrefix(result.Value, "decision:"))
	if !slices.Contains(p.Decisions, decision) {
		return "", false, errors.New("selected approval decision is no longer available")
	}
	return decision, false, nil
}

func approvalDetailLabel(p agentPendingApproval) string {
	base := fmt.Sprintf("%s request=%s thread=%s turn=%s item=%s", p.Kind, safeApprovalDetail(p.RequestID), safeApprovalDetail(p.ThreadID), safeApprovalDetail(p.TurnID), safeApprovalDetail(p.ItemID))
	if p.ApprovalID != nil {
		base += " approvalId=" + safeApprovalDetail(*p.ApprovalID)
	}
	if p.Reason != "" {
		base += " reason=" + safeApprovalDetail(p.Reason)
	}
	switch p.Kind {
	case codexappserver.ApprovalCommand:
		base += " command=" + safeApprovalDetail(p.Command) + " cwd=" + safeApprovalDetail(p.CWD)
		if p.NetworkHost != "" {
			base += " network=" + safeApprovalDetail(p.NetworkProtocol+"://"+p.NetworkHost)
		}
	case codexappserver.ApprovalFileChange:
		if p.GrantRoot != nil {
			base += " unstableGrantRoot=" + safeApprovalDetail(*p.GrantRoot)
		}
	case codexappserver.ApprovalPermissions:
		base += " cwd=" + safeApprovalDetail(p.RequestCWD) + " permissions=" + safeApprovalDetail(string(p.Permissions))
	}
	return boundRenderedText(base, 640)
}

func approvalDecisionLabelLocale(locale i18n.Locale, p agentPendingApproval, decision codexappserver.ApprovalDecision) string {
	target := approvalDecisionTarget(p)
	switch decision {
	case codexappserver.DecisionAccept:
		effect := localizeText(locale, i18n.KeyAgentControlDecisionAccept, "Allow once — only this {kind} request")
		return boundApprovalLabel(strings.ReplaceAll(effect, "{kind}", string(p.Kind)) + "; " + target)
	case codexappserver.DecisionDecline:
		return boundApprovalLabel(localizeText(locale, i18n.KeyAgentControlDecisionDecline, "Decline — deny once and continue the turn") + "; " + target)
	case codexappserver.DecisionCancel:
		return boundApprovalLabel(localizeText(locale, i18n.KeyAgentControlDecisionCancel, "Decline and interrupt — deny once and interrupt this exact turn") + "; " + target)
	case codexappserver.DecisionGrantTurn:
		return boundApprovalLabel(localizeText(locale, i18n.KeyAgentControlDecisionGrant, "Grant requested permissions for this turn — scope=turn; strictAutoReview=null") + "; " + target)
	default:
		return boundApprovalLabel(safeApprovalDetail(string(decision)) + " — unavailable; " + target)
	}
}

func approvalDecisionTarget(p agentPendingApproval) string {
	switch p.Kind {
	case codexappserver.ApprovalCommand:
		target := "command=" + safeApprovalDetail(p.Command) + " cwd=" + safeApprovalDetail(p.CWD)
		if p.NetworkHost != "" || p.NetworkProtocol != "" {
			target += " network=" + safeApprovalDetail(p.NetworkProtocol+"://"+p.NetworkHost)
		}
		return target
	case codexappserver.ApprovalFileChange:
		target := "item=" + safeApprovalDetail(p.ItemID)
		if p.Reason != "" {
			target += " reason=" + safeApprovalDetail(p.Reason)
		}
		if p.GrantRoot != nil {
			target += " unstableGrantRoot=" + safeApprovalDetail(*p.GrantRoot)
		}
		return target
	case codexappserver.ApprovalPermissions:
		return "cwd=" + safeApprovalDetail(p.RequestCWD) + " permissions=" + safeApprovalDetail(string(p.Permissions))
	default:
		return "request=" + safeApprovalDetail(p.RequestID)
	}
}

func safeApprovalDetail(value string) string {
	runes := []rune(value)
	truncated := false
	if len(runes) > 256 {
		runes, truncated = runes[:256], true
	}
	quoted := strconv.QuoteToGraphic(string(runes))
	if truncated {
		quoted += "…[truncated]"
	}
	return boundRenderedText(quoted, 320)
}

func boundApprovalLabel(value string) string { return boundRenderedText(value, 768) }

func boundRenderedText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	suffix := []rune("…[truncated]")
	if limit <= len(suffix) {
		return string(suffix[:limit])
	}
	return string(runes[:limit-len(suffix)]) + string(suffix)
}

func (c *agentCommand) agentActionText(fallback string) string {
	key := map[string]i18n.Key{
		agentActionSendTurn: i18n.KeyAgentControlSendTurn, agentActionSteerTurn: i18n.KeyAgentControlSteerTurn,
		agentActionInterruptTurn: i18n.KeyAgentControlInterruptTurn, agentActionReviewApproval: i18n.KeyAgentControlReviewApproval,
		agentActionOpenCodex: i18n.KeyAgentControlOpenCodex,
	}[fallback]
	if key == "" {
		return fallback
	}
	return localizeText(appLocale(os.UserHomeDir, os.Getenv), key, fallback)
}

func (c *agentCommand) focusExactCodex(binding exactAgentControlBinding, stdout, stderr io.Writer) error {
	if c.focus == nil {
		return fmt.Errorf("%s unavailable: exact focus route is not configured", c.agentActionText(agentActionOpenCodex))
	}
	return c.focus.Run([]string{"pane", "uid:" + binding.Identity.PaneUID, "--project", "uid:" + binding.ProjectUID, "--window", "uid:" + binding.WindowUID}, stdout, stderr)
}

func addOpenCodexBindingRecovery(err error, binding exactAgentControlBinding) error {
	return fmt.Errorf("%w; %s: `projmux focus pane uid:%s --project uid:%s --window uid:%s`", err, agentActionOpenCodex, binding.Identity.PaneUID, binding.ProjectUID, binding.WindowUID)
}

func addOpenCodexRecovery(err error, registry coremetadata.Registry, agent coremetadata.Agent) error {
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok {
		return err
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return err
	}
	project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return err
	}
	return addOpenCodexBindingRecovery(err, exactAgentControlBinding{Identity: codexLifecycleIdentity{PaneUID: pane.Metadata.UID}, ProjectUID: project.Metadata.UID, WindowUID: window.Metadata.UID})
}
