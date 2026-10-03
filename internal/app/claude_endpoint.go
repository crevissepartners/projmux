package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Exec replaces the hook shell: the registration process's direct parent must
// be the exact activation-exec process after it exec'd Claude. An unmanaged
// nested Claude may inherit the private activation environment, but cannot pass
// this producer check for its own SessionStart.
const claudeRegistrationHookCommand = "exec projmux internal claude-endpoint-register >/dev/null 2>&1 # " + claudeHookManagedMarker

// claudeEndpointPollInterval is the helper accept loop's idle cadence only. It
// never bounds how long a readiness answer may take; see
// claudeEndpointReadinessWriteDeadline and the client bounds below.
const claudeEndpointPollInterval = 100 * time.Millisecond

// claudeEndpointReadinessWriteDeadline bounds only the one-byte readiness write,
// armed after current() has been evaluated. Under load current() (socket and
// process identity checks plus a Registry read) took longer than the 100ms poll
// interval that used to be armed before it, so the write missed its deadline and
// the helper closed the connection unanswered: a live peer then read EOF and was
// refused as "lease is stale". One byte never fills a socket buffer, so this
// bound only guards the single accept loop against being preempted between
// arming it and writing; the readiness budget is the client's read bound.
const claudeEndpointReadinessWriteDeadline = time.Second

// Client bounds for one Claude lease probe (dial, readiness byte, coordination
// probe) and one coordination eligibility call. Each bound is the larger of the
// previous 200ms and max + (max - p90) of that step, measured under a CPU hog of
// 3x nproc (loadavg 33-59) against live helpers. The readiness read is measured
// only against helpers that answer after current(), because the old 100ms helper
// cap hid every slower answer; the other steps were never capped by the helper:
//
//	dial               p90   3.8ms  max  24.5ms  -> 45ms  -> stays 200ms
//	readiness read     p90  54.4ms  max 197.5ms  -> 341ms -> 350ms (1.8x max)
//	coordination probe p90  63.5ms  max 179.9ms  -> 296ms -> 300ms (1.7x max)
//	eligibility        p90  60.3ms  max 123.3ms  -> 186ms -> stays 200ms
//
// Cost: identity and socket checks run before every bound, so a dead peer is
// refused without waiting. A live helper that never answers is refused after
// dial + readiness read (550ms). One ResolveTarget on a slow but healthy peer can
// wait up to lease + lease + eligibility (1,900ms, was 1,400ms). This is a margin
// over the measured distribution, not a proof for heavier load.
const (
	claudeLeaseDialTimeout               = 200 * time.Millisecond
	claudeLeaseReadinessReadTimeout      = 350 * time.Millisecond
	claudeLeaseCoordinationProbeTimeout  = 300 * time.Millisecond
	claudeCoordinationEligibilityTimeout = 200 * time.Millisecond
)

// claudeEndpointIdleRegistryFloor bounds helper exit under a stat identity collision to <=2s including the 100ms tick granularity.
const claudeEndpointIdleRegistryFloor = 1800 * time.Millisecond

// claudeEndpointBootstrap travels only over an anonymous pipe from the exact
// SessionStart hook to its detached helper. Never log or persist this value.
type claudeEndpointBootstrap struct {
	RegistryPath string
	AgentUID     string
	PaneUID      string
	Generation   string
	Registration coremetadata.ClaudeRegistration
	// PriorRegistrationGeneration is the registrationGeneration the hook saw
	// on the pane when it built this bootstrap. The helper's claim is a CAS on
	// it, so a helper whose hook looked before a newer SessionStart claimed
	// cannot overwrite that newer registration.
	PriorRegistrationGeneration string
	HookProcess                 coremetadata.ProcessIdentity
	Socket                      string
	Token                       string
	ReplyTool                   *claudeReplyToolPolicy
	ProcessProof                *claudeProcessProof
}

func (claudeEndpointBootstrap) String() string   { return "[private Claude registration]" }
func (claudeEndpointBootstrap) GoString() string { return "[private Claude registration]" }

func prepareClaudeActivationProcess(spec superviseSpec) (bool, error) {
	store := resourceStoreAtPath(spec.RegistryPath)
	claudeRegistration := false
	_, _, err := store.updateConvergent(func(reg *coremetadata.Registry) error {
		agent, ok := reg.Agent(spec.AgentUID)
		if !ok || agent.Spec.Provider != aiModeClaude {
			return nil
		}
		claudeRegistration = true
		process, _, err := claudeadapter.Process(os.Getpid())
		if err != nil {
			return errors.New("claude process identity unavailable")
		}
		return intmetadata.DefaultMutator().RecordClaudeProcess(reg, spec.PaneUID, spec.AgentUID, spec.Generation, process)
	})
	return claudeRegistration, err
}

// claudeRegistrationProceed is the zero reason: the step that returns it
// refused nothing, and the registration goes on.
const claudeRegistrationProceed diagnostics.ClaudeRegistrationReason = ""

// claudeRegistrationRefusal is an error carrying one refusal's closed reason.
// Its text is the reason alone, never an underlying error.
type claudeRegistrationRefusal struct {
	reason diagnostics.ClaudeRegistrationReason
}

func (e *claudeRegistrationRefusal) Error() string {
	return "claude registration refused: " + string(e.reason)
}

func refuseClaudeRegistration(reason diagnostics.ClaudeRegistrationReason) error {
	return &claudeRegistrationRefusal{reason: reason}
}

// claudeRegistrationSubject is the Agent and Pane one registration record
// names. It is empty until they matched the Registry.
type claudeRegistrationSubject struct {
	AgentUID string
	PaneUID  string
}

// Only the separate SessionStart registration hook calls this route. Existing
// status hooks and their parser/projection remain unchanged. Every failure is
// quiet and fail-closed; no raw upstream input or credential reaches errors.
//
// The hook writes at most one agent.claude.registration record, and only here
// (an unmanaged session, with no activation Registry path, writes none):
// after the whole attempt returned, so never between starting the helper and
// the helper's producer check. A confirmed admission records nothing, because
// the helper records ready itself. The append is best-effort and changes
// neither the output nor the nil result.
func runClaudeEndpointRegistration(args []string, recorder *diagnostics.ClaudeRegistrationRecorder) error {
	return recordClaudeEndpointRegistration(recorder, args, os.Getenv, os.Stdin, os.Getppid(), startClaudeEndpointHelper)
}

// recordClaudeEndpointRegistration is runClaudeEndpointRegistration with the
// hook process's inputs passed in.
func recordClaudeEndpointRegistration(recorder *diagnostics.ClaudeRegistrationRecorder, args []string, env func(string) string, stdin io.Reader, parentPID int, start func(claudeEndpointBootstrap) error) error {
	started := time.Now()
	subject, reason := claudeEndpointRegistrationHook(args, env, stdin, parentPID, start)
	if reason != claudeRegistrationProceed && reason.Recorded() {
		recorder.Record(diagnostics.ClaudeRegistrationRecord{Source: diagnostics.ClaudeRegistrationSourceHook, Reason: reason,
			Duration: time.Since(started), AgentUID: subject.AgentUID, PaneUID: subject.PaneUID})
	}
	return nil
}

// claudeEndpointRegistrationHook is the hook's whole attempt. It returns
// claudeRegistrationProceed once the helper confirmed its admission, and the
// refusal otherwise; the subject is set only from the provider process check
// onward, once the bootstrap matched the pane and its agent against Registry.
func claudeEndpointRegistrationHook(args []string, env func(string) string, stdin io.Reader, parentPID int, start func(claudeEndpointBootstrap) error) (claudeRegistrationSubject, diagnostics.ClaudeRegistrationReason) {
	if len(args) != 0 {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationHookArguments
	}
	registryPath := env(internalClaudeRegistryPathEnv)
	// A Claude session projmux did not launch carries no activation at all;
	// the user-wide hook still runs for it, and it records nothing.
	if registryPath == "" {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationUnmanagedSession
	}
	if exactActivationRegistryPath(registryPath) != nil {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationRegistryPathInvalid
	}
	processPath := env(internalClaudeProcessBindingEnv) != "" || env(internalClaudeProcessHostEnv) != ""
	store := intmetadata.NewStore(registryPath)
	var reg coremetadata.Registry
	var err error
	if !processPath {
		reg, err = store.LoadDegradedReadOnly()
		if err != nil {
			return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationRegistryUnreadable
		}
	}
	data, err := io.ReadAll(io.LimitReader(stdin, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationHookInputUnreadable
	}
	if env(internalClaudeProcessBindingEnv) != "" || env(internalClaudeProcessHostEnv) != "" {
		var payload struct {
			Event   string `json:"hook_event_name"`
			Session string `json:"session_id"`
		}
		if json.Unmarshal(data, &payload) != nil || payload.Event != "SessionStart" {
			return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationPayloadNotSessionStart
		}
		if _, ok := claudeProcessHookProof(env, payload.Session, parentPID); !ok {
			return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationProviderProcessMismatch
		}
	}
	if processPath {
		reg, err = store.LoadDegradedReadOnly()
		if err != nil {
			return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationRegistryUnreadable
		}
	}
	bootstrap, reason := claudeRegistrationBootstrap(reg, registryPath, data, env, parentPID)
	subject := claudeRegistrationSubject{AgentUID: bootstrap.AgentUID, PaneUID: bootstrap.PaneUID}
	if reason != claudeRegistrationProceed {
		return subject, reason
	}
	return subject, registerClaudeEndpoint(bootstrap, start)
}

// registerClaudeEndpoint is the hook's part of one registration once its
// bootstrap is built: start is startClaudeEndpointHelper outside tests.
//
// Claude cancels this hook at its 5s timeout, so the hook takes no Registry
// lock at all: it starts the helper straight from its lock-free bootstrap. The
// helper claims and records the registration in one transaction in its own
// lifetime, bounded by the Registry lock acquisition timeout. A hook cancelled
// before its helper admits the registration therefore leaves the pane as it
// found it, never with a claimed registration that nothing will make Ready.
//
// It returns claudeRegistrationProceed once the helper confirmed its admission
// and the start refusal otherwise. A start error without a reason is counted
// as a failed start.
func registerClaudeEndpoint(bootstrap claudeEndpointBootstrap, start func(claudeEndpointBootstrap) error) diagnostics.ClaudeRegistrationReason {
	if bootstrap.ProcessProof != nil && !checkClaudeProcessHost(*bootstrap.ProcessProof, false) {
		return diagnostics.ClaudeRegistrationProviderProcessMismatch
	}
	err := start(bootstrap)
	if err == nil {
		return claudeRegistrationProceed
	}
	var refusal *claudeRegistrationRefusal
	if errors.As(err, &refusal) && refusal.reason.Refusal() {
		return refusal.reason
	}
	return diagnostics.ClaudeRegistrationHelperStartFailed
}

// claudeRegistrationBootstrap returns claudeRegistrationProceed with a usable
// bootstrap, or the refusal. A refusal returns the zero bootstrap until the
// pane and its agent matched the Registry, and after that one carrying only
// their UIDs, which the hook's record names.
func claudeRegistrationBootstrap(reg coremetadata.Registry, registryPath string, data []byte, env func(string) string, parentPID int) (claudeEndpointBootstrap, diagnostics.ClaudeRegistrationReason) {
	var payload struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Event != "SessionStart" {
		return claudeEndpointBootstrap{}, diagnostics.ClaudeRegistrationPayloadNotSessionStart
	}
	paneUID, generation := env(internalActivationPaneUIDEnv), env(internalActivationGenerationEnv)
	pane, ok := reg.Pane(paneUID)
	if !ok || pane.Status.Activation.Generation != generation || generation == "" || pane.Status.Activation.Claude == nil {
		return claudeEndpointBootstrap{}, diagnostics.ClaudeRegistrationPaneBindingMismatch
	}
	agent, ok := reg.Agent(pane.Status.Activation.AgentUID)
	if !ok || agent.Spec.Provider != aiModeClaude || agent.Status.Phase != coremetadata.PhaseRunning ||
		agent.Status.PaneRef != paneUID || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerUID() != agent.Metadata.UID || pane.Spec.Role != coremetadata.PaneRoleAgent {
		return claudeEndpointBootstrap{}, diagnostics.ClaudeRegistrationAgentMismatch
	}
	matched := claudeEndpointBootstrap{AgentUID: agent.Metadata.UID, PaneUID: paneUID}
	var proof *claudeProcessProof
	if env(internalClaudeProcessBindingEnv) != "" || env(internalClaudeProcessHostEnv) != "" {
		verified, ok := claudeProcessHookProof(env, payload.SessionID, parentPID)
		if !ok || verified.Binding.Agent != agent.Metadata.UID || verified.Binding.Pane != paneUID ||
			verified.Binding.Generation != generation {
			return matched, diagnostics.ClaudeRegistrationProviderProcessMismatch
		}
		proof = &verified
	}
	process := pane.Status.Activation.Claude.Process
	actual, _, err := claudeadapter.Process(parentPID)
	if err != nil || actual != process || int64(actual.OwnerUID) != int64(os.Getuid()) {
		return matched, diagnostics.ClaudeRegistrationProviderProcessMismatch
	}
	socket, token := env("CLAUDE_CODE_MESSAGING_SOCKET"), env("CLAUDE_CODE_MESSAGING_TOKEN")
	if socket == "" || token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return matched, diagnostics.ClaudeRegistrationMessagingEnvInvalid
	}
	// Hook identities are untrusted data too. Refuse a credential or locator
	// embedded in any field destined for Registry, even when syntactically valid.
	for _, value := range []string{payload.SessionID} {
		if strings.Contains(value, token) || strings.Contains(value, socket) {
			return matched, diagnostics.ClaudeRegistrationSessionIDEmbedsLocator
		}
	}
	if _, err := inspectClaudeSocket(socket); err != nil {
		return matched, diagnostics.ClaudeRegistrationMessagingSocket
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return matched, diagnostics.ClaudeRegistrationNonceUnavailable
	}
	authority := coremetadata.ClaudeAuthorityRef{SessionID: payload.SessionID, Process: process,
		RegistrationGeneration: hex.EncodeToString(nonce), LeaseProcess: process}
	if !authority.Valid() {
		return matched, diagnostics.ClaudeRegistrationAuthorityInvalid
	}
	hookProcess, _, err := claudeadapter.Process(os.Getpid())
	if err != nil {
		return matched, diagnostics.ClaudeRegistrationHookIdentity
	}
	replyTool, err := captureClaudeReplyToolPolicy(env)
	if err != nil {
		return matched, diagnostics.ClaudeRegistrationReplyToolPolicy
	}
	return claudeEndpointBootstrap{RegistryPath: registryPath, AgentUID: agent.Metadata.UID, PaneUID: paneUID, Generation: generation,
		Registration:                coremetadata.ClaudeRegistration{Authority: authority},
		PriorRegistrationGeneration: pane.Status.Activation.Claude.RegistrationGeneration,
		HookProcess:                 hookProcess,
		Socket:                      socket, Token: token, ReplyTool: replyTool, ProcessProof: proof}, claudeRegistrationProceed
}

// claudeEndpointHelperRoute is the internal route word of the per-agent
// messaging endpoint helper.
//
// It is a constant rather than three literals because two of its readers are
// not the dispatcher: this file spawns the helper by that word, and the
// whole-fleet process census names the helper's role from it. A rename that
// reached only the dispatcher would leave a live long-lived process
// unclassified without failing anything.
const claudeEndpointHelperRoute = "claude-endpoint-helper"

func startClaudeEndpointHelper(bootstrap claudeEndpointBootstrap) error {
	binary, err := os.Executable()
	if err != nil {
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperExecutable)
	}
	input, err := json.Marshal(bootstrap)
	if err != nil {
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperBootstrap)
	}
	readAck, writeAck, err := os.Pipe()
	if err != nil {
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperAckPipe)
	}
	defer readAck.Close()
	// #nosec G204 -- os.Executable above identifies this running Projmux binary;
	// the hidden route and argv are fixed, and bootstrap secrets use only stdin.
	cmd := exec.Command(binary, "internal", claudeEndpointHelperRoute)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.ExtraFiles = []*os.File{writeAck}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The official secret is transferred only in the private pipe, never in
	// helper argv or its inherited environment.
	cmd.Env = claudeHelperEnvironment(os.Environ())
	if err := cmd.Start(); err != nil {
		_ = writeAck.Close()
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperStartFailed)
	}
	_ = writeAck.Close()
	// Only an unconfirmed acknowledgement is a refusal. A failed Release after
	// the helper acknowledged still leaves an admitted helper that records
	// ready itself.
	if errors.Is(awaitClaudeHelperAdmission(readAck, time.Now().Add(3*time.Second), cmd.Process), errClaudeHelperAdmissionUnconfirmed) {
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationHelperUnconfirmed)
	}
	return nil
}

// errClaudeHelperAdmissionUnconfirmed is awaitClaudeHelperAdmission's refusal:
// no acknowledgement byte arrived before the deadline or EOF.
var errClaudeHelperAdmissionUnconfirmed = errors.New("claude helper admission unconfirmed")

// claudeHelperAck is the hook's read side of the helper's one-byte admission
// acknowledgement.
type claudeHelperAck interface {
	io.Reader
	SetReadDeadline(time.Time) error
}

// claudeHelperProcess is the started helper the hook lets go of.
type claudeHelperProcess interface {
	Release() error
}

// awaitClaudeHelperAdmission releases the helper however the wait ends. The
// hook's wait budget says nothing about whether the registration is valid:
// killing a helper still waiting on the Registry lock when the deadline passes
// stops it before it claims and records its registration.
// A released helper settles by itself. Its claim (Begin) and Record run in one
// transaction; Begin is a CAS on the registrationGeneration the hook observed
// and Record a CAS on the exact registrationGeneration, so a stale helper
// cannot overwrite a newer one. If the transaction fails, or the helper is
// stale, it exits without writing and its defers remove the lease socket,
// owner receipt, and coordination socket. Its lock wait is bounded by the
// Registry lock acquisition timeout (defaultLockTimeout in
// internal/integrations/metadata/store.go).
func awaitClaudeHelperAdmission(readAck claudeHelperAck, deadline time.Time, helper claudeHelperProcess) error {
	_ = readAck.SetReadDeadline(deadline)
	var ack [1]byte
	_, err := io.ReadFull(readAck, ack[:])
	if releaseErr := helper.Release(); err == nil && ack[0] == 1 {
		return releaseErr
	}
	return errClaudeHelperAdmissionUnconfirmed
}

func claudeHelperEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		if key != "CLAUDE_CODE_MESSAGING_SOCKET" && key != "CLAUDE_CODE_MESSAGING_TOKEN" {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func claudeHelperCredentialEnvironmentPresent(lookup func(string) (string, bool)) bool {
	for _, key := range []string{"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		if _, present := lookup(key); present {
			return true
		}
	}
	return false
}

// runClaudeEndpointHelper is the detached helper. It writes one ready record
// right after its acknowledgement byte, and one more record when it returns:
// the refusal that stopped it before Ready, or the end of its serving loop
// after Ready. That last record is appended only after the acknowledgement is
// closed, so the hook's EOF never waits on the journal.
func runClaudeEndpointHelper(args []string, recorder *diagnostics.ClaudeRegistrationRecorder) error {
	parentPID := os.Getppid()
	return recordClaudeEndpointHelper(recorder, claudeEndpointHelperInput{
		args: args, lookupEnv: os.LookupEnv, stdin: os.Stdin,
		openAck:  func() *os.File { return os.NewFile(3, "claude-endpoint-ack") },
		producer: func(bootstrap claudeEndpointBootstrap) bool { return claudeHelperProducerMatches(bootstrap, parentPID) },
		serve: func(bootstrap claudeEndpointBootstrap, ack io.Writer, admitted func()) diagnostics.ClaudeRegistrationReason {
			return serveClaudeRegistration(context.Background(), bootstrap, ack, claudeEndpointIdleOptions{
				stat: (*intmetadata.Store).RegistryFileIdentity, now: time.Now, floor: claudeEndpointIdleRegistryFloor, admitted: admitted})
		},
	})
}

// recordClaudeEndpointHelper is runClaudeEndpointHelper with the helper
// process's inputs passed in.
func recordClaudeEndpointHelper(recorder *diagnostics.ClaudeRegistrationRecorder, in claudeEndpointHelperInput) error {
	started := time.Now()
	record := func(subject claudeRegistrationSubject, reason diagnostics.ClaudeRegistrationReason) {
		recorder.Record(diagnostics.ClaudeRegistrationRecord{Source: diagnostics.ClaudeRegistrationSourceHelper, Reason: reason,
			Duration: time.Since(started), AgentUID: subject.AgentUID, PaneUID: subject.PaneUID})
	}
	subject, reason := claudeEndpointHelper(in, func(subject claudeRegistrationSubject) { record(subject, diagnostics.ClaudeRegistrationReady) })
	record(subject, reason)
	return nil
}

// claudeEndpointHelperInput is what one helper invocation reads. Outside tests
// it is the process's own argv, environment, fd 3, stdin, and parent.
type claudeEndpointHelperInput struct {
	args      []string
	lookupEnv func(string) (string, bool)
	stdin     io.Reader
	openAck   func() *os.File
	producer  func(claudeEndpointBootstrap) bool
	serve     func(bootstrap claudeEndpointBootstrap, ack io.Writer, admitted func()) diagnostics.ClaudeRegistrationReason
}

// claudeEndpointHelper returns the reason the helper stopped. It closes the
// acknowledgement before it returns on every path, so a caller that records
// the result appends only after the hook read its byte or EOF.
//
// The subject is set only once the producer check passed: the bootstrap then
// provably came from the live hook that matched this pane and its agent
// against the Registry, so its UIDs are those Registry-matched values.
func claudeEndpointHelper(in claudeEndpointHelperInput, admitted func(claudeRegistrationSubject)) (claudeRegistrationSubject, diagnostics.ClaudeRegistrationReason) {
	ack := in.openAck()
	if len(in.args) != 0 || claudeHelperCredentialEnvironmentPresent(in.lookupEnv) {
		// Never inspected or written, fd 3 is still let go of before the
		// caller records, like on every other path.
		if ack != nil {
			_ = ack.Close()
		}
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationHelperArguments
	}
	if ack == nil {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationHelperAckMissing
	}
	defer ack.Close()
	info, err := ack.Stat()
	if err != nil {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationHelperAckMissing
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationHelperAckNotPipe
	}
	var bootstrap claudeEndpointBootstrap
	data, err := io.ReadAll(io.LimitReader(in.stdin, 64*1024+1))
	if err != nil || len(data) > 64*1024 || json.Unmarshal(data, &bootstrap) != nil {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationHelperInput
	}
	if !in.producer(bootstrap) {
		return claudeRegistrationSubject{}, diagnostics.ClaudeRegistrationProducerMismatch
	}
	subject := claudeRegistrationSubject{AgentUID: bootstrap.AgentUID, PaneUID: bootstrap.PaneUID}
	reason := in.serve(bootstrap, ack, func() { admitted(subject) })
	_ = ack.Close()
	return subject, reason
}

func claudeHelperProducerMatches(bootstrap claudeEndpointBootstrap, parentPID int) bool {
	hook, providerPID, err := claudeadapter.Process(parentPID)
	if err != nil || hook != bootstrap.HookProcess || providerPID != bootstrap.Registration.Authority.Process.PID {
		return false
	}
	provider, _, err := claudeadapter.Process(providerPID)
	return err == nil && provider == bootstrap.Registration.Authority.Process
}

type claudeSocketIdentity struct {
	device uint64
	inode  uint64
	owner  uint32
	mode   os.FileMode
}

func inspectClaudeSocket(path string) (claudeSocketIdentity, error) {
	refused := errors.New("claude socket is unavailable")
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return claudeSocketIdentity{}, refused
	}
	var first claudeSocketIdentity
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || (info.Mode()&os.ModeSymlink != 0 && !trustedDarwinTempAlias(current)) {
			return claudeSocketIdentity{}, refused
		}
		if current == path {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || int64(stat.Uid) != int64(os.Getuid()) {
				return claudeSocketIdentity{}, refused
			}
			first = claudeSocketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, mode: info.Mode()}
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return claudeSocketIdentity{}, refused
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || int64(stat.Uid) != int64(os.Getuid()) {
		return claudeSocketIdentity{}, refused
	}
	last := claudeSocketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, mode: info.Mode()}
	if last != first {
		return claudeSocketIdentity{}, refused
	}
	return last, nil
}

func trustedDarwinTempAlias(path string) bool {
	if runtime.GOOS != "darwin" || (path != "/tmp" && path != "/var") {
		return false
	}
	target, err := os.Readlink(path)
	return err == nil && (target == "/private"+path || target == "private"+path)
}

// This is Projmux's own private readiness socket, never the provider address.
// It can be derived from nonsecret exact registration identity without storing
// either socket path in Registry or printing it in a capability projection.
func claudeActivationLeaseDir(registryPath, paneUID, generation string) string {
	digest := sha256.Sum256([]byte(registryPath + "\x00" + paneUID + "\x00" + generation))
	// Both supported operating systems provide /tmp. A fixed short root avoids
	// Darwin's long per-user TMPDIR exceeding sun_path and makes the creator,
	// hook, supervisor, and read-only client independent of inherited TMPDIR.
	return filepath.Join("/tmp", "pmx-ce-"+hex.EncodeToString(digest[:16]))
}

func claudeLeaseSocket(registryPath, paneUID, generation, registrationGeneration string) string {
	digest := sha256.Sum256([]byte(registrationGeneration))
	return filepath.Join(claudeActivationLeaseDir(registryPath, paneUID, generation), hex.EncodeToString(digest[:16])+".sock")
}

func privateClaudeLeaseDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Getuid())
}

// The supervisor knows the exact activation, so after reaping its child it can
// remove orphaned helper sockets even if a helper was killed without defers.
// This never reads or waits on Registry, touches a provider socket, or changes
// the termination journal contract. Normal Registry convergence clears the
// nonsecret registration when it consumes that exact supervisor receipt.
func cleanupClaudeActivationLeases(spec superviseSpec) {
	if spec.AgentUID == "" || spec.Generation == "" || exactActivationRegistryPath(spec.RegistryPath) != nil {
		return
	}
	dir := claudeActivationLeaseDir(spec.RegistryPath, spec.PaneUID, spec.Generation)
	if !privateClaudeLeaseDir(dir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".sock.json") {
			path := filepath.Join(dir, name)
			if receipt, ok := readClaudeLeaseOwner(path, spec); ok && path == claudeLeaseSocket(spec.RegistryPath, spec.PaneUID, spec.Generation, receipt.Authority.RegistrationGeneration)+".json" {
				target := claudeCoordinationTarget{AgentUID: receipt.AgentUID, PaneUID: receipt.PaneUID, Generation: receipt.Generation,
					Provider: aiModeClaude, Authority: receipt.Authority}
				if coordination := claudeCoordinationSocket(spec.RegistryPath, target); filepath.Dir(coordination) == dir {
					_ = localipc.RemoveOwnedSocket(coordination, receipt.CoordinationSocket)
				}
				_ = os.Remove(path)
			}
			continue
		}
		if len(name) != 37 || !strings.HasSuffix(name, ".sock") {
			continue
		}
		digest := strings.TrimSuffix(name, ".sock")
		if _, err := hex.DecodeString(digest); err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := inspectClaudeSocket(path); err == nil {
			_ = os.Remove(path)
		}
	}
	_ = os.Remove(dir)
}

// claudeEndpointIdleOptions carries the idle Registry gate's inputs. Only the
// accept-loop tick consumes stat, now, and floor; delivery-time checks never do.
type claudeEndpointIdleOptions struct {
	stat  func(*intmetadata.Store) (intmetadata.RegistryFileIdentity, error)
	now   func() time.Time
	floor time.Duration
	// poster, when set, receives the helper's provider poster. Tests only.
	poster func(*liveClaudeProviderPoster)
	// admitted, when set, runs once right after the acknowledgement byte is
	// written. The helper records ready there.
	admitted func()
}

// claudeEndpointIdleRegistryGate decides whether one idle accept-loop tick
// reloads the Registry. The accept-loop goroutine owns it alone; every
// delivery-time check keeps calling the full current closure.
type claudeEndpointIdleRegistryGate struct {
	stat      func() (intmetadata.RegistryFileIdentity, error)
	now       func() time.Time
	floor     time.Duration
	evaluated bool
	identity  intmetadata.RegistryFileIdentity
	at        time.Time
}

// current runs the identity part on every tick and the Registry part only when
// the Registry stat identity changed, the stat failed, or the floor elapsed.
func (g *claudeEndpointIdleRegistryGate) current(identity, registry func() bool) bool {
	if !identity() {
		return false
	}
	// The stat is captured before the load, so a write racing the load leaves a
	// newer identity behind and forces another evaluation on the next tick.
	observed, err := g.stat()
	now := g.now()
	if g.evaluated && err == nil && observed == g.identity && now.Sub(g.at) < g.floor {
		return true
	}
	g.evaluated, g.identity, g.at = err == nil, observed, now
	return registry()
}

// serveClaudeRegistration claims, records, and serves one registration, and
// returns the reason it stopped: a refusal before Ready, or an ended-* reason
// after it.
func serveClaudeRegistration(ctx context.Context, bootstrap claudeEndpointBootstrap, ack io.Writer, idle claudeEndpointIdleOptions) diagnostics.ClaudeRegistrationReason {
	if exactActivationRegistryPath(bootstrap.RegistryPath) != nil || bootstrap.Token == "" {
		return diagnostics.ClaudeRegistrationBootstrapInvalid
	}
	resolveRoute := coremetadata.ResolveAgentRoute
	if bootstrap.ProcessProof != nil {
		if !checkClaudeProcessHost(*bootstrap.ProcessProof, false) {
			return diagnostics.ClaudeRegistrationProviderProcessMismatch
		}
		resolveRoute = processClaudeRouteResolver(bootstrap.RegistryPath, *bootstrap.ProcessProof)
	}
	process, _, err := claudeadapter.Process(os.Getpid())
	if err != nil {
		return diagnostics.ClaudeRegistrationHelperIdentity
	}
	bootstrap.Registration.Authority.LeaseProcess = process
	if !bootstrap.Registration.Authority.Valid() {
		return diagnostics.ClaudeRegistrationAuthorityInvalid
	}
	socketIdentity, err := inspectClaudeSocket(bootstrap.Socket)
	if err != nil {
		return diagnostics.ClaudeRegistrationMessagingSocket
	}
	leasePath := claudeLeaseSocket(bootstrap.RegistryPath, bootstrap.PaneUID, bootstrap.Generation, bootstrap.Registration.Authority.RegistrationGeneration)
	if err := os.Mkdir(filepath.Dir(leasePath), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return diagnostics.ClaudeRegistrationLeaseUnavailable
	}
	if !privateClaudeLeaseDir(filepath.Dir(leasePath)) {
		return diagnostics.ClaudeRegistrationLeaseUnavailable
	}
	defer os.Remove(filepath.Dir(leasePath))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: leasePath, Net: "unix"})
	if err != nil {
		return diagnostics.ClaudeRegistrationLeaseUnavailable
	}
	defer listener.Close()
	listener.SetUnlinkOnClose(false)
	ownedLease, _ := os.Lstat(leasePath)
	defer func() {
		if current, err := os.Lstat(leasePath); err == nil && ownedLease != nil && os.SameFile(ownedLease, current) {
			_ = os.Remove(leasePath)
		}
	}()
	if os.Chmod(leasePath, 0o600) != nil {
		return diagnostics.ClaudeRegistrationLeaseUnavailable
	}
	leaseIdentity, err := inspectClaudeSocket(leasePath)
	if err != nil {
		return diagnostics.ClaudeRegistrationLeaseUnavailable
	}
	coordinationTarget := claudeCoordinationTarget{AgentUID: bootstrap.AgentUID, PaneUID: bootstrap.PaneUID,
		Generation: bootstrap.Generation, Provider: aiModeClaude, Authority: bootstrap.Registration.Authority}
	coordinationListener, err := localipc.Listen(claudeCoordinationSocket(bootstrap.RegistryPath, coordinationTarget))
	if err != nil {
		return diagnostics.ClaudeRegistrationCoordinationListener
	}
	coordinationListenerOwned := true
	defer func() {
		if coordinationListenerOwned {
			_ = coordinationListener.Close()
		}
	}()
	coordinationIdentity := coordinationListener.Identity()
	if writeClaudeLeaseOwner(leasePath+".json", bootstrap, coordinationIdentity) != nil {
		return diagnostics.ClaudeRegistrationLeaseOwnerUnavailable
	}
	defer os.Remove(leasePath + ".json")
	store := intmetadata.NewStore(bootstrap.RegistryPath)
	mutator := intmetadata.DefaultMutator()
	entered := false
	_, _, err = store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		entered = true
		return admitClaudeRegistration(reg, bootstrap, mutator)
	})
	if err != nil {
		return claudeRegistrationTransactionReason(err, entered)
	}
	defer func() {
		_, _, _ = store.UpdateConvergent(func(reg *coremetadata.Registry) error {
			mutator.ClearClaudeRegistration(reg, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, bootstrap.Registration.Authority)
			return nil
		})
	}()
	initial, err := store.LoadDegradedReadOnly()
	if err != nil {
		return diagnostics.ClaudeRegistrationRegistryUnreadable
	}
	expectedRoute, routeReason := resolveRoute(initial, bootstrap.AgentUID)
	if routeReason != "" || !coordinationTarget.matches(expectedRoute) {
		return diagnostics.ClaudeRegistrationRouteMismatch
	}
	identityCurrent := func() bool {
		if bootstrap.ProcessProof != nil && !checkClaudeProcessHost(*bootstrap.ProcessProof, false) {
			return false
		}
		if observed, err := inspectClaudeSocket(leasePath); err != nil || observed != leaseIdentity {
			return false
		}
		if observed, err := localipc.InspectOwnedSocket(coordinationListener.Path); err != nil || observed != coordinationIdentity {
			return false
		}
		actual, _, err := claudeadapter.Process(bootstrap.Registration.Authority.Process.PID)
		if err != nil || actual != bootstrap.Registration.Authority.Process {
			return false
		}
		observed, err := inspectClaudeSocket(bootstrap.Socket)
		if err != nil || observed != socketIdentity {
			return false
		}
		return true
	}
	registryCurrent := func() bool {
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			return false
		}
		route, reason := resolveRoute(reg, bootstrap.AgentUID)
		authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
		return reason == "" && ok && route.Same(expectedRoute) && route.PaneUID == bootstrap.PaneUID && route.Generation == bootstrap.Generation && authority == bootstrap.Registration.Authority
	}
	current := func() bool {
		return identityCurrent() && registryCurrent()
	}
	idleRegistry := claudeEndpointIdleRegistryGate{stat: func() (intmetadata.RegistryFileIdentity, error) { return idle.stat(store) },
		now: idle.now, floor: idle.floor}
	dialogueBroker, err := newLiveClaudeDialogueBroker(bootstrap.RegistryPath)
	if err != nil {
		return diagnostics.ClaudeRegistrationDialogueBroker
	}
	if bootstrap.ProcessProof != nil {
		dialogueBroker.resolveRoute = resolveRoute
	}
	providerPoster := &liveClaudeProviderPoster{socket: bootstrap.Socket, token: bootstrap.Token,
		socketIdentity: socketIdentity, process: bootstrap.Registration.Authority.Process, current: current}
	if idle.poster != nil {
		idle.poster(providerPoster)
	}
	var replyTool *claudeReplyToolGate
	if bootstrap.ReplyTool != nil {
		replyTool, err = newClaudeReplyToolGate(*bootstrap.ReplyTool)
		if err != nil {
			return diagnostics.ClaudeRegistrationReplyToolGate
		}
	}
	coordination := startClaudeCoordinationServerWithPoster(coordinationListener, expectedRoute, current, dialogueBroker, providerPoster, replyTool)
	coordinationListenerOwned = false
	defer coordination.Close()
	if !current() {
		return diagnostics.ClaudeRegistrationStaleBeforeAck
	}
	// Record already succeeded, so a failed write means only that the hook
	// stopped waiting (Claude Code ends it at its hook timeout). Exiting here
	// would clear the Ready registration this helper just recorded and leave a
	// live Claude registered and then lost, so the helper keeps serving.
	_, _ = ack.Write([]byte{1})
	if idle.admitted != nil {
		idle.admitted()
	}
	for {
		if ctx.Err() != nil {
			return diagnostics.ClaudeRegistrationEndedContextDone
		}
		if !idleRegistry.current(identityCurrent, registryCurrent) {
			return diagnostics.ClaudeRegistrationEndedNotCurrent
		}
		_ = listener.SetDeadline(time.Now().Add(claudeEndpointPollInterval))
		connection, err := listener.AcceptUnix()
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			return diagnostics.ClaudeRegistrationEndedAcceptFailed
		}
		answerClaudeLeaseReadiness(connection, current, time.Now)
	}
}

// admitClaudeRegistration is the helper's one claim-and-record transaction.
// Begin and Record commit together, so no helper ever publishes a claimed
// registration without Ready. Every refusal is a *claudeRegistrationRefusal,
// which the Store hands back unchanged.
func admitClaudeRegistration(reg *coremetadata.Registry, bootstrap claudeEndpointBootstrap, mutator coremetadata.Mutator) error {
	authority := bootstrap.Registration.Authority
	if actual, _, err := claudeadapter.Process(authority.Process.PID); err != nil || actual != authority.Process {
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationProviderProcessGone)
	}
	if err := mutator.BeginClaudeRegistrationAfter(reg, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation,
		bootstrap.PriorRegistrationGeneration, authority); err != nil {
		return refuseClaudeRegistration(claudeRegistrationClaimRefusal(reg, bootstrap, mutator))
	}
	if err := mutator.RecordClaudeRegistration(reg, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, bootstrap.Registration); err != nil {
		// Begin just claimed, or found already claimed, this exact generation
		// and session, so Record can refuse only a different lease recorded
		// under them.
		return refuseClaudeRegistration(diagnostics.ClaudeRegistrationClaimRefusedCompeting)
	}
	return nil
}

// claudeRegistrationClaimRefusal names why BeginClaudeRegistrationAfter
// refused, from the Registry exactly as that refused claim saw it. The
// unconditional claim shares its target check and nothing else, so it failing
// on a copy means the activation itself is no longer this helper's; otherwise
// the registrationGeneration moved: to this helper's own generation under a
// different session (competing), or to another one (newer).
func claudeRegistrationClaimRefusal(reg *coremetadata.Registry, bootstrap claudeEndpointBootstrap, mutator coremetadata.Mutator) diagnostics.ClaudeRegistrationReason {
	authority := bootstrap.Registration.Authority
	probe := reg.Clone()
	if mutator.BeginClaudeRegistration(&probe, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, authority) != nil {
		return diagnostics.ClaudeRegistrationClaimRefusedActivation
	}
	if pane, _ := reg.Pane(bootstrap.PaneUID); pane.Status.Activation.Claude.RegistrationGeneration == authority.RegistrationGeneration {
		return diagnostics.ClaudeRegistrationClaimRefusedCompeting
	}
	return diagnostics.ClaudeRegistrationClaimRefusedNewer
}

// claudeRegistrationTransactionReason classifies a failed claim-and-record
// transaction. The callback's own refusal comes back unchanged. entered says
// whether the Store ran the callback: once it did, only the Store's
// validation and durable write remain to fail; before it, the lock or the
// degraded-Registry gate refused.
//
// lock-acquire-failed is the one fallback: the callback never ran and the
// Store error is neither a lock timeout nor a degraded Registry. That is a
// failed lock acquisition, or a locked Registry read or recovery inspection
// failure the Store did not classify as degraded.
func claudeRegistrationTransactionReason(err error, entered bool) diagnostics.ClaudeRegistrationReason {
	var refusal *claudeRegistrationRefusal
	switch {
	case errors.As(err, &refusal):
		return refusal.reason
	case entered:
		return diagnostics.ClaudeRegistrationRegistryWriteFailed
	case errors.Is(err, intmetadata.ErrLockTimeout):
		return diagnostics.ClaudeRegistrationLockTimeout
	case errors.Is(err, intmetadata.ErrRegistryDegraded), errors.Is(err, intmetadata.ErrMalformedRegistry),
		errors.Is(err, intmetadata.ErrRegistryStateLost), errors.Is(err, intmetadata.ErrRegistryPermission):
		return diagnostics.ClaudeRegistrationRegistryDegraded
	default:
		return diagnostics.ClaudeRegistrationLockAcquireFailed
	}
}

// claudeLeaseReadinessConn is the part of an accepted lease connection the
// readiness answer uses.
type claudeLeaseReadinessConn interface {
	SetWriteDeadline(time.Time) error
	Write([]byte) (int, error)
	Close() error
}

// answerClaudeLeaseReadiness evaluates current() before arming any deadline, so
// a slow check delays the answer instead of cancelling it. A connection closed
// without the byte therefore means the helper judged its lease not current.
func answerClaudeLeaseReadiness(connection claudeLeaseReadinessConn, current func() bool, now func() time.Time) {
	defer connection.Close()
	if !current() {
		return
	}
	_ = connection.SetWriteDeadline(now().Add(claudeEndpointReadinessWriteDeadline))
	_, _ = connection.Write([]byte{1})
}

// claudeProbeOutcome separates a peer that did not answer within a client bound
// from one whose lease is actually stale or absent, so a refusal can say which.
type claudeProbeOutcome uint8

const (
	claudeProbeReady claudeProbeOutcome = iota
	// claudeProbeStale: identity, socket, or peer checks failed, nothing listened,
	// the helper closed without answering, or it answered not current.
	claudeProbeStale
	// claudeProbeUnanswered: a live helper did not answer within a client bound.
	claudeProbeUnanswered
	// claudeProbeUnqualified: the helper answered that coordination is not qualified.
	claudeProbeUnqualified
)

// claudeProbeWaitOutcome classifies a failed bounded wait: only an expired bound
// is unanswered; EOF, refusal, and every other error are stale.
func claudeProbeWaitOutcome(err error) claudeProbeOutcome {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return claudeProbeUnanswered
	}
	return claudeProbeStale
}

func probeClaudeRegistrationLease(registryPath string, route coremetadata.AgentRouteRef) bool {
	return classifyClaudeRegistrationLease(registryPath, route) == claudeProbeReady
}

// classifyClaudeRegistrationLease checks process identity and sockets before any
// bounded wait, so a dead peer is refused as stale without spending a bound.
func classifyClaudeRegistrationLease(registryPath string, route coremetadata.AgentRouteRef) claudeProbeOutcome {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !authority.Valid() {
		return claudeProbeStale
	}
	for _, expected := range []coremetadata.ProcessIdentity{authority.Process, authority.LeaseProcess} {
		actual, _, err := claudeadapter.Process(expected.PID)
		if err != nil || actual != expected {
			return claudeProbeStale
		}
	}
	path := claudeLeaseSocket(registryPath, route.PaneUID, route.Generation, authority.RegistrationGeneration)
	if _, err := inspectClaudeSocket(path); err != nil {
		return claudeProbeStale
	}
	// Dial only Projmux's readiness helper. Never connect to the provider inbox;
	// its secret path exists only in serveClaudeRegistration's private memory.
	connection, err := net.DialTimeout("unix", path, claudeLeaseDialTimeout)
	if err != nil {
		return claudeProbeWaitOutcome(err)
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return claudeProbeStale
	}
	peer, err := claudeadapter.PeerProcess(unixConnection)
	if err != nil || peer != authority.LeaseProcess {
		return claudeProbeStale
	}
	_ = connection.SetDeadline(time.Now().Add(claudeLeaseReadinessReadTimeout))
	var ready [1]byte
	if _, err := io.ReadFull(connection, ready[:]); err != nil {
		return claudeProbeWaitOutcome(err)
	}
	if ready[0] != 1 {
		return claudeProbeStale
	}
	target, ok := claudeTargetForRoute(route)
	if !ok {
		return claudeProbeStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeLeaseCoordinationProbeTimeout)
	defer cancel()
	response, err := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "probe", Target: target,
	})
	if err != nil {
		return claudeCoordinationCallOutcome(ctx)
	}
	if response.Kind != "ready" {
		return claudeProbeStale
	}
	return claudeProbeReady
}

// claudeCoordinationCallOutcome classifies a failed coordination call by its own
// bound: callClaudeCoordination arms the connection with the context deadline.
func claudeCoordinationCallOutcome(ctx context.Context) claudeProbeOutcome {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return claudeProbeUnanswered
	}
	return claudeProbeStale
}

func probeClaudeCoordinationEligibility(registryPath string, route coremetadata.AgentRouteRef) bool {
	return classifyClaudeCoordinationEligibility(registryPath, route) == claudeProbeReady
}

// classifyClaudeCoordinationEligibility reports the lease probe's own outcome
// when it fails, so a lease failure is never reported as unqualified.
func classifyClaudeCoordinationEligibility(registryPath string, route coremetadata.AgentRouteRef) claudeProbeOutcome {
	if lease := classifyClaudeRegistrationLease(registryPath, route); lease != claudeProbeReady {
		return lease
	}
	target, ok := claudeTargetForRoute(route)
	if !ok {
		return claudeProbeStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeCoordinationEligibilityTimeout)
	defer cancel()
	response, err := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "eligibility", Target: target,
	})
	if err != nil {
		return claudeCoordinationCallOutcome(ctx)
	}
	if response.Kind == "stale" {
		return claudeProbeStale
	}
	if response.Kind == "qualified" && response.ProviderVersion == claudeFrozenFrameProviderVersion &&
		response.Reason == "exact-public-init-and-explicit-reply" && !response.AutoResend && !response.Ambiguous {
		return claudeProbeReady
	}
	return claudeProbeUnqualified
}
