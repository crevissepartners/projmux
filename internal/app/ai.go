package app

import (
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	intpsmux "github.com/crevissepartners/projmux/internal/integrations/psmux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	aiModeSelective   = "selective"
	aiModeResume      = "resume"
	aiModeClaude      = "claude"
	aiModeCodex       = "codex"
	aiModeAntigravity = "antigravity"
	aiModeShell       = "shell"

	aiResumeNewValue = "new"

	aiPaneManagedOption         = "@projmux_ai_managed"
	aiPaneAgentOption           = "@projmux_ai_agent"
	aiPaneContextOption         = "@projmux_ai_context"
	aiPaneStateOption           = "@projmux_ai_state"
	aiPaneBadgeKindOption       = "@projmux_ai_badge_kind"
	aiPaneTopicOption           = "@projmux_ai_topic"
	aiPaneTopicManualOption     = "@projmux_ai_topic_manual"
	aiPaneHookActiveOption      = "@projmux_ai_hook_active"
	aiPaneThreadIDOption        = "@projmux_ai_thread_id"
	aiPaneSessionIDOption       = "@projmux_ai_session_id"
	aiPaneResumeIDOption        = "@projmux_ai_resume_id"
	aiPaneResumeSourceOption    = "@projmux_ai_resume_source"
	aiPaneTranscriptPathOption  = "@projmux_ai_transcript_path"
	aiPaneResumeUpdatedAtOption = "@projmux_ai_resume_updated_at"

	aiBadgeKindInProgress       = aibadge.InProgress
	aiBadgeKindApprovalRequired = aibadge.ApprovalRequired
	aiBadgeKindInputRequired    = aibadge.InputRequired
	aiBadgeKindResponseComplete = aibadge.ResponseComplete
)

type aiCommandRunner interface {
	Run(options intpickercompat.Options) (intpickercompat.Result, error)
}

type aiCommand struct {
	runner       aiCommandRunner
	nativePicker intpicker.Runner
	executable   func() (string, error)
	lookupEnv    func(string) string
	homeDir      func() (string, error)
	stdin        io.Reader
	readFile     func(string) ([]byte, error)
	writeFile    func(string, []byte, os.FileMode) error
	mkdirAll     func(string, os.FileMode) error
	runCommand   func(ctx context.Context, name string, args ...string) error
	readCommand  func(ctx context.Context, name string, args ...string) ([]byte, error)
	now          func() time.Time
	sleep        func(time.Duration)
	producer     attentionNotifyProducer
	notifyStore  notifyStore
	events       notifyQueueRefreshEvents
}

func newAICommand() *aiCommand {
	return &aiCommand{
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		executable:   os.Executable,
		lookupEnv:    os.Getenv,
		homeDir:      os.UserHomeDir,
		stdin:        os.Stdin,
		readFile:     os.ReadFile,
		writeFile:    os.WriteFile,
		mkdirAll:     os.MkdirAll,
		runCommand:   runExternalCommand,
		readCommand:  readExternalCommand,
		now:          time.Now,
		sleep:        time.Sleep,
		producer:     newAttentionNotifyProducer(),
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
	case "split":
		return c.runSplit(args[1:], stderr)
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
	return c.applyAIStatusWithNotify(state, paneID, attentionNotifyInput{})
}

func (c *aiCommand) applyAIStatusWithNotify(state, paneID string, notifyIn attentionNotifyInput) error {
	return c.applyAIStatusInternal(state, paneID, notifyIn, true, true)
}

func (c *aiCommand) applyAIStatusWithBadgeKind(state, paneID, badgeKind string) error {
	return c.applyAIStatusWithNotify(state, paneID, attentionNotifyInput{BadgeKind: badgeKind})
}

func (c *aiCommand) applyAIStatusStateOnly(state, paneID string, notifyIn attentionNotifyInput) error {
	return c.applyAIStatusInternal(state, paneID, notifyIn, false, false)
}

func (c *aiCommand) applyAIStatusQueueOnly(state, paneID string, notifyIn attentionNotifyInput) error {
	notifyIn.SuppressHooks = true
	return c.applyAIStatusInternal(state, paneID, notifyIn, true, false)
}

func (c *aiCommand) applyAIStatusInternal(state, paneID string, notifyIn attentionNotifyInput, dispatchQueue, dispatchDesktop bool) error {
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
	switch state {
	case "thinking":
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "thinking")
		c.setAIPaneBadgeKind(paneID, badgeKind)
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, attentionStateOption, attentionStateBusy)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionAckOption)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionFocusArmedOption)
		c.notifyProducer().AckReplyReady(notifyIn)
	case "waiting":
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "waiting")
		c.setAIPaneBadgeKind(paneID, badgeKind)
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
			c.notifyProducer().AckReplyReady(notifyIn)
		}
	case "idle", "":
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "idle")
		c.setAIPaneBadgeKind(paneID, badgeKind)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionStateOption)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionFocusArmedOption)
		c.notifyProducer().AckReplyReady(notifyIn)
	default:
		return fmt.Errorf("unknown ai status state: %s", state)
	}
	return nil
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
		Summary:  summary,
		Body:     aiNotificationBody(bodyTitle, aiProjectName(panePath), c.gitBranchForPath(panePath), sessionName, windowName),
		Urgency:  aiOSNotificationUrgency(severity),
		ExpireMS: c.notificationExpireMS(),
		AppName:  desktopAppID,
		Icon:     c.notificationIcon(agent),
		Tag:      paneID,
		Group:    sessionName,
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
		c.recordAINotification(paneID, key)
		return nil
	}

	notification := aiNotification{
		Summary:  aiSummaryForKindLocale(replyKind, agentName, cleanTitle, c.locale()),
		Body:     aiNotificationBody(cleanTitle, aiProjectName(panePath), c.gitBranchForPath(panePath), sessionName, windowName),
		Urgency:  aiOSNotificationUrgency(replyKind),
		ExpireMS: c.notificationExpireMS(),
		AppName:  desktopAppID,
		Icon:     c.notificationIcon(agentName),
		Tag:      paneID,
		Group:    sessionName,
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

	interval := c.watchInterval()
	settleLimit := c.watchSettleLoops()
	phase := "idle"
	lastState := ""
	settleCount := 0
	lastBusySignal := ""
	for {
		alive, hookActive := c.readAIWatchTitleGate(paneID)
		if !alive {
			return nil
		}
		if hookActive {
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

type aiSplitInvocation struct {
	direction  string
	agent      string
	agentSet   bool
	forceAgent bool
	extraArgs  []string
}

type aiSplitLaunchPath string

const (
	aiSplitLaunchDirect  aiSplitLaunchPath = "direct"
	aiSplitLaunchDefault aiSplitLaunchPath = "default"
	aiSplitLaunchPicker  aiSplitLaunchPath = "picker"
)

func (c *aiCommand) runSplit(args []string, stderr io.Writer) error {
	invocation, err := parseAISplitInvocation(args, stderr)
	if err != nil {
		return err
	}
	if invocation.agentSet {
		if invocation.agent == aiModeSelective {
			return c.openPickerToggle(invocation.direction)
		}
		if invocation.agent == aiModeResume {
			return c.openResumePickerToggle(invocation.direction)
		}
		if invocation.agent == aiModeShell && len(invocation.extraArgs) == 0 {
			return c.runShellSplit(invocation.direction)
		}
		if !invocation.forceAgent {
			if err := c.requireAIAgentEnabled(invocation.agent, aiSplitLaunchDirect); err != nil {
				return err
			}
		}
		return c.runDirectAgentSplitWithExtraArgs(invocation.agent, invocation.direction, invocation.extraArgs)
	}

	mode := c.getMode()
	switch mode {
	case aiModeClaude:
		if err := c.requireAIAgentEnabled(aiModeClaude, aiSplitLaunchDefault); err != nil {
			return err
		}
		return c.runDirectAgentSplit(aiModeClaude, invocation.direction)
	case aiModeCodex:
		if err := c.requireAIAgentEnabled(aiModeCodex, aiSplitLaunchDefault); err != nil {
			return err
		}
		return c.runDirectAgentSplit(aiModeCodex, invocation.direction)
	case aiModeAntigravity:
		if err := c.requireAIAgentEnabled(aiModeAntigravity, aiSplitLaunchDefault); err != nil {
			return err
		}
		return c.runDirectAgentSplit(aiModeAntigravity, invocation.direction)
	case aiModeShell:
		return c.runShellSplit(invocation.direction)
	case aiModeSelective:
		return c.openPickerToggle(invocation.direction)
	case aiModeResume:
		return c.openResumePickerToggle(invocation.direction)
	default:
		return c.openPickerToggle(invocation.direction)
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
		return c.runShellSplit(direction)
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

	switch normalizeAIMode(result.Value) {
	case aiModeClaude:
		if err := c.requireAIAgentEnabled(aiModeClaude, aiSplitLaunchPicker); err != nil {
			return err
		}
		return c.runAgentSplit(aiModeClaude, direction)
	case aiModeCodex:
		if err := c.requireAIAgentEnabled(aiModeCodex, aiSplitLaunchPicker); err != nil {
			return err
		}
		return c.runAgentSplit(aiModeCodex, direction)
	case aiModeAntigravity:
		if err := c.requireAIAgentEnabled(aiModeAntigravity, aiSplitLaunchPicker); err != nil {
			return err
		}
		return c.runAgentSplit(aiModeAntigravity, direction)
	case aiModeShell:
		return c.runShellSplit(direction)
	default:
		return nil
	}
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
	result, err := runPickerOptionBackend(c.homeDir, c.lookupEnv, c.nativePicker, c.runner, c.themedPickerOptions(intpickercompat.Options{
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
	return runPickerOptionBackend(c.homeDir, c.lookupEnv, c.nativePicker, c.runner, c.themedPickerOptions(intpickercompat.Options{
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
	// Defer the turn count: discovery returns candidates fast (early-exit, no
	// per-turn scan) so the picker renders immediately, and the turn column is
	// filled for the displayed rows by a background pass (see runResumeSessionPicker).
	sessions, err := aisessions.Discover(contextDir, aisessions.DiscoverOptions{
		HomeDir:    homeDir,
		Depth:      depth,
		DeferTurns: true,
	})
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return c.runAgentPickerSelection(direction)
	}

	limit := resolveAIResumePickerLimit(c.homeDir, c.lookupEnv, contextDir).Limit
	result, err := c.runResumeSessionPicker(direction, sessions, limit, contextDir, depth)
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
	selection = enrichAIResumeSelection(selection, sessions)
	return c.runSelectedResumeSession(selection, direction)
}

func (c *aiCommand) runResumeSessionPicker(direction string, sessions []aisessions.SessionMeta, limit int, baseCWD string, depth int) (intpickercompat.Result, error) {
	if c.nativePicker == nil {
		return intpickercompat.Result{}, errors.New("native picker is not configured")
	}
	locale := appLocale(c.homeDir, c.lookupEnv)
	var now time.Time
	if c.now != nil {
		now = c.now()
	}
	entries, visible, total := aiResumeSessionRows(sessions, limit, now, locale, baseCWD, depth)
	footer := fmt.Sprintf(localizeUIText(locale, "Showing latest %d resume sessions."), visible)
	if total > visible {
		footer = fmt.Sprintf(localizeUIText(locale, "Showing latest %d of %d resume sessions."), visible, total)
	}
	// The rows above render immediately with a blank turn column (discovery
	// deferred the expensive per-turn scan). Fill it in a background pass over
	// just the displayed sessions and hand the picker rebuilt rows; the turn
	// count pops in without blocking the initial list. Values/search keys are
	// unchanged by the turn count, so focus and filtering are preserved.
	displayed := sessions
	if n := normalizeResumePickerLimit(limit); len(displayed) > n {
		displayed = displayed[:n]
	}
	deferredUpdate := func() (intpicker.DeferredUpdate, error) {
		aisessions.EnrichTurns(displayed)
		enriched, _, _ := aiResumeSessionRows(sessions, limit, now, locale, baseCWD, depth)
		return intpicker.DeferredUpdate{Items: intpickercompat.PickerItemsFromEntries(enriched)}, nil
	}
	return runPickerOptionBackend(c.homeDir, c.lookupEnv, c.nativePicker, c.runner, c.themedPickerOptions(intpickercompat.Options{
		UI:             "ai-resume-picker",
		Entries:        entries,
		Title:          localizeUIText(locale, "AI Resume - Split direction: ") + direction,
		Prompt:         "AI Resume > ",
		Footer:         projmuxFooter(footer),
		ExpectKeys:     []string{"enter"},
		Bindings:       pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, aiResumePickerPopupMode(direction), "esc", "ctrl-c", "ctrl-alt-s"),
		DeferredUpdate: deferredUpdate,
	}))
}

type aiResumeSelection struct {
	agent     string
	resumeID  string
	source    string
	updatedAt time.Time
}

// Resume picker row column schema (Phase 0 — row enrichment slice).
//
// Every row lays out the same fixed-width columns so they align in the popup
// regardless of locale or CJK content, with the title as the trailing
// variable-width column. The recency anchor leads (it matches the newest-first
// sort axis), followed by a per-agent colour badge:
//
//	<rel>  [agent]   <branch>            [<cwd>] <turns> <title…>
//	 6      (tight)+pad→8   18                  (14)     5       rest
//
// Column order / cell width (visible cells, fixed columns left-aligned):
//   - relative age: aiResumeRelCellWidth (compact, locale-aware, dim) — leads.
//   - agent badge: tight per-agent-coloured "[name]" padded to
//     aiResumeBadgeCellWidth; padding sits outside the brackets/colour.
//   - branch: aiResumeBranchCellWidth (dim, cut).
//   - extra-meta slot: the depth>0 relative-cwd column (aiResumeCWDCellWidth,
//     dim); empty at depth 0 so the layout collapses to the base view.
//   - turns: aiResumeTurnsCellWidth ("8t"/"31t", dim), blank when unknown.
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
	aiResumeTurnsCellWidth  = 5
	aiResumeTitleMaxCells   = 90
	aiResumeEmptyCell       = "-"
)

func aiResumeSessionRows(sessions []aisessions.SessionMeta, limit int, now time.Time, locale i18n.Locale, baseCWD string, depth int) ([]intpickercompat.Entry, int, int) {
	limit = normalizeResumePickerLimit(limit)
	total := len(sessions)
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	rows := make([]intpickercompat.Entry, 0, len(sessions)+1)
	rows = append(rows, intpickercompat.Entry{
		Label:     "\x1b[32m[+ New Session]\x1b[0m",
		Value:     aiResumeNewValue,
		SearchKey: "new session fresh agent picker",
	})
	for _, session := range sessions {
		rows = append(rows, aiResumeSessionRow(session, now, locale, baseCWD, depth))
	}
	return rows, len(sessions), total
}

func aiResumeSessionRow(session aisessions.SessionMeta, now time.Time, locale i18n.Locale, baseCWD string, depth int) intpickercompat.Entry {
	agent := strings.TrimSpace(session.Agent)
	resumeID := strings.TrimSpace(session.ResumeID)
	branch := strings.TrimSpace(session.Context.Branch)
	if branch == "" {
		branch = aiResumeEmptyCell
	}
	title := cleanAIResumeTitle(session.Title, resumeID)

	// Recency anchor leads (matches the newest-first sort axis), then the
	// per-agent colour badge, branch (+cwd at depth>0), turn count, and the
	// flexible title. Fixed columns pad/truncate to a stable cell width.
	relTime := ansiDim(aiResumeFitCell(aiResumeRelativeAge(now, session.LastModified, locale), aiResumeRelCellWidth))
	badge := aiResumeAgentBadge(agent)
	branchCell := ansiDim(aiResumeFitCell(branch, aiResumeBranchCellWidth))

	// Extra-meta slot (depth>0 cwd column). Empty at depth 0, so the column
	// contributes nothing and the layout collapses to the base view. At depth>0
	// every row carries a fixed-width relative-cwd cell (exact matches render
	// "./") so the title column stays aligned, plus a trailing gap.
	relCWD := aiResumeExtraMetaCell(session, baseCWD, depth)
	extra := ""
	if relCWD != "" {
		extra = ansiDim(aiResumeFitCell(relCWD, aiResumeCWDCellWidth)) + " "
	}
	turnsCell := ansiDim(aiResumeTurnsCell(session.Turns))

	label := fmt.Sprintf("%s %s %s %s%s %s",
		relTime, badge, branchCell, extra, turnsCell, title)

	return intpickercompat.Entry{
		Label:     label,
		Value:     aiResumePickerValue(agent, resumeID),
		SearchKey: strings.TrimSpace(strings.Join([]string{agent, title, resumeID, branch, relCWD}, " ")),
	}
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

// aiResumeTurnsCell renders the user-turn count ("8t", "31t") in a fixed-width
// dim column, or a blank (padded) cell when the count is unknown (zero).
func aiResumeTurnsCell(turns int) string {
	label := ""
	if turns > 0 {
		label = strconv.Itoa(turns) + "t"
	}
	return aiResumeFitCell(label, aiResumeTurnsCellWidth)
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

func (c *aiCommand) runSelectedResumeSession(selection aiResumeSelection, direction string) error {
	mode := normalizeAIMode(selection.agent)
	if mode != aiModeClaude && mode != aiModeCodex && mode != aiModeAntigravity {
		return nil
	}
	if err := c.requireAIAgentEnabled(mode, aiSplitLaunchPicker); err != nil {
		return err
	}
	resumeArgv, err := resumeArgsForAgent(mode, selection.resumeID)
	if err != nil {
		_ = c.displayMessage(fmt.Sprintf("Could not resume %s session: %v; launching new session", mode, err))
		return c.runAgentSplit(mode, direction)
	}
	agentBin := c.findAgentBinary(mode)
	if agentBin == "" {
		message := c.missingAgentRunnerMessage(mode)
		_ = c.displayMessage(message)
		return errors.New(message)
	}
	normalizedResumeID := resumeArgv[len(resumeArgv)-1]
	resumeArgv[0] = agentBin
	return c.runAgentSplitResolvedWithOptions(mode, direction, nil, c.resolveTargetPane(), c.resolveAgentContextDir(mode), aiSplitLaunchOptions{
		execArgv:    resumeArgv,
		pathPrepend: filepath.Dir(agentBin),
		resume: aiPaneResumeMetadata{
			sessionID: normalizedResumeID,
			resumeID:  normalizedResumeID,
			source:    selection.source,
			updatedAt: selection.updatedAt,
		},
	})
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
// instead of the runPickerOptionBackend fallback default. It degrades to the
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
	rows := make([]intpickercompat.Entry, 0, len(enabled)+2)
	for _, provider := range aiprovider.PickerEligible() {
		if aiEnabledAgentsContains(enabled, config.AIAgentProvider(provider.ID)) {
			rows = append(rows, c.agentRow(provider))
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

func (c *aiCommand) agentRow(provider aiprovider.Metadata) intpickercompat.Entry {
	status := "\x1b[33m[MISSING]\x1b[0m"
	if c.agentAvailable(string(provider.ID)) {
		status = "\x1b[32m[READY]\x1b[0m"
	}
	desc := provider.DisplayName + " split"
	return intpickercompat.Entry{
		Label:     fmt.Sprintf("%-8s %s %s", provider.ID, status, desc),
		Value:     string(provider.ID),
		SearchKey: string(provider.ID) + " " + desc,
	}
}

func (c *aiCommand) enabledAIAgents() []config.AIAgentProvider {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
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
	args := []string{"tmux", "popup-toggle"}
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
	args := []string{"tmux", "popup-toggle"}
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

func (c *aiCommand) runAgentSplit(mode, direction string) error {
	return c.runAgentSplitWithExtraArgs(mode, direction, nil)
}

func (c *aiCommand) runDirectAgentSplit(mode, direction string) error {
	return c.runDirectAgentSplitWithExtraArgs(mode, direction, nil)
}

func (c *aiCommand) runDirectAgentSplitWithExtraArgs(mode, direction string, extraArgs []string) error {
	targetPane := c.resolveTargetPane()
	contextDir := c.resolveAgentContextDir(mode)
	return c.runAgentSplitResolved(mode, direction, extraArgs, targetPane, contextDir)
}

func (c *aiCommand) runAgentSplitWithExtraArgs(mode, direction string, extraArgs []string) error {
	return c.runAgentSplitResolved(mode, direction, extraArgs, c.resolveTargetPane(), c.resolveAgentContextDir(mode))
}

type aiSplitLaunchOptions struct {
	execArgv    []string
	pathPrepend string
	resume      aiPaneResumeMetadata
}

type aiPaneResumeMetadata struct {
	sessionID string
	resumeID  string
	source    string
	updatedAt time.Time
}

func (c *aiCommand) runAgentSplitResolved(mode, direction string, extraArgs []string, targetPane, contextDir string) error {
	return c.runAgentSplitResolvedWithOptions(mode, direction, extraArgs, targetPane, contextDir, aiSplitLaunchOptions{})
}

func (c *aiCommand) runAgentSplitResolvedWithOptions(mode, direction string, extraArgs []string, targetPane, contextDir string, options aiSplitLaunchOptions) error {
	execArgv := append([]string(nil), options.execArgv...)
	pathPrepend := options.pathPrepend
	if len(execArgv) == 0 {
		var err error
		execArgv, pathPrepend, err = c.agentExecArgv(mode, extraArgs)
		if err != nil {
			return err
		}
	}
	usePSMux := c.usePSMuxAIBackend()
	title := c.buildAgentTitle(mode, contextDir)
	command, commandArgs, err := c.agentSplitCommand(mode, pathPrepend, contextDir, title, execArgv)
	if err != nil {
		return err
	}
	if targetPane == "" {
		return c.run(command[0], command[1:]...)
	}

	splitDirection := intmux.SplitRight
	if direction == "down" {
		splitDirection = intmux.SplitDown
	}
	paneID, err := c.muxRunner().SplitWindow(context.Background(), intmux.SplitWindowOptions{
		ReturnPaneID: true,
		Direction:    splitDirection,
		Target:       targetPane,
		Cwd:          contextDir,
		Command:      commandArgs,
	})
	if err != nil {
		return err
	}
	if usePSMux {
		return nil
	}
	c.configureAIPane(paneID, mode, contextDir, title, options.resume)
	c.applySplitLayout(targetPane, direction)
	c.startAIWatchTitle(paneID)
	return nil
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

func (c *aiCommand) runShellSplit(direction string) error {
	usePSMux := c.usePSMuxAIBackend()
	targetPane := c.resolveTargetPane()
	contextDir := c.resolveContextDir()
	splitDirection := intmux.SplitRight
	if direction == "down" {
		splitDirection = intmux.SplitDown
	}
	command := loginShellCommand(defaultInteractiveShell(c.lookupEnv))
	returnPaneID := false
	if usePSMux {
		rendered, err := psmuxSplitCommandTail(psmuxInteractiveShellCommand(c.lookupEnv))
		if err != nil {
			return err
		}
		command = []string{rendered}
		returnPaneID = true
	}

	paneID, err := c.muxRunner().SplitWindow(context.Background(), intmux.SplitWindowOptions{
		ReturnPaneID: returnPaneID,
		Direction:    splitDirection,
		Target:       targetPane,
		Cwd:          contextDir,
		Command:      command,
	})
	if err != nil {
		return err
	}
	if usePSMux {
		_ = strings.TrimSpace(paneID)
		return nil
	}
	c.applySplitLayout(targetPane, direction)
	return nil
}

type aiPaneGeometry struct {
	id     string
	left   int
	top    int
	width  int
	height int
}

func (c *aiCommand) applySplitLayout(targetPane, direction string) {
	targetPane = strings.TrimSpace(targetPane)
	if targetPane == "" {
		return
	}
	panes, target, ok := c.readSplitPaneGeometry(targetPane)
	if !ok {
		return
	}
	peers := splitLayoutPeers(panes, target, direction)
	if len(peers) < 2 {
		return
	}
	if direction == "down" {
		resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
			_ = c.run("tmux", "resize-pane", "-t", p.id, "-y", fmt.Sprintf("%d", size))
		}, func(p aiPaneGeometry) int { return p.height })
		return
	}
	resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
		_ = c.run("tmux", "resize-pane", "-t", p.id, "-x", fmt.Sprintf("%d", size))
	}, func(p aiPaneGeometry) int { return p.width })
}

func (c *aiCommand) readSplitPaneGeometry(targetPane string) ([]aiPaneGeometry, aiPaneGeometry, bool) {
	out, err := c.read("tmux", "list-panes", "-t", targetPane, "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}")
	if err != nil {
		return nil, aiPaneGeometry{}, false
	}
	panes := parseSplitPaneGeometry(string(out))
	for _, pane := range panes {
		if pane.id == targetPane {
			return panes, pane, true
		}
	}
	return panes, aiPaneGeometry{}, false
}

func parseSplitPaneGeometry(value string) []aiPaneGeometry {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	panes := make([]aiPaneGeometry, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		pane := aiPaneGeometry{
			id:     strings.TrimSpace(fields[0]),
			left:   parsePositiveInt(fields[1]),
			top:    parsePositiveInt(fields[2]),
			width:  parsePositiveInt(fields[3]),
			height: parsePositiveInt(fields[4]),
		}
		if pane.width <= 0 || pane.height <= 0 {
			continue
		}
		panes = append(panes, pane)
	}
	return panes
}

func splitLayoutPeers(panes []aiPaneGeometry, target aiPaneGeometry, direction string) []aiPaneGeometry {
	peers := make([]aiPaneGeometry, 0, len(panes))
	for _, pane := range panes {
		if direction == "down" {
			if pane.left == target.left && pane.width == target.width {
				peers = append(peers, pane)
			}
			continue
		}
		if pane.top == target.top && pane.height == target.height {
			peers = append(peers, pane)
		}
	}
	if direction == "down" {
		sort.Slice(peers, func(i, j int) bool { return peers[i].top < peers[j].top })
		return peers
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].left < peers[j].left })
	return peers
}

func resizePanesEvenly(peers []aiPaneGeometry, resize func(aiPaneGeometry, int), currentSize func(aiPaneGeometry) int) {
	total := 0
	for _, pane := range peers {
		total += currentSize(pane)
	}
	if total <= 0 {
		return
	}
	base := total / len(peers)
	remainder := total % len(peers)
	for index, pane := range peers {
		size := base
		if index < remainder {
			size++
		}
		resize(pane, size)
	}
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

func (c *aiCommand) resolveAgentContextDir(mode string) string {
	switch mode {
	case aiModeClaude:
		if dir := c.env("CLAUDE_CONTEXT_DIR"); isDir(dir) {
			return dir
		}
	case aiModeCodex:
		if dir := c.env("CODEX_CONTEXT_DIR"); isDir(dir) {
			return dir
		}
	}
	return c.resolveContextDir()
}

func (c *aiCommand) resolveTargetPane() string {
	if pane := strings.TrimSpace(c.env("TMUX_SPLIT_TARGET_PANE")); pane != "" {
		if resolved := c.readMuxTrimmed("display-message", "-p", "-t", pane, "-F", "#{pane_id}"); resolved != "" {
			return resolved
		}
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

func (c *aiCommand) agentLaunchCommand(mode, agentBin, contextDir, title string) string {
	return c.agentLaunchCommandForArgv(mode, filepath.Dir(agentBin), contextDir, title, []string{agentBin})
}

func (c *aiCommand) agentSplitCommand(mode, pathPrepend, contextDir, title string, execArgv []string) ([]string, []string, error) {
	if c.usePSMuxAIBackend() {
		rendered, err := psmuxSplitCommandTail(execArgv)
		if err != nil {
			return nil, nil, err
		}
		return execArgv, []string{rendered}, nil
	}
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
		`if [[ -n "${TMUX:-}" ]]; then tmux select-pane -T "$`+titleVar+`" >/dev/null 2>&1 || true; fi`,
		strings.Join(execParts, " "),
	)
	return strings.Join(parts, " && ")
}

func (c *aiCommand) usePSMuxAIBackend() bool {
	return usePSMuxBackend(c.lookupEnv, nil)
}

func psmuxSplitCommandTail(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("psmux split command requires argv")
	}
	return intpsmux.RenderPowerShellCommand(argv[0], argv[1:]...)
}

func psmuxInteractiveShellCommand(lookupEnv func(string) string) []string {
	if lookupEnv != nil {
		if shell := strings.TrimSpace(lookupEnv("SHELL")); shell != "" && !strings.ContainsAny(shell, "\x00\r\n") {
			return []string{shell}
		}
	}
	return []string{"powershell", "-NoLogo"}
}

func (c *aiCommand) configureAIPane(paneID, mode, contextDir, title string, resume aiPaneResumeMetadata) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return
	}
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneManagedOption, "1")
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneAgentOption, normalizeAIMode(mode))
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneContextOption, strings.TrimSpace(contextDir))
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, displayAITopic(title))
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneStateOption, "idle")
	c.configureAIPaneResumeMetadata(paneID, resume)
}

func (c *aiCommand) configureAIPaneResumeMetadata(paneID string, resume aiPaneResumeMetadata) {
	sessionID := strings.TrimSpace(resume.sessionID)
	resumeID := strings.TrimSpace(resume.resumeID)
	source := strings.TrimSpace(resume.source)
	if sessionID != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneSessionIDOption, sessionID)
	}
	if resumeID != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneResumeIDOption, resumeID)
	}
	if source != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneResumeSourceOption, source)
	}
	if !resume.updatedAt.IsZero() {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneResumeUpdatedAtOption, resume.updatedAt.UTC().Format(time.RFC3339))
	}
}

func (c *aiCommand) startAIWatchTitle(paneID string) {
	if strings.TrimSpace(paneID) == "" {
		return
	}
	binaryPath, err := c.binaryPath()
	if err != nil || strings.TrimSpace(binaryPath) == "" {
		return
	}
	_ = c.run("tmux", "run-shell", "-b", shellQuote(binaryPath)+" ai watch-title "+shellQuote(paneID))
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

	if c.usePSMuxAIBackend() {
		return c.findPSMuxAgentBinary(string(provider.ID), binName)
	}

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

func (c *aiCommand) findPSMuxAgentBinary(mode, binName string) string {
	if path := c.findPSMuxPowerShellCommand(binName); path != "" {
		return path
	}
	if path := firstExistingWindowsCommandCandidate(psmuxWindowsPathCandidates(c.env("PATH"), binName)); path != "" {
		return path
	}
	if path := firstExistingWindowsCommandCandidate(psmuxWhereCandidates(c.readTrimmed("where.exe", binName), binName)); path != "" {
		return path
	}

	home := c.homeOrEmpty()
	if mode == aiModeCodex {
		matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", "codex"))
		if path := newestExistingFile(matches); path != "" {
			return path
		}
	}
	return ""
}

func (c *aiCommand) findPSMuxPowerShellCommand(binName string) string {
	script := "$cmd = Get-Command -Name " + powerShellSingleQuote(binName) + " -CommandType Application,ExternalScript -ErrorAction SilentlyContinue | Select-Object -First 1; if ($cmd) { if ($cmd.Source) { $cmd.Source } elseif ($cmd.Path) { $cmd.Path } }"
	return firstExistingWindowsCommandCandidate([]string{
		c.readTrimmed("powershell", "-NoProfile", "-Command", script),
		c.readTrimmed("pwsh", "-NoProfile", "-Command", script),
	})
}

func (c *aiCommand) missingAgentRunnerMessage(mode string) string {
	if c.usePSMuxAIBackend() {
		if mode == aiModeClaude {
			nativePath := filepath.Join(c.homeOrEmpty(), ".local", "bin", "claude.exe")
			if existingFile(nativePath) {
				return fmt.Sprintf("selected runner is installed at %s but is not on PATH; add %s to PATH and restart psmux", nativePath, filepath.Dir(nativePath))
			}
		}
		return fmt.Sprintf("selected runner is not installed or unsupported on this mux backend: %s", mode)
	}
	return fmt.Sprintf("selected runner is not installed: %s", mode)
}

func psmuxWhereCandidates(output, binName string) []string {
	var candidates []string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		candidates = append(candidates, line)
	}
	return sortWindowsCommandCandidates(candidates, binName)
}

func psmuxWindowsPathCandidates(pathList, binName string) []string {
	var candidates []string
	for _, dir := range splitPSMuxPathList(pathList) {
		for _, name := range windowsCommandCandidateNames(binName) {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	return candidates
}

func splitPSMuxPathList(pathList string) []string {
	if strings.TrimSpace(pathList) == "" {
		return nil
	}
	var parts []string
	if strings.Contains(pathList, ";") {
		parts = strings.Split(pathList, ";")
	} else {
		parts = filepath.SplitList(pathList)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func windowsCommandCandidateNames(binName string) []string {
	return []string{binName + ".cmd", binName + ".ps1", binName + ".exe", binName}
}

func sortWindowsCommandCandidates(candidates []string, binName string) []string {
	priority := map[string]int{}
	for i, name := range windowsCommandCandidateNames(binName) {
		priority[strings.ToLower(name)] = i
	}
	out := append([]string(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		left, ok := priority[strings.ToLower(windowsPathBase(out[i]))]
		if !ok {
			left = len(priority)
		}
		right, ok := priority[strings.ToLower(windowsPathBase(out[j]))]
		if !ok {
			right = len(priority)
		}
		return left < right
	})
	return out
}

func windowsPathBase(path string) string {
	idx := strings.LastIndexAny(path, `/\`)
	if idx < 0 {
		return path
	}
	return path[idx+1:]
}

func firstExistingWindowsCommandCandidate(paths []string) string {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if existingFile(path) {
			return path
		}
	}
	return ""
}

func existingFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func newestExistingFile(paths []string) string {
	var newestPath string
	var newestMod time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if newestPath == "" || info.ModTime().After(newestMod) {
			newestPath = path
			newestMod = info.ModTime()
		}
	}
	return newestPath
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
	if c.usePSMuxAIBackend() {
		return c.muxRunner().Read(context.Background(), args...)
	}
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
	backend := aiCommandMuxBackend{
		runCommand:  c.runCommand,
		readCommand: c.readCommand,
	}
	if c.usePSMuxAIBackend() {
		backend.commandName = "psmux"
		backend.prefix = []string{"-L", defaultAppSocket}
	}
	return intmux.NewRunner(backend)
}

type aiCommandMuxBackend struct {
	runCommand  func(ctx context.Context, name string, args ...string) error
	readCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	commandName string
	prefix      []string
}

func (b aiCommandMuxBackend) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	actualName := name
	actualArgs := args
	if name == "tmux" && strings.TrimSpace(b.commandName) != "" {
		actualName = b.commandName
		actualArgs = append(append([]string(nil), b.prefix...), psmuxMuxArgs(args)...)
	}
	if name == "tmux" && !aiMuxCommandNeedsOutput(args) {
		if b.runCommand != nil {
			return nil, b.runCommand(ctx, actualName, actualArgs...)
		}
		return nil, errors.New("ai command runner is not configured")
	}
	if b.readCommand == nil {
		return nil, errors.New("ai command reader is not configured")
	}
	return b.readCommand(ctx, actualName, actualArgs...)
}

func psmuxMuxArgs(args []string) []string {
	if len(args) == 0 || args[0] != "split-window" {
		return args
	}
	detached := false
	direction := ""
	returnPaneID := false
	format := ""
	target := ""
	cwd := ""
	commandStart := len(args)
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-d":
			detached = true
		case "-h", "-v":
			direction = args[i]
		case "-P":
			returnPaneID = true
		case "-F":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "-t":
			if i+1 < len(args) {
				target = args[i+1]
				i++
			}
		case "-c":
			if i+1 < len(args) {
				cwd = args[i+1]
				i++
			}
		default:
			commandStart = i
			i = len(args)
		}
	}
	out := []string{"split-window"}
	if detached {
		out = append(out, "-d")
	}
	if direction != "" {
		out = append(out, direction)
	}
	if returnPaneID {
		out = append(out, "-P")
		if format != "" {
			out = append(out, "-F", format)
		}
	}
	if target != "" {
		out = append(out, "-t", target)
	}
	if cwd != "" {
		out = append(out, "-c", cwd)
	}
	if commandStart < len(args) {
		out = append(out, args[commandStart:]...)
	}
	return out
}

func aiMuxCommandNeedsOutput(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "display-message", "list-panes", "list-windows", "capture-pane", "show-option", "show-options", "show-hooks":
		return true
	case "split-window", "new-session":
		if slices.Contains(args[1:], "-P") {
			return true
		}
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

func parseAISplitInvocation(args []string, stderr io.Writer) (aiSplitInvocation, error) {
	invocation := aiSplitInvocation{direction: "right"}
	positionals := make([]string, 0, 1)
	extraArgsSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			extraArgsSeen = true
			invocation.extraArgs = append([]string(nil), args[i+1:]...)
			i = len(args)
		case arg == "--agent":
			if i+1 >= len(args) {
				printAIUsage(stderr)
				return aiSplitInvocation{}, errors.New("ai split --agent requires a value")
			}
			invocation.agent = strings.TrimSpace(args[i+1])
			invocation.agentSet = true
			i++
		case strings.HasPrefix(arg, "--agent="):
			invocation.agent = strings.TrimSpace(strings.TrimPrefix(arg, "--agent="))
			invocation.agentSet = true
		case arg == "--force-agent":
			invocation.forceAgent = true
		case strings.HasPrefix(arg, "-"):
			printAIUsage(stderr)
			return aiSplitInvocation{}, fmt.Errorf("unknown ai split flag: %s", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if extraArgsSeen && len(invocation.extraArgs) == 0 {
		printAIUsage(stderr)
		return aiSplitInvocation{}, errors.New("ai split -- requires extra args")
	}
	if extraArgsSeen && strings.TrimSpace(invocation.extraArgs[0]) == "" {
		printAIUsage(stderr)
		return aiSplitInvocation{}, errors.New("ai split extra args require a non-empty first argument")
	}
	if extraArgsSeen && !invocation.agentSet {
		printAIUsage(stderr)
		return aiSplitInvocation{}, errors.New("ai split extra args require --agent")
	}
	if invocation.forceAgent && !invocation.agentSet {
		printAIUsage(stderr)
		return aiSplitInvocation{}, errors.New("ai split --force-agent requires --agent claude, --agent codex, or --agent antigravity")
	}
	direction, err := parseAISplitDirection(positionals, "ai split", stderr)
	if err != nil {
		return aiSplitInvocation{}, err
	}
	invocation.direction = direction
	if invocation.agentSet {
		switch invocation.agent {
		case aiModeClaude, aiModeCodex, aiModeAntigravity, aiModeShell, aiModeSelective, aiModeResume:
		default:
			printAIUsage(stderr)
			return aiSplitInvocation{}, fmt.Errorf("unknown ai split agent: %s", invocation.agent)
		}
		if invocation.agent == aiModeSelective && len(invocation.extraArgs) > 0 {
			printAIUsage(stderr)
			return aiSplitInvocation{}, errors.New("ai split --agent selective cannot use extra args")
		}
		if invocation.agent == aiModeResume && len(invocation.extraArgs) > 0 {
			printAIUsage(stderr)
			return aiSplitInvocation{}, errors.New("ai split --agent resume cannot use extra args")
		}
		if invocation.agent == aiModeShell && len(invocation.extraArgs) > 0 {
			printAIUsage(stderr)
			return aiSplitInvocation{}, errors.New("ai split --agent shell cannot use extra args")
		}
		if invocation.forceAgent && invocation.agent != aiModeClaude && invocation.agent != aiModeCodex && invocation.agent != aiModeAntigravity {
			printAIUsage(stderr)
			return aiSplitInvocation{}, errors.New("ai split --force-agent only applies to --agent claude, --agent codex, or --agent antigravity")
		}
	}
	return invocation, nil
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
// When launchURI is non-empty the root <toast> element gains a
// `launch="<uri>" activationType="protocol"` pair. Windows then hands the
// URI to the registered scheme handler on click — for the WSL scope shipped
// today that is a hidden PowerShell wrapper around
// `wsl.exe -d <distro> --exec <abs-binary-path> focus --uri <uri>`, wired from
// buildRegisterURIProtocolPowerShell. The URI itself is produced by
// buildFocusURI (already URL-encoded once); we xml-escape it for the attribute
// so the two layers compose without double-decoding.
//
// When launchURI is empty the launch attribute is omitted entirely and the
// toast behaves as a passive notification — the existing pre-protocol path.
// This lets the WSL hook short-circuit cleanly when the inbound
// notification has no pane id (`aiNotification.Tag` empty) without breaking
// non-WSL notify paths that never compute a URI.
func buildToastPowerShell(summary, body, appName, tag, group, iconPath, launchURI string, expireMS int) string {
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
	toastAttrs := ""
	if launchURI != "" {
		toastAttrs = ` launch="` + xmlEscape(launchURI) + `" activationType="protocol"`
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
<toast` + toastDuration + toastAttrs + `>
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

// buildRegisterURIProtocolPowerShell emits an idempotent PowerShell script
// that registers the `projmux://` URI scheme in the user's HKCU registry so
// a Toast click can hand control back to projmux running inside WSL.
//
// Registry layout (HKCU\SOFTWARE\Classes\<scheme>):
//
//	(Default)                          = "URL:projmux"
//	URL Protocol                       = ""
//	shell\open\command\(Default)       = wscript.exe //B //Nologo <launcher.vbs> "%1"
//
// The GUI-subsystem WScript launcher avoids the visible console flash caused
// by Windows ShellExecute launching console-subsystem `wsl.exe` or
// `powershell.exe` directly from the protocol handler. WScript.Shell.Run does
// not pass quoted fixed arguments to wsl.exe the same way PowerShell does, so
// the launcher uses hidden `%ComSpec% /d /s /c` as the command-line parser and
// caret-escapes URI query separators before invoking wsl.exe. Inside that
// command, `--exec` instead of `--` remains load-bearing: `wsl.exe -- <cmd>
// <args>` routes `<cmd> <args>` through the user's default login shell
// (zsh/bash). The `projmux://` URI carries `&` characters as query-parameter
// separators, and zsh parses `&` as a background-job operator before projmux
// ever runs, emitting `zsh:1: parse error near '&'`. `--exec` skips the shell
// and invokes the binary directly with the args verbatim. Because `--exec`
// doesn't load shell init files, PATH may be empty, so we register the
// absolute WSL filesystem path to the projmux binary captured at registration
// time (whichever binary actually wrote the registry key).
//
// The handler captures the user's *current* WSL_DISTRO_NAME because the
// click is received on the Windows side with no knowledge of which distro
// produced the notification. This is the documented limitation: users with
// multiple WSL distros only get the handler bound to whichever distro fired
// the first toast on this tmux server. The follow-up roadmap entry covers
// multi-distro dispatch.
//
// The script is safe to re-run — every Set-ItemProperty is overwriting
// idempotently, and the New-Item is gated on Test-Path. Caller gates the
// invocation behind a tmux user-option marker so this runs at most once per
// server boot anyway (see ensureWSLURIProtocol).
func buildRegisterURIProtocolPowerShell(scheme, distro, binaryPath string) string {
	return `$regPath = "HKCU:\SOFTWARE\Classes\` + psEscape(scheme) + `"
$cmdPath = "$regPath\shell\open\command"
$launcherRoot = $env:LOCALAPPDATA
if ([string]::IsNullOrWhiteSpace($launcherRoot)) {
  $launcherRoot = $env:TEMP
}
$launcherDir = Join-Path $launcherRoot 'projmux'
$launcherPath = Join-Path $launcherDir '` + psEscape(scheme) + `-uri-handler.vbs'
$launcherScript = @'
` + buildWSLURIProtocolLauncherVBScript(distro, binaryPath) + `
'@
try {
  if (-not (Test-Path $regPath)) {
    New-Item -Path $regPath -Force | Out-Null
  }
  if (-not (Test-Path $cmdPath)) {
    New-Item -Path $cmdPath -Force | Out-Null
  }
  if (-not (Test-Path $launcherDir)) {
    New-Item -Path $launcherDir -ItemType Directory -Force | Out-Null
  }
  Set-Content -Path $launcherPath -Value $launcherScript -Encoding ASCII
  Set-ItemProperty -Path $regPath -Name '(Default)' -Value 'URL:` + psEscape(scheme) + `' -Type String
  Set-ItemProperty -Path $regPath -Name 'URL Protocol' -Value '' -Type String
  $launchCmd = 'wscript.exe //B //Nologo "' + $launcherPath + '" "%1"'
  Set-ItemProperty -Path $cmdPath -Name '(Default)' -Value $launchCmd -Type String
} catch { }
`
}

func buildWSLURIProtocolLauncherVBScript(distro, binaryPath string) string {
	return `Option Explicit

Dim uri
If WScript.Arguments.Count < 1 Then
  WScript.Quit 0
End If
uri = WScript.Arguments.Item(0)

Dim shell, inner, command
inner = "wsl.exe -d " & CmdEscape(` + vbsDoubleQuoted(distro) + `) & " --exec " & CmdEscape(` + vbsDoubleQuoted(binaryPath) + `) & " focus --uri " & CmdEscape(uri)
command = "%ComSpec% /d /s /c " & Chr(34) & inner & Chr(34)
Set shell = CreateObject("WScript.Shell")
shell.Run command, 0, False

Function CmdEscape(value)
  Dim s
  s = value
  s = Replace(s, "^", "^^")
  s = Replace(s, "&", "^&")
  s = Replace(s, "|", "^|")
  s = Replace(s, "<", "^<")
  s = Replace(s, ">", "^>")
  CmdEscape = s
End Function`
}

func vbsDoubleQuoted(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
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
	fmt.Fprintln(w, "  projmux ai split [--agent <claude|codex|antigravity|shell|selective|resume>] [--force-agent] [right|down] [-- <extra-arg>...]")
	fmt.Fprintln(w, "  projmux ai picker [--inside] [--shell] [--resume] [right|down]")
	fmt.Fprintln(w, "  projmux ai settings [--get|--set <mode>]")
	fmt.Fprintln(w, "  projmux ai status set <thinking|waiting|idle> [pane]")
	fmt.Fprintln(w, "  projmux ai notify [notify|reset] [pane]")
	fmt.Fprintln(w, "  projmux ai watch-title [pane]")
	fmt.Fprintln(w, "  projmux ai ingest codex-hook < payload.json")
	fmt.Fprintln(w, "  projmux ai ingest claude-hook < payload.json")
	fmt.Fprintln(w, "  projmux ai ingest antigravity-hook < payload.json")
	fmt.Fprintln(w, "  projmux ai ingest bell --pane <pane_id>")
	fmt.Fprintln(w, "  projmux ai ingest log [--tail N] [--json] [--path]")
	fmt.Fprintln(w, "  projmux ai integrate codex [--dry-run] [--remove]")
	fmt.Fprintln(w, "  projmux ai integrate claude [--dry-run] [--remove]")
	fmt.Fprintln(w, "  projmux ai integrate tmux-bell [--dry-run] [--remove]")
	fmt.Fprintln(w, "  projmux ai topic set <text> [--pane <id>]")
	fmt.Fprintln(w, "  projmux ai topic clear [--pane <id>]")
	fmt.Fprintln(w, "  projmux ai topic get [--pane <id>]")
}
