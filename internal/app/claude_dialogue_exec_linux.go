package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"golang.org/x/sys/unix"
)

func runClaudeDialogueExec(args []string) (runErr error) {
	if len(args) < 2 || args[0] != "--" || !filepath.IsAbs(args[1]) || os.Getenv(internalClaudeReplyGuardEnv) != "1" {
		return errClaudeReplyTool
	}
	registryPath := os.Getenv(internalClaudeRegistryPathEnv)
	if exactActivationRegistryPath(registryPath) != nil {
		return errClaudeReplyTool
	}
	registry, err := intmetadata.NewStore(registryPath).LoadDegradedReadOnly()
	if err != nil {
		return errClaudeReplyTool
	}
	pane, ok := registry.Pane(os.Getenv(internalActivationPaneUIDEnv))
	if !ok || pane.Status.Activation.Claude == nil {
		return errClaudeReplyTool
	}
	activation := pane.Status.Activation
	process, _, err := localipc.Process(os.Getpid())
	if err != nil || activation.Claude.Process != process || activation.Generation != os.Getenv(internalActivationGenerationEnv) {
		return errClaudeReplyTool
	}
	spec := superviseSpec{PaneUID: pane.Metadata.UID, AgentUID: activation.AgentUID, Generation: activation.Generation, RegistryPath: registryPath, DialogueReplyOnly: true}
	candidate, err := os.Executable()
	if err != nil {
		return errClaudeReplyTool
	}
	profile, err := createClaudeDialogueProfile(spec, candidate)
	if err != nil {
		return err
	}
	profileInfo, err := os.Lstat(profile)
	if err != nil {
		return errClaudeReplyTool
	}
	defer func() {
		if runErr != nil {
			_ = removeClaudeDialogueProfileFiles(profile, profileInfo, true)
		}
	}()
	if profile != os.Getenv(internalClaudeDialogueProfileEnv) {
		return errClaudeReplyTool
	}
	input, keepalive, err := os.Pipe()
	if err != nil {
		return err
	}
	defer input.Close()
	defer keepalive.Close()
	output, providerOutput, err := os.Pipe()
	if err != nil {
		return err
	}
	defer output.Close()
	defer providerOutput.Close()
	diagnostics, providerDiagnostics, err := os.Pipe()
	if err != nil {
		return err
	}
	defer diagnostics.Close()
	defer providerDiagnostics.Close()
	initial := []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"Reply READY.\"}}\n")
	if _, err := keepalive.Write(initial); err != nil {
		return err
	}
	// Only the owned candidate observer inherits these extra descriptors. The
	// vendor receives ordinary stdin/stdout/stderr, never an extra-FD contract.
	observer := exec.Command(candidate, "internal", "claude-dialogue-observe", profile) // #nosec G204 -- own os.Executable image and private generation profile validated before Start; fixed observer argv, no model/shell input.
	observer.Stdin = output
	observer.Stdout = os.Stdout
	observer.Stderr = os.Stderr
	observer.ExtraFiles = []*os.File{diagnostics, keepalive, os.Stdin}
	policy, err := captureClaudeReplyToolPolicy(os.Getenv)
	if err != nil || policy == nil {
		return errClaudeReplyTool
	}
	observer.Env = append(policy.Environment, internalClaudeReplyGuardEnv+"=1", internalClaudeDialogueProfileEnv+"="+profile)
	if err := observer.Start(); err != nil {
		return err
	}
	defer func() {
		if runErr != nil {
			_ = observer.Process.Kill()
			_ = observer.Wait()
		}
	}()
	identity, _, err := localipc.Process(observer.Process.Pid)
	if err != nil {
		return errClaudeReplyTool
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(profile, "observer.json"), append(body, '\n'), 0o600); err != nil {
		return err
	}
	native := claudeDialogueNativeArgs(args[1:], profile)
	environment := claudeDialogueNativeEnvironment(os.Environ(), profile)
	if err := unix.Dup2(int(input.Fd()), 0); err != nil {
		return err
	}
	if err := unix.Dup2(int(providerOutput.Fd()), 1); err != nil {
		return err
	}
	if err := unix.Dup2(int(providerDiagnostics.Fd()), 2); err != nil {
		return err
	}
	// The existing public provider launch resolves this executable/argv. There
	// is no shell interpretation here, and the admitted provider PID is retained.
	return unix.Exec(native[0], native, environment)
}

func runClaudeDialogueObserver(args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != os.Getenv(internalClaudeDialogueProfileEnv) {
		return errClaudeReplyTool
	}
	candidate, err := os.Executable()
	if err != nil {
		return errClaudeReplyTool
	}
	profile, err := readClaudeDialogueProfile(args[0], candidate)
	if err != nil {
		return err
	}
	stream := claudeDialogueStream{candidate: candidate}
	var route coremetadata.AgentRouteRef
	lookup := func() error {
		registryPath := os.Getenv(internalClaudeRegistryPathEnv)
		if exactActivationRegistryPath(registryPath) != nil {
			return errClaudeReplyTool
		}
		registry, err := intmetadata.NewStore(registryPath).LoadDegradedReadOnly()
		if err != nil {
			return errClaudeReplyTool
		}
		current, reason := coremetadata.ResolveAgentRoute(registry, profile.AgentUID)
		if reason != "" || current.PaneUID != profile.PaneUID || current.Generation != profile.Generation {
			return errClaudeReplyTool
		}
		authority, ok := current.Authority().(coremetadata.ClaudeAuthorityRef)
		parent, _, err := localipc.Process(os.Getppid())
		if !ok || err != nil || parent != authority.Process || authority.SessionID != stream.session {
			return errClaudeReplyTool
		}
		route = current
		return nil
	}
	send := func(observation *claudeDialogueObservation) error {
		if err := lookup(); err != nil {
			return err
		}
		target, ok := claudeTargetForRoute(route)
		if !ok {
			return errClaudeReplyTool
		}
		ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
		defer cancel()
		response, err := callClaudeCoordination(ctx, os.Getenv(internalClaudeRegistryPathEnv), route, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "profile-observe", Target: target, Observation: observation})
		if err != nil || response.Kind != "profile-observed" {
			return errClaudeReplyTool
		}
		return nil
	}
	defer func() {
		if stream.session != "" {
			_ = send(&claudeDialogueObservation{Kind: "invalid", SessionID: stream.session})
		}
	}()
	keepalive := os.NewFile(4, "reply-only-input-lifetime")
	if keepalive == nil {
		return errClaudeReplyTool
	}
	defer keepalive.Close()
	return consumeClaudeDialoguePipes(0, 3, 5, keepalive, func(line []byte) error {
		observation, err := stream.inspect(line)
		if err != nil {
			return err
		}
		if observation == nil {
			return nil
		}
		if err := send(observation); err != nil {
			return err
		}
		if observation.Kind == "ready" {
			_, err = fmt.Fprintln(stdout, "Claude reply-only activation is ready for explicit qualification. Ctrl-D finishes this activation.")
		}
		return err
	})
}

func consumeClaudeDialoguePipes(outputFD, diagnosticsFD, terminalFD int32, keepalive io.Closer, inspect func([]byte) error) error {
	// Poll prioritizes stderr before publishing a ready assertion from stdout.
	// The terminal is consumed only as an EOF signal: its bytes are discarded.
	fds := []unix.PollFd{{Fd: outputFD, Events: unix.POLLIN | unix.POLLHUP}, {Fd: diagnosticsFD, Events: unix.POLLIN | unix.POLLHUP}, {Fd: terminalFD, Events: unix.POLLIN | unix.POLLHUP}}
	var pending []byte
	buffer := make([]byte, 8192)
	for {
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return errClaudeDialogueStream
		}
		if fds[1].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			if fds[0].Revents&unix.POLLHUP != 0 {
				return nil
			}
			return errClaudeDialogueStream
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 || (fds[2].Fd >= 0 && fds[2].Revents&(unix.POLLERR|unix.POLLNVAL) != 0) {
			return errClaudeDialogueStream
		}
		if fds[1].Revents&unix.POLLIN != 0 {
			n, err := unix.Read(int(diagnosticsFD), buffer)
			if err != nil || n > 0 {
				return errClaudeDialogueStream
			}
		}
		if fds[2].Fd >= 0 && fds[2].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
			n, err := unix.Read(int(terminalFD), buffer)
			if err != nil || n == 0 {
				_ = keepalive.Close()
				fds[2].Fd = -1
			}
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
			continue
		}
		n, err := unix.Read(int(outputFD), buffer)
		if err != nil {
			return errClaudeDialogueStream
		}
		if n == 0 {
			if len(pending) != 0 {
				return errClaudeDialogueStream
			}
			return nil
		}
		pending = append(pending, buffer[:n]...)
		if len(pending) > 1024*1024 {
			return errClaudeDialogueStream
		}
		for {
			end := bytes.IndexByte(pending, '\n')
			if end < 0 {
				break
			}
			if err := inspect(pending[:end]); err != nil {
				return err
			}
			pending = pending[end+1:]
		}
	}
}

func cleanupClaudeDialogueProfile(spec superviseSpec) error {
	if !spec.DialogueReplyOnly {
		return nil
	}
	directory, err := claudeDialogueProfilePath(spec)
	if err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || !localipc.OwnedByCurrentUser(info) {
		return errClaudeReplyTool
	}
	identity, err := readClaudeDialogueObserver(directory)
	if err != nil {
		return err
	}
	if err := waitClaudeDialogueObserverExit(identity, 20*time.Second); err != nil {
		return err
	}
	return removeClaudeDialogueProfileFiles(directory, info, false)
}

// Zombie state still has an exact birth and an exit-ready pidfd. A generic
// liveness error cannot distinguish that from EACCES or a replaced PID.
func claudeDialogueProcessBirth(pid int) (coremetadata.ProcessIdentity, error) {
	if pid <= 1 {
		return coremetadata.ProcessIdentity{}, errClaudeReplyTool
	}
	path := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path) // #nosec G304 -- /proc/<validated integer PID>/stat only; captures an exact owned writer birth, never model path input.
	if err != nil {
		return coremetadata.ProcessIdentity{}, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return coremetadata.ProcessIdentity{}, errClaudeReplyTool
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return coremetadata.ProcessIdentity{}, errClaudeReplyTool
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return coremetadata.ProcessIdentity{}, errClaudeReplyTool
	}
	// Linux changes ownership of several /proc files to root after exit.
	// The process real UID in status remains available for a zombie, whereas
	// a file-owner mismatch alone would lose an already captured writer.
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return coremetadata.ProcessIdentity{}, err
	}
	var ownerUID uint64
	ownerFound := false
	for line := range strings.SplitSeq(string(status), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		ids := strings.Fields(line)
		if len(ids) != 5 {
			return coremetadata.ProcessIdentity{}, errClaudeReplyTool
		}
		ownerUID, err = strconv.ParseUint(ids[1], 10, 32)
		if err != nil {
			return coremetadata.ProcessIdentity{}, errClaudeReplyTool
		}
		ownerFound = true
	}
	if !ownerFound {
		return coremetadata.ProcessIdentity{}, errClaudeReplyTool
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return coremetadata.ProcessIdentity{}, err
	}
	return coremetadata.ProcessIdentity{PID: pid, OwnerUID: uint32(ownerUID), Start: "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19]}, nil
}

func waitClaudeDialogueObserverExit(identity coremetadata.ProcessIdentity, timeout time.Duration) error {
	if !identity.Valid() || timeout <= 0 || timeout > 20*time.Second {
		return errClaudeReplyTool
	}
	before, err := claudeDialogueProcessBirth(identity.PID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || before != identity {
		return errClaudeReplyTool
	}
	fd, err := unix.PidfdOpen(identity.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return errClaudeReplyTool
	}
	defer unix.Close(fd)
	after, err := claudeDialogueProcessBirth(identity.PID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errClaudeReplyTool
	}
	if err == nil && after != identity {
		return errClaudeReplyTool
	}
	if fd < 0 || fd > 1<<31-1 {
		return errClaudeReplyTool
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("reply-only observer exit deadline; profile retained")
		}
		count, err := unix.Poll(poll, int((remaining+time.Millisecond-1)/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || count != 1 || poll[0].Revents&unix.POLLIN == 0 || poll[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return errors.New("reply-only observer exit was not proven; profile retained")
		}
		return nil
	}
}

func claudeDialogueNativeArgs(original []string, profile string) []string {
	native := append([]string{}, original...)
	native = append(native, "--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json",
		"--restricted", "--no-chrome", "--disable-slash-commands", "--prompt-suggestions", "false",
		"--tools", "Bash", "--permission-mode", "dontAsk", "--setting-sources", "", "--settings", filepath.Join(profile, "settings.json"),
		"--strict-mcp-config", "--mcp-config", filepath.Join(profile, "mcp.json"))
	return native
}
