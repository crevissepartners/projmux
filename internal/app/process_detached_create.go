package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const processDetachedStartupTimeout = 30 * time.Second
const processDetachedWireLimit = 2 << 20
const processDetachedLogLimit = 64 << 10

// Only the fixed internal route is argv. The request (including the initial
// task) is an unlinked private file; the handshake is a private duplex fd.
// The intermediate launcher never owns a provider and exits before success.
type processDetachedRequest struct {
	Options      processAgentCreateOptions
	Output       cli.OutputMode
	Caller       coremetadata.ProcessIdentity
	RegistryPath string
}
type processDetachedReply struct {
	Owner                 coremetadata.ProcessIdentity
	Binding               processhost.Binding
	Stdout, Stderr, Error string
	Phase                 string
}

func (c *createCommand) runDetachedProcessCreate(request processAgentCreateRequest, mode cli.OutputMode, stdout, stderr io.Writer) error {
	ctx, cancel := processForegroundLifetime()
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, processDetachedStartupTimeout)
	defer timeout()
	stateDir, err := c.store.stateDir()
	if err != nil {
		return err
	}
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	wire := processDetachedRequest{Options: request.options, Output: mode, Caller: caller, RegistryPath: intmetadata.PathFor(stateDir)}
	return launchProcessDetached(ctx, executable, wire, c, stdout, stderr)
}

func launchProcessDetached(ctx context.Context, executable string, request processDetachedRequest, command *createCommand, stdout, stderr io.Writer) (returnErr error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(encoded) > processDetachedWireLimit {
		return usageError("process-detached-request-too-large: nothing was created")
	}
	payload, err := os.CreateTemp("", "pmx-detach-*")
	if err != nil {
		return err
	}
	defer payload.Close()
	if err = os.Remove(payload.Name()); err != nil {
		return err
	}
	if _, err = payload.Write(encoded); err != nil {
		return err
	}
	if _, err = payload.Seek(0, io.SeekStart); err != nil {
		return err
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	syscall.CloseOnExec(pair[0])
	syscall.CloseOnExec(pair[1])
	own := os.NewFile(uintptr(pair[0]), "detach-caller")
	child := os.NewFile(uintptr(pair[1]), "detach-owner")
	defer child.Close()
	conn, err := net.FileConn(own)
	_ = own.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	interrupt := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer interrupt()
	// #nosec G204 -- this is the caller's own executable with fixed internal argv; private request data travels only through fd 4.
	launcher := exec.Command(executable, "internal", "process-agent-launch")
	launcher.ExtraFiles = []*os.File{child, payload}
	if err = launcher.Start(); err != nil {
		return err
	}
	_ = child.Close()
	done := make(chan error, 1)
	go func() { done <- launcher.Wait() }()
	decoder := json.NewDecoder(io.LimitReader(conn, processDetachedWireLimit))
	encoder := json.NewEncoder(conn)
	var identity, ready processDetachedReply
	if err = decoder.Decode(&identity); err != nil {
		_ = launcher.Process.Kill()
		<-done
		return fmt.Errorf("process-detached-identity-handshake-failed: %w", errors.Join(err, ctx.Err()))
	}
	if !identity.Owner.Valid() || int64(identity.Owner.OwnerUID) != int64(os.Getuid()) {
		_ = launcher.Process.Kill()
		<-done
		return errors.New("process-detached-owner-identity-invalid")
	}
	// Once an exact owner is known, every failed startup asks only that kernel
	// identity to stop. A failed/closed handshake also makes the owner roll back.
	failed := true
	defer func() {
		if failed {
			_ = conn.Close()
			signalDetachedProcessOwner(identity.Owner)
			returnErr = errors.Join(returnErr, awaitDetachedStartupCleanup(command, identity.Owner, ready.Binding))
		}
	}()
	kernelOwner, _, identityErr := localipc.Process(identity.Owner.PID)
	if identityErr != nil || kernelOwner != identity.Owner {
		_ = launcher.Process.Kill()
		<-done
		return errors.New("process-detached-owner-identity-unconfirmed")
	}
	if err = decoder.Decode(&ready); err != nil {
		return fmt.Errorf("process-detached-startup-handshake-failed: owner pid %d: %w", identity.Owner.PID, errors.Join(err, ctx.Err()))
	}
	if ready.Phase == "starting" && ready.Error == "" {
		started := ready
		if err = decoder.Decode(&ready); err != nil {
			ready = started
			return fmt.Errorf("process-detached-startup-handshake-failed: %w", errors.Join(err, ctx.Err()))
		}
		if ready.Binding != started.Binding || ready.Owner != started.Owner {
			return errors.New("process-detached-startup-binding-changed")
		}
	}
	if err = <-done; err != nil {
		return err
	}
	if ready.Error != "" {
		return errors.New(ready.Error)
	}
	if ready.Owner != identity.Owner || ready.Binding.Agent == "" {
		return errors.New("process-detached-binding-invalid")
	}
	reg, err := command.store.snapshot()
	if err != nil {
		return err
	}
	activation, _, current := reg.CurrentProcessActivation(metadataProcessBinding(ready.Binding))
	if !current || activation.HostProcess != identity.Owner {
		return processhost.ErrStale
	}
	if _, err = io.WriteString(stderr, ready.Stderr); err != nil {
		return err
	}
	if _, err = io.WriteString(stdout, ready.Stdout); err != nil {
		return err
	}
	// Output failure and caller cancellation precede commit. The owner never
	// treats EOF as success and retains the deadline until this acknowledgment.
	if err = encoder.Encode("commit"); err != nil {
		return err
	}
	var acknowledged string
	if err = decoder.Decode(&acknowledged); err != nil {
		return err
	}
	if acknowledged != "owned" {
		return errors.New("process-detached-commit-refused")
	}
	failed = false
	return nil
}

func signalDetachedProcessOwner(identity coremetadata.ProcessIdentity) {
	current, _, err := localipc.Process(identity.PID)
	if err == nil && current == identity {
		if process, err := os.FindProcess(identity.PID); err == nil {
			_ = process.Signal(syscall.SIGTERM)
		}
	}
}

func runProcessDetachedEntry(route string, command *createCommand) error {
	handshake := os.NewFile(3, "detach-handshake")
	payload := os.NewFile(4, "detach-request")
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	defer handshake.Close()
	defer payload.Close()
	if _, err := payload.Stat(); err != nil {
		return err
	}
	if route == "process-agent-launch" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		// #nosec G204 -- self executable and fixed argv; fd 4 carries the private request.
		owner := exec.Command(executable, "internal", "process-agent-owner")
		owner.ExtraFiles = []*os.File{handshake, payload}
		owner.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err = owner.Start(); err != nil {
			return err
		}
		return owner.Process.Release()
	}
	conn, err := net.FileConn(handshake)
	_ = handshake.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(processDetachedStartupTimeout)); err != nil {
		return err
	}
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(io.LimitReader(conn, processDetachedWireLimit))
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return err
	}
	if err = encoder.Encode(processDetachedReply{Owner: identity}); err != nil {
		return err
	}
	var req processDetachedRequest
	reader := json.NewDecoder(io.LimitReader(payload, processDetachedWireLimit))
	reader.DisallowUnknownFields()
	if err = reader.Decode(&req); err != nil {
		return err
	}
	_ = payload.Close()
	if err = bindDetachedCaller(command, req.Caller); err != nil {
		return err
	}
	// Detached creation cannot redirect the Registry to another domain.
	stateDir, pathErr := command.store.stateDir()
	if pathErr != nil {
		return pathErr
	}
	if req.RegistryPath != intmetadata.PathFor(stateDir) {
		return errors.New("process-detached-registry-mismatch")
	}
	request, err := newProcessAgentCreateRequest(req.Options)
	if err != nil {
		return err
	}
	life, cancel := processForegroundLifetime()
	life = (processOwnerLifetime{}).withContext(life)
	defer cancel()
	// Cancellation is a startup transaction until the caller commits. Closing
	// the handshake or reaching its deadline rolls back only this new generation.
	startup := life
	startupTimer := time.AfterFunc(processDetachedStartupTimeout, cancel)
	defer startupTimer.Stop()
	result, err := command.startProcessAgent(startup, request)
	fail := func(cause error) error {
		cleanup := command.failProcessCreate(&result, cause)
		_ = encoder.Encode(processDetachedReply{Owner: identity, Binding: result.Binding, Error: cleanup.Error()})
		return cleanup
	}
	if err != nil {
		return fail(err)
	}
	if err = encoder.Encode(processDetachedReply{Owner: identity, Binding: result.Binding, Phase: "starting"}); err != nil {
		return fail(err)
	}
	var display, notices bytes.Buffer
	if err = command.runProcessPostCreate(startup, result, &notices); err != nil {
		return fail(fmt.Errorf("process-post-create-hook-failed: %w", err))
	}
	control, err := command.newProcessCreateControl(result)
	if err != nil {
		return fail(err)
	}
	if err = submitProcessInitialPrompt(startup, result, req.Options.Payload); err != nil {
		return fail(err)
	}
	result.creator.report(&notices)
	if err = command.writeProcessCreateResult(&display, &notices, req.Output, result, true); err != nil {
		return fail(err)
	}
	output := display.Bytes()
	errorsOutput := notices.Bytes()
	reply := processDetachedReply{Owner: identity, Binding: result.Binding, Stdout: string(output), Stderr: string(errorsOutput)}
	if err = encoder.Encode(reply); err != nil {
		return fail(err)
	}
	var commit string
	if err = decoder.Decode(&commit); err != nil {
		return fail(err)
	}
	if commit != "commit" {
		return fail(errors.New("process-detached-commit-invalid"))
	}
	snapshot, err := result.Handle.Observe(result.Binding)
	if err != nil {
		return fail(err)
	}
	if snapshot.State == "exited" || snapshot.State == "unknown" || snapshot.State == "stopping" || startup.Err() != nil {
		return fail(errors.New("process-detached-owner-ended-before-commit"))
	}
	if handle, ok := result.Handle.(processGenerationHandle); ok {
		if err = handle.CheckGeneration(life, result.Binding); err != nil {
			return fail(err)
		}
	}
	log, err := newProcessDetachedLog(req.RegistryPath, result.Binding)
	if err != nil {
		return fail(err)
	}
	defer log.file.Close()
	if err = encoder.Encode("owned"); err != nil {
		return fail(err)
	}
	startupTimer.Stop()
	_ = conn.Close()
	return waitCreatedProcessAgent(life, cancel, &result, control, nil, log)
}

// Re-run creator's existing kernel/Registry proof from the still-live caller,
// rather than infer provenance from the new owner after its session boundary.
func bindDetachedCaller(command *createCommand, caller coremetadata.ProcessIdentity) error {
	current, _, err := localipc.Process(caller.PID)
	if err != nil || current != caller || int64(caller.OwnerUID) != int64(os.Getuid()) {
		return errors.New("process-detached-caller-identity-invalid")
	}
	ancestors := func() ([]coremetadata.ProcessIdentity, error) {
		current, _, err := localipc.Process(caller.PID)
		if err != nil || current != caller {
			return nil, errors.New("process-detached-caller-ended")
		}
		var chain []coremetadata.ProcessIdentity
		pid := caller.PID
		for range creatorProcessChainMaxSteps {
			identity, parent, err := localipc.Process(pid)
			if err != nil {
				return chain, err
			}
			chain = append(chain, identity)
			if parent <= 1 || parent == pid {
				break
			}
			pid = parent
		}
		return chain, nil
	}
	command.processCreatorAncestors = ancestors
	command.processAncestors = func() ([]int, error) {
		chain, err := ancestors()
		pids := make([]int, len(chain))
		for i := range chain {
			pids[i] = chain[i].PID
		}
		return pids, err
	}
	return nil
}

type processDetachedLog struct {
	file    *os.File
	written int
}

func newProcessDetachedLog(registryPath string, binding processhost.Binding) (*processDetachedLog, error) {
	for _, component := range []string{binding.Agent, binding.Generation} {
		if !filepath.IsLocal(component) || filepath.Base(component) != component || component == "." {
			return nil, errors.New("process-detached-log-binding-invalid")
		}
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(registryPath)), "process-owners")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !localipc.OwnedByCurrentUser(info) {
		return nil, errors.New("process-detached-log-directory-not-private")
	}
	// #nosec G304 -- single-component binding names under the exact owned private state directory; exclusive nofollow creation cannot replace or follow an existing log.
	file, err := os.OpenFile(filepath.Join(dir, binding.Agent+"-"+binding.Generation+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	return &processDetachedLog{file: file}, nil
}

// Provider streams and the initial task never pass through this writer. It
// retains only a bounded tail of owned Wait diagnostics, never caller fds.
func (l *processDetachedLog) Write(p []byte) (int, error) {
	n := len(p)
	// Never persist free-form diagnostics: they can embed a provider failure,
	// hook text, or a request body. Exact lifecycle evidence remains in the journal.
	p = fmt.Appendf(nil, "owner lifecycle diagnostic; bytes=%d\n", n)
	if len(p) > processDetachedLogLimit {
		p = p[len(p)-processDetachedLogLimit:]
	}
	if l.written+len(p) > processDetachedLogLimit {
		if err := l.file.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := l.file.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		l.written = 0
	}
	written, err := l.file.Write(p)
	l.written += written
	if err != nil {
		return 0, err
	}
	return n, nil
}

// A failed startup is not reported cleaned up merely because TERM was sent.
// Observe exact owner retirement and, when known, the reserved generation's
// rollback or durable Wait before returning; retain exact recovery references.
func awaitDetachedStartupCleanup(command *createCommand, owner coremetadata.ProcessIdentity, binding processhost.Binding) error {
	ctx, cancel := context.WithTimeout(context.Background(), processRelaunchRetirementTimeout)
	defer cancel()
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	for {
		if binding.Agent == "" {
			if reg, err := command.store.snapshot(); err == nil {
				for _, pane := range reg.Panes {
					if activation := pane.Status.Activation.Process; activation != nil && activation.HostProcess == owner && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == activation.Binding {
						binding = processSchemaBinding(activation.Binding)
						break
					}
				}
			}
		}
		gone := processIdentityAbsent(owner, localipc.Process)
		settled := binding.Agent == ""
		if !settled {
			reg, err := command.store.snapshot()
			if err == nil {
				agent, exists := reg.Agent(binding.Agent)
				pane, present := reg.Pane(binding.Pane)
				settled = !exists && !present
				if exists && present && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == metadataProcessBinding(binding) {
					settled = agent.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) && coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)
				}
			}
		}
		if gone && settled {
			return nil
		}
		select {
		case <-ctx.Done():
			if binding.Agent != "" {
				return fmt.Errorf("process-detached-cleanup-unconfirmed: remaining agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s; cleanup: projmux agent stop uid:%s: %w", binding.Agent, binding.Pane, binding.Agent, binding.Agent, ctx.Err())
			}
			return fmt.Errorf("process-detached-cleanup-unconfirmed: inspect exact owner pid %d start %s: %w", owner.PID, owner.Start, ctx.Err())
		case <-ticker.C:
		}
	}
}
