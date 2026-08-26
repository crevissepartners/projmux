package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/version"
)

const (
	aiModeSelective   = "selective"
	aiModeResume      = "resume"
	aiModeClaude      = "claude"
	aiModeCodex       = "codex"
	aiModeAntigravity = "antigravity"
	aiModeShell       = "shell"

	aiActionCodexAdvancedLaunch = "codex-advanced-launch"

	aiResumeNewValue = "new"

	aiPaneManagedOption          = "@projmux_ai_managed"
	aiPaneAgentOption            = "@projmux_ai_agent"
	aiPaneLaunchAuthorshipOption = tmuxopts.AgentLaunchAuthorshipPane
	aiPaneContextOption          = "@projmux_ai_context"
	aiPaneStateOption            = "@projmux_ai_state"
	aiPaneBadgeKindOption        = "@projmux_ai_badge_kind"
	aiPaneTopicOption            = "@projmux_ai_topic"
	aiPaneTopicManualOption      = "@projmux_ai_topic_manual"
	aiPaneHookActiveOption       = "@projmux_ai_hook_active"
	aiPaneThreadIDOption         = "@projmux_ai_thread_id"
	aiPaneSessionIDOption        = "@projmux_ai_session_id"
	aiPaneResumeIDOption         = "@projmux_ai_resume_id"
	aiPaneResumeSourceOption     = "@projmux_ai_resume_source"
	aiPaneTranscriptPathOption   = "@projmux_ai_transcript_path"
	aiPaneResumeUpdatedAtOption  = "@projmux_ai_resume_updated_at"

	canonicalCreateTargetClientEnv = "PROJMUX_POPUP_TARGET_CLIENT"

	aiBadgeKindInProgress       = aibadge.InProgress
	aiBadgeKindApprovalRequired = aibadge.ApprovalRequired
	aiBadgeKindInputRequired    = aibadge.InputRequired
	aiBadgeKindResponseComplete = aibadge.ResponseComplete
)

type aiCommandRunner interface {
	Run(options intpickercompat.Options) (intpickercompat.Result, error)
}

type codexCapabilitySession interface {
	Snapshot() corecap.Snapshot
	Refresh(context.Context) (corecap.Snapshot, error)
	Close() error
}

type aiCommand struct {
	runner                        aiCommandRunner
	nativePicker                  intpicker.Runner
	executable                    func() (string, error)
	lookupEnv                     func(string) string
	homeDir                       func() (string, error)
	stdin                         io.Reader
	readFile                      func(string) ([]byte, error)
	writeFile                     func(string, []byte, os.FileMode) error
	mkdirAll                      func(string, os.FileMode) error
	runCommand                    func(ctx context.Context, name string, args ...string) error
	readCommand                   func(ctx context.Context, name string, args ...string) ([]byte, error)
	now                           func() time.Time
	sleep                         func(time.Duration)
	producer                      attentionNotifyProducer
	notifyStore                   notifyStore
	events                        notifyQueueRefreshEvents
	notifyDiagnostics             *diagnostics.NotifyFocusRecorder
	operationalDiagnostics        *diagnostics.AIRecorder
	openCodexCapabilitySession    func(context.Context) (codexCapabilitySession, error)
	openCodexCatalog              aisessions.OpenCodexCatalog
	discoverResumeSummaryProvider func(context.Context, string, string, aisessions.ResumeSummaryOptions, int) (aisessions.ResumeSummaryDiscovery, error)
	readResumeDetail              func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (aisessions.ResumeDetail, error)
	// readResumePreview is retained as a narrow test seam for fixtures that
	// exercise preview timing without the metadata projection.
	readResumePreview          func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (aisessions.Preview, error)
	codexCapabilityCache       *corecap.Cache
	codexCapabilitySessionMu   sync.Mutex
	codexCapabilitySession     codexCapabilitySession
	notifyDeliveryOwnsTopLevel bool
	// loadRegistry and updateRegistry are the resource registry seam the hook
	// ingest path uses to persist the provider session ref onto the Agent. Both
	// are nil unless explicitly wired, and a nil seam disables the write
	// entirely: the many fixtures that build an aiCommand literal must never
	// reach the real state directory just because a hook was ingested.
	loadRegistry   func() (coremetadata.Registry, error)
	updateRegistry func(func(*coremetadata.Registry) error) (coremetadata.Registry, error)
	// panes is the canonical create route the Projmux split UI hands its intents
	// to. It is nil unless explicitly wired; see createPaneFromIntent for why an
	// unwired seam fails loudly instead of quietly creating nothing.
	panes canonicalPaneCreator
	// A hook's session ref and semantic interaction are staged until the event
	// has been classified, then committed in one Registry transaction. Quiet
	// events flush only the session ref at the top-level ingest return.
	agentObservationMu      sync.Mutex
	pendingAgentSessionRefs map[string]coremetadata.AgentSessionObservation
	pendingCodexBindings    map[string]coremetadata.CodexActivationObservation
}

func newAICommand() *aiCommand {
	return &aiCommand{
		nativePicker:                  intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		executable:                    resolveExecutablePath,
		lookupEnv:                     os.Getenv,
		homeDir:                       os.UserHomeDir,
		stdin:                         os.Stdin,
		readFile:                      os.ReadFile,
		writeFile:                     os.WriteFile,
		mkdirAll:                      os.MkdirAll,
		runCommand:                    runExternalCommand,
		readCommand:                   readExternalCommand,
		openCodexCatalog:              aisessions.NewDefaultCodexCatalogOpener(version.String()),
		discoverResumeSummaryProvider: aisessions.DiscoverResumeSummariesContext,
		readResumeDetail:              aisessions.ReadResumeDetail,
		now:                           time.Now,
		sleep:                         time.Sleep,
		producer:                      newAttentionNotifyProducer(),
		openCodexCapabilitySession: func(ctx context.Context) (codexCapabilitySession, error) {
			return codexappserver.OpenDefaultCapabilitySession(ctx, version.String())
		},
		codexCapabilityCache: &corecap.Cache{},
		// The read is the zero-side-effect LoadReadOnly path and the write is
		// the store's locked read -> mutate -> validate -> atomic replace
		// transaction, so ingest can never create or corrupt the registry.
		loadRegistry:   loadResourceRegistry,
		updateRegistry: updateResourceRegistry,
	}
}

// newSettingsAIFallback builds the minimally wired aiCommand Settings falls
// back to when no ai dependency was injected. It lives next to aiCommand so
// the settings files never construct the struct directly.
func newSettingsAIFallback(homeDir func() (string, error), lookupEnv func(string) string) *aiCommand {
	return &aiCommand{homeDir: homeDir, lookupEnv: lookupEnv}
}

// notifyProducer returns the wired-up producer or a noop when the command
// was constructed without one (the test fixtures build aiCommand structs
// directly).
func (c *aiCommand) notifyProducer() attentionNotifyProducer {
	if c == nil || c.producer == nil {
		return noopAttentionNotifyProducer{}
	}
	return c.producer
}

// notifyLookup adapts aiCommand's existing read helpers to the producer
// lookup contract. It uses the same readTmuxPaneOption/readTrimmed surface
// the rest of the AI flow already uses.
func (c *aiCommand) notifyLookup() attentionNotifyLookup {
	return aiNotifyLookup{cmd: c}
}

type aiNotifyLookup struct {
	cmd *aiCommand
}

func (l aiNotifyLookup) PaneOption(paneID, option string) string {
	if l.cmd == nil {
		return ""
	}
	return l.cmd.readTmuxPaneOption(paneID, option)
}

func (l aiNotifyLookup) PaneFormat(paneID, format string) string {
	if l.cmd == nil {
		return ""
	}
	return l.cmd.readTmuxDisplayMessageTrimmed(paneID, format)
}

func (c *aiCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai requires a subcommand")
	}

	switch args[0] {
	case "launch-default":
		return c.runLaunchDefault(args[1:], stderr)
	case "launch-provider":
		return c.runDirectProvider(args[1:], stderr)
	case "launch-shell":
		return c.runDirectShell(args[1:], stderr)
	case "picker":
		return c.runPicker(args[1:], stderr)
	case "settings":
		return c.runSettings(args[1:], stdout, stderr)
	case "status":
		return c.runStatus(args[1:], stderr)
	case "notify":
		return c.runNotify(args[1:], stderr)
	case "watch-title":
		return c.runWatchTitle(args[1:], stderr)
	case "ingest":
		return c.runIngest(args[1:], stdout, stderr)
	case "integrate":
		return c.runIntegrate(args[1:], stdout, stderr)
	case "topic":
		return c.runTopic(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printAIUsage(stdout)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai subcommand: %s", args[0])
	}
}

func (c *aiCommand) runDirectProvider(args []string, stderr io.Writer) error {
	if len(args) != 2 {
		printAIUsage(stderr)
		return usageError("internal agent-pane launch-provider requires <provider> <right|down>")
	}
	direction, err := parseAISplitDirection(args[1:], "internal agent-pane launch-provider", stderr)
	if err != nil {
		return err
	}
	provider, err := requireCanonicalProvider(canonicalCreateAgent, args[0])
	if err != nil {
		return err
	}
	if err := c.requireAIAgentEnabled(provider, aiSplitLaunchCanonical); err != nil {
		return err
	}
	return c.createAgentPane(canonicalProducerDirectProvider, provider, direction)
}

func (c *aiCommand) runDirectShell(args []string, stderr io.Writer) error {
	direction, err := parseAISplitDirection(args, "internal agent-pane launch-shell", stderr)
	if err != nil {
		return err
	}
	return c.createShellPane(canonicalProducerDirectShell, direction)
}

func (c *aiCommand) runStatus(args []string, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai status requires a subcommand")
	}
	switch args[0] {
	case "set":
		if len(args) < 2 || len(args) > 3 {
			printAIUsage(stderr)
			return errors.New("ai status set requires <thinking|waiting|idle> [pane]")
		}
		paneID := strings.TrimSpace(c.env("TMUX_PANE"))
		if len(args) == 3 {
			paneID = strings.TrimSpace(args[2])
		}
		return c.applyAIStatus(args[1], paneID)
	case "help", "--help", "-h":
		printAIUsage(stderr)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai status subcommand: %s", args[0])
	}
}

func (c *aiCommand) applyAIStatus(state, paneID string) error {
	return c.applyAIStatusInternalWithSource(state, paneID, attentionNotifyInput{}, true, true, string(coremetadata.InteractionSourceCompatibilityAI), true)
}

func (c *aiCommand) applyAIStatusWithNotify(state, paneID string, notifyIn attentionNotifyInput) error {
	return c.applyAIStatusInternalWithSource(state, paneID, notifyIn, true, true, string(coremetadata.InteractionSourceProviderHook), true)
}

func (c *aiCommand) applyAIStatusWithBadgeKind(state, paneID, badgeKind string) error {
	if c.resourceOwnedPane(paneID) {
		return nil
	}
	return c.applyAIStatusInternalWithSource(state, paneID, attentionNotifyInput{BadgeKind: badgeKind}, true, true, "", false)
}

func (c *aiCommand) applyAIStatusStateOnly(state, paneID string, notifyIn attentionNotifyInput) error {
	return c.applyAIStatusInternalWithSource(state, paneID, notifyIn, false, false, string(coremetadata.InteractionSourceProviderHook), true)
}

func (c *aiCommand) applyAIStatusQueueOnly(state, paneID string, notifyIn attentionNotifyInput) error {
	notifyIn.SuppressHooks = true
	return c.applyAIStatusInternalWithSource(state, paneID, notifyIn, true, false, string(coremetadata.InteractionSourceProviderHook), true)
}

func (c *aiCommand) applyAIStatusQueueOnlyWithoutActivation(state, paneID string, notifyIn attentionNotifyInput) error {
	notifyIn.SuppressHooks = true
	return c.applyAIStatusInternalWithActivationPolicy(state, paneID, notifyIn, true, false,
		string(coremetadata.InteractionSourceProviderHook), true, false)
}

func (c *aiCommand) applyAIStatusInternalWithSource(state, paneID string, notifyIn attentionNotifyInput, dispatchQueue, dispatchDesktop bool, source string, persist bool) error {
	return c.applyAIStatusInternalWithActivationPolicy(state, paneID, notifyIn, dispatchQueue, dispatchDesktop, source, persist, true)
}

func (c *aiCommand) applyAIStatusInternalWithActivationPolicy(state, paneID string, notifyIn attentionNotifyInput, dispatchQueue, dispatchDesktop bool, source string, persist, activationEligible bool) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}
	notifyIn.PaneID = paneID
	if notifyIn.Lookup == nil {
		notifyIn.Lookup = c.notifyLookup()
	}

	state = strings.TrimSpace(state)
	badgeKind := aiBadgeKindForStatus(state, notifyIn.BadgeKind)
	kind := semanticInteractionForAIStatus(state, badgeKind)
	managed := false
	if persist {
		committed, isManaged, err := c.persistManagedAgentInteractionWithActivationPolicy(paneID, kind, source, activationEligible)
		if err != nil {
			if errors.Is(err, errManagedAgentObservationIgnored) {
				return nil
			}
			return err
		}
		managed = isManaged
		if managed {
			if err := c.projectManagedAgentInteraction(paneID, kind); err != nil {
				return committedMirrorError("ai status", coremetadata.KindAgent, committed.Metadata.UID, err)
			}
		}
	}
	switch state {
	case "thinking":
		if !managed {
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "thinking")
			c.setAIPaneBadgeKind(paneID, badgeKind)
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, attentionStateOption, attentionStateBusy)
		}
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionAckOption)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionFocusArmedOption)
		c.notifyProducer().AckReplyReady(notifyIn)
	case "waiting":
		if !managed {
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "waiting")
			c.setAIPaneBadgeKind(paneID, badgeKind)
		}
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionAckOption)
		visible := c.paneVisibleToClient(paneID)
		if visible {
			// Badge follows visibility only: pane is already in front of the
			// user, so auto-ack the reply badge regardless of Force.
			_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionStateOption)
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, attentionAckOption, "1")
			_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionFocusArmedOption)
		} else {
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, attentionStateOption, attentionStateReply)
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, attentionFocusArmedOption, "1")
		}
		// Force controls notification delivery, not the badge.
		if dispatchQueue && (notifyIn.Force || !visible) {
			if dispatchDesktop {
				_ = c.notifyAIWithInput(paneID, notifyIn)
			}
			c.notifyProducer().PushReplyReady(notifyIn)
		} else {
			if dispatchQueue && visible && !notifyIn.Force && c.notifyDiagnostics != nil {
				labels := notifyLabels(notify.SourceAI, notifyIn.Metadata)
				c.notifyDiagnostics.RecordNotify(diagnostics.TransitionNotifyDelivery, diagnostics.DispositionSuppressed, labels.provider, labels.category, diagnostics.RouteVisiblePane, "", c.now(), false)
			}
			c.notifyProducer().AckReplyReady(notifyIn)
		}
	case "idle", "":
		if !managed {
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "idle")
			c.setAIPaneBadgeKind(paneID, badgeKind)
			_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionStateOption)
		}
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionFocusArmedOption)
		c.notifyProducer().AckReplyReady(notifyIn)
	default:
		return fmt.Errorf("unknown ai status state: %s", state)
	}
	return nil
}

func (c *aiCommand) projectManagedAgentInteraction(paneID string, kind coremetadata.AgentInteractionKind) error {
	state, badge, attention := agentTmuxProjection(kind)
	for _, field := range []struct{ option, value string }{
		{aiPaneStateOption, state},
		{aiPaneBadgeKindOption, badge},
		{attentionStateOption, attention},
	} {
		args := []string{"set-option", "-p", "-t", paneID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", paneID, field.option}
		}
		if err := c.run("tmux", args...); err != nil {
			return err
		}
	}
	return nil
}

func semanticInteractionForAIStatus(state, badge string) coremetadata.AgentInteractionKind {
	switch normalizeAIBadgeKind(badge) {
	case aiBadgeKindInProgress:
		return coremetadata.InteractionInProgress
	case aiBadgeKindApprovalRequired:
		return coremetadata.InteractionApprovalRequired
	case aiBadgeKindInputRequired:
		return coremetadata.InteractionInputRequired
	case aiBadgeKindResponseComplete:
		return coremetadata.InteractionResponseComplete
	}
	switch strings.TrimSpace(state) {
	case "thinking":
		return coremetadata.InteractionInProgress
	case "idle", "":
		return coremetadata.InteractionIdle
	default:
		return coremetadata.InteractionUnknown
	}
}

func (c *aiCommand) setAIPaneBadgeKind(paneID, kind string) {
	kind = normalizeAIBadgeKind(kind)
	if kind == "" {
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, aiPaneBadgeKindOption)
		return
	}
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneBadgeKindOption, kind)
}

func (c *aiCommand) runNotify(args []string, stderr io.Writer) error {
	action := "notify"
	paneID := strings.TrimSpace(c.env("TMUX_PANE"))
	switch len(args) {
	case 0:
	case 1:
		if args[0] == "notify" || args[0] == "reset" {
			action = args[0]
		} else {
			paneID = strings.TrimSpace(args[0])
		}
	case 2:
		action = args[0]
		paneID = strings.TrimSpace(args[1])
	default:
		printAIUsage(stderr)
		return errors.New("ai notify accepts [notify|reset] [pane]")
	}

	switch action {
	case "reset":
		return c.resetAINotification(paneID)
	case "notify":
		previous := c.notifyDeliveryOwnsTopLevel
		c.notifyDeliveryOwnsTopLevel = true
		defer func() { c.notifyDeliveryOwnsTopLevel = previous }()
		return c.notifyAIForce(paneID)
	case "help", "--help", "-h":
		printAIUsage(stderr)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai notify action: %s", action)
	}
}

func (c *aiCommand) resetAINotification(paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return nil
	}
	_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, "@projmux_desktop_notified")
	_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, "@projmux_desktop_notification_key")
	_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, "@projmux_desktop_notification_at")
	return nil
}

func (c *aiCommand) notifyAI(paneID string) error {
	return c.notifyAIWithMode(paneID, false)
}

func (c *aiCommand) notifyAIForce(paneID string) error {
	return c.notifyAIWithMode(paneID, true)
}

func (c *aiCommand) notifyAIWithInput(paneID string, in attentionNotifyInput) error {
	if strings.TrimSpace(in.Text) == "" {
		return c.notifyAI(paneID)
	}
	return c.notifyAITextWithMetadata(paneID, in.Text, in.Severity, in.Force, in.Metadata)
}

func (c *aiCommand) notifyAITextWithMetadata(paneID, text, severity string, force bool, metadata map[string]string) error {
	paneID = strings.TrimSpace(paneID)
	text = strings.TrimSpace(text)
	if paneID == "" || text == "" {
		return nil
	}
	key := aiNotificationKey("hook", text)
	if !force && c.duplicateAINotificationRecent(paneID, key) {
		labels := notifyLabels(notify.SourceAI, metadata)
		notifyDeliveryDiagnostics{recorder: c.notifyDiagnostics, provider: labels.provider, category: labels.category, ownsTopLevel: c.notifyDeliveryOwnsTopLevel}.
			record(diagnostics.DispositionSuppressed, diagnostics.RouteDedupe, "", c.now())
		c.recordAINotification(paneID, key)
		return nil
	}
	notification := c.aiTextNotificationWithMetadata(paneID, text, severity, metadata)
	if err := c.notificationNotifier().Notify(notification); err != nil {
		return nil
	}
	c.recordAINotification(paneID, key)
	return nil
}

func (c *aiCommand) aiTextNotificationWithMetadata(paneID, text, severity string, metadata map[string]string) aiNotification {
	sessionName := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#S")
	windowName := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#W")
	panePath := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_current_path}")
	agent := aiNotificationTextAgentWithMetadata(text, metadata)
	rendered := renderAINotifyText(text, metadata, c.locale())
	summary := rendered.Summary
	bodyTitle := rendered.Detail
	if summary == "" {
		summary = strings.TrimSpace(text)
	}
	return aiNotification{
		Summary:            summary,
		Body:               aiNotificationBody(bodyTitle, aiProjectName(panePath), c.gitBranchForPath(panePath), sessionName, windowName),
		Urgency:            aiOSNotificationUrgency(severity),
		ExpireMS:           c.notificationExpireMS(),
		AppName:            desktopAppID,
		Icon:               c.notificationIcon(agent),
		Tag:                paneID,
		Group:              sessionName,
		diagnosticProvider: notifyLabels(notify.SourceAI, metadata).provider,
		diagnosticCategory: notifyLabels(notify.SourceAI, metadata).category,
	}
}

func (c *aiCommand) notifyAIWithMode(paneID string, force bool) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}
	info := c.readAIPaneInfo(paneID)
	sessionName := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#S")
	windowName := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#W")
	panePath := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_current_path}")
	agentName := aiAgentDisplayName(info.agent)
	if agentName == "AI" {
		agentName = aiAgentDisplayName(info.title)
	}
	cleanTitle := info.topic
	if cleanTitle == "" {
		cleanTitle = displayAITopic(info.title)
	}
	replyEvidence := strings.Join([]string{info.topic, info.title, info.capture}, "\n")
	replyKind := aiReplyKindForTitle(replyEvidence)
	key := aiNotificationKey(replyKind, defaultString(cleanTitle, info.title))
	if !force && c.duplicateAINotificationRecent(paneID, key) {
		labels := notifyLabels(notify.SourceAI, map[string]string{notify.MetaAgent: info.agent, notify.MetaCategory: replyKind})
		notifyDeliveryDiagnostics{recorder: c.notifyDiagnostics, provider: labels.provider, category: labels.category, ownsTopLevel: c.notifyDeliveryOwnsTopLevel}.
			record(diagnostics.DispositionSuppressed, diagnostics.RouteDedupe, "", c.now())
		c.recordAINotification(paneID, key)
		return nil
	}

	notification := aiNotification{
		Summary:            aiSummaryForKindLocale(replyKind, agentName, cleanTitle, c.locale()),
		Body:               aiNotificationBody(cleanTitle, aiProjectName(panePath), c.gitBranchForPath(panePath), sessionName, windowName),
		Urgency:            aiOSNotificationUrgency(replyKind),
		ExpireMS:           c.notificationExpireMS(),
		AppName:            desktopAppID,
		Icon:               c.notificationIcon(agentName),
		Tag:                paneID,
		Group:              sessionName,
		diagnosticProvider: notifyLabels(notify.SourceAI, map[string]string{notify.MetaAgent: info.agent}).provider,
		diagnosticCategory: notifyCategory(replyKind),
	}
	if err := c.notificationNotifier().Notify(notification); err != nil {
		return nil
	}
	c.recordAINotification(paneID, key)
	return nil
}

func (c *aiCommand) runWatchTitle(args []string, stderr io.Writer) error {
	if len(args) > 1 {
		printAIUsage(stderr)
		return errors.New("ai watch-title accepts at most 1 [pane] argument")
	}
	paneID := strings.TrimSpace(c.env("TMUX_PANE"))
	if len(args) == 1 {
		paneID = strings.TrimSpace(args[0])
	}
	if paneID == "" {
		return nil
	}
	// Resource-owned Agents are hook/Registry driven. A title watcher would
	// read pane content and could manufacture semantic state or activation from
	// presentation text, so fail closed before the first title/capture read.
	if c.resourceOwnedPane(paneID) {
		return nil
	}
	started := c.now()
	c.recordAIWatcher(diagnostics.AIResultStarted, "", started, false)

	interval := c.watchInterval()
	settleLimit := c.watchSettleLoops()
	phase := "idle"
	lastState := ""
	settleCount := 0
	lastBusySignal := ""
	for {
		// A legacy watcher may already be running when a Pane is adopted into
		// resource identity. Re-check before every sample so it cannot read title
		// or capture content, write semantic options, or satisfy activation after
		// that boundary changes.
		if c.resourceOwnedPane(paneID) {
			return nil
		}
		alive, hookActive := c.readAIWatchTitleGate(paneID)
		if !alive {
			c.recordAIWatcher(diagnostics.AIResultPaneGone, "", started, true)
			return nil
		}
		if hookActive {
			c.recordAIWatcher(diagnostics.AIResultHookActive, "", started, true)
			return nil
		}
		snapshot := c.readAIWatchSnapshot(paneID)
		snapshot = c.bootstrapAIWatchMetadata(paneID, snapshot)
		nextState := "idle"
		nextBadgeKind := ""
		busy, busySignal := aiBusySignal(snapshot.title, snapshot.capture)
		replyEvidence := strings.Join([]string{snapshot.title, latestAIPaneCaptureLine(snapshot.capture)}, "\n")
		switch {
		case busy:
			phase = "busy"
			nextState = "thinking"
			nextBadgeKind = aiBadgeKindInProgress
			if busySignal == lastBusySignal {
				settleCount++
				if settleCount >= settleLimit {
					phase = "replied"
					nextState = "waiting"
					nextBadgeKind = aiBadgeKindResponseComplete
					lastBusySignal = ""
				}
			} else {
				settleCount = 0
				lastBusySignal = busySignal
			}
		case snapshot.ack != "1" && isAIReplyTitle(replyEvidence):
			phase = "replied"
			settleCount = 0
			lastBusySignal = ""
			nextState = "waiting"
			nextBadgeKind = aiBadgeKindForReplyEvidence(replyEvidence)
		case snapshot.ack != "1" && snapshot.attentionState == attentionStateBusy:
			phase = "replied"
			settleCount = 0
			lastBusySignal = ""
			nextState = "waiting"
			nextBadgeKind = aiBadgeKindResponseComplete
		case snapshot.ack != "1" && (snapshot.aiState == "waiting" || snapshot.attentionState == attentionStateReply):
			phase = "replied"
			settleCount = 0
			lastBusySignal = ""
			nextState = "waiting"
			nextBadgeKind = defaultString(normalizeAIBadgeKind(snapshot.aiBadgeKind), aiBadgeKindResponseComplete)
		case phase == "busy":
			settleCount++
			if settleCount >= settleLimit {
				phase = "replied"
				nextState = "waiting"
				nextBadgeKind = aiBadgeKindResponseComplete
				lastBusySignal = ""
			} else {
				nextState = "thinking"
				nextBadgeKind = aiBadgeKindInProgress
			}
		case phase == "replied" && snapshot.ack != "1":
			settleCount = 0
			lastBusySignal = ""
			nextState = "waiting"
			nextBadgeKind = aiBadgeKindResponseComplete
		default:
			settleCount = 0
			lastBusySignal = ""
		}

		if nextState == "waiting" {
			c.recordAITopic(paneID, bestAITopic(snapshot.title, snapshot.capture), snapshot.topicManual)
		}
		if nextState != lastState || aiAttentionMismatch(nextState, snapshot.attentionState) || snapshot.aiState != nextState || aiBadgeKindMismatch(nextState, nextBadgeKind, snapshot.aiBadgeKind) {
			_ = c.applyAIStatusWithBadgeKind(nextState, paneID, nextBadgeKind)
			lastState = nextState
		}
		c.sleepFor(interval)
	}
}

func (c *aiCommand) runTopic(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai topic requires a subcommand")
	}
	switch args[0] {
	case "set":
		rest, paneID, err := parseAITopicArgs(args[1:])
		if err != nil {
			printAIUsage(stderr)
			return err
		}
		if len(rest) == 0 {
			printAIUsage(stderr)
			return errors.New("ai topic set requires <text>")
		}
		if len(rest) > 1 {
			printAIUsage(stderr)
			return errors.New("ai topic set accepts a single <text> argument")
		}
		text := strings.TrimSpace(rest[0])
		if text == "" {
			printAIUsage(stderr)
			return errors.New("ai topic set requires non-empty <text>")
		}
		paneID, err = c.resolveTopicPaneID(paneID)
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return err
		}
		if committed, managed, err := c.persistManagedAgentTopic(paneID, text); managed || err != nil {
			if err != nil {
				return err
			}
			if err := c.projectManagedAgentTopic(paneID, text); err != nil {
				return committedMirrorError("ai topic", coremetadata.KindAgent, committed.Metadata.UID, err)
			}
			return nil
		}
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, text)
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicManualOption, "on")
		return nil
	case "clear":
		rest, paneID, err := parseAITopicArgs(args[1:])
		if err != nil {
			printAIUsage(stderr)
			return err
		}
		if len(rest) > 0 {
			printAIUsage(stderr)
			return errors.New("ai topic clear takes no positional arguments")
		}
		paneID, err = c.resolveTopicPaneID(paneID)
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return err
		}
		if committed, managed, err := c.persistManagedAgentTopic(paneID, ""); managed || err != nil {
			if err != nil {
				return err
			}
			if err := c.projectManagedAgentTopic(paneID, ""); err != nil {
				return committedMirrorError("ai topic", coremetadata.KindAgent, committed.Metadata.UID, err)
			}
			return nil
		}
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, aiPaneTopicOption)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, aiPaneTopicManualOption)
		return nil
	case "get":
		rest, paneID, err := parseAITopicArgs(args[1:])
		if err != nil {
			printAIUsage(stderr)
			return err
		}
		if len(rest) > 0 {
			printAIUsage(stderr)
			return errors.New("ai topic get takes no positional arguments")
		}
		paneID, err = c.resolveTopicPaneID(paneID)
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return err
		}
		if agent, managed, err := c.managedAgentForPane(paneID); err != nil {
			return err
		} else if managed {
			fmt.Fprintln(stdout, strings.TrimSpace(agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic]))
			return nil
		}
		fmt.Fprintln(stdout, c.readTmuxPaneOption(paneID, aiPaneTopicOption))
		return nil
	case "help", "--help", "-h":
		printAIUsage(stdout)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai topic subcommand: %s", args[0])
	}
}

func (c *aiCommand) projectManagedAgentTopic(paneID, topic string) error {
	for _, field := range []struct{ option, value string }{
		{aiPaneTopicOption, strings.TrimSpace(topic)},
		{aiPaneTopicManualOption, map[bool]string{true: "on"}[strings.TrimSpace(topic) != ""]},
	} {
		args := []string{"set-option", "-p", "-t", paneID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", paneID, field.option}
		}
		if err := c.run("tmux", args...); err != nil {
			return err
		}
	}
	return nil
}

func parseAITopicArgs(args []string) ([]string, string, error) {
	rest := make([]string, 0, len(args))
	paneID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pane":
			if i+1 >= len(args) {
				return nil, "", errors.New("--pane requires a value")
			}
			paneID = strings.TrimSpace(args[i+1])
			i++
		default:
			if value, ok := strings.CutPrefix(args[i], "--pane="); ok {
				paneID = strings.TrimSpace(value)
				continue
			}
			rest = append(rest, args[i])
		}
	}
	return rest, paneID, nil
}

func (c *aiCommand) resolveTopicPaneID(explicit string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit, nil
	}
	if envPane := strings.TrimSpace(c.env("TMUX_PANE")); envPane != "" {
		return envPane, nil
	}
	if pane := c.readTrimmed("tmux", "display-message", "-p", "#{pane_id}"); pane != "" {
		return pane, nil
	}
	return "", errors.New("ai topic requires a tmux pane (set --pane or run inside tmux)")
}

type aiSplitLaunchPath string

const (
	// aiSplitLaunchCanonical is the resource-backed `create agent` route.
	aiSplitLaunchCanonical aiSplitLaunchPath = "canonical"
	// aiSplitLaunchDefault is the saved-default split binding, whose message names
	// the saved mode because that is the thing the operator has to change.
	aiSplitLaunchDefault aiSplitLaunchPath = "default"
	// aiSplitLaunchPicker is the Alt-7 and resume pickers.
	aiSplitLaunchPicker aiSplitLaunchPath = "picker"
)

// runLaunchDefault answers the saved-default split binding.
//
// It is the one producer whose result depends on hidden state -- the saved mode
// file -- which is exactly why the canonical `create agent` route refuses to
// consult it: a canonical route whose outcome depends on something the operator
// cannot see in the argv is not canonical. This route is where that lookup is
// allowed to live, and its whole job is to turn the saved mode into one of the
// intents or pickers the rest of the UI already has.
func (c *aiCommand) runLaunchDefault(args []string, stderr io.Writer) error {
	direction, err := parseAISplitDirection(args, "internal agent-pane launch-default", stderr)
	if err != nil {
		return err
	}
	mode := c.getMode()
	switch mode {
	case aiModeClaude, aiModeCodex, aiModeAntigravity:
		// The Settings gate runs before the intent is built. A saved default that
		// has since been switched off fails clearly rather than falling back to
		// another provider, and it costs zero Registry and zero tmux mutations.
		if err := c.requireAIAgentEnabled(mode, aiSplitLaunchDefault); err != nil {
			return err
		}
		return c.createAgentPane(canonicalProducerSavedDefault, mode, direction)
	case aiModeShell:
		return c.createShellPane(canonicalProducerSavedDefault, direction)
	case aiModeResume:
		return c.openResumePickerToggle(direction)
	default:
		// aiModeSelective and any unrecognized saved value open the picker. The
		// picker is the honest answer to "the saved default does not name a
		// launch", and it is what an unset mode has always meant.
		return c.openPickerToggle(direction)
	}
}

func (c *aiCommand) runPicker(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai picker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inside := fs.Bool("inside", false, "run inside an already-open popup")
	shellOnly := fs.Bool("shell", false, "open a plain shell split")
	resumeOnly := fs.Bool("resume", false, "open the resume session picker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	direction, err := parseAISplitDirection(fs.Args(), "ai picker", stderr)
	if err != nil {
		return err
	}
	if *shellOnly && *resumeOnly {
		printAIUsage(stderr)
		return errors.New("ai picker cannot combine --shell and --resume")
	}
	if *shellOnly {
		return c.createShellPane(canonicalProducerDirectShell, direction)
	}
	if *resumeOnly {
		return c.runResumePicker(direction)
	}
	if !*inside && c.env("TMUX") != "" {
		return c.openPicker(direction)
	}

	return c.runAgentPickerSelection(direction)
}

func (c *aiCommand) runAgentPickerSelection(direction string) error {
	result, err := c.runAgentPicker(direction)
	if err != nil {
		if isNoSelectionExit(err) {
			return nil
		}
		return err
	}
	if result.Value == "" || result.Key != "enter" {
		return nil
	}

	selected := strings.TrimSpace(result.Value)
	if selected == aiActionCodexAdvancedLaunch {
		if err := c.requireAIAgentEnabled(aiModeCodex, aiSplitLaunchPicker); err != nil {
			return err
		}
		selection, picked, err := c.runCodexCapabilityPicker()
		if err != nil {
			return err
		}
		if !picked || strings.TrimSpace(selection.ModelID) == "" {
			return nil
		}
		defer c.discardCodexCapabilitySession(selection.Epoch)
		return c.createCodexCapabilityAgentPane(canonicalProducerProviderPicker, direction, selection)
	}

	mode := normalizeAIMode(selected)
	switch mode {
	case aiModeCodex:
		if err := c.requireAIAgentEnabled(mode, aiSplitLaunchPicker); err != nil {
			return err
		}
		return c.createAgentPane(canonicalProducerProviderPicker, mode, direction)
	case aiModeClaude, aiModeAntigravity:
		if err := c.requireAIAgentEnabled(mode, aiSplitLaunchPicker); err != nil {
			return err
		}
		return c.createAgentPane(canonicalProducerProviderPicker, mode, direction)
	case aiModeShell:
		return c.createShellPane(canonicalProducerProviderPicker, direction)
	default:
		return nil
	}
}

const codexCapabilityPickerTimeout = 20 * time.Second

type codexAdvancedUnavailableError struct {
	reason string
}

func (e codexAdvancedUnavailableError) Error() string {
	return "Codex advanced launch unavailable: " + e.reason
}

func (e codexAdvancedUnavailableError) Unwrap() error {
	return corecap.ErrUnavailable
}

func codexAdvancedUnavailable(reason string, err error) error {
	reason = strings.TrimSpace(reason)
	if err != nil {
		reason = strings.TrimSpace(err.Error())
		reason = strings.TrimSpace(strings.TrimPrefix(reason, corecap.ErrUnavailable.Error()+":"))
	}
	if reason == "" {
		reason = "capability discovery failed"
	}
	return codexAdvancedUnavailableError{reason: reason}
}

// runCodexCapabilityPicker is entered only through the explicit advanced
// action. Discovery failure is therefore a refusal, not permission to replace
// the user's selected action with a default launch.
func (c *aiCommand) runCodexCapabilityPicker() (corecap.Selection, bool, error) {
	if c.openCodexCapabilitySession == nil || c.nativePicker == nil {
		return corecap.Selection{}, false, codexAdvancedUnavailable("capability discovery is not configured", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexCapabilityPickerTimeout)
	defer cancel()
	session, err := c.openCodexCapabilitySession(ctx)
	if err != nil || session == nil {
		c.replaceCodexCapabilitySession(nil)
		if c.codexCapabilityCache != nil {
			c.codexCapabilityCache.Invalidate()
		}
		if err != nil {
			return corecap.Selection{}, false, codexAdvancedUnavailable("", err)
		}
		return corecap.Selection{}, false, codexAdvancedUnavailable("capability discovery returned no session", nil)
	}
	snapshot := session.Snapshot()
	if !snapshot.Epoch.Valid() {
		_ = session.Close()
		c.replaceCodexCapabilitySession(nil)
		if c.codexCapabilityCache != nil {
			c.codexCapabilityCache.Invalidate()
		}
		return corecap.Selection{}, false, codexAdvancedUnavailable("capability snapshot has no valid connection/version epoch", nil)
	}
	cache := c.codexCapabilityCache
	if cache == nil {
		cache = &corecap.Cache{}
		c.codexCapabilityCache = cache
	}
	cache.Replace(snapshot)
	c.replaceCodexCapabilitySession(session)
	rows, selections := codexCapabilityRows(appLocale(c.homeDir, c.lookupEnv), snapshot)
	if len(rows) == 0 {
		c.discardCodexCapabilitySession(snapshot.Epoch)
		return corecap.Selection{}, false, codexAdvancedUnavailable("no visible model and supported effort combinations", nil)
	}
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, c.themedPickerOptions(intpickercompat.Options{
		UI:         "ai-codex-capability-picker",
		Entries:    rows,
		Title:      "Codex Launch - Model and effort",
		Prompt:     "Codex > ",
		Footer:     projmuxFooter("Choose a model and supported reasoning effort."),
		ExpectKeys: []string{"enter"},
		Bindings:   pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, "ai-codex-capability-picker", "esc", "ctrl-c", "ctrl-alt-s"),
	}))
	if err != nil {
		c.discardCodexCapabilitySession(snapshot.Epoch)
		if isNoSelectionExit(err) {
			return corecap.Selection{}, true, nil
		}
		return corecap.Selection{}, true, fmt.Errorf("run Codex capability picker: %w", err)
	}
	if result.Key != "enter" || result.Value == "" {
		c.discardCodexCapabilitySession(snapshot.Epoch)
		return corecap.Selection{}, true, nil
	}
	selection, ok := selections[result.Value]
	if !ok {
		c.discardCodexCapabilitySession(snapshot.Epoch)
		return corecap.Selection{}, true, corecap.ErrStaleSelection
	}
	if _, err := cache.Validate(selection); err != nil {
		c.discardCodexCapabilitySession(snapshot.Epoch)
		return corecap.Selection{}, true, err
	}
	return selection, true, nil
}

func (c *aiCommand) replaceCodexCapabilitySession(session codexCapabilitySession) {
	c.codexCapabilitySessionMu.Lock()
	old := c.codexCapabilitySession
	c.codexCapabilitySession = session
	c.codexCapabilitySessionMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func (c *aiCommand) discardCodexCapabilitySession(epoch corecap.Epoch) {
	c.codexCapabilitySessionMu.Lock()
	session := c.codexCapabilitySession
	if session != nil && session.Snapshot().Epoch == epoch {
		c.codexCapabilitySession = nil
	} else {
		session = nil
	}
	c.codexCapabilitySessionMu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

func (c *aiCommand) takeCodexCapabilitySession(epoch corecap.Epoch) codexCapabilitySession {
	c.codexCapabilitySessionMu.Lock()
	defer c.codexCapabilitySessionMu.Unlock()
	session := c.codexCapabilitySession
	if session == nil || session.Snapshot().Epoch != epoch {
		return nil
	}
	c.codexCapabilitySession = nil
	return session
}

func codexCapabilityRows(locale i18n.Locale, snapshot corecap.Snapshot) ([]intpickercompat.Entry, map[string]corecap.Selection) {
	rows := []intpickercompat.Entry{}
	selections := map[string]corecap.Selection{}
	defaultMarker := localizeUIText(locale, "[DEFAULT]")
	unspecifiedModality := localizeUIText(locale, "unspecified modality")
	personality := localizeUIText(locale, "personality")
	for modelIndex, model := range snapshot.Models {
		for effortIndex, effort := range model.Efforts {
			value := fmt.Sprintf("capability:%d:%d", modelIndex, effortIndex)
			marker := ""
			if model.Default && effort == model.DefaultEffort {
				marker = " " + defaultMarker
			}
			features := strings.Join(model.InputModalities, "+")
			if features == "" {
				features = unspecifiedModality
			}
			if model.SupportsPersonality {
				features += ", " + personality
			}
			rows = append(rows, intpickercompat.Entry{
				Label:     fmt.Sprintf("%-24s %-10s %s%s", model.DisplayName, effort, features, marker),
				Value:     value,
				SearchKey: strings.Join([]string{model.ID, model.LaunchName, model.DisplayName, effort, features}, " "),
			})
			selections[value] = corecap.Selection{Epoch: snapshot.Epoch, ModelID: model.ID, LaunchName: model.LaunchName, Effort: effort}
		}
	}
	return rows, selections
}

// The split UI's three terminal actions.
//
// Each one produces a canonical create intent and hands it to the create route.
// They used to call tmux's `split-window` themselves, which is why a pane the
// operator opened from the picker was a runtime object the Registry had never
// heard of: it had no uid, no owner Window, no Agent row, and it showed up in the
// Main UI only after something else happened to reconcile. The intent is the
// whole of what this layer knows -- which provider, which side, and for a resume
// which conversation -- and every question about where the pane comes from is
// answered once, by create.

// createAgentPane opens a provider Agent beside the current Pane.
func (c *aiCommand) createAgentPane(producer canonicalCreateProducer, mode, direction string) error {
	return c.createPaneFromIntent(agentPaneIntent{producer: producer, provider: mode, placement: direction})
}

func (c *aiCommand) createCodexCapabilityAgentPane(producer canonicalCreateProducer, direction string, selection corecap.Selection) error {
	return c.createPaneFromIntent(agentPaneIntent{
		producer: producer, provider: aiModeCodex, placement: direction, codexCapability: &selection,
	})
}

// createShellPane opens a plain shell Pane beside the current Pane. A shell
// surface is a Pane and not an Agent, which is why this is a different intent
// rather than a provider named "shell".
func (c *aiCommand) createShellPane(producer canonicalCreateProducer, direction string) error {
	return c.createPaneFromIntent(agentPaneIntent{producer: producer, placement: direction})
}

// createResumedAgentPane keeps source-free callers on the historical intent.
func (c *aiCommand) createResumedAgentPane(producer canonicalCreateProducer, mode, direction, conversationID string) error {
	return c.createResumedAgentPaneWithSource(producer, mode, direction, conversationID, "")
}

func (c *aiCommand) createResumedAgentPaneWithSource(producer canonicalCreateProducer, mode, direction, conversationID, source string) error {
	return c.createPaneFromIntent(agentPaneIntent{
		producer: producer, provider: mode, placement: direction, conversationID: conversationID, resumeSource: source,
	})
}

// createPaneFromIntent is the one call from the split UI into create.
//
// A nil seam is an error rather than a silent no-op. The fixtures that build an
// aiCommand literal do reach this path, and the failure that matters is a UI
// action that reports success while creating nothing -- so an unwired creator
// says so.
func (c *aiCommand) createPaneFromIntent(intent agentPaneIntent) error {
	if c.panes == nil {
		return errors.New("the Projmux split UI has no canonical create route configured")
	}
	// The anchor is attached here, at the single funnel, so all four terminal
	// actions -- provider, shell, resume, and the resume picker's `new` row --
	// carry the same origin pane. When this producer runs in the pane it acts on
	// there is no origin env and the anchor stays empty, which is create's
	// inherited-target path unchanged.
	intent.anchorPaneID = c.splitOriginPane()
	intent.targetClient = c.splitOriginClient()
	var diagnostics bytes.Buffer
	err := c.panes.createFromIntent(intent, io.Discard, &diagnostics)
	if err == nil {
		// A successful split writes nothing. The producer on the other end of
		// this call is a foreground tmux `run-shell` job, and tmux paints
		// whatever such a job writes -- diagnostics included -- as a view-mode
		// screen over the pane the operator was working in. The new Pane is the
		// feedback a successful create owes them.
		return nil
	}
	reason := canonicalCreateFailureReason(err, diagnostics.String())
	if intent.targetClient != "" {
		if displayErr := c.run("tmux", "display-message", "-c", intent.targetClient, "-d", "10000", reason); displayErr != nil {
			return fmt.Errorf("%s; display canonical create failure to client %q: %v", reason, intent.targetClient, displayErr)
		}
		// The refusal reached the operator on the exact client that asked for
		// the Pane. Returning it as well would exit non-zero, and tmux shows a
		// foreground job's "returned N" in the same overlay this contract
		// removes -- the same refusal twice, one of them on top of their work.
		return nil
	}
	return err
}

func canonicalCreateFailureReason(err error, diagnostics string) string {
	reason := strings.TrimSpace(err.Error())
	diagnostics = strings.TrimSpace(diagnostics)
	if diagnostics != "" && !strings.Contains(reason, diagnostics) {
		reason += ": " + diagnostics
	}
	return "projmux create failed: " + strings.Join(strings.Fields(reason), " ")
}

func (c *aiCommand) runSettings(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai settings", flag.ContinueOnError)
	fs.SetOutput(stderr)
	get := fs.Bool("get", false, "print the configured AI split mode")
	set := fs.String("set", "", "set the configured AI split mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai settings does not accept positional arguments")
	}

	if *get {
		_, err := fmt.Fprintln(stdout, c.getMode())
		return err
	}
	if strings.TrimSpace(*set) != "" {
		return c.setMode(*set)
	}

	if c.nativePicker == nil {
		return errors.New("native picker is not configured")
	}
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, c.themedPickerOptions(intpickercompat.Options{
		UI:         "ai-settings",
		Entries:    c.settingsRows(),
		Title:      "AI Settings - Default split mode",
		Prompt:     "AI Setting > ",
		Footer:     projmuxFooter("Choose the default split mode for future AI launches."),
		ExpectKeys: []string{"enter"},
		Bindings:   pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, "ai-split-settings", "esc", "ctrl-c", "ctrl-alt-s"),
	}))
	if err != nil {
		if isNoSelectionExit(err) {
			return nil
		}
		return fmt.Errorf("run ai settings picker: %w", err)
	}
	if result.Key != "enter" || result.Value == "" {
		return nil
	}
	return c.setMode(result.Value)
}

func (c *aiCommand) runAgentPicker(direction string) (intpickercompat.Result, error) {
	if c.nativePicker == nil {
		return intpickercompat.Result{}, errors.New("native picker is not configured")
	}
	return runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, c.themedPickerOptions(intpickercompat.Options{
		UI:         "ai-picker",
		Entries:    c.agentRows(),
		Title:      localizeUIText(appLocale(c.homeDir, c.lookupEnv), "AI Launch - Split direction: ") + direction,
		Prompt:     "AI Launch > ",
		Footer:     projmuxFooter("Choose an agent or shell target to launch."),
		ExpectKeys: []string{"enter"},
		Bindings:   pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, aiSplitPickerPopupMode(direction), "esc", "ctrl-c", "ctrl-alt-s"),
	}))
}

func (c *aiCommand) runResumePicker(direction string) error {
	contextDir := c.resolveContextDir()
	homeDir, _ := c.home()
	depth := resolveAIResumeScanDepth(c.homeDir, c.lookupEnv, contextDir).Depth
	limit := resolveAIResumePickerLimit(c.homeDir, c.lookupEnv, contextDir).Limit
	controller := newAIResumeLiveController(c, contextDir, homeDir, depth, limit)
	defer controller.close()
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := controller.initialEntries()
	footer, moreNotLoaded := controller.footer()
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, c.themedPickerOptions(intpickercompat.Options{
		UI:                    "ai-resume-picker",
		Entries:               entries,
		Title:                 localizeUIText(locale, "AI Resume - Split direction: ") + direction,
		Prompt:                "AI Resume > ",
		Footer:                footer,
		MoreNotLoaded:         moreNotLoaded,
		SelectionDetail:       controller.initialDetail(),
		ExpectKeys:            []string{"enter"},
		Bindings:              pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, aiResumePickerPopupMode(direction), "esc", "ctrl-c", "ctrl-alt-s"),
		DeferredUpdate:        controller.update,
		DeferredUpdateTrigger: controller.events,
		FocusChanged:          controller.focus,
	}))
	if err != nil {
		if isNoSelectionExit(err) {
			return nil
		}
		return err
	}
	if result.Value == "" || result.Key != "enter" {
		return nil
	}
	if result.Value == aiResumeNewValue {
		return c.runAgentPickerSelection(direction)
	}
	selection, ok := parseAIResumePickerValue(result.Value)
	if !ok {
		return nil
	}
	selection = enrichAIResumeSelectionFromSummaries(selection, controller.snapshotSummaries())
	return c.runSelectedResumeSession(selection, direction)
}

type aiResumeSelection struct {
	agent     string
	resumeID  string
	source    string
	updatedAt time.Time
}

// Resume picker row column schema.
//
// Every row lays out the same fixed-width columns so they align in the popup
// regardless of locale or CJK content, with the title as the trailing
// variable-width column. The recency anchor leads (it matches the newest-first
// sort axis), followed by a per-agent colour badge:
//
//	<rel>  [agent]   <branch>            [<cwd>] <title…>
//	 6      (tight)+pad→8   18                  (14)       rest
//
// Column order / cell width (visible cells, fixed columns left-aligned):
//   - relative age: aiResumeRelCellWidth (compact, locale-aware, dim) — leads.
//   - agent badge: tight per-agent-coloured "[name]" padded to
//     aiResumeBadgeCellWidth; padding sits outside the brackets/colour.
//   - branch: aiResumeBranchCellWidth (dim, cut).
//   - extra-meta slot: the depth>0 relative-cwd column (aiResumeCWDCellWidth,
//     dim); empty at depth 0 so the layout collapses to the base view.
//   - title: trailing variable width, cut with an ellipsis past
//     aiResumeTitleMaxCells.
//
// The absolute time and short resume id are intentionally dropped from the
// visible columns; the resume id stays in SearchKey so id search still works.
const (
	aiResumeAgentCellWidth  = 6
	aiResumeBadgeCellWidth  = aiResumeAgentCellWidth + 2 // tight "[name]" + brackets
	aiResumeRelCellWidth    = 6
	aiResumeBranchCellWidth = 18
	aiResumeCWDCellWidth    = 14
	aiResumeTitleMaxCells   = 90
	aiResumeEmptyCell       = "-"
)

// aiResumeSummaryRowsWithLabels renders the frozen Phase-0 list projection.
// Only ResumeSummary fields participate in labels/search/value; detail fields
// such as turns, runtime status, fallback reason, and preview bytes cannot
// mutate the list because they are absent from this input type.
func aiResumeSummaryRowsWithLabels(summaries []aisessions.ResumeSummary, conversationLabels map[string]string, now time.Time, locale i18n.Locale, baseCWD string, depth int) []intpickercompat.Entry {
	rows := make([]intpickercompat.Entry, 0, len(summaries)+1)
	rows = append(rows, intpickercompat.Entry{
		Label:     "\x1b[32m[+ New Session]\x1b[0m",
		Value:     aiResumeNewValue,
		SearchKey: "new session fresh agent picker",
	})
	for _, summary := range summaries {
		session := aiResumeSessionMetaFromSummary(summary, baseCWD)
		rows = append(rows, aiResumeSessionRowWithLabel(session, conversationLabels[strings.TrimSpace(summary.ResumeID)], now, locale, baseCWD, depth))
	}
	return rows
}

func aiResumeSessionMetaFromSummary(summary aisessions.ResumeSummary, baseCWD string) aisessions.SessionMeta {
	cwd := ""
	if relative := strings.TrimSpace(summary.RelativeCWD); relative == "./" {
		cwd = baseCWD
	} else if after, ok := strings.CutPrefix(relative, "./"); ok {
		cwd = filepath.Join(baseCWD, after)
	}
	return aisessions.SessionMeta{
		Agent: summary.Provider, ResumeID: summary.ResumeID, Title: summary.Label,
		LastModified: summary.LastModified, UpdatedAt: summary.UpdatedAt,
		Context: aisessions.SessionContext{CWD: cwd, Branch: summary.Branch}, Source: summary.Source,
	}
}

func aiResumeSessionRowWithLabel(session aisessions.SessionMeta, boundLabel string, now time.Time, locale i18n.Locale, baseCWD string, depth int) intpickercompat.Entry {
	agent := strings.TrimSpace(session.Agent)
	resumeID := strings.TrimSpace(session.ResumeID)
	branch := strings.TrimSpace(session.Context.Branch)
	if branch == "" {
		branch = aiResumeEmptyCell
	}
	conversation := cleanAIResumeTitle(session.Title, resumeID)
	if strings.EqualFold(agent, aiModeCodex) &&
		(session.Source == aisessions.SourceCodexAppServer || session.Source == aisessions.SourceCodexRollout) {
		conversation = aiResumeCodexConversationLabel(session, boundLabel)
	}
	conversation = truncateAIResumeCells(conversation, aiResumeTitleMaxCells)
	relCWD := aiResumeExtraMetaCell(session, baseCWD, depth)
	parts := []string{
		ansiDim(aiResumeFitCell(aiResumeRelativeAge(now, session.LastModified, locale), aiResumeRelCellWidth)),
		aiResumeAgentBadge(agent),
		ansiDim(aiResumeFitCell(branch, aiResumeBranchCellWidth)),
	}
	if relCWD != "" {
		parts = append(parts, ansiDim(aiResumeFitCell(relCWD, aiResumeCWDCellWidth)))
	}
	parts = append(parts, conversation)

	return intpickercompat.Entry{
		Label:     strings.Join(parts, " "),
		Value:     aiResumePickerValue(agent, resumeID),
		SearchKey: strings.TrimSpace(strings.Join([]string{agent, conversation, strings.TrimSpace(session.Title), resumeID, branch, relCWD, session.Source}, " ")),
	}
}

func aiResumeCodexConversationLabel(session aisessions.SessionMeta, boundLabel string) string {
	resumeID := strings.TrimSpace(session.ResumeID)
	shortID := aiResumeShortID(resumeID)
	title := strings.Join(strings.Fields(session.Title), " ")
	if session.Source == aisessions.SourceCodexAppServer && title != "" && title != shortID {
		return title
	}
	if boundLabel = strings.Join(strings.Fields(boundLabel), " "); boundLabel != "" {
		return boundLabel
	}
	if session.Source == aisessions.SourceCodexAppServer || session.Source == aisessions.SourceCodexRollout {
		if shortID != "" {
			return shortID
		}
		return "(untitled)"
	}
	return cleanAIResumeTitle(title, resumeID)
}

func aiResumeShortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// resolveAIResumeConversationLabels reads the Registry at most once per picker
// invocation. A label is admitted only through an exact Codex thread binding;
// cwd, title, pane ordering, prompt, and transcript content are never consulted.
func (c *aiCommand) resolveAIResumeConversationLabels(sessions []aisessions.SessionMeta) map[string]string {
	if c == nil || c.loadRegistry == nil {
		return nil
	}
	wantsCodex := false
	for _, session := range sessions {
		if strings.EqualFold(strings.TrimSpace(session.Agent), aiModeCodex) {
			wantsCodex = true
			break
		}
	}
	if !wantsCodex {
		return nil
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return nil
	}
	return aiResumeExactAgentLabels(registry)
}

func (c *aiCommand) resolveAIResumeSummaryLabels(summaries []aisessions.ResumeSummary) map[string]string {
	sessions := make([]aisessions.SessionMeta, 0, len(summaries))
	for _, summary := range summaries {
		sessions = append(sessions, aiResumeSessionMetaFromSummary(summary, ""))
	}
	return c.resolveAIResumeConversationLabels(sessions)
}

func aiResumeExactAgentLabels(registry coremetadata.Registry) map[string]string {
	type resolved struct {
		uid   string
		label string
		rank  int
	}
	byThread := make(map[string]resolved)
	for _, agent := range registry.Agents {
		if !strings.EqualFold(strings.TrimSpace(agent.Spec.Provider), aiModeCodex) ||
			agent.Status.SessionRef == nil ||
			!strings.EqualFold(strings.TrimSpace(agent.Status.SessionRef.Provider), aiModeCodex) ||
			agent.Status.SessionRef.Codex == nil {
			continue
		}
		threadID := strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID)
		if threadID == "" {
			continue
		}
		label := strings.TrimSpace(agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic])
		rank := 0
		if label == "" {
			label = strings.TrimSpace(agent.Metadata.Name)
			rank = 1
		}
		if label == "" {
			continue
		}
		candidate := resolved{uid: agent.Metadata.UID, label: label, rank: rank}
		if current, ok := byThread[threadID]; !ok || candidate.rank < current.rank ||
			(candidate.rank == current.rank && candidate.uid < current.uid) {
			byThread[threadID] = candidate
		}
	}
	labels := make(map[string]string, len(byThread))
	for threadID, candidate := range byThread {
		labels[threadID] = candidate.label
	}
	return labels
}

// aiResumeAgentBadge renders the agent tag as a tight, per-agent-coloured
// "[name]" token padded to a fixed column width. The brackets hug the name
// ("[codex]", never "[codex ]") and only the tight token is coloured; the
// alignment padding after "]" stays uncoloured so columns line up in the popup.
func aiResumeAgentBadge(agent string) string {
	inner := i18n.TruncateTerminalCells(strings.TrimSpace(agent), aiResumeAgentCellWidth)
	tight := "[" + inner + "]"
	badge := aiResumeAgentColor(agent) + tight + "\x1b[0m"
	if pad := aiResumeBadgeCellWidth - i18n.TerminalCellWidth(tight); pad > 0 {
		badge += strings.Repeat(" ", pad)
	}
	return badge
}

// aiResumeAgentColor maps an agent to a distinct ANSI colour so the badge tells
// at a glance which resume CLI a row uses. The theme palette carries no
// per-agent accent token, so these are the roadmap's fixed-hue fallback; codex
// keeps the historical cyan. Unknown agents fall back to default foreground.
func aiResumeAgentColor(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case aiModeClaude:
		return "\x1b[35m" // magenta
	case aiModeCodex:
		return "\x1b[36m" // cyan (historical badge colour)
	case aiModeAntigravity:
		return "\x1b[34m" // blue
	default:
		return "\x1b[37m"
	}
}

// aiResumeRelativeAge renders a compact, locale-aware relative age ("2h", "3d",
// "2시간"). It returns empty when either timestamp is unknown so the column pads
// to a blank cell instead of a bogus age.
func aiResumeRelativeAge(now, modified time.Time, locale i18n.Locale) string {
	if now.IsZero() || modified.IsZero() {
		return ""
	}
	age := max(now.Sub(modified), 0)
	return i18n.FormatDuration(age, locale, i18n.FormatCompact)
}

// aiResumeExtraMetaCell fills the reserved extra-meta column. At depth 0 it
// stays empty (the column is hidden, matching the historical view); at depth>0
// it returns the session cwd as a path relative to the picker's base cwd
// ("./", "./web", "./web/api") so the user can tell child-directory sessions
// apart. A session whose recorded cwd cannot be made relative (it should always
// be a descendant given discovery filtering) collapses to an empty cell.
func aiResumeExtraMetaCell(session aisessions.SessionMeta, baseCWD string, depth int) string {
	if depth <= 0 {
		return ""
	}
	return aiResumeRelativeCWD(baseCWD, session.Context.CWD)
}

// aiResumeRelativeCWD renders recorded as a "./"-prefixed path relative to base.
// The exact cwd renders "./"; descendants render "./sub" (slash-normalised).
// It returns "" when either path is empty or recorded escapes base (a "..").
func aiResumeRelativeCWD(base, recorded string) string {
	base = strings.TrimSpace(base)
	recorded = strings.TrimSpace(recorded)
	if base == "" || recorded == "" {
		return ""
	}
	rel, err := filepath.Rel(base, recorded)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "./"
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return "./" + rel
}

func cleanAIResumeTitle(title, resumeID string) string {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = strings.TrimSpace(resumeID)
	}
	if title == "" {
		return "(untitled)"
	}
	return truncateAIResumeCells(title, aiResumeTitleMaxCells)
}

// aiResumeFitCell truncates value to width terminal cells and right-pads with
// spaces, so fixed columns align even with CJK or empty content.
func aiResumeFitCell(value string, width int) string {
	fitted := i18n.TruncateTerminalCells(strings.TrimSpace(value), width)
	if pad := width - i18n.TerminalCellWidth(fitted); pad > 0 {
		fitted += strings.Repeat(" ", pad)
	}
	return fitted
}

// truncateAIResumeCells clips value to limit terminal cells, appending an
// ellipsis when it overflows. Width-based (not rune-count) so CJK titles stay
// inside narrow popups.
func truncateAIResumeCells(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	if i18n.TerminalCellWidth(value) <= limit {
		return value
	}
	return i18n.TruncateTerminalCells(value, limit-1) + "…"
}

func aiResumePickerValue(agent, resumeID string) string {
	return "resume\t" + strings.TrimSpace(agent) + "\t" + strings.TrimSpace(resumeID)
}

func parseAIResumePickerValue(value string) (aiResumeSelection, bool) {
	parts := strings.Split(value, "\t")
	if len(parts) != 3 || parts[0] != "resume" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
		return aiResumeSelection{}, false
	}
	return aiResumeSelection{agent: strings.TrimSpace(parts[1]), resumeID: strings.TrimSpace(parts[2])}, true
}

func enrichAIResumeSelection(selection aiResumeSelection, sessions []aisessions.SessionMeta) aiResumeSelection {
	agent := normalizeAIMode(selection.agent)
	resumeID := strings.TrimSpace(selection.resumeID)
	for _, session := range sessions {
		if normalizeAIMode(session.Agent) != agent || strings.TrimSpace(session.ResumeID) != resumeID {
			continue
		}
		selection.source = strings.TrimSpace(session.Source)
		selection.updatedAt = session.LastModified
		return selection
	}
	return selection
}

func enrichAIResumeSelectionFromSummaries(selection aiResumeSelection, summaries []aisessions.ResumeSummary) aiResumeSelection {
	sessions := make([]aisessions.SessionMeta, 0, len(summaries))
	for _, summary := range summaries {
		sessions = append(sessions, aiResumeSessionMetaFromSummary(summary, ""))
	}
	return enrichAIResumeSelection(selection, sessions)
}

func (c *aiCommand) runSelectedResumeSession(selection aiResumeSelection, direction string) error {
	mode := normalizeAIMode(selection.agent)
	if mode != aiModeClaude && mode != aiModeCodex && mode != aiModeAntigravity {
		return nil
	}
	if err := c.requireAIAgentEnabled(mode, aiSplitLaunchPicker); err != nil {
		return err
	}
	// The conversation id is normalized by the provider's own resume builder, so
	// a row whose id this provider cannot address is caught before any Registry
	// or tmux mutation. Degrading to a fresh conversation stays the deliberate
	// behavior of this interactive path -- the operator asked for "resume
	// something", and the picker already told them which row failed -- and it is
	// the one difference from `agent resume`, which must never fall through.
	resumeArgv, err := resumeArgsForAgent(mode, selection.resumeID)
	if err != nil {
		_ = c.displayMessage(fmt.Sprintf("Could not resume %s session: %v; launching new session", mode, err))
		return c.createAgentPane(canonicalProducerResumePicker, mode, direction)
	}
	if strings.TrimSpace(selection.source) == "" {
		return c.createResumedAgentPane(canonicalProducerResumePicker, mode, direction, resumeArgv[len(resumeArgv)-1])
	}
	return c.createResumedAgentPaneWithSource(canonicalProducerResumePicker, mode, direction, resumeArgv[len(resumeArgv)-1], selection.source)
}

func resumeArgsForAgent(mode, resumeID string) ([]string, error) {
	switch mode {
	case aiModeClaude:
		return claude.ResumeArgs(resumeID)
	case aiModeCodex:
		return codex.ResumeArgs(resumeID)
	case aiModeAntigravity:
		return antigravity.ResumeArgs(resumeID)
	default:
		return nil, fmt.Errorf("unsupported resume agent: %s", mode)
	}
}

// themedPickerOptions fills options.Theme with the global theme source so AI
// split-picker and AI settings popups paint the themed surface/background
// instead of the runNativePickerOption fallback default. It degrades to the
// built-in fallback theme on a config read error, matching the switch.go /
// notify.go sidebar pattern. Theme is global-only, so no project path
// participates.
func (c *aiCommand) themedPickerOptions(options intpickercompat.Options) intpickercompat.Options {
	if options.Theme != nil {
		return options
	}
	if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, ""); err == nil {
		return source.pickerCompatOptions(options)
	}
	return fallbackRenderThemeSource().pickerCompatOptions(options)
}

func aiSplitPickerPopupMode(direction string) string {
	if direction == "down" {
		return "ai-split-picker-down"
	}
	return "ai-split-picker-right"
}

func aiResumePickerPopupMode(direction string) string {
	if direction == "down" {
		return "ai-split-resume-down"
	}
	return "ai-split-resume-right"
}

func (c *aiCommand) settingsRows() []intpickercompat.Entry {
	current := c.getMode()
	enabled := c.enabledAIAgents()
	modes := []struct {
		mode string
		desc string
	}{
		{aiModeSelective, "show picker each time"},
		{aiModeResume, "show resume session picker"},
	}
	for _, provider := range aiprovider.SettingsVisible() {
		modes = append(modes, struct {
			mode string
			desc string
		}{
			mode: string(provider.ID),
			desc: "always run " + provider.DisplayName + " split",
		})
	}
	modes = append(modes, struct {
		mode string
		desc string
	}{aiModeShell, "always open plain shell split"})
	rows := make([]intpickercompat.Entry, 0, len(modes)+1)
	if provider, ok := aiModeProvider(current); ok && !aiEnabledAgentsContains(enabled, provider) {
		rows = append(rows, intpickercompat.Entry{
			Label:     ansiDim(fmt.Sprintf("[INFO] saved default %s is disabled in Enabled agents", provider)),
			Value:     "",
			SearchKey: "default split mode disabled enabled agents",
		})
	}
	for _, item := range modes {
		if provider, ok := aiModeProvider(item.mode); ok && !aiEnabledAgentsContains(enabled, provider) {
			continue
		}
		tag := ansiDim("[ ]")
		if item.mode == current {
			tag = "\x1b[32m[ACTIVE]\x1b[0m"
		}
		rows = append(rows, intpickercompat.Entry{
			Label:     fmt.Sprintf("%s \x1b[36m%-9s\x1b[0m  \x1b[90m%s\x1b[0m", tag, item.mode, item.desc),
			Value:     item.mode,
			SearchKey: item.mode + " " + item.desc,
		})
	}
	return rows
}

func (c *aiCommand) agentRows() []intpickercompat.Entry {
	enabled := c.enabledAIAgents()
	rows := make([]intpickercompat.Entry, 0, len(enabled)+3)
	locale := c.locale()
	for _, provider := range aiprovider.PickerEligible() {
		if aiEnabledAgentsContains(enabled, config.AIAgentProvider(provider.ID)) {
			rows = append(rows, c.agentRow(provider, locale))
			if provider.ID == aiprovider.Codex {
				rows = append(rows, c.codexAdvancedLaunchRow(locale))
			}
		}
	}
	if len(enabled) == 0 {
		rows = append(rows, intpickercompat.Entry{
			Label:     ansiDim("[INFO] AI agents disabled in Settings; use shell or re-enable Claude/Codex/Antigravity."),
			Value:     "",
			SearchKey: "AI agents disabled settings enabled agents shell",
		})
	}
	rows = append(rows, intpickercompat.Entry{
		Label:     fmt.Sprintf("%-8s \x1b[34m[READY]\x1b[0m Plain shell split (\x1b[90mno agent\x1b[0m)", aiModeShell),
		Value:     aiModeShell,
		SearchKey: aiModeShell + " plain shell split no agent",
	})
	return rows
}

func (c *aiCommand) agentRow(provider aiprovider.Metadata, locale i18n.Locale) intpickercompat.Entry {
	status := "\x1b[33m[MISSING]\x1b[0m"
	if c.agentAvailable(string(provider.ID)) {
		status = "\x1b[32m[READY]\x1b[0m"
	}
	desc := provider.DisplayName + " split"
	if provider.ID == aiprovider.Codex {
		desc = localizeUIText(locale, "Codex default launch")
	}
	return intpickercompat.Entry{
		Label:     fmt.Sprintf("%-8s %s %s", provider.ID, status, desc),
		Value:     string(provider.ID),
		SearchKey: string(provider.ID) + " " + desc,
	}
}

func (c *aiCommand) codexAdvancedLaunchRow(locale i18n.Locale) intpickercompat.Entry {
	status := "\x1b[36m" + localizeUIText(locale, "[ADVANCED]") + "\x1b[0m"
	desc := localizeUIText(locale, "Codex advanced launch")
	detail := localizeUIText(locale, "choose model and reasoning effort")
	return intpickercompat.Entry{
		Label:     fmt.Sprintf("%-8s %s %s (\x1b[90m%s\x1b[0m)", "codex+", status, desc, detail),
		Value:     aiActionCodexAdvancedLaunch,
		SearchKey: strings.Join([]string{aiModeCodex, "advanced", "model", "reasoning", "effort", desc, detail}, " "),
	}
}

func (c *aiCommand) enabledAIAgents() []config.AIAgentProvider {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	agents, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	return agents
}

func (c *aiCommand) requireAIAgentEnabled(mode string, path aiSplitLaunchPath) error {
	provider, ok := aiModeProvider(mode)
	if !ok {
		return nil
	}
	if aiEnabledAgentsContains(c.enabledAIAgents(), provider) {
		return nil
	}
	message := disabledAIAgentLaunchMessage(mode, path)
	_ = c.displayMessage(message)
	return errors.New(message)
}

func aiModeProvider(mode string) (config.AIAgentProvider, bool) {
	provider, ok := aiprovider.Lookup(normalizeAIMode(mode))
	if !ok || !provider.SettingsVisible {
		return "", false
	}
	return config.AIAgentProvider(provider.ID), true
}

func disabledAIAgentLaunchMessage(mode string, path aiSplitLaunchPath) string {
	switch path {
	case aiSplitLaunchDefault:
		return fmt.Sprintf("AI split default %s is disabled in Settings > AI Settings > Enabled agents; choose another default or use --agent shell", mode)
	case aiSplitLaunchPicker:
		return fmt.Sprintf("AI agent %s is disabled in Settings > AI Settings > Enabled agents", mode)
	case aiSplitLaunchCanonical:
		return fmt.Sprintf("AI agent %s is disabled in Settings > AI Settings > Enabled agents; enable it there before creating an Agent", mode)
	default:
		return fmt.Sprintf("AI agent %s is disabled in Settings > AI Settings > Enabled agents; enable it or pass --force-agent for this direct launch", mode)
	}
}

func (c *aiCommand) getMode() string {
	content, err := os.ReadFile(c.configFile())
	if err != nil {
		return aiModeSelective
	}
	return normalizeAIMode(strings.TrimSpace(string(content)))
}

func (c *aiCommand) setMode(mode string) error {
	mode = normalizeAIMode(mode)
	path := c.configFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(mode+"\n"), 0o644); err != nil {
		return err
	}
	_ = c.displayMessage("ai split default: " + mode)
	return nil
}

func (c *aiCommand) configFile() string {
	configHome := strings.TrimSpace(c.env("XDG_CONFIG_HOME"))
	if configHome == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			return filepath.Join(".config", "projmux", "tmux-ai-split-mode")
		}
		configHome = filepath.Join(homeDir, ".config")
	}
	return filepath.Join(configHome, "projmux", "tmux-ai-split-mode")
}

func (c *aiCommand) openPicker(direction string) error {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return err
	}
	targetPane := c.resolveTargetPane()
	command := shellEnv("TMUX_SPLIT_TARGET_PANE", targetPane) + shellQuote(binaryPath) + " ai picker --inside " + shellQuote(direction)
	width, height := c.popupSize(40, 64, 30, 12)
	err = c.run("tmux", "display-popup", "-E", "-w", width, "-h", height, command)
	if isNoSelectionExit(err) {
		return nil
	}
	return err
}

func (c *aiCommand) openPickerToggle(direction string) error {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return err
	}
	mode := "ai-split-picker-right"
	if direction == "down" {
		mode = "ai-split-picker-down"
	}
	args := []string{"internal", "tmux", "popup-toggle"}
	if clientKey := c.readTrimmed("tmux", "display-message", "-p", "-F", "#{client_tty}"); clientKey != "" {
		args = append(args, "--client", clientKey)
	}
	args = append(args, mode)
	err = c.run(binaryPath, args...)
	if isNoSelectionExit(err) {
		return nil
	}
	return err
}

func (c *aiCommand) openResumePickerToggle(direction string) error {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return err
	}
	args := []string{"internal", "tmux", "popup-toggle"}
	if clientKey := c.readTrimmed("tmux", "display-message", "-p", "-F", "#{client_tty}"); clientKey != "" {
		args = append(args, "--client", clientKey)
	}
	args = append(args, aiResumePickerPopupMode(direction))
	err = c.run(binaryPath, args...)
	if isNoSelectionExit(err) {
		return nil
	}
	return err
}

// aiPaneResumeMetadata is the live routing index a resumed managed Pane carries
// from the moment it exists: the provider conversation id hook ingest scans to
// decide which live pane an incoming event belongs to, plus the discovery
// provenance the picker recorded. The durable pointer on the Agent is a separate
// value and is not written from here.
type aiPaneResumeMetadata struct {
	sessionID string
	resumeID  string
	source    string
	updatedAt time.Time
}

type agentLaunchPlan struct {
	// title is the agent pane title, also the seed of the pane's topic option.
	title string
	// command is the argv for a caller that runs the launch in place.
	command []string
	// commandArgs is the argv for a caller that hands the launch to a split.
	commandArgs []string
}

// planAgentLaunch builds the launch for one provider without creating anything.
//
// A failure here -- a missing agent binary, an unusable exec argv -- happens
// before any mutation, which is what lets the create routes guarantee a failed
// launch leaves zero resources behind.
func (c *aiCommand) planAgentLaunch(mode, contextDir string, extraArgs, execArgvOverride []string, pathPrependOverride string) (agentLaunchPlan, error) {
	execArgv := append([]string(nil), execArgvOverride...)
	pathPrepend := pathPrependOverride
	if len(execArgv) == 0 {
		var err error
		execArgv, pathPrepend, err = c.agentExecArgv(mode, extraArgs)
		if err != nil {
			return agentLaunchPlan{}, err
		}
	}
	title := c.buildAgentTitle(mode, contextDir)
	command, commandArgs, err := c.agentSplitCommand(mode, pathPrepend, contextDir, title, execArgv)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	return agentLaunchPlan{title: title, command: command, commandArgs: commandArgs}, nil
}

// The three methods below are the seam the canonical `create agent` route
// consumes. They are the launch half of the legacy split with the two halves
// this Phase must not inherit removed: nothing here resolves a target pane,
// splits a window, or calls applySplitLayout, so the detached materializer
// stays the only thing that creates the pane and the client never moves.

// PlanAgentLaunch builds the provider launch for one detached Agent create.
//
// The workspace and the initial task are assembled by providerLaunchArgs rather
// than concatenated here, because where the workspace options stop is a property
// of the provider's own parser: Claude's variadic `--add-dir` eats a payload
// appended straight after it and starts a session with no task at all.
func (c *aiCommand) PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (title string, argv []string, err error) {
	extra, err := providerLaunchArgs(provider, workspace, payload)
	if err != nil {
		return "", nil, err
	}
	plan, err := c.planAgentLaunch(provider, workspace.CWD, extra, nil, "")
	if err != nil {
		return "", nil, err
	}
	return plan.title, plan.commandArgs, nil
}

// PlanAgentLaunchWithCapability is the narrow optional launch seam used only by
// the Codex picker. Other providers and static Codex launches stay unchanged.
func (c *aiCommand) PlanAgentLaunchWithCapability(provider string, workspace coremetadata.AgentWorkspace, payload []string, selection corecap.Selection) (title string, argv []string, err error) {
	if normalizeAIMode(provider) != aiModeCodex {
		return "", nil, fmt.Errorf("provider %q does not accept Codex capabilities", provider)
	}
	if c.codexCapabilityCache == nil {
		return "", nil, corecap.ErrStaleSelection
	}
	session := c.takeCodexCapabilitySession(selection.Epoch)
	if session == nil {
		c.codexCapabilityCache.Invalidate()
		return "", nil, corecap.ErrStaleSelection
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), codexCapabilityPickerTimeout)
	defer cancel()
	refreshed, err := session.Refresh(ctx)
	if err != nil {
		c.codexCapabilityCache.Invalidate()
		return "", nil, fmt.Errorf("%w: refresh current Codex model capabilities: %v", corecap.ErrStaleSelection, err)
	}
	c.codexCapabilityCache.Replace(refreshed)
	if _, err := c.codexCapabilityCache.Validate(selection); err != nil {
		return "", nil, err
	}
	extra, err := providerLaunchArgs(provider, workspace, payload)
	if err != nil {
		return "", nil, err
	}
	extra, err = codexCapabilityLaunchArgs(selection, extra)
	if err != nil {
		return "", nil, err
	}
	plan, err := c.planAgentLaunch(provider, workspace.CWD, extra, nil, "")
	if err != nil {
		return "", nil, err
	}
	return plan.title, plan.commandArgs, nil
}

func codexCapabilityLaunchArgs(selection corecap.Selection, base []string) ([]string, error) {
	model := strings.TrimSpace(selection.LaunchName)
	effort := strings.TrimSpace(selection.Effort)
	if model == "" || effort == "" {
		return nil, corecap.ErrStaleSelection
	}
	out := []string{"--model", model, "--config", "model_reasoning_effort=" + strconv.Quote(effort)}
	return append(out, base...), nil
}

// AwaitAgentActivation waits only for exact provider-hook evidence committed to
// Agent authority. SessionStart keeps the Agent pending but opens a separately
// bounded acknowledgement window; only the initial-task hook acknowledges. It
// does not inspect pane_title, capture-pane, semantic tmux presentation, or
// provider conversation content.
func (c *aiCommand) AwaitAgentActivation(ctx context.Context, runner tmuxCommandRunner, paneID string, startupTimeout, acknowledgementTimeout time.Duration) (bool, string, error) {
	if runner == nil {
		return false, "provider-hook", errors.New("agent activation observer is not configured")
	}
	if c.loadRegistry == nil {
		return false, "provider-hook", errors.New("agent activation Registry observer is not configured")
	}
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", "#{"+tmuxopts.PaneUID+"}")
	if err != nil {
		return false, "provider-hook", fmt.Errorf("read managed Pane identity: %w", err)
	}
	paneUID := strings.TrimSpace(string(out))
	if paneUID == "" {
		return false, "provider-hook", errors.New("managed Pane carries no resource identity")
	}
	initial, loadErr := c.loadRegistry()
	if loadErr != nil {
		return false, "provider-hook", fmt.Errorf("read Agent activation authority: %w", loadErr)
	}
	agentUID, generation, ok := exactAgentActivationBinding(initial, paneUID, strings.TrimSpace(paneID))
	if !ok {
		return false, "provider-hook", errors.New("managed Pane carries no exact Agent activation binding")
	}
	startupDeadline := c.nowTime().Add(startupTimeout)
	var acknowledgementDeadline time.Time
	for {
		registry, loadErr := c.loadRegistry()
		if loadErr != nil {
			return false, "provider-hook", fmt.Errorf("read Agent activation authority: %w", loadErr)
		}
		currentAgentUID, currentGeneration, bound := exactAgentActivationBinding(registry, paneUID, strings.TrimSpace(paneID))
		if !bound || currentAgentUID != agentUID || currentGeneration != generation {
			return false, "provider-hook", errors.New("managed Agent activation binding changed while awaiting acknowledgement")
		}
		agent, present := registry.Agent(agentUID)
		if present && agent.Status.Activation.State == coremetadata.ActivationAcknowledged &&
			agent.Status.Activation.Source == string(coremetadata.InteractionSourceProviderHook) {
			return true, "provider-hook", nil
		}
		if acknowledgementDeadline.IsZero() && present &&
			agent.Status.Activation.State == coremetadata.ActivationPending &&
			agent.Status.Activation.Source == string(coremetadata.InteractionSourceProviderHook) &&
			!agent.Status.Activation.ObservedAt.IsZero() {
			// The Registry timestamp is the provider's exact SessionStart commit,
			// not the observer's later poll, so polling cannot silently extend the
			// five-second acknowledgement budget.
			acknowledgementDeadline = agent.Status.Activation.ObservedAt.Add(acknowledgementTimeout)
		}
		if err := ctx.Err(); err != nil {
			return false, "provider-hook", err
		}
		now := c.nowTime()
		if (!acknowledgementDeadline.IsZero() && !now.Before(acknowledgementDeadline)) ||
			(acknowledgementDeadline.IsZero() && !now.Before(startupDeadline)) {
			return false, "provider-hook", nil
		}
		c.sleepFor(50 * time.Millisecond)
	}
}

// exactAgentActivationBinding returns the one Agent→Pane materialization a
// provider acknowledgement may refine. Pane uid is durable and therefore not
// enough by itself; the generation changes on resume/replacement.
func exactAgentActivationBinding(registry coremetadata.Registry, paneUID, runtimeID string) (string, string, bool) {
	pane, ok := registry.Pane(paneUID)
	if !ok || strings.TrimSpace(pane.Status.Activation.Generation) == "" ||
		strings.TrimSpace(pane.Status.Activation.AgentUID) == "" ||
		strings.TrimSpace(pane.Status.Activation.RuntimeID) == "" ||
		pane.Status.Activation.RuntimeID != strings.TrimSpace(runtimeID) {
		return "", "", false
	}
	agentUID := pane.Status.Activation.AgentUID
	agent, ok := registry.Agent(agentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != paneUID {
		return "", "", false
	}
	return agentUID, pane.Status.Activation.Generation, true
}

func (c *aiCommand) nowTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// RequireAgentEnabled applies the Settings enabled-agents gate to the canonical
// route.
//
// The gate is deliberately shared with the legacy route: a provider the operator
// switched off in Settings does not become launchable by spelling the command
// differently. The message differs in one respect only -- it never mentions
// `--force-agent`, which is a legacy compatibility flag and is not promoted to a
// canonical flag, so advertising it here would promise a capability this route
// does not have. The error classification is identical to the legacy path: a
// plain error, which cmd/projmux reports on stderr and exits 1 for.
func (c *aiCommand) RequireAgentEnabled(provider string) error {
	return c.requireAIAgentEnabled(provider, aiSplitLaunchCanonical)
}

// BindManagedAgentPane applies managed-agent pane options without starting the
// legacy title/content watcher. Resource Agents are driven only by explicit
// provider-hook metadata and canonical/manual status mutations.
//
// This is what makes a resource-backed Agent pane indistinguishable from a
// legacy one to the statusbar, the attention tracker, and the notification
// pipeline. None of these calls moves a client.
func (c *aiCommand) BindManagedAgentPane(paneID, provider, contextDir, title string) {
	c.configureAIPane(paneID, provider, contextDir, title, aiPaneResumeMetadata{})
}

func (c *aiCommand) BindManagedAgentPaneOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, provider, contextDir, title string,
) error {
	return c.configureAIPaneOnRoute(ctx, runner, paneID, provider, contextDir, title, aiPaneResumeMetadata{})
}

// PlanNativeCodexResume builds the TUI attachment for a thread already created
// or resumed through the local app-server. It carries no prompt: turn/start is
// the sole initial-prompt writer on the native lane.
func (c *aiCommand) PlanNativeCodexResume(workspace coremetadata.AgentWorkspace, threadID string) (string, []string, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return "", nil, errors.New("native Codex thread is empty")
	}
	agentBin := c.findAgentBinary(aiModeCodex)
	if agentBin == "" {
		return "", nil, errors.New(c.missingAgentRunnerMessage(aiModeCodex))
	}
	workspaceArgs, err := providerLaunchArgs(aiModeCodex, workspace, nil)
	if err != nil {
		return "", nil, err
	}
	execArgv := append([]string{agentBin}, workspaceArgs...)
	execArgv = append(execArgv, "resume", "--remote", "unix://", threadID)
	plan, err := c.planAgentLaunch(aiModeCodex, workspace.CWD, nil, execArgv, filepath.Dir(agentBin))
	if err != nil {
		return "", nil, err
	}
	return plan.title, plan.commandArgs, nil
}

// BindNativeCodexPane seeds the live hook routing index with the exact native
// thread before any late hook is allowed to refine the binding.
func (c *aiCommand) BindNativeCodexPane(paneID, contextDir, title, threadID string) {
	threadID = strings.TrimSpace(threadID)
	c.configureAIPane(paneID, aiModeCodex, contextDir, title, aiPaneResumeMetadata{
		sessionID: threadID, resumeID: threadID, source: "app-server", updatedAt: c.nowTime().UTC(),
	})
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneThreadIDOption, threadID)
}

func (c *aiCommand) BindNativeCodexPaneOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, contextDir, title, threadID string,
) error {
	threadID = strings.TrimSpace(threadID)
	if err := c.configureAIPaneOnRoute(ctx, runner, paneID, aiModeCodex, contextDir, title, aiPaneResumeMetadata{
		sessionID: threadID, resumeID: threadID, source: "app-server", updatedAt: c.nowTime().UTC(),
	}); err != nil {
		return err
	}
	return runAIPaneOptionOnRoute(ctx, runner, paneID, aiPaneThreadIDOption, threadID)
}

func (c *aiCommand) agentExecArgv(mode string, extraArgs []string) ([]string, string, error) {
	if mode == aiModeShell {
		if len(extraArgs) > 0 {
			return nil, "", errors.New("ai split --agent shell cannot use extra args")
		}
		return loginShellCommand(defaultInteractiveShell(c.lookupEnv)), "", nil
	}
	agentBin := c.findAgentBinary(mode)
	if agentBin == "" {
		message := c.missingAgentRunnerMessage(mode)
		_ = c.displayMessage(message)
		return nil, "", errors.New(message)
	}
	execArgv := []string{agentBin}
	execArgv = append(execArgv, extraArgs...)
	return execArgv, filepath.Dir(agentBin), nil
}

func (c *aiCommand) resolveContextDir() string {
	if dir := c.env("TMUX_SPLIT_CONTEXT_DIR"); isDir(dir) {
		return dir
	}
	if c.env("TMUX") != "" {
		if path := c.readMuxTrimmed("display-message", "-p", "-F", "#{pane_current_path}"); isDir(path) {
			return c.anchorPaneContextDir(path)
		}
	}
	if target := c.resolveRecentTmuxTarget(); target != "" {
		if path := c.readMuxTrimmed("display-message", "-p", "-t", target, "-F", "#{pane_current_path}"); isDir(path) {
			return path
		}
	}
	return c.resolveIDEContextDir()
}

// anchorPaneContextDir reconciles the live pane cwd against the current
// session's project anchor (@projmux_project_path, set at creation). When the
// pane sits inside the project tree (the anchor itself or a descendant) the
// intentional subdir is respected and the pane cwd wins; when it has drifted
// outside (another repo, $HOME, …) the anchor wins so discovery and launch stay
// project-relative. Sessions without an anchor (pre-anchor or externally
// created) fall back to the pane cwd unchanged — no regression.
func (c *aiCommand) anchorPaneContextDir(paneCWD string) string {
	session := c.readMuxTrimmed("display-message", "-p", "-F", "#{session_name}")
	anchor := c.resolveSessionProjectPath(session)
	if anchor == "" {
		return paneCWD
	}
	if pathWithinTree(anchor, paneCWD) {
		return paneCWD
	}
	return anchor
}

// resolveSessionProjectPath reads the project cwd anchor stored on the named
// session at creation time (@projmux_project_path). It returns the anchor only
// when it still resolves to a directory; otherwise "" so callers fall back to
// the live pane cwd.
func (c *aiCommand) resolveSessionProjectPath(sessionName string) string {
	if strings.TrimSpace(sessionName) == "" {
		return ""
	}
	anchor := c.readMuxTrimmed("show-options", "-t", sessionName, "-v", inttmux.ProjectPathSessionOption)
	if isDir(anchor) {
		return anchor
	}
	return ""
}

// pathWithinTree reports whether path is anchor itself or a descendant of it.
// It uses a pure path comparison (filepath.Rel with no leading ".."), matching
// the roadmap's "path descendant" rule; both inputs are assumed to be existing
// directories so no symlink resolution is attempted.
func pathWithinTree(anchor, path string) bool {
	anchor = strings.TrimSpace(anchor)
	path = strings.TrimSpace(path)
	if anchor == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(anchor, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

// splitOriginPane is the pane a popup-hosted split UI acts on.
//
// $TMUX_SPLIT_TARGET_PANE is written by popup-toggle when it opens a split
// picker (see buildPopupToggleWithStyle) and by openPicker's inline popup, and it
// holds the pane the operator pressed the key in. It is read here and only here: the
// popup job inherits $TMUX but no $TMUX_PANE, so the origin pane is the split
// UI's own knowledge to hand to create, not an ambient scope other verbs may
// consult.
//
// The value is resolved back through tmux so what leaves this function is an
// exact `%N` on the inherited server. An unresolvable value is returned
// verbatim rather than dropped, which keeps one behavior for both callers: the
// pane appears in the popup argv and in create's refusal text as the operator
// spelled it instead of silently becoming a different target.
func (c *aiCommand) splitOriginPane() string {
	pane := strings.TrimSpace(c.env("TMUX_SPLIT_TARGET_PANE"))
	if pane == "" {
		return ""
	}
	if resolved := c.readMuxTrimmed("display-message", "-p", "-t", pane, "-F", "#{pane_id}"); resolved != "" {
		return resolved
	}
	return pane
}

func (c *aiCommand) splitOriginClient() string {
	return strings.TrimSpace(c.env(canonicalCreateTargetClientEnv))
}

func (c *aiCommand) resolveTargetPane() string {
	if pane := c.splitOriginPane(); pane != "" {
		return pane
	}
	if c.env("TMUX") != "" {
		return c.readMuxTrimmed("display-message", "-p", "-F", "#{pane_id}")
	}
	if target := c.resolveRecentTmuxTarget(); target != "" {
		return c.readMuxTrimmed("display-message", "-p", "-t", target, "-F", "#{pane_id}")
	}
	return ""
}

func (c *aiCommand) resolveRecentTmuxTarget() string {
	out, err := c.readMux("list-clients", "-F", "#{client_activity}\t#{session_id}")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	bestActivity := ""
	bestTarget := ""
	for _, line := range lines {
		activity, target, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(target) == "" {
			continue
		}
		if bestActivity == "" || strings.TrimSpace(activity) > bestActivity {
			bestActivity = strings.TrimSpace(activity)
			bestTarget = strings.TrimSpace(target)
		}
	}
	return bestTarget
}

func (c *aiCommand) resolveIDEContextDir() string {
	homeDir, err := c.home()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return ""
	}
	cacheDir := filepath.Join(homeDir, ".cache", "ide")
	for _, name := range []string{"vscode", "code", "intellij-idea", "pycharm", "goland", "webstorm", "clion", "rider", "idea", "android-studio"} {
		content, err := os.ReadFile(filepath.Join(cacheDir, name))
		if err == nil {
			if path := strings.TrimSpace(string(content)); isDir(path) {
				return path
			}
		}
	}
	return ""
}

func (c *aiCommand) popupSize(widthPercent, widthMin, heightPercent, heightMin int) (string, string) {
	return c.popupAxisSize("width", widthPercent, widthMin), c.popupAxisSize("height", heightPercent, heightMin)
}

func (c *aiCommand) buildAgentTitle(mode, contextDir string) string {
	switch mode {
	case aiModeClaude:
		if title := strings.TrimSpace(c.env("CLAUDE_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if contextDir != "" {
			return "claude:" + filepath.Base(contextDir)
		}
		return "claude"
	case aiModeCodex:
		if title := strings.TrimSpace(c.env("CODEX_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if contextDir != "" {
			return "codex:" + filepath.Base(contextDir)
		}
		return "codexcli"
	case aiModeAntigravity:
		if title := strings.TrimSpace(c.env("ANTIGRAVITY_THREAD_TITLE")); title != "" {
			return "antigravity:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "antigravity:" + title
		}
		if contextDir != "" {
			return "antigravity:" + filepath.Base(contextDir)
		}
		return "antigravity"
	default:
		return mode
	}
}

func (c *aiCommand) agentSplitCommand(mode, pathPrepend, contextDir, title string, execArgv []string) ([]string, []string, error) {
	command := c.agentLaunchCommandForArgv(mode, pathPrepend, contextDir, title, execArgv)
	commandShell := posixCommandShell(c.lookupEnv)
	return append([]string{commandShell, "-lc"}, command), []string{commandShell, "-lc", command}, nil
}

func (c *aiCommand) agentLaunchCommandForArgv(mode, pathPrepend, contextDir, title string, execArgv []string) string {
	titleVar := "__" + mode + "_title"
	parts := []string{}
	// nvm/fnm/asdf/volta colocate `node` with the agent CLI; non-interactive
	// login shells may not source their init scripts, so without this prepend
	// the agent's `env node` shebang can fail and the pane exits immediately.
	if pathPrepend != "" && pathPrepend != "." && pathPrepend != "/" {
		parts = append(parts, "export PATH="+shellQuote(pathPrepend)+`":$PATH"`)
	}
	if contextDir != "" {
		parts = append(parts, "cd "+shellQuote(contextDir))
	}
	execParts := make([]string, 0, len(execArgv)+1)
	execParts = append(execParts, "exec")
	for _, arg := range execArgv {
		execParts = append(execParts, shellQuote(arg))
	}
	parts = append(parts,
		titleVar+"="+shellQuote(title),
		`printf '\033]0;%s\007' "$`+titleVar+`"`,
		strings.Join(execParts, " "),
	)
	return strings.Join(parts, " && ")
}

func (c *aiCommand) configureAIPane(paneID, mode, contextDir, title string, resume aiPaneResumeMetadata) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return
	}
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneManagedOption, "1")
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneAgentOption, normalizeAIMode(mode))
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneLaunchAuthorshipOption, "1")
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneContextOption, strings.TrimSpace(contextDir))
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, displayAITopic(title))
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "idle")
	c.configureAIPaneResumeMetadata(paneID, resume)
}

func (c *aiCommand) configureAIPaneOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, mode, contextDir, title string,
	resume aiPaneResumeMetadata,
) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}
	if err := runAIPaneTitleOnRoute(ctx, runner, paneID, title); err != nil {
		return err
	}
	for _, option := range [][2]string{
		{aiPaneManagedOption, "1"},
		{aiPaneAgentOption, normalizeAIMode(mode)},
		{aiPaneLaunchAuthorshipOption, "1"},
		{aiPaneContextOption, strings.TrimSpace(contextDir)},
		{aiPaneTopicOption, displayAITopic(title)},
		{aiPaneStateOption, "idle"},
	} {
		if err := runAIPaneOptionOnRoute(ctx, runner, paneID, option[0], option[1]); err != nil {
			return err
		}
	}
	for _, option := range aiPaneResumeOptions(resume) {
		if err := runAIPaneOptionOnRoute(ctx, runner, paneID, option[0], option[1]); err != nil {
			return err
		}
	}
	return nil
}

func runAIPaneTitleOnRoute(ctx context.Context, runner tmuxCommandRunner, paneID, title string) error {
	if runner == nil {
		return errors.New("managed Agent Pane binding requires an exact tmux runner")
	}
	if _, err := runner.Run(ctx, "tmux", "select-pane", "-T", title, "-t", paneID); err != nil {
		return fmt.Errorf("set Pane title: %w", err)
	}
	return nil
}

func runAIPaneOptionOnRoute(ctx context.Context, runner tmuxCommandRunner, paneID, option, value string) error {
	if runner == nil {
		return errors.New("managed Agent Pane binding requires an exact tmux runner")
	}
	if _, err := runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, option, value); err != nil {
		return fmt.Errorf("set Pane option %s: %w", option, err)
	}
	return nil
}

func (c *aiCommand) configureAIPaneResumeMetadata(paneID string, resume aiPaneResumeMetadata) {
	for _, option := range aiPaneResumeOptions(resume) {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, option[0], option[1])
	}
}

func aiPaneResumeOptions(resume aiPaneResumeMetadata) [][2]string {
	options := make([][2]string, 0, 4)
	for _, option := range [][2]string{
		{aiPaneSessionIDOption, strings.TrimSpace(resume.sessionID)},
		{aiPaneResumeIDOption, strings.TrimSpace(resume.resumeID)},
		{aiPaneResumeSourceOption, strings.TrimSpace(resume.source)},
	} {
		if option[1] != "" {
			options = append(options, option)
		}
	}
	if !resume.updatedAt.IsZero() {
		options = append(options, [2]string{aiPaneResumeUpdatedAtOption, resume.updatedAt.UTC().Format(time.RFC3339)})
	}
	return options
}

func (c *aiCommand) popupAxisSize(axis string, percent, minimum int) string {
	format := "#{client_width}"
	if axis == "height" {
		format = "#{client_height}"
	}
	total := parsePositiveInt(c.readTrimmed("tmux", "display-message", "-p", "-F", format))
	if total <= 0 {
		return fmt.Sprintf("%d%%", percent)
	}
	value := min(max(total*percent/100, minimum), total)
	return fmt.Sprintf("%d", value)
}

func (c *aiCommand) agentAvailable(mode string) bool {
	if mode == aiModeShell {
		return true
	}
	return c.findAgentBinary(mode) != ""
}

func (c *aiCommand) findAgentBinary(mode string) string {
	provider, ok := aiprovider.Lookup(mode)
	if !ok || !provider.PickerEligible || strings.TrimSpace(provider.BinaryName) == "" {
		return ""
	}
	binName := provider.BinaryName

	home := c.homeOrEmpty()
	if path := firstExecutable(
		c.readTrimmed("command", "-v", binName),
		filepath.Join(home, ".npm-global", "bin", binName),
		filepath.Join(home, ".local", "bin", binName),
	); path != "" {
		return path
	}
	if path := newestExecutable(nodeManagerCandidates(home, binName)); path != "" {
		return path
	}
	if provider.ID == aiprovider.Codex {
		matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", "codex"))
		return newestExecutable(matches)
	}
	return ""
}

func (c *aiCommand) missingAgentRunnerMessage(mode string) string {
	return fmt.Sprintf("selected runner is not installed: %s", mode)
}

// nodeManagerCandidates returns possible install paths for a globally-installed
// npm CLI when the user manages Node via nvm / fnm / asdf / volta. These tools
// install into versioned prefixes that aren't on PATH unless the shell ran
// their init script, so we probe the on-disk layouts directly.
func nodeManagerCandidates(home, binName string) []string {
	if home == "" || binName == "" {
		return nil
	}
	var candidates []string
	globs := []string{
		filepath.Join(home, ".nvm", "versions", "node", "*", "bin", binName),
		filepath.Join(home, ".fnm", "node-versions", "*", "installation", "bin", binName),
		filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin", binName),
	}
	for _, pattern := range globs {
		matches, _ := filepath.Glob(pattern)
		candidates = append(candidates, matches...)
	}
	candidates = append(candidates, filepath.Join(home, ".volta", "bin", binName))
	return candidates
}

func (c *aiCommand) displayMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	if strings.TrimSpace(c.env("TMUX")) == "" {
		return nil
	}
	return c.run("tmux", "display-message", message)
}

func (c *aiCommand) binaryPath() (string, error) {
	if c.executable == nil {
		return "", errors.New("ai executable resolver is not configured")
	}
	return c.executable()
}

func (c *aiCommand) home() (string, error) {
	if c.homeDir == nil {
		return "", errors.New("ai home directory resolver is not configured")
	}
	return c.homeDir()
}

func (c *aiCommand) homeOrEmpty() string {
	homeDir, err := c.home()
	if err != nil {
		return ""
	}
	return homeDir
}

func (c *aiCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func (c *aiCommand) locale() i18n.Locale {
	if c == nil {
		return i18n.FallbackLocale
	}
	return appLocale(c.homeDir, c.lookupEnv)
}

func (c *aiCommand) run(name string, args ...string) error {
	if c.runCommand == nil {
		return errors.New("ai command runner is not configured")
	}
	return c.runCommand(context.Background(), name, args...)
}

func (c *aiCommand) read(name string, args ...string) ([]byte, error) {
	if c.readCommand == nil {
		return nil, errors.New("ai command reader is not configured")
	}
	return c.readCommand(context.Background(), name, args...)
}

func (c *aiCommand) readMux(args ...string) ([]byte, error) {
	return c.read("tmux", args...)
}

func (c *aiCommand) readMuxTrimmed(args ...string) string {
	out, err := c.readMux(args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (c *aiCommand) muxRunner() intmux.Runner {
	return intmux.NewRunner(aiCommandMuxBackend{
		runCommand:  c.runCommand,
		readCommand: c.readCommand,
	})
}

type aiCommandMuxBackend struct {
	runCommand  func(ctx context.Context, name string, args ...string) error
	readCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (b aiCommandMuxBackend) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "tmux" && !aiMuxCommandNeedsOutput(args) {
		if b.runCommand != nil {
			return nil, b.runCommand(ctx, name, args...)
		}
		return nil, errors.New("ai command runner is not configured")
	}
	if b.readCommand == nil {
		return nil, errors.New("ai command reader is not configured")
	}
	return b.readCommand(ctx, name, args...)
}

func aiMuxCommandNeedsOutput(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "display-message", "list-panes", "list-windows", "capture-pane", "show-option", "show-options", "show-hooks":
		return true
	}
	return false
}

func (c *aiCommand) readTrimmed(name string, args ...string) string {
	out, err := c.read(name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (c *aiCommand) readTmuxDisplayMessageTrimmed(paneID, format string) string {
	output, err := c.muxRunner().DisplayMessageTrimmed(context.Background(), intmux.DisplayMessageOptions{
		Target: paneID,
		Format: format,
	})
	if err != nil {
		return ""
	}
	return output
}

func (c *aiCommand) readTmuxPaneOption(paneID, option string) string {
	output, err := c.muxRunner().ShowPaneOption(context.Background(), paneID, option)
	if err != nil {
		return ""
	}
	return output
}

// paneVisibleToClient mirrors attentionCommand.paneVisibleToClient: a pane is
// only "visible" when some attached client's #{client_active_pane} matches it.
// pane_active alone is true even when every client has moved to a different
// window or session, which made the auto-ack silently swallow reply pings.
func (c *aiCommand) paneVisibleToClient(paneID string) bool {
	output, err := c.read("tmux", "list-clients", "-F", "#{client_active_pane}")
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(strings.TrimRight(string(output), "\r\n"), "\n") {
		if strings.TrimSpace(line) == paneID {
			return true
		}
	}
	return false
}

func (c *aiCommand) duplicateAINotificationRecent(paneID, key string) bool {
	if key == "" {
		return false
	}
	dedupeSeconds := c.aiNotifyDedupeSeconds()
	if c.readTmuxPaneOption(paneID, "@projmux_desktop_notification_key") != key {
		return false
	}
	lastAt := parsePositiveInt(c.readTmuxPaneOption(paneID, "@projmux_desktop_notification_at"))
	if lastAt <= 0 {
		return false
	}
	return c.now().Unix()-int64(lastAt) < int64(dedupeSeconds)
}

func (c *aiCommand) recordAINotification(paneID, key string) {
	runner := c.muxRunner()
	_ = runner.SetPaneOption(context.Background(), paneID, "@projmux_desktop_notified", "1")
	if key != "" {
		_ = runner.SetPaneOption(context.Background(), paneID, "@projmux_desktop_notification_key", key)
	}
	_ = runner.SetPaneOption(context.Background(), paneID, "@projmux_desktop_notification_at", fmt.Sprintf("%d", c.now().Unix()))
}

func (c *aiCommand) resolvePowerShell() string {
	if path := c.readTrimmed("command", "-v", "powershell.exe"); path != "" {
		return path
	}
	for _, candidate := range []string{
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe",
		"/mnt/c/Windows/system32/WindowsPowerShell/v1.0/powershell.exe",
	} {
		if isExecutable(candidate) {
			return candidate
		}
	}
	return ""
}

func (c *aiCommand) isWSL() bool {
	if c.env("WSL_DISTRO_NAME") != "" {
		return true
	}
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	content, err := readFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(content)), "microsoft")
}

func (c *aiCommand) gitBranchForPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if _, err := c.read("git", "-C", path, "rev-parse", "--is-inside-work-tree"); err != nil {
		return ""
	}
	if branch := c.readTrimmed("git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD"); branch != "" {
		return branch
	}
	return c.readTrimmed("git", "-C", path, "rev-parse", "--short", "HEAD")
}

type aiPaneInfo struct {
	title          string
	command        string
	path           string
	agent          string
	context        string
	topic          string
	topicManual    string
	aiState        string
	aiBadgeKind    string
	attentionState string
	ack            string
	capture        string
}

func (c *aiCommand) readAIPaneInfo(paneID string) aiPaneInfo {
	const delim = "__PROJMUX_TMUX_AI_SEP__"
	format := strings.Join([]string{
		"#{pane_title}",
		"#{pane_current_command}",
		"#{pane_current_path}",
		"#{" + aiPaneAgentOption + "}",
		"#{" + aiPaneContextOption + "}",
		"#{" + aiPaneTopicOption + "}",
		"#{" + aiPaneTopicManualOption + "}",
		"#{" + aiPaneStateOption + "}",
		"#{" + aiPaneBadgeKindOption + "}",
		"#{" + attentionStateOption + "}",
		"#{" + attentionAckOption + "}",
	}, delim)
	snapshot := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, format)
	fields := strings.Split(snapshot, delim)
	info := aiPaneInfo{}
	if len(fields) > 0 {
		info.title = strings.TrimSpace(fields[0])
	}
	if len(fields) > 1 {
		info.command = strings.TrimSpace(fields[1])
	}
	if len(fields) > 2 {
		info.path = strings.TrimSpace(fields[2])
	}
	if len(fields) > 3 {
		info.agent = strings.TrimSpace(fields[3])
	}
	if len(fields) > 4 {
		info.context = strings.TrimSpace(fields[4])
	}
	if len(fields) > 5 {
		info.topic = strings.TrimSpace(fields[5])
	}
	if len(fields) > 10 {
		info.topicManual = strings.TrimSpace(fields[6])
		info.aiState = strings.TrimSpace(fields[7])
		info.aiBadgeKind = normalizeAIBadgeKind(fields[8])
		info.attentionState = strings.TrimSpace(fields[9])
		info.ack = strings.TrimSpace(fields[10])
	} else if len(fields) > 9 {
		info.topicManual = strings.TrimSpace(fields[6])
		info.aiState = strings.TrimSpace(fields[7])
		info.attentionState = strings.TrimSpace(fields[8])
		info.ack = strings.TrimSpace(fields[9])
	} else {
		if len(fields) > 6 {
			info.aiState = strings.TrimSpace(fields[6])
		}
		if len(fields) > 7 {
			info.attentionState = strings.TrimSpace(fields[7])
		}
		if len(fields) > 8 {
			info.ack = strings.TrimSpace(fields[8])
		}
	}
	if info.title == "" {
		info.title = c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}")
	}
	info.capture = c.readAIPaneCapture(paneID)
	return info
}

func (c *aiCommand) bootstrapAIWatchMetadata(paneID string, info aiPaneInfo) aiPaneInfo {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return info
	}
	agent := normalizeAIMode(strings.TrimSpace(info.agent))
	if agent == aiModeSelective {
		agent = inferAIAgent(info)
	}
	if agent == "" || agent == aiModeSelective || agent == aiModeShell {
		return info
	}
	if strings.TrimSpace(info.agent) == "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneManagedOption, "1")
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneAgentOption, agent)
		info.agent = agent
	}
	if strings.TrimSpace(info.context) == "" && strings.TrimSpace(info.path) != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneContextOption, strings.TrimSpace(info.path))
		info.context = strings.TrimSpace(info.path)
	}
	if strings.TrimSpace(info.topic) == "" && !isTruthyTmuxOption(info.topicManual) {
		if topic := bestAITopic(info.title, info.capture); topic != "" {
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, topic)
			info.topic = topic
		}
	}
	return info
}

func inferAIAgent(info aiPaneInfo) string {
	evidence := strings.ToLower(strings.Join([]string{info.agent, info.title, info.command, info.capture}, "\n"))
	switch {
	case strings.Contains(evidence, "claude"):
		return aiModeClaude
	case strings.Contains(evidence, "codex") || strings.Contains(evidence, "gpt-"):
		return aiModeCodex
	default:
		return ""
	}
}

func (c *aiCommand) readAIWatchSnapshot(paneID string) aiPaneInfo {
	info := c.readAIPaneInfo(paneID)
	if info.title != "" || info.agent != "" || info.topic != "" || info.aiState != "" || info.attentionState != "" || info.ack != "" {
		return info
	}

	const delim = "__PROJMUX_TMUX_AI_SEP__"
	snapshot := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}"+delim+"#{"+attentionStateOption+"}"+delim+"#{"+attentionAckOption+"}")
	title, rest, ok := strings.Cut(snapshot, delim)
	if !ok {
		info.title = snapshot
		return info
	}
	info.title = strings.TrimSpace(title)
	info.attentionState, info.ack, _ = strings.Cut(rest, delim)
	info.attentionState = strings.TrimSpace(info.attentionState)
	info.ack = strings.TrimSpace(info.ack)
	return info
}

func (c *aiCommand) readAIWatchTitleGate(paneID string) (alive bool, hookActive bool) {
	const delim = "__PROJMUX_TMUX_AI_GATE_SEP__"
	if out, err := c.read("tmux", "display-message", "-p", "-t", paneID, "#{pane_id}"+delim+"#{"+aiPaneHookActiveOption+"}"); err == nil {
		currentPaneID, active, ok := strings.Cut(strings.TrimSpace(string(out)), delim)
		if ok {
			if strings.TrimSpace(currentPaneID) != paneID {
				return false, false
			}
			return true, isTruthyTmuxOption(active)
		}
	}
	currentPaneID, err := c.read("tmux", "display-message", "-p", "-t", paneID, "#{pane_id}")
	if err != nil || strings.TrimSpace(string(currentPaneID)) != paneID {
		return false, false
	}
	return true, isTruthyTmuxOption(c.readTmuxPaneOption(paneID, aiPaneHookActiveOption))
}

func (c *aiCommand) readAIPaneCapture(paneID string) string {
	out, err := c.muxRunner().CapturePane(context.Background(), intmux.CapturePaneOptions{
		Target:    paneID,
		StartLine: -80,
		JoinLines: true,
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (c *aiCommand) recordAITopic(paneID, topic, manual string) {
	topic = strings.TrimSpace(topic)
	if paneID == "" || topic == "" || isTruthyTmuxOption(manual) {
		return
	}
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, topic)
}

func isTruthyTmuxOption(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (c *aiCommand) watchInterval() time.Duration {
	value := strings.TrimSpace(c.env("PROJMUX_CODEX_TITLE_WATCH_INTERVAL"))
	if value == "" {
		return 400 * time.Millisecond
	}
	if strings.ContainsAny(value, "hmsuµns") {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	parts := strings.SplitN(value, ".", 2)
	seconds := parsePositiveInt(parts[0])
	millis := 0
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		millis = parsePositiveInt(frac)
	}
	d := time.Duration(seconds)*time.Second + time.Duration(millis)*time.Millisecond
	if d <= 0 {
		return 400 * time.Millisecond
	}
	return d
}

func (c *aiCommand) watchSettleLoops() int {
	loops := parsePositiveInt(c.env("PROJMUX_CODEX_REPLY_SETTLE_LOOPS"))
	if loops <= 0 {
		return 75
	}
	return loops
}

func (c *aiCommand) sleepFor(d time.Duration) {
	if c.sleep == nil {
		time.Sleep(d)
		return
	}
	c.sleep(d)
}

func parseAISplitDirection(args []string, command string, stderr io.Writer) (string, error) {
	direction := "right"
	switch len(args) {
	case 0:
	case 1:
		direction = strings.TrimSpace(args[0])
	default:
		printAIUsage(stderr)
		return "", fmt.Errorf("%s accepts at most 1 [right|down] argument", command)
	}
	switch direction {
	case "right", "down":
		return direction, nil
	default:
		printAIUsage(stderr)
		return "", fmt.Errorf("%s direction must be right or down", command)
	}
}

func trimAIStatePrefix(title string) string {
	title = strings.TrimLeft(title, " \t")
	if title == "" {
		return ""
	}
	r, size := utf8DecodeRune(title)
	if (r >= 0x2800 && r <= 0x28ff) || r == '✳' || r == '✔' {
		return strings.TrimLeft(title[size:], " \t")
	}
	return title
}

func normalizeAITitle(title string) string {
	return strings.ToLower(trimAIStatePrefix(title))
}

func displayAITopic(title string) string {
	topic := trimAIStatePrefix(title)
	for _, prefix := range []string{"codex:", "Codex:", "claude:", "Claude:", "antigravity:", "Antigravity:"} {
		topic = strings.TrimPrefix(topic, prefix)
	}
	return strings.TrimSpace(topic)
}

func bestAITopic(title, capture string) string {
	if topic := displayAITopic(title); topic != "" && !isGenericAITopic(topic) {
		return topic
	}
	for line := range strings.SplitSeq(capture, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isGenericAITopic(line) {
			continue
		}
		if isAIReplyTitle(line) {
			return displayAITopic(line)
		}
	}
	return displayAITopic(title)
}

func isGenericAITopic(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "codex", "codexcli", "claude", "ai", "thinking", "running", "working", "waiting", "idle", "done", "complete", "completed":
		return true
	default:
		return false
	}
}

func aiReplyKindForTitle(title string) string {
	normalized := normalizeAITitle(title)
	switch {
	case strings.Contains(normalized, "approval") || strings.Contains(normalized, "approve") || strings.Contains(normalized, "permission") || strings.Contains(normalized, "allow"):
		return "approval_required"
	case strings.Contains(normalized, "select") || strings.Contains(normalized, "choice") || strings.Contains(normalized, "pick") || strings.Contains(normalized, "which"):
		return "selection_required"
	case strings.Contains(normalized, "confirm") || strings.Contains(normalized, "confirmation"):
		return "confirmation_required"
	case strings.Contains(normalized, "waiting for input") || strings.Contains(normalized, "input") || strings.Contains(normalized, "answer") || (strings.Contains(normalized, "reply") && !strings.Contains(normalized, "response")):
		return "input_required"
	default:
		return "response_ready"
	}
}

func aiAgentDisplayName(title string) string {
	normalized := normalizeAITitle(title)
	switch {
	case strings.HasPrefix(normalized, "claude:") || strings.Contains(normalized, "claude"):
		return "Claude"
	case strings.HasPrefix(normalized, "codex:") || strings.Contains(normalized, "codex"):
		return "Codex"
	case strings.HasPrefix(normalized, "antigravity:") || strings.Contains(normalized, "antigravity"):
		return "Antigravity"
	default:
		return "AI"
	}
}

func aiSummaryForKind(kind, agentName, topic string) string {
	return aiSummaryForKindLocale(kind, agentName, topic, i18n.FallbackLocale)
}

func aiSummaryForKindLocale(kind, agentName, topic string, locale i18n.Locale) string {
	category := "response_complete"
	switch kind {
	case "approval_required":
		category = "approval_required"
	case "selection_required":
		category = "selection_required"
	case "confirmation_required":
		category = "confirmation_required"
	case "input_required":
		category = "input_required"
	}
	summary := aiNotifyCategoryLabel(category, locale)
	if strings.TrimSpace(agentName) != "" {
		summary = joinAINotifyText(strings.TrimSpace(agentName), summary)
	}
	return summary
}

func aiOSNotificationUrgency(string) string {
	return "normal"
}

func aiNotificationTextAgentWithMetadata(text string, metadata map[string]string) string {
	if agent := aiNotificationMetadataAgent(metadata); agent != "" {
		return agent
	}
	if parts := parseAITextNotificationParts(text); parts.Agent != "" {
		return parts.Agent
	}
	switch {
	case strings.HasPrefix(strings.TrimSpace(text), "Codex"):
		return "Codex"
	case strings.HasPrefix(strings.TrimSpace(text), "Claude"):
		return "Claude"
	default:
		return "AI"
	}
}

func aiNotificationMetadataAgent(metadata map[string]string) string {
	switch strings.ToLower(strings.TrimSpace(metadata[notify.MetaAgent])) {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	default:
		return ""
	}
}

func aiNotificationMetadataCategoryID(metadata map[string]string) string {
	category := strings.TrimSpace(metadata[notify.MetaCategory])
	if category == "" {
		return ""
	}
	return category
}

type aiTextNotificationParts struct {
	Agent    string
	Category string
	Detail   string
}

func parseAITextNotificationParts(text string) aiTextNotificationParts {
	fields := strings.Split(strings.TrimSpace(text), " · ")
	if len(fields) < 2 {
		return aiTextNotificationParts{}
	}
	agent := strings.TrimSpace(fields[0])
	if !isKnownAgent(strings.ToLower(agent)) {
		return aiTextNotificationParts{}
	}
	category := strings.TrimSpace(fields[1])
	if !isFixedAINotificationCategory(category) {
		return aiTextNotificationParts{}
	}
	return aiTextNotificationParts{
		Agent:    agent,
		Category: category,
		Detail:   strings.TrimSpace(strings.Join(fields[2:], " · ")),
	}
}

func isFixedAINotificationCategory(category string) bool {
	_, _, ok := aiNotifyCategoryMessageKey(aiNotifyCategoryFromLabel(category))
	return ok
}

type renderedAINotifyText struct {
	Agent    string
	Category string
	Summary  string
	Detail   string
	Full     string
}

func renderAINotifyText(text string, metadata map[string]string, locale i18n.Locale) renderedAINotifyText {
	text = strings.TrimSpace(text)
	parts := parseAITextNotificationParts(text)

	agent := strings.TrimSpace(parts.Agent)
	if agent == "" {
		agent = aiNotificationMetadataAgent(metadata)
	}
	category := ""
	if parts.Category != "" {
		category = aiNotifyCategoryFromLabel(parts.Category)
	} else {
		category = aiNotificationMetadataCategoryID(metadata)
	}
	detail := strings.TrimSpace(parts.Detail)
	if parts.Category == "" {
		detail = text
	}

	if category == "" {
		return renderedAINotifyText{
			Agent:   agent,
			Summary: text,
			Detail:  detail,
			Full:    text,
		}
	}
	if agent == "" {
		agent = "AI"
	}
	categoryLabel := aiNotifyCategoryLabel(category, locale)
	summary := joinAINotifyDisplayText(agent, categoryLabel)
	displayDetail := detail
	if aiNotifySuppressDetail(category, displayDetail) {
		displayDetail = ""
	}
	full := summary
	if displayDetail != "" {
		full = joinAINotifyDisplayText(summary, displayDetail)
	}
	return renderedAINotifyText{
		Agent:    agent,
		Category: category,
		Summary:  summary,
		Detail:   displayDetail,
		Full:     full,
	}
}

func joinAINotifyDisplayText(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " · ")
}

func aiNotifySuppressDetail(category, detail string) bool {
	normalized := strings.ToLower(strings.TrimSpace(category))
	return (normalized == "response_complete" || normalized == "response_ready") && strings.EqualFold(strings.TrimSpace(detail), "Ready")
}

func aiNotifyCategoryFromLabel(label string) string {
	normalized := strings.ToLower(strings.TrimSpace(label))
	normalized = strings.ReplaceAll(normalized, "_", " ")
	switch normalized {
	case "approval required":
		return "approval_required"
	case "response complete", "response ready":
		return "response_complete"
	case "input required":
		return "input_required"
	case "selection required":
		return "selection_required"
	case "confirmation required":
		return "confirmation_required"
	case "error":
		return "error"
	case "subagent stopped":
		return "subagent_stopped"
	case "teammate waiting":
		return "teammate_waiting"
	case "review pending":
		return "review_pending"
	default:
		return strings.ReplaceAll(normalized, " ", "_")
	}
}

func aiNotifyCategoryLabel(category string, locale i18n.Locale) string {
	key, fallback, ok := aiNotifyCategoryMessageKey(category)
	if !ok {
		return titleCaseAINotifyCategory(strings.ReplaceAll(strings.TrimSpace(category), "_", " "))
	}
	return localizeText(locale, key, fallback)
}

func aiNotifyCategoryMessageKey(category string) (i18n.Key, string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(category))
	switch normalized {
	case "approval_required":
		return i18n.KeyNotifyAIApprovalRequired, "Approval required", true
	case "input_required":
		return i18n.KeyNotifyAIInputRequired, "Input required", true
	case "selection_required":
		return i18n.KeyNotifyAISelectionRequired, "Selection required", true
	case "confirmation_required":
		return i18n.KeyNotifyAIConfirmationRequired, "Confirmation required", true
	case "error":
		return i18n.KeyNotifyAIError, "Error", true
	case "subagent_stopped":
		return i18n.KeyNotifyAISubagentStopped, "Subagent stopped", true
	case "teammate_waiting":
		return i18n.KeyNotifyAITeammateWaiting, "Teammate waiting", true
	case "review_pending":
		return i18n.KeyNotifyAIReviewPending, "Review pending:", true
	default:
		return i18n.KeyNotifyAIResponseComplete, "Response complete", normalized == "" || normalized == "response_complete" || normalized == "response_ready"
	}
}

func titleCaseAINotifyCategory(category string) string {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(category)))
	for i, word := range words {
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func aiProjectName(path string) string {
	project := filepath.Base(strings.TrimSpace(path))
	if project == "." || project == string(filepath.Separator) {
		return ""
	}
	return project
}

func aiNotificationBody(title, project, branch, sessionName, windowName string) string {
	context := displayAITopic(title)
	if isGenericAITopic(context) || aiReplyKindForTitle(context) != "response_ready" {
		context = ""
	}
	projectPart := ""
	switch {
	case project != "" && branch != "":
		projectPart = project + "/" + branch
	case project != "":
		projectPart = project
	case branch != "":
		projectPart = branch
	}
	switch {
	case context != "" && projectPart != "":
		return context + " · " + projectPart
	case context != "":
		return context
	case projectPart != "":
		return projectPart
	default:
		return ""
	}
}

func aiNotificationKey(kind, title string) string {
	return kind + "|" + normalizeAITitle(displayAITopic(title))
}

func aiBusySignal(title, capture string) (bool, string) {
	if isAIBusyTitle(title) {
		return true, "title:" + strings.TrimSpace(title)
	}
	line := latestAIPaneCaptureLine(capture)
	if isAIBusyTitle(line) {
		return true, "capture:" + normalizeAITitle(line)
	}
	return false, ""
}

func latestAIPaneCaptureLine(capture string) string {
	var latest string
	for line := range strings.SplitSeq(capture, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			latest = line
		}
	}
	return latest
}

func isAIBusyTitle(title string) bool {
	if title == "" {
		return false
	}
	r, _ := utf8DecodeRune(strings.TrimLeft(title, " \t"))
	if r >= 0x2800 && r <= 0x28ff {
		return true
	}
	normalized := normalizeAITitle(title)
	return strings.Contains(normalized, "thinking") ||
		strings.Contains(normalized, "responding") ||
		strings.Contains(normalized, "running") ||
		strings.Contains(normalized, "working") ||
		strings.Contains(normalized, "streaming") ||
		strings.Contains(normalized, "generating")
}

func normalizeAIBadgeKind(kind string) string {
	return aibadge.Normalize(kind)
}

func aiBadgeKindForStatus(state, explicit string) string {
	if kind := normalizeAIBadgeKind(explicit); kind != "" {
		return kind
	}
	switch strings.TrimSpace(state) {
	case "thinking":
		return aiBadgeKindInProgress
	case "waiting":
		return aiBadgeKindResponseComplete
	default:
		return ""
	}
}

func aiBadgeKindForReplyEvidence(evidence string) string {
	switch aiReplyKindForTitle(evidence) {
	case aiBadgeKindApprovalRequired:
		return aiBadgeKindApprovalRequired
	case aiBadgeKindInputRequired, "selection_required", "confirmation_required":
		return aiBadgeKindInputRequired
	default:
		return aiBadgeKindResponseComplete
	}
}

func aiBadgeKindForNotifyCategory(category string) string {
	switch strings.TrimSpace(category) {
	case aiBadgeKindApprovalRequired:
		return aiBadgeKindApprovalRequired
	case aiBadgeKindInputRequired:
		return aiBadgeKindInputRequired
	case aiBadgeKindResponseComplete, "response_ready":
		return aiBadgeKindResponseComplete
	default:
		return ""
	}
}

func aiBadgeKindMismatch(state, expected, actual string) bool {
	want := aiBadgeKindForStatus(state, expected)
	got := normalizeAIBadgeKind(actual)
	if want == "" {
		return got != ""
	}
	return got != want
}

func isAIReplyTitle(title string) bool {
	if title == "" {
		return false
	}
	normalized := normalizeAITitle(title)
	if strings.Contains(normalized, "approval") ||
		strings.Contains(normalized, "approve") ||
		strings.Contains(normalized, "permission") ||
		strings.Contains(normalized, "allow") ||
		strings.Contains(normalized, "input required") ||
		strings.Contains(normalized, "need input") ||
		strings.Contains(normalized, "select") ||
		strings.Contains(normalized, "choice") ||
		strings.Contains(normalized, "pick") ||
		strings.Contains(normalized, "which") ||
		strings.Contains(normalized, "confirm") {
		return true
	}
	return (strings.Contains(normalized, "response") && !strings.Contains(normalized, "responding")) ||
		strings.Contains(normalized, "reply") ||
		strings.Contains(normalized, "response needed") ||
		strings.Contains(normalized, "waiting for input") ||
		strings.Contains(normalized, "waiting") ||
		strings.Contains(normalized, "complete") ||
		strings.Contains(normalized, "completed") ||
		strings.Contains(normalized, "done") ||
		strings.Contains(normalized, "idle")
}

func aiAttentionMismatch(nextState, attentionState string) bool {
	switch nextState {
	case "thinking":
		return attentionState != attentionStateBusy
	case "waiting":
		return attentionState != attentionStateReply
	default:
		return attentionState != ""
	}
}

// buildToastPowerShell composes the PowerShell snippet that Windows runs to
// surface a Toast notification.
//
// The Toast is always passive: the root <toast> element never gains a
// `launch=` / `activationType="protocol"` pair, so Windows has no click
// target to activate and projmux never registers a URI scheme handler.
// Desktop delivery must not be able to pull the host terminal window
// forward, and a clickable Toast is exactly that capability.
func buildToastPowerShell(summary, body, appName, tag, group, iconPath string, expireMS int) string {
	tagLine := ""
	if tag != "" {
		tagLine = "$toast.Tag = '" + psEscape(truncate64(tag)) + "'"
	}
	groupLine := ""
	if group != "" {
		groupLine = "$toast.Group = '" + psEscape(truncate64(group)) + "'"
	}
	iconXML := ""
	if iconPath != "" {
		iconXML = "\n      <image placement=\"appLogoOverride\" hint-crop=\"circle\" src=\"" + xmlEscape(iconPath) + "\"/>"
	}
	toastDuration := ""
	expirationLine := ""
	if expireMS > 0 {
		toastDuration = ` duration="short"`
		expirationLine = "$toast.ExpirationTime = [DateTimeOffset]::Now.AddMilliseconds(" + fmt.Sprintf("%d", expireMS) + ")"
	}
	return `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
$xml = @'
<toast` + toastDuration + `>
  <visual>
    <binding template="ToastGeneric">` + iconXML + `
      <text>` + xmlEscape(summary) + `</text>
      <text>` + xmlEscape(body) + `</text>
    </binding>
  </visual>
</toast>
'@
$tpl = [Windows.Data.Xml.Dom.XmlDocument]::new()
$tpl.LoadXml($xml)
$toast = [Windows.UI.Notifications.ToastNotification]::new($tpl)
` + tagLine + `
` + groupLine + `
` + expirationLine + `
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + psEscape(appName) + `').Show($toast)
`
}

func buildRegisterToastAppIDPowerShell(appID, displayName, iconURI string) string {
	iconLine := "Remove-ItemProperty -Path $regPath -Name 'IconUri' -ErrorAction SilentlyContinue"
	if iconURI != "" {
		iconLine = "Set-ItemProperty -Path $regPath -Name 'IconUri' -Value '" + psEscape(iconURI) + "' -Type String"
	}
	return `$regPath = "HKCU:\SOFTWARE\Classes\AppUserModelId\` + psEscape(appID) + `"
Add-Type -Language CSharp @"
using System;
using System.Runtime.InteropServices;

[ComImport, Guid("00021401-0000-0000-C000-000000000046")]
internal class ShellLink
{
}

[ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("000214F9-0000-0000-C000-000000000046")]
internal interface IShellLinkW
{
    void GetPath([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszFile, int cch, IntPtr pfd, uint fFlags);
    void GetIDList(out IntPtr ppidl);
    void SetIDList(IntPtr pidl);
    void GetDescription([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszName, int cch);
    void SetDescription([MarshalAs(UnmanagedType.LPWStr)] string pszName);
    void GetWorkingDirectory([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszDir, int cch);
    void SetWorkingDirectory([MarshalAs(UnmanagedType.LPWStr)] string pszDir);
    void GetArguments([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszArgs, int cch);
    void SetArguments([MarshalAs(UnmanagedType.LPWStr)] string pszArgs);
    void GetHotkey(out short pwHotkey);
    void SetHotkey(short wHotkey);
    void GetShowCmd(out int piShowCmd);
    void SetShowCmd(int iShowCmd);
    void GetIconLocation([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszIconPath, int cch, out int piIcon);
    void SetIconLocation([MarshalAs(UnmanagedType.LPWStr)] string pszIconPath, int iIcon);
    void SetRelativePath([MarshalAs(UnmanagedType.LPWStr)] string pszPathRel, uint dwReserved);
    void Resolve(IntPtr hwnd, uint fFlags);
    void SetPath([MarshalAs(UnmanagedType.LPWStr)] string pszFile);
}

[ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("0000010b-0000-0000-C000-000000000046")]
internal interface IPersistFile
{
    void GetClassID(out Guid pClassID);
    [PreserveSig] int IsDirty();
    void Load([MarshalAs(UnmanagedType.LPWStr)] string pszFileName, uint dwMode);
    void Save([MarshalAs(UnmanagedType.LPWStr)] string pszFileName, bool fRemember);
    void SaveCompleted([MarshalAs(UnmanagedType.LPWStr)] string pszFileName);
    void GetCurFile([MarshalAs(UnmanagedType.LPWStr)] out string ppszFileName);
}

[ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99")]
internal interface IPropertyStore
{
    uint GetCount(out uint cProps);
    uint GetAt(uint iProp, out PROPERTYKEY pkey);
    uint GetValue(ref PROPERTYKEY key, [Out] PROPVARIANT pv);
    uint SetValue(ref PROPERTYKEY key, PROPVARIANT pv);
    uint Commit();
}

[StructLayout(LayoutKind.Sequential, Pack = 4)]
internal struct PROPERTYKEY
{
    public Guid fmtid;
    public uint pid;

    public PROPERTYKEY(string formatId, uint propertyId)
    {
        fmtid = new Guid(formatId);
        pid = propertyId;
    }
}

[StructLayout(LayoutKind.Sequential)]
internal sealed class PROPVARIANT : IDisposable
{
    private ushort vt;
    private ushort wReserved1;
    private ushort wReserved2;
    private ushort wReserved3;
    private IntPtr value;
    private int value2;

    public PROPVARIANT(string text)
    {
        vt = 31;
        value = Marshal.StringToCoTaskMemUni(text);
    }

    public void Dispose()
    {
        PropVariantClear(this);
        GC.SuppressFinalize(this);
    }

    ~PROPVARIANT()
    {
        Dispose();
    }

    [DllImport("ole32.dll")]
    private static extern int PropVariantClear([In, Out] PROPVARIANT propVariant);
}

public static class ProjmuxToastShortcut
{
    public static void Save(string shortcutPath, string targetPath, string arguments, string description, string iconLocation, string appId)
    {
        var shellLink = (IShellLinkW)new ShellLink();
        shellLink.SetPath(targetPath);
        shellLink.SetArguments(arguments);
        shellLink.SetDescription(description);
        if (!string.IsNullOrWhiteSpace(iconLocation))
        {
            shellLink.SetIconLocation(iconLocation, 0);
        }
        var persist = (IPersistFile)shellLink;
        var store = (IPropertyStore)shellLink;
        var key = new PROPERTYKEY("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3", 5);
        using (var value = new PROPVARIANT(appId))
        {
            store.SetValue(ref key, value);
        }
        store.Commit();
        persist.Save(shortcutPath, true);
    }
}
"@
if (-not (Test-Path $regPath)) {
  New-Item -Path $regPath -Force | Out-Null
}
Set-ItemProperty -Path $regPath -Name 'DisplayName' -Value '` + psEscape(displayName) + `' -Type String
Set-ItemProperty -Path $regPath -Name 'ShowInSettings' -Value 1 -Type DWord
` + iconLine + `
$shortcutDir = [Environment]::GetFolderPath('Programs')
$shortcutPath = Join-Path $shortcutDir 'projmux.lnk'
# The shortcut target is intentionally cmd.exe /c exit (a no-op). The
# shortcut is never actually launched — it exists solely as a property bag
# so the Windows toast platform can attach the
# PKEY_AppUserModel_ID (pid=5) value via IPropertyStore.SetValue in
# ProjmuxToastShortcut::Save. That AppUserModelID is what routes the toast
# under our DisplayName + icon.
#
# DO NOT change this target back to
#   powershell.exe -WindowStyle Hidden -Command exit
# Windows Defender silently quarantines Start Menu shortcuts whose target
# is "powershell.exe -WindowStyle Hidden ..." within seconds of creation,
# which leaves no shortcut at all and silently breaks the AppID routing
# (and, since the URI-launch click handler depends on the AppID being live
# when the toast fires, breaks click activation too). cmd.exe /c exit
# is treated as benign and survives Defender's heuristics.
#
# Note: PKEY_AppUserModel_ToastActivatorCLSID (pid=26) is intentionally
# NOT set on this shortcut. The Win32 unpackaged toast click path falls
# back to ShellExecute on the launch URI ONLY when no COM Toast Activator
# is registered alongside the AppID. Setting ToastActivatorCLSID would
# route Windows down the COM activator path first, which silently fails
# in our unpackaged setup and does not fall through to the URI handler.
$targetPath = [Environment]::ExpandEnvironmentVariables('%SystemRoot%\System32\cmd.exe')
$arguments = '/c exit'
$description = 'projmux tmux AI notifications'
$iconLocation = ''
if ('` + psEscape(iconURI) + `' -ne '') {
  $iconLocation = '` + psEscape(iconURI) + `'
}
[ProjmuxToastShortcut]::Save($shortcutPath, $targetPath, $arguments, $description, $iconLocation, '` + psEscape(appID) + `')
`
}

// buildLegacyToastCleanupPowerShell removes the Windows artifacts left over
// from the `projmux.TmuxCodex` era. It is intentionally idempotent and
// swallows every error: the script may run on machines that never had the
// legacy registration (fresh installs after the rename), and we never want
// the cleanup attempt itself to break notification dispatch.
//
// Two things are scrubbed:
//  1. The legacy `projmux Tmux Codex.lnk` shortcut in the Start Menu's
//     Programs folder (where `buildRegisterToastAppIDPowerShell` used to
//     drop it). Get-StartApps confirms presence before delete so we don't
//     touch unrelated shortcuts named similarly by users.
//  2. The legacy `HKCU:\Software\Classes\AppUserModelId\projmux.TmuxCodex`
//     registry key, which carried DisplayName / IconUri / ShowInSettings
//     metadata for the old AppID.
//
// The cleanup is gated by the caller (see `ensureWSLLegacyAppIDCleaned` in
// `notification.go`) with the `@projmux_legacy_appid_cleaned` tmux marker
// so we attempt it at most once per tmux server.
func buildLegacyToastCleanupPowerShell() string {
	return `try {
  $legacy = Get-StartApps | Where-Object Name -eq 'projmux Tmux Codex'
  if ($legacy) {
    $shortcutDir = [Environment]::GetFolderPath('Programs')
    $shortcutPath = Join-Path $shortcutDir 'projmux Tmux Codex.lnk'
    Remove-Item -Path $shortcutPath -Force -ErrorAction SilentlyContinue
  }
} catch { }
try {
  Remove-Item -Path 'HKCU:\Software\Classes\AppUserModelId\projmux.TmuxCodex' -Recurse -Force -ErrorAction SilentlyContinue
} catch { }
`
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

func psEscape(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func truncate64(value string) string {
	runes := []rune(value)
	if len(runes) <= 64 {
		return value
	}
	return string(runes[:64])
}

func encodeUTF16LEBase64(value string) string {
	runes := utf16.Encode([]rune(value))
	bytes := make([]byte, len(runes)*2)
	for i, r := range runes {
		binary.LittleEndian.PutUint16(bytes[i*2:], r)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func utf8DecodeRune(value string) (rune, int) {
	for _, r := range value {
		return r, len(string(r))
	}
	return 0, 0
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func normalizeAIMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case aiModeClaude, aiModeCodex, aiModeAntigravity, aiModeSelective, aiModeResume, aiModeShell:
		return strings.TrimSpace(mode)
	default:
		return aiModeSelective
	}
}

func runExternalCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isNoSelectionExit(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			switch status.ExitStatus() {
			case 1, 129, 130:
				return true
			}
		}
	}
	message := err.Error()
	return strings.Contains(message, "exit status 1") ||
		strings.Contains(message, "exit status 129") ||
		strings.Contains(message, "exit status 130")
}

func readExternalCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "command" && len(args) >= 2 && args[0] == "-v" {
		path, err := exec.LookPath(args[1])
		if err != nil {
			return nil, err
		}
		return []byte(path + "\n"), nil
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shellEnv(name, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return name + "=" + shellQuote(value) + " "
}

func ansiDim(value string) string {
	return "\x1b[90m" + value + "\x1b[0m"
}

func isDir(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func firstExecutable(paths ...string) string {
	for _, path := range paths {
		if isExecutable(path) {
			return path
		}
	}
	return ""
}

func newestExecutable(paths []string) string {
	var newestPath string
	var newestMod time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if newestPath == "" || info.ModTime().After(newestMod) {
			newestPath = path
			newestMod = info.ModTime()
		}
	}
	return newestPath
}

func parsePositiveInt(value string) int {
	n := 0
	for _, r := range strings.TrimSpace(value) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func printAIUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux create agent --provider <claude|codex|antigravity> [--project <ref>] [--window <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <extra-arg>...]")
	fmt.Fprintln(w, "  projmux create pane [--project <ref>] [--window <ref>]... [--create-window] [--placement right|down] [-o <mode>]")
	fmt.Fprintln(w, "  projmux config edit [--get|--set <mode>]")
	fmt.Fprintln(w, "  projmux agent status set <thinking|waiting|idle> [pane]")
	fmt.Fprintln(w, "  projmux create notification [flags]")
	fmt.Fprintln(w, "  projmux internal agent-hook watch-title [pane]")
	fmt.Fprintln(w, "  projmux internal agent-hook ingest codex-hook < payload.json")
	fmt.Fprintln(w, "  projmux internal agent-hook ingest claude-hook < payload.json")
	fmt.Fprintln(w, "  projmux internal agent-hook ingest antigravity-hook [--event <PreInvocation|PostInvocation|PostToolUse|Stop|Statusline>] < payload.json")
	fmt.Fprintln(w, "  projmux internal agent-hook ingest bell --pane <pane_id>")
	fmt.Fprintln(w, "  projmux diagnostics agent-hook [--tail N] [--json] [--path]")
	fmt.Fprintln(w, "  projmux agent integrate <codex|claude|antigravity|tmux-bell> [--dry-run] [--remove]")
	fmt.Fprintln(w, "  projmux agent topic set <text> [--pane <id>]")
	fmt.Fprintln(w, "  projmux agent topic clear [--pane <id>]")
	fmt.Fprintln(w, "  projmux agent topic get [--pane <id>]")
}
