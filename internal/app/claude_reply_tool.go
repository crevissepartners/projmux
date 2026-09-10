package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

const internalClaudeReplyGuardEnv = "PMX_INTERNAL_CLAUDE_REPLY_GUARD"
const claudeReplyToolTicketLifetime = 15 * time.Second

// This nonsecret policy is captured by the exact SessionStart child and passed
// over the existing bootstrap pipe. It is not Registry or model-authored state.
type claudeReplyToolPolicy struct {
	Executable  string   `json:"executable"`
	Directory   string   `json:"directory"`
	Environment []string `json:"environment"`
	ProfileDir  string   `json:"profileDir,omitempty"`
}

type claudeReplyToolInput struct {
	ToolUseID string `json:"toolUseId"`
	Command   string `json:"command"`
	Directory string `json:"directory"`
}

type claudeReplyToolResult struct {
	Marker           string                 `json:"marker,omitempty"`
	Policy           *claudeReplyToolPolicy `json:"policy,omitempty"`
	Argv             []string               `json:"argv,omitempty"`
	ExecutableSHA256 string                 `json:"executableSHA256,omitempty"`
}

type claudeReplyToolPermit struct {
	toolUseID string
	argv      []string
	expires   time.Time
	peer      coremetadata.ProcessIdentity
}

type claudeReplyToolGate struct {
	profileObserver coremetadata.ProcessIdentity
	evidence        map[string]claudeDialogueGuardEvidence
	profile         *claudeDialogueProfile
	mu              sync.Mutex
	policy          claudeReplyToolPolicy
	executable      *os.File
	executableInfo  os.FileInfo
	directoryInfo   os.FileInfo
	digest          string
	permits         map[string]claudeReplyToolPermit
	issued          map[string]bool
	executing       map[coremetadata.ProcessIdentity]claudeReplyToolPermit
}

var errClaudeReplyTool = errors.New("isolated explicit reply execution refused")
var claudeReplyMarkerPattern = regexp.MustCompile(`^pmx-reply-ticket-[a-f0-9]{16}-[a-f0-9]{48}$`)
var claudeReplyCarrierMarkerPattern = regexp.MustCompile(`pmx-reply-ticket-[a-f0-9]{16}-[a-f0-9]{48}`)

func captureClaudeReplyToolPolicy(getenv func(string) string) (*claudeReplyToolPolicy, error) {
	return captureClaudeReplyToolPolicyFrom(getenv, os.Executable, os.Getwd)
}

func captureClaudeReplyToolPolicyFrom(getenv func(string) string, executablePath, workingDirectory func() (string, error)) (*claudeReplyToolPolicy, error) {
	if getenv(internalClaudeReplyGuardEnv) != "1" {
		return nil, nil
	}
	executable, err := executablePath()
	if err != nil {
		return nil, errClaudeReplyTool
	}
	directory, err := workingDirectory()
	if err != nil {
		return nil, errClaudeReplyTool
	}
	policy := &claudeReplyToolPolicy{Executable: executable, Directory: directory, ProfileDir: getenv(internalClaudeDialogueProfileEnv)}
	// Never copy a provider credential, config helper, or user command's env.
	for _, key := range []string{"HOME", "PATH", "TMUX", "TMUX_PANE", "TMUX_TMPDIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME", internalClaudeRegistryPathEnv, internalActivationPaneUIDEnv, internalActivationGenerationEnv} {
		if value := getenv(key); value != "" {
			policy.Environment = append(policy.Environment, key+"="+value)
		}
	}
	return policy, nil
}

// SHELL_PREFIX receives a complete shell invocation as one opaque argument.
// Only a canonical ticket is extracted; no other byte is executed, interpreted,
// logged or retained. The ticket selects an exact action already authorized in
// helper memory and still requires the current provider-owned caller identity.
func claudeReplyCarrierMarker(carrier string) (string, error) {
	if len(carrier) > localipc.MaxFrameBytes || !utf8.ValidString(carrier) || strings.ContainsRune(carrier, '\x00') || strings.Count(carrier, "pmx-reply-ticket-") != 1 {
		return "", errClaudeReplyTool
	}
	indices := claudeReplyCarrierMarkerPattern.FindStringIndex(carrier)
	if indices == nil {
		return "", errClaudeReplyTool
	}
	identifier := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
	}
	if indices[0] > 0 && identifier(carrier[indices[0]-1]) || indices[1] < len(carrier) && identifier(carrier[indices[1]]) {
		return "", errClaudeReplyTool
	}
	return carrier[indices[0]:indices[1]], nil
}

func newClaudeReplyToolGate(policy claudeReplyToolPolicy) (*claudeReplyToolGate, error) {
	if !filepath.IsAbs(policy.Executable) || filepath.Clean(policy.Executable) != policy.Executable || !filepath.IsAbs(policy.Directory) || filepath.Clean(policy.Directory) != policy.Directory {
		return nil, errClaudeReplyTool
	}
	before, err := os.Lstat(policy.Executable)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 || before.Mode().Perm()&0o022 != 0 || !localipc.OwnedByCurrentUser(before) {
		return nil, errClaudeReplyTool
	}
	file, err := os.Open(policy.Executable) // #nosec G304 -- exact absolute owned executable, pinned and compared below.
	if err != nil {
		return nil, errClaudeReplyTool
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, errClaudeReplyTool
	}
	directory, err := os.Lstat(policy.Directory)
	if err != nil || !directory.IsDir() || !localipc.OwnedByCurrentUser(directory) {
		_ = file.Close()
		return nil, errClaudeReplyTool
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		_ = file.Close()
		return nil, errClaudeReplyTool
	}
	var profile *claudeDialogueProfile
	var profileObserver coremetadata.ProcessIdentity
	if policy.ProfileDir != "" {
		value, err := readClaudeDialogueProfile(policy.ProfileDir, policy.Executable)
		if err != nil {
			_ = file.Close()
			return nil, errClaudeReplyTool
		}
		profile = &value
		profileObserver, err = readClaudeDialogueObserver(policy.ProfileDir)
		if err != nil {
			_ = file.Close()
			return nil, errClaudeReplyTool
		}
	}
	return &claudeReplyToolGate{profileObserver: profileObserver, profile: profile, policy: policy, executable: file, executableInfo: after, directoryInfo: directory, digest: hex.EncodeToString(digest.Sum(nil)),
		evidence: make(map[string]claudeDialogueGuardEvidence), permits: make(map[string]claudeReplyToolPermit), issued: make(map[string]bool), executing: make(map[coremetadata.ProcessIdentity]claudeReplyToolPermit)}, nil
}

func (g *claudeReplyToolGate) close() {
	if g != nil {
		_ = g.executable.Close()
	}
}

// The executable inode is pinned independently of the pathname. On this
// Linux-only opt-in execution surface, /proc proves the actual caller image.
func (g *claudeReplyToolGate) currentExecutable(peer coremetadata.ProcessIdentity) bool {
	if g == nil {
		return false
	}
	if g.profile != nil {
		current, err := readClaudeDialogueProfile(g.policy.ProfileDir, g.policy.Executable)
		if err != nil || current != *g.profile {
			return false
		}
		observer, err := readClaudeDialogueObserver(g.policy.ProfileDir)
		if err != nil || observer != g.profileObserver {
			return false
		}
	}
	actual, _, err := localipc.Process(peer.PID)
	if err != nil || actual != peer {
		return false
	}
	current, err := os.Lstat(g.policy.Executable)
	if err != nil || !os.SameFile(current, g.executableInfo) || current.Mode().Perm()&0o022 != 0 {
		return false
	}
	image, err := os.Stat(filepath.Join("/proc", strconvPID(peer.PID), "exe"))
	if err != nil || !os.SameFile(image, g.executableInfo) {
		return false
	}
	dir, err := os.Lstat(g.policy.Directory)
	return err == nil && os.SameFile(dir, g.directoryInfo)
}

func (g *claudeReplyToolGate) ready() bool {
	process, _, err := localipc.Process(os.Getpid())
	return err == nil && g.currentExecutable(process)
}

func strconvPID(pid int) string {
	// Process PID is a validated integer, never a path supplied by a model.
	return strconv.Itoa(pid)
}

// parseClaudeReplyCommand recognizes only literal shell words. Quotes turn
// text into data; expansion, substitutions, redirects, chains and env prefixes
// never reach a shell. The return value is executed as argv, never evaluated.
func parseClaudeReplyCommand(command, executable string) ([]string, error) {
	if len(command) > coremessage.MaxPayloadBytes+1024 || !utf8.ValidString(command) || strings.ContainsAny(command, "\x00\r\n") {
		return nil, errClaudeReplyTool
	}
	var argv []string
	for i := 0; i < len(command); {
		for i < len(command) && command[i] == ' ' {
			i++
		}
		if i == len(command) {
			break
		}
		var word strings.Builder
		started := false
		for i < len(command) && command[i] != ' ' {
			started = true
			if command[i] == '\'' {
				i++
				end := strings.IndexByte(command[i:], '\'')
				if end < 0 {
					return nil, errClaudeReplyTool
				}
				word.WriteString(command[i : i+end])
				i += end + 1
				continue
			}
			if command[i] == '\\' && i+1 < len(command) && command[i+1] == '\'' {
				word.WriteByte('\'')
				i += 2
				continue
			}
			c := command[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/_:.-", rune(c))) {
				return nil, errClaudeReplyTool
			}
			word.WriteByte(c)
			i++
		}
		if !started {
			return nil, errClaudeReplyTool
		}
		argv = append(argv, word.String())
	}
	if len(argv) != 9 || argv[0] != executable || argv[1] != "agent" || argv[2] != "message" || argv[3] != "send" || !strings.HasPrefix(argv[4], "uid:") || argv[5] != "--reply-to" || argv[7] != "--" || !validCoordinationRef(argv[6]) || !validClaudeAssistantReply(argv[8]) {
		return nil, errClaudeReplyTool
	}
	return argv, nil
}

func (h *claudeCoordinationHub) permitsExplicitTool(argv []string, route coremetadata.AgentRouteRef, broker claudeDialogueBroker) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireQualificationLocked(h.now())
	message := h.messages[argv[6]]
	if h.closed || broker == nil || message == nil || message.envelope.BrokerEnvelope == nil || message.delivery.State != agentdelivery.StateDelivered || message.replyReserved || !message.envelope.Deadline.After(h.now()) {
		return false
	}
	if message.replyRef != "" && !knownZeroExplicitReply(broker, message.envelope.MessageRef) {
		return false
	}
	original := message.envelope.BrokerEnvelope
	if original.Target != publicMessageRoute(route) || argv[4] != "uid:"+original.Source.AgentUID || !broker.Current(*original) {
		return false
	}
	if h.qualifiedVersion != claudeFrozenFrameProviderVersion {
		state := h.qualification
		if state == nil || state.state != "pending" || !state.frameComplete || state.ref != original.MessageRef || argv[8] != state.marker {
			return false
		}
	}
	return true
}

func (g *claudeReplyToolGate) prepare(input claudeReplyToolInput, peer coremetadata.ProcessIdentity,
	route coremetadata.AgentRouteRef, h *claudeCoordinationHub, broker claudeDialogueBroker,
) (*claudeReplyToolResult, error) {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !claudeProviderDescendant(peer, authority.Process) || !g.currentExecutable(peer) || input.Directory != g.policy.Directory || !validCoordinationRef(input.ToolUseID) {
		return nil, errClaudeReplyTool
	}
	argv, err := parseClaudeReplyCommand(input.Command, g.policy.Executable)
	if err != nil || !h.permitsExplicitTool(argv, route, broker) {
		return nil, errClaudeReplyTool
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.issued[input.ToolUseID] || len(g.issued) >= 32 {
		return nil, errClaudeReplyTool
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return nil, errClaudeReplyTool
	}
	id := sha256.Sum256([]byte(input.ToolUseID))
	marker := "pmx-reply-ticket-" + hex.EncodeToString(id[:8]) + "-" + hex.EncodeToString(nonce)
	g.issued[input.ToolUseID] = true
	g.evidence[input.ToolUseID] = claudeDialogueGuardEvidence{MessageRef: argv[6], TargetAgentUID: strings.TrimPrefix(argv[4], "uid:")}
	g.permits[marker] = claudeReplyToolPermit{toolUseID: input.ToolUseID, argv: argv, expires: time.Now().Add(claudeReplyToolTicketLifetime)}
	return &claudeReplyToolResult{Marker: marker}, nil
}

func (g *claudeReplyToolGate) consume(marker string, peer coremetadata.ProcessIdentity,
	route coremetadata.AgentRouteRef, h *claudeCoordinationHub, broker claudeDialogueBroker,
) (*claudeReplyToolResult, error) {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !claudeProviderDescendant(peer, authority.Process) || !g.currentExecutable(peer) || !claudeReplyMarkerPattern.MatchString(marker) {
		return nil, errClaudeReplyTool
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	permit, ok := g.permits[marker]
	if !ok {
		return nil, errClaudeReplyTool
	}
	delete(g.permits, marker) // Failed consumption is terminal; never a queued permit.
	id := sha256.Sum256([]byte(permit.toolUseID))
	if !strings.HasPrefix(marker, "pmx-reply-ticket-"+hex.EncodeToString(id[:8])+"-") || !permit.expires.After(time.Now()) || !h.permitsExplicitTool(permit.argv, route, broker) {
		return nil, errClaudeReplyTool
	}
	if _, exists := g.executing[peer]; exists {
		return nil, errClaudeReplyTool
	}
	permit.peer = peer
	g.executing[peer] = permit
	evidence := g.evidence[permit.toolUseID]
	evidence.ExecutionProcess = peer
	g.evidence[permit.toolUseID] = evidence
	policy := g.policy
	return &claudeReplyToolResult{Policy: &policy, Argv: append([]string(nil), permit.argv...), ExecutableSHA256: g.digest}, nil
}

func (g *claudeReplyToolGate) authorizeCommit(peer coremetadata.ProcessIdentity, reply coremessage.Envelope) bool {
	if !g.currentExecutable(peer) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	permit, ok := g.executing[peer]
	if !ok {
		return false
	}
	delete(g.executing, peer)
	allowed := permit.peer == peer && permit.expires.After(time.Now()) && permit.argv[4] == "uid:"+reply.Target.AgentUID && permit.argv[6] == reply.ReplyTo && permit.argv[8] == reply.Payload
	if allowed {
		evidence := g.evidence[permit.toolUseID]
		evidence.AuthorizedReplyRef = reply.MessageRef
		g.evidence[permit.toolUseID] = evidence
	}
	return allowed
}
