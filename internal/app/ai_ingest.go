package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	aiBellDedupeOption = "@projmux_ai_bell_notified_at"
	aiBellDedupeWindow = 5 * time.Second
	aiIngestLogName    = "ai-ingest.log"
	aiIngestLogMaxSize = 1024 * 1024
	aiIngestLogRetain  = 512 * 1024
)

var aiIngestListPanesFormats = []string{
	intmux.TmuxFormat("pane_id"),
	intmux.TmuxFormat("pane_current_path"),
	intmux.PaneOptionFormat(aiPaneThreadIDOption),
	intmux.PaneOptionFormat(aiPaneSessionIDOption),
}

var aiIngestListPanesFormat = intmux.JoinFormats(intmux.FieldDelimiter, aiIngestListPanesFormats...)

var aiBellPaneFormats = []string{
	intmux.TmuxFormat("session_name"),
	intmux.TmuxFormat("window_id"),
	intmux.TmuxFormat("window_name"),
	intmux.TmuxFormat("pane_id"),
	intmux.TmuxFormat("pane_title"),
	intmux.TmuxFormat("pane_current_command"),
	intmux.TmuxFormat("socket_path"),
}

var aiBellPaneFormat = intmux.JoinFormats(intmux.FieldDelimiter, aiBellPaneFormats...)

// aiPaneMatchInput carries everything a provider hook knows about the Pane it
// belongs to. ExplicitPane is the identity the hook command was handed; the
// remaining fields are the inherited-environment evidence the matcher has always
// used and still uses whenever no explicit identity arrived.
//
// Provider is the hook's own provider, which every route already knows about
// itself. It exists so the matcher can tell an explicit identity that is the
// hook's own from one it merely inherited: a provider host launched from
// somebody else's Pane carries that Pane's activation envelope verbatim, and
// having a value and owning it are different things.
type aiPaneMatchInput struct {
	ExplicitPane string
	Provider     string
	CWD          string
	ThreadID     string
	SessionID    string
}

// The closed vocabulary a failed attribution reports. Every token names which
// step could not answer, so a hook that never had an identity is distinguishable
// from one whose identity went stale and from one whose Pane inventory was
// simply unreachable. Tokens are provider-neutral and carry no path, payload, or
// provider text.
const (
	aiPaneMatchReasonNoMatch             = "no matching pane"
	aiPaneMatchReasonNoInventory         = "pane inventory unavailable"
	aiPaneMatchReasonRegistryUnavailable = "pane registry unavailable"
	aiPaneMatchReasonExplicitUnknown     = "explicit pane is not registered"
	aiPaneMatchReasonExplicitNoRuntime   = "explicit pane has no live runtime"
	aiPaneMatchReasonExplicitStale       = "explicit pane binding is stale"
	aiPaneMatchReasonConversationUnknown = "conversation is not registered to a pane"
	aiPaneMatchReasonConversationShared  = "conversation is registered to several panes"
	aiPaneMatchReasonExplicitForeign     = "explicit pane belongs to another provider"
	aiPaneMatchReasonExplicitForeignOnly = "explicit pane belongs to another provider; no other match"
)

type aiPaneMatchRow struct {
	PaneID    string
	CWD       string
	ThreadID  string
	SessionID string
}

type aiIngestLogEntry struct {
	At        string         `json:"at"`
	Source    string         `json:"source"`
	Event     string         `json:"event,omitempty"`
	Result    string         `json:"result"`
	Reason    aiIngestReason `json:"reason,omitempty"`
	Pane      string         `json:"pane,omitempty"`
	CWD       string         `json:"cwd,omitempty"`
	ThreadID  string         `json:"thread_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	TurnID    string         `json:"turn_id,omitempty"`
	// Epoch is the observer epoch label a lifecycle transition belongs to. It
	// is what makes two adjacent records comparable: the same label twice is
	// one epoch reporting twice, a new label is a new connection.
	Epoch string `json:"epoch,omitempty"`
	// Repeat counts identical transitions coalesced into this record by the
	// observer journal's rate window. Zero, and therefore omitted, means this
	// record stands for exactly one transition.
	Repeat int `json:"repeat,omitempty"`
}

func (c *aiCommand) runIngest(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		printAIUsage(stderr)
		return errors.New("internal agent-hook ingest requires <agent-kind>")
	}
	switch args[0] {
	case codexNativeLifecycleIngestRoute:
		target, err := parseCodexNativeLifecycleTarget(args[1:])
		if err != nil {
			return err
		}
		return c.runCodexNativeLifecycleObserver(target)
	case "codex-hook":
		explicitPane, err := parseAIHookPaneArgument("codex-hook", args[1:], stderr)
		if err != nil {
			return err
		}
		reader := c.stdin
		if reader == nil {
			reader = os.Stdin
		}
		data, err := io.ReadAll(io.LimitReader(reader, 1024*1024+1))
		if err != nil {
			c.recordAIIngestFailure(diagnostics.ProviderCodex, diagnostics.AIKindPayload, diagnostics.AIFailurePayloadRead)
			return fmt.Errorf("read codex hook payload: %w", err)
		}
		if len(data) > 1024*1024 {
			c.recordAIIngestFailure(diagnostics.ProviderCodex, diagnostics.AIKindPayload, diagnostics.AIFailurePayloadOversized)
			return errors.New("codex hook payload exceeds 1 MiB")
		}
		return c.ingestCodexHook(data, explicitPane)
	case "claude-hook":
		explicitPane, err := parseAIHookPaneArgument("claude-hook", args[1:], stderr)
		if err != nil {
			return err
		}
		reader := c.stdin
		if reader == nil {
			reader = os.Stdin
		}
		data, err := io.ReadAll(io.LimitReader(reader, 1024*1024+1))
		if err != nil {
			c.recordAIIngestFailure(diagnostics.ProviderClaude, diagnostics.AIKindPayload, diagnostics.AIFailurePayloadRead)
			return fmt.Errorf("read claude hook payload: %w", err)
		}
		if len(data) > 1024*1024 {
			c.recordAIIngestFailure(diagnostics.ProviderClaude, diagnostics.AIKindPayload, diagnostics.AIFailurePayloadOversized)
			return errors.New("claude hook payload exceeds 1 MiB")
		}
		return c.ingestClaudeHook(data, explicitPane)
	case "antigravity-hook":
		fs := flag.NewFlagSet("internal agent-hook ingest antigravity-hook", flag.ContinueOnError)
		fs.SetOutput(stderr)
		eventName := fs.String("event", "", "authoritative Antigravity hook event name")
		explicitPane := fs.String("pane", "", aiHookPaneArgumentUsage)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			printAIUsage(stderr)
			return errors.New("internal agent-hook ingest antigravity-hook does not accept positional payload arguments")
		}
		reader := c.stdin
		if reader == nil {
			reader = os.Stdin
		}
		data, err := io.ReadAll(io.LimitReader(reader, 1024*1024+1))
		if err != nil {
			c.recordAIIngestFailure(diagnostics.ProviderAntigravity, diagnostics.AIKindPayload, diagnostics.AIFailurePayloadRead)
			return fmt.Errorf("read antigravity hook payload: %w", err)
		}
		if len(data) > 1024*1024 {
			c.recordAIIngestFailure(diagnostics.ProviderAntigravity, diagnostics.AIKindPayload, diagnostics.AIFailurePayloadOversized)
			return errors.New("antigravity hook payload exceeds 1 MiB")
		}
		if err := c.ingestAntigravityHook(data, *eventName, strings.TrimSpace(*explicitPane)); err != nil {
			return err
		}
		if strings.TrimSpace(*eventName) == "" {
			return nil
		}
		// Antigravity statusline commands render their stdout. The managed
		// bridge is ingest-only and must stay empty so stack_with_default=true
		// leaves the built-in line visible. Hook events retain their response
		// JSON contracts below.
		if normalizeAntigravityEventName(*eventName) == "Statusline" {
			return nil
		}
		response, err := antigravityHookResponse(*eventName)
		if err != nil {
			c.recordAIIngestFailure(diagnostics.ProviderAntigravity, classifyAIHookKind(diagnostics.ProviderAntigravity, *eventName), diagnostics.AIFailureRoute)
			return err
		}
		_, err = fmt.Fprintln(stdout, string(response))
		return err
	case "bell":
		return c.runIngestBell(args[1:], stderr)
	case "log":
		return c.runIngestLog(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printAIUsage(stderr)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown internal agent-hook ingest source: %s", args[0])
	}
}

// aiHookPaneArgumentUsage documents the one flag every provider hook route
// accepts. It is optional on purpose: a provider process shared by several Panes
// carries no identity to hand over, and an absent value selects the established
// matcher rather than an error.
const aiHookPaneArgumentUsage = "exact Pane this hook belongs to, as a Pane uid or a %N runtime id"

// parseAIHookPaneArgument reads the explicit Pane identity a hook command was
// handed. The route stays payload-on-stdin only; --pane is the sole argument and
// an empty value is a valid answer meaning "nothing was handed over".
func parseAIHookPaneArgument(route string, args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet("internal agent-hook ingest "+route, flag.ContinueOnError)
	fs.SetOutput(stderr)
	explicitPane := fs.String("pane", "", aiHookPaneArgumentUsage)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return "", errors.New("internal agent-hook ingest " + route + " reads JSON from stdin and accepts no payload arguments")
	}
	return strings.TrimSpace(*explicitPane), nil
}

func (c *aiCommand) runIngestBell(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("internal agent-hook ingest bell", flag.ContinueOnError)
	fs.SetOutput(stderr)
	paneID := fs.String("pane", "", "target tmux pane id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("internal agent-hook ingest bell does not accept positional arguments")
	}
	if strings.TrimSpace(*paneID) == "" {
		c.recordAIIngestIgnored(diagnostics.ProviderTmuxBell, diagnostics.AIKindBell, diagnostics.AIFailureTargetInvalid, true)
		printAIUsage(stderr)
		return errors.New("internal agent-hook ingest bell requires --pane <pane_id>")
	}
	return c.ingestBell(*paneID)
}

func (c *aiCommand) runIngestLog(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("diagnostics agent-hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tail := fs.Int("tail", 50, "number of recent log entries to print")
	jsonOut := fs.Bool("json", false, "print raw JSONL entries")
	pathOnly := fs.Bool("path", false, "print the ingest log path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("diagnostics agent-hook does not accept positional arguments")
	}

	path, err := c.aiIngestLogPath()
	if err != nil {
		return err
	}
	if *pathOnly {
		fmt.Fprintln(stdout, path)
		return nil
	}

	localstate.RepairPrivateFile(path)
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read agent-hook diagnostics log: %w", err)
	}
	lines := nonEmptyLines(string(data))
	if *tail >= 0 && len(lines) > *tail {
		lines = lines[len(lines)-*tail:]
	}
	for _, line := range lines {
		if *jsonOut {
			fmt.Fprintln(stdout, line)
			continue
		}
		var entry aiIngestLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			fmt.Fprintln(stdout, line)
			continue
		}
		fmt.Fprintln(stdout, formatAIIngestLogEntry(entry))
	}
	return nil
}

type bellPaneInfo struct {
	Session string
	Window  string
	WinName string
	Pane    string
	Title   string
	Command string
	Socket  string
}

func (c *aiCommand) ingestBell(paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "ignored", Reason: aiIngestReasonBlankPane})
		return nil
	}
	info, ok := c.readBellPaneInfo(paneID)
	if !ok {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "ignored", Reason: aiIngestReasonPaneNotFound, Pane: paneID})
		return nil
	}
	if c.duplicateBellRecent(info.Pane) {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "deduped", Pane: info.Pane})
		return nil
	}
	store, err := c.aiNotifyStore()
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "error", Reason: aiIngestFailureReason(aiIngestReasonNotifyStoreFailed, err), Pane: info.Pane})
		return err
	}
	text := composeBellNotifyText(info)
	metadata := map[string]string{
		notify.MetaAgent: "bell",
		notify.MetaEvent: "bell",
		"pane":           info.Pane,
	}
	if info.Session != "" {
		metadata["session"] = info.Session
	}
	if info.Window != "" {
		metadata["window"] = info.Window
	}
	if info.WinName != "" {
		metadata["window_name"] = info.WinName
	}
	if info.Title != "" {
		metadata["pane_title"] = info.Title
	}
	if info.Command != "" {
		metadata["pane_command"] = info.Command
	}
	if info.Socket != "" {
		metadata["socket"] = info.Socket
	}

	if _, _, err := store.Push(notify.PushInput{
		ID:       "ai:bell:" + info.Session + ":" + info.Pane,
		Text:     text,
		Severity: notify.SeverityInfo,
		Source:   notify.SourceAI,
		Metadata: metadata,
		TTL:      attentionNotifyTTL,
		Target: notify.Target{
			Socket:  info.Socket,
			Session: info.Session,
			Window:  info.Window,
			Pane:    info.Pane,
		},
	}); err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "error", Reason: aiIngestFailureReason(aiIngestReasonNotifyPushFailed, err), Pane: info.Pane})
		return err
	}
	c.publishNotifyQueueRefreshBestEffort()
	c.recordBellNotification(info.Pane)
	c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "notify", Pane: info.Pane})
	return nil
}

func (c *aiCommand) aiNotifyStore() (notifyStore, error) {
	if c.notifyStore != nil {
		return c.notifyStore, nil
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve notify store paths: %w", err)
	}
	return notify.NewDefaultStore(paths), nil
}

func (c *aiCommand) readBellPaneInfo(paneID string) (bellPaneInfo, bool) {
	fields, err := c.muxRunner().DisplayPaneFields(context.Background(), paneID, aiBellPaneFormats...)
	if err != nil || len(fields) < 7 {
		return bellPaneInfo{}, false
	}
	info := bellPaneInfo{
		Session: fields[0],
		Window:  fields[1],
		WinName: fields[2],
		Pane:    fields[3],
		Title:   fields[4],
		Command: fields[5],
		Socket:  fields[6],
	}
	if info.Pane == "" {
		info.Pane = paneID
	}
	return info, info.Session != ""
}

func (c *aiCommand) duplicateBellRecent(paneID string) bool {
	lastAt := parsePositiveInt(c.readTmuxPaneOption(paneID, aiBellDedupeOption))
	if lastAt <= 0 {
		return false
	}
	return c.now().Unix()-int64(lastAt) < int64(aiBellDedupeWindow/time.Second)
}

func (c *aiCommand) recordBellNotification(paneID string) {
	c.recordAIPaneOption(paneID, aiBellDedupeOption, fmt.Sprintf("%d", c.now().Unix()))
}

func composeBellNotifyText(info bellPaneInfo) string {
	context := strings.TrimSpace(info.Title)
	if context == "" {
		context = strings.TrimSpace(info.Command)
	}
	if context == "" {
		context = strings.TrimSpace(info.WinName)
	}
	if context == "" {
		return "bell"
	}
	return "bell · " + context
}

func (c *aiCommand) aiIngestLogPath() (string, error) {
	homeDir, err := c.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths, err := config.Homes{
		HomeDir:    homeDir,
		ConfigHome: c.env("XDG_CONFIG_HOME"),
		StateHome:  c.env("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, aiIngestLogName), nil
}

func (c *aiCommand) appendAIIngestLog(entry aiIngestLogEntry) {
	// Both sinks below read the same record, so the reflection outcome is
	// folded in first: a hook whose Pane writes did not land must not reach
	// either of them still calling itself a delivery.
	entry = c.honestAIIngestResult(entry)
	// The legacy file remains the compatibility surface. Phase 5 projects only
	// anomalous, closed classifications into the common journal independently
	// of whether this best-effort legacy append succeeds.
	c.recordAIIngestFromLegacy(entry)
	path, err := c.aiIngestLogPath()
	if err != nil {
		return
	}
	if entry.At == "" {
		entry.At = c.now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	mkdirAll := c.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if err := mkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	localstate.RepairPrivateFile(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return
	}
	localstate.RepairPrivateFile(path)
	c.trimAIIngestLogFile(path)
}

func (c *aiCommand) trimAIIngestLogFile(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= aiIngestLogMaxSize {
		return
	}
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil || len(data) <= aiIngestLogMaxSize {
		return
	}
	start := max(len(data)-aiIngestLogRetain, 0)
	if start > 0 {
		if offset := bytes.IndexByte(data[start:], '\n'); offset >= 0 {
			start += offset + 1
		}
	}
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	_ = writeFile(path, data[start:], 0o600)
}

func nonEmptyLines(text string) []string {
	raw := strings.Split(strings.TrimSpace(text), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func formatAIIngestLogEntry(entry aiIngestLogEntry) string {
	parts := []string{entry.At, entry.Source}
	if entry.Event != "" {
		parts = append(parts, entry.Event)
	}
	parts = append(parts, entry.Result)
	for _, field := range []struct {
		key   string
		value string
	}{
		{"pane", entry.Pane},
		{"cwd", entry.CWD},
		{"thread", entry.ThreadID},
		{"session", entry.SessionID},
		{"turn", entry.TurnID},
		{"reason", string(entry.Reason)},
	} {
		if strings.TrimSpace(field.value) != "" {
			parts = append(parts, field.key+"="+field.value)
		}
	}
	return strings.Join(parts, " ")
}

func (c *aiCommand) markAIHookPane(paneID, agent, cwd, threadID, sessionID, transcriptPath string) {
	c.recordAIPaneOption(paneID, aiPaneHookActiveOption, "1")
	// A hook is observation, not launch authorship. Only an exact current
	// Agent->Pane Registry binding may receive the managed/provider projection;
	// an unbound Window shell remains a transient hook observation and can never
	// become a later topology-promotion input.
	binding, owned, bindingErr := c.managedAgentBindingForPane(paneID)
	exactOwnedProvider := bindingErr == nil && owned && coremetadata.NormalizeProvider(agent) == binding.agent.Spec.Provider
	if exactOwnedProvider {
		c.recordAIPaneOption(paneID, aiPaneManagedOption, "1")
		if agent != "" {
			c.recordAIPaneOption(paneID, aiPaneAgentOption, agent)
		}
	}
	if cwd != "" {
		c.recordAIPaneOption(paneID, aiPaneContextOption, cwd)
	}
	if threadID != "" {
		c.recordAIPaneOption(paneID, aiPaneThreadIDOption, threadID)
	}
	if sessionID != "" {
		c.recordAIPaneOption(paneID, aiPaneSessionIDOption, sessionID)
		c.writeAIHookResumeMetadata(paneID, sessionID)
	}
	if transcriptPath = strings.TrimSpace(transcriptPath); transcriptPath != "" {
		c.recordAIPaneOption(paneID, aiPaneTranscriptPathOption, transcriptPath)
	}
	// The pane options above are the live routing index and stay exactly as
	// they were. This is the second, additive home: the durable conversation
	// pointer on the Agent resource, which survives the Pane the options die
	// with.
	if exactOwnedProvider {
		c.stageAgentSessionRef(paneID, coremetadata.AgentSessionObservation{
			Provider:       agent,
			SessionID:      sessionID,
			ThreadID:       threadID,
			TranscriptPath: transcriptPath,
		})
	}
}

func (c *aiCommand) writeAIHookResumeMetadata(paneID, resumeID string) {
	resumeID = strings.TrimSpace(resumeID)
	if resumeID == "" {
		return
	}
	c.recordAIPaneOption(paneID, aiPaneResumeIDOption, resumeID)
	c.recordAIPaneOption(paneID, aiPaneResumeSourceOption, "hook")
	c.recordAIPaneOption(paneID, aiPaneResumeUpdatedAtOption, c.now().UTC().Format(time.RFC3339))
}

// matchAIPane resolves the Pane a hook event belongs to and, when it cannot,
// says which step failed. An explicit identity handed to the hook command is
// normally the whole answer: it is resolved through the Registry and does not
// fall through to the inherited-environment ladder, because falling through
// would attribute the event to whichever other live Pane happens to share a
// working directory. The established three-step ladder is unchanged and remains
// the answer whenever no explicit identity arrived.
//
// The one explicit value that is not an answer is one that was never the hook's
// own. A provider host shared by several Panes inherits the activation envelope
// of whichever Pane happened to launch it, so the argument can arrive holding a
// live, perfectly resolvable Pane that belongs to a different provider. Having a
// value and owning it are different things, and only the Registry can tell them
// apart: when it positively records a different provider for that Pane, the
// value is refused and the event continues to the steps that answer from what
// the hook itself carries. The inherited tmux environment is deliberately not
// among them -- it comes from the same envelope that was just refused.
//
// Behind that ladder sits one further step for the hook nobody can hand an
// identity to. A provider host shared by several Panes inherits neither the
// activation envelope nor a tmux environment, so the explicit argument arrives
// empty and the ladder cannot even read a Pane inventory. What that hook does
// carry is the conversation its event belongs to, and the Registry already
// records which Pane owns which conversation. Reading that record is a lookup,
// not a binding: nothing here decides what a conversation is bound to, and a
// conversation no Pane claims fails by name rather than landing on a neighbour.
func (c *aiCommand) matchAIPane(in aiPaneMatchInput) (string, string) {
	if explicit := strings.TrimSpace(in.ExplicitPane); explicit != "" {
		paneID, reason := c.resolveExplicitAIPane(in.Provider, explicit)
		if paneID != "" || reason != aiPaneMatchReasonExplicitForeign {
			return paneID, reason
		}
		return c.matchAIPaneAfterForeignExplicit(in)
	}
	if envPane := strings.TrimSpace(c.env("TMUX_PANE")); envPane != "" {
		return envPane, ""
	}
	paneID, reason := c.matchAIPaneFromInventory(in)
	if paneID != "" {
		return paneID, reason
	}
	if strings.TrimSpace(in.ThreadID) == "" && strings.TrimSpace(in.SessionID) == "" {
		return "", reason
	}
	return c.resolveRegisteredConversationPane(in.ThreadID, in.SessionID)
}

// matchAIPaneAfterForeignExplicit continues a refused inherited identity through
// the steps that answer from what the hook itself carries. The Registry
// conversation lookup goes first because it is the only step that answers from
// the payload alone; the established inventory ladder follows it unchanged. The
// inherited `TMUX_PANE` step is skipped on purpose: that variable arrives in the
// very envelope whose Pane identity was just refused, so honouring it would
// reintroduce the same misattribution through a second door.
//
// Provider coherence is applied once more here, to whatever these steps resolve.
// This path exists only because the explicit value was foreign, and its second
// step matches on working directory alone -- on a machine where every provider
// Pane sits in the same repository that step would hand the event straight back
// to a Pane of the very provider just refused. The check is applied to the
// resolved answer rather than inside the ladder, so the ladder and the path that
// was handed nothing keep their behavior exactly.
//
// A foreign answer ends the attempt rather than restarting the ladder looking
// for a later row: refusing with a reason is the conservative end, and rescanning
// would mean changing the shared ladder. Silence stays silence -- a Pane the
// Registry records no provider for is not a contradiction and is still taken.
//
// A failure here keeps the refusal legible. The reason is its own token rather
// than the downstream step's, so the record still says the hook was handed
// somebody else's Pane instead of only saying the inventory was unreadable.
func (c *aiCommand) matchAIPaneAfterForeignExplicit(in aiPaneMatchInput) (string, string) {
	registry := coremetadata.Registry{}
	if c.loadRegistry != nil {
		if loaded, err := c.loadRegistry(); err == nil {
			registry = loaded
		}
	}
	own := func(paneID string) bool {
		return !explicitAIPaneIsForeign(in.Provider, registeredPaneProvider(registry, paneID))
	}
	if strings.TrimSpace(in.ThreadID) != "" || strings.TrimSpace(in.SessionID) != "" {
		if paneID, _ := c.resolveRegisteredConversationPane(in.ThreadID, in.SessionID); paneID != "" && own(paneID) {
			return paneID, ""
		}
	}
	if paneID, _ := c.matchAIPaneFromInventory(in); paneID != "" && own(paneID) {
		return paneID, ""
	}
	return "", aiPaneMatchReasonExplicitForeignOnly
}

// registeredPaneProvider reports the provider the Registry records for a Pane,
// addressed by either spelling a step can produce. It demands the same round
// trip resolveExplicitAIPane does -- activation handle, owning Agent, and that
// Agent pointing back at this Pane -- and reports nothing for anything short of
// that. Nothing is silence, which the coherence predicate reads as agreement
// rather than disagreement, so an unregistered or half-torn Pane never becomes a
// refusal of its own.
func registeredPaneProvider(registry coremetadata.Registry, ref string) string {
	pane, ok := explicitAIPaneResource(registry, strings.TrimSpace(ref))
	if !ok {
		return ""
	}
	agentUID := strings.TrimSpace(pane.Status.Activation.AgentUID)
	if agentUID == "" {
		return ""
	}
	agent, ok := registry.Agent(agentUID)
	if !ok || agent.Status.PaneRef != pane.Metadata.UID {
		return ""
	}
	return agent.Spec.Provider
}

// matchAIPaneFromInventory is the established three-step ladder over the live
// tmux Pane inventory: working directory first, then the conversation options
// the panes themselves carry. It is unchanged; it only reports its own failure
// to a caller that now has one more step to try.
func (c *aiCommand) matchAIPaneFromInventory(in aiPaneMatchInput) (string, string) {
	rows, listed := c.listAIPaneMatchRows()
	if !listed {
		return "", aiPaneMatchReasonNoInventory
	}
	if cwd := cleanMatchPath(in.CWD); cwd != "" {
		for _, row := range rows {
			if cleanMatchPath(row.CWD) == cwd {
				return row.PaneID, ""
			}
		}
	}
	threadID := strings.TrimSpace(in.ThreadID)
	sessionID := strings.TrimSpace(in.SessionID)
	if threadID == "" && sessionID == "" {
		return "", aiPaneMatchReasonNoMatch
	}
	for _, row := range rows {
		if threadID != "" && strings.TrimSpace(row.ThreadID) == threadID {
			return row.PaneID, ""
		}
		if sessionID != "" && strings.TrimSpace(row.SessionID) == sessionID {
			return row.PaneID, ""
		}
	}
	return "", aiPaneMatchReasonNoMatch
}

// resolveRegisteredConversationPane answers from the Registry alone, so it works
// for a hook whose process reaches no tmux server at all. It refuses rather than
// guesses: an identifier no Pane records, and an identifier two Panes record,
// both end as a named failure, because a hook that lands on the wrong Pane is
// worse than one that lands nowhere.
func (c *aiCommand) resolveRegisteredConversationPane(threadID, sessionID string) (string, string) {
	if c.loadRegistry == nil {
		return "", aiPaneMatchReasonRegistryUnavailable
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return "", aiPaneMatchReasonRegistryUnavailable
	}
	panes := registeredConversationPanes(registry)
	for _, id := range []string{threadID, sessionID} {
		runtimeID, ok := panes[strings.TrimSpace(id)]
		if !ok || strings.TrimSpace(id) == "" {
			continue
		}
		if runtimeID == "" {
			return "", aiPaneMatchReasonConversationShared
		}
		return runtimeID, ""
	}
	return "", aiPaneMatchReasonConversationUnknown
}

// registeredConversationPanes indexes every conversation identifier the Registry
// currently records against the live runtime handle of the Pane that holds it.
//
// The index is built from the resource model, never from a provider name: a
// Pane contributes the conversation its current activation refinement recorded
// and the one its Agent's durable session pointer names, and a provider that
// records neither simply contributes nothing. The same round trip
// resolveExplicitAIPane demands is required here — activation handle, owning
// Agent, and that Agent pointing back at this Pane — so a half-torn binding
// never becomes an attribution target.
//
// A conversation claimed by two Panes maps to the empty handle. That is the
// ambiguity marker, not an absent entry: the caller has to be able to tell "no
// Pane holds this" from "more than one does", and neither may resolve.
func registeredConversationPanes(registry coremetadata.Registry) map[string]string {
	panes := make(map[string]string, len(registry.Panes))
	claim := func(conversationID, runtimeID string) {
		conversationID = strings.TrimSpace(conversationID)
		if conversationID == "" {
			return
		}
		if held, ok := panes[conversationID]; ok && held != runtimeID {
			panes[conversationID] = ""
			return
		}
		panes[conversationID] = runtimeID
	}
	for i := range registry.Panes {
		pane := &registry.Panes[i]
		runtimeID := strings.TrimSpace(pane.Status.Activation.RuntimeID)
		agentUID := strings.TrimSpace(pane.Status.Activation.AgentUID)
		if runtimeID == "" || agentUID == "" {
			continue
		}
		agent, ok := registry.Agent(agentUID)
		if !ok || agent.Status.PaneRef != pane.Metadata.UID {
			continue
		}
		if codex := pane.Status.Activation.Codex; codex != nil {
			claim(codex.ThreadID, runtimeID)
		}
		claim(agent.Status.SessionRef.ConversationID(), runtimeID)
	}
	return panes
}

// resolveExplicitAIPane turns the identity a hook was handed into the exact
// runtime handle its Pane currently holds. The Registry is the authority, not
// the value itself: a `%N` that no longer belongs to the Pane that owned it, or
// a Pane whose Agent binding no longer round-trips, is a failure rather than a
// second-best guess. Resolution reads no tmux server, so it survives an
// app-server that inherited no tmux environment.
//
// The last check is coherence rather than liveness, and it is what separates an
// identity the hook owns from one it inherited. The Registry already records
// which provider an Agent runs, in exactly the shape markAIHookPane compares
// against, so a Codex hook handed a Claude Pane is a contradiction the resource
// model states outright. Only a recorded and different provider refuses: a Pane
// the Registry records no provider for -- an unbound shell where somebody just
// ran a provider by hand -- is not a contradiction and resolves as before. The
// rule is symmetric over providers and names none of them.
func (c *aiCommand) resolveExplicitAIPane(provider, ref string) (string, string) {
	if c.loadRegistry == nil {
		return "", aiPaneMatchReasonRegistryUnavailable
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return "", aiPaneMatchReasonRegistryUnavailable
	}
	pane, ok := explicitAIPaneResource(registry, ref)
	if !ok {
		return "", aiPaneMatchReasonExplicitUnknown
	}
	runtimeID := strings.TrimSpace(pane.Status.Activation.RuntimeID)
	if runtimeID == "" {
		return "", aiPaneMatchReasonExplicitNoRuntime
	}
	if agentUID := strings.TrimSpace(pane.Status.Activation.AgentUID); agentUID != "" {
		agent, ok := registry.Agent(agentUID)
		if !ok || agent.Status.PaneRef != pane.Metadata.UID {
			return "", aiPaneMatchReasonExplicitStale
		}
		if explicitAIPaneIsForeign(provider, agent.Spec.Provider) {
			return "", aiPaneMatchReasonExplicitForeign
		}
	}
	return runtimeID, ""
}

// explicitAIPaneIsForeign reports whether the Registry positively contradicts a
// hook's claim on a Pane. Both sides are normalized through the one provider
// vocabulary, and an unrecognized or absent value on either side is silence, not
// disagreement: only two recognized providers that differ are a contradiction.
func explicitAIPaneIsForeign(hookProvider, paneProvider string) bool {
	hook := coremetadata.NormalizeProvider(hookProvider)
	pane := coremetadata.NormalizeProvider(paneProvider)
	return hook != "" && pane != "" && hook != pane
}

// explicitAIPaneResource accepts either spelling a hook can be handed: the
// stable Pane uid projmux plants in the activation envelope, or the exact `%N`
// runtime handle. Both are looked up in the Registry so neither is trusted on
// its own.
func explicitAIPaneResource(registry coremetadata.Registry, ref string) (*coremetadata.Pane, bool) {
	if strings.HasPrefix(ref, "%") {
		for i := range registry.Panes {
			if strings.TrimSpace(registry.Panes[i].Status.Activation.RuntimeID) == ref {
				return &registry.Panes[i], true
			}
		}
		return nil, false
	}
	return registry.Pane(ref)
}

func (c *aiCommand) listAIPaneMatchRows() ([]aiPaneMatchRow, bool) {
	rows, err := c.muxRunner().ListPanes(context.Background(), intmux.ListPanesOptions{
		All:     true,
		Formats: aiIngestListPanesFormats,
	})
	if err != nil {
		return nil, false
	}
	matches := make([]aiPaneMatchRow, 0, len(rows))
	for _, fields := range rows {
		paneID := fields[0]
		if paneID == "" {
			continue
		}
		matches = append(matches, aiPaneMatchRow{
			PaneID:    paneID,
			CWD:       fields[1],
			ThreadID:  fields[2],
			SessionID: fields[3],
		})
	}
	return matches, true
}

func cleanMatchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringFromAny(raw[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNestedString(value any, keys ...string) string {
	nested, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return firstString(nested, keys...)
}

func firstAny(raw map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := raw[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func firstNestedAny(value any, keys ...string) any {
	nested, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return firstAny(nested, keys...)
}

func firstBool(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		if value, ok := boolFromAny(raw[key]); ok {
			return value, true
		}
	}
	return false, false
}

func firstNestedBool(value any, keys ...string) (bool, bool) {
	nested, ok := value.(map[string]any)
	if !ok {
		return false, false
	}
	return firstBool(nested, keys...)
}

func mapFromAny(value any) map[string]any {
	if raw, ok := value.(map[string]any); ok {
		out := make(map[string]any, len(raw))
		maps.Copy(out, raw)
		return out
	}
	return map[string]any{}
}

func boolFromAny(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

func boolMetadataValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func truncateRunes(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
