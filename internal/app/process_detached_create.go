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
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const processDetachedStartupTimeout = 30 * time.Second
const processDetachedWireLimit = 2 << 20
const processDetachedLogLimit = 64 << 10

// Only the fixed internal route is argv. The request (including the initial
// task) is an unlinked private file; the handshake is a private duplex fd.
// The intermediate launcher never owns a provider and exits before success.
type processDetachedRequest struct {
	RelaunchContext *mutationruntime.ProcessCreateContext `json:",omitempty"`
	Relaunch        *processDetachedRelaunchRequest       `json:",omitempty"`
	Resume          *processDetachedResumeRequest         `json:",omitempty"`
	Options         processAgentCreateOptions
	Output          cli.OutputMode
	Caller          coremetadata.ProcessIdentity
	RegistryPath    string
	Context         *mutationruntime.ProcessCreateContext `json:",omitempty"`
	ResumeContext   *mutationruntime.ProcessCreateContext `json:",omitempty"`
}
type processDetachedReply struct {
	Relaunch              json.RawMessage `json:",omitempty"`
	ErrorDetail           json.RawMessage `json:",omitempty"`
	Owner                 coremetadata.ProcessIdentity
	Binding               processhost.Binding
	Stdout, Stderr, Error string
	Phase                 string
	Claim                 *deferredClaimRecord                    `json:",omitempty"`
	Result                *mutationruntime.ProcessCreateCommitted `json:",omitempty"`
	ErrorCode             string                                  `json:",omitempty"`
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

func launchProcessDetached(ctx context.Context, executable string, request processDetachedRequest, command *createCommand, stdout, stderr io.Writer) error {
	convert := func(reply mutationruntime.ProcessDetachedReply) processDetachedReply {
		var out processDetachedReply
		raw, _ := json.Marshal(reply)
		_ = json.Unmarshal(raw, &out)
		return out
	}
	launcher := mutationruntime.ProcessDetachedLauncher{Executable: executable, Verify: func(reply mutationruntime.ProcessDetachedReply, owner coremetadata.ProcessIdentity) error {
		ready := convert(reply)
		identity := processDetachedReply{Owner: owner}
		if request.Relaunch != nil && ready.Binding.Agent != request.Relaunch.Source.Metadata.UID {
			return errors.New("process-detached-binding-invalid")
		}
		reg, err := command.store.snapshot()
		if err != nil {
			return err
		}
		if request.Relaunch == nil && request.Resume == nil {
			return mutationruntime.VerifyDetachedProcessCreateBinding(reg, ready.Binding, identity.Owner)
		}
		activation, _, current := reg.CurrentProcessActivation(metadataProcessBinding(ready.Binding))
		if ready.Claim != nil {
			claimAllowed := request.Relaunch != nil || (request.Resume != nil && request.Resume.WaitForPeer)
			if !claimAllowed || !detachedClaimCurrent(command, reg, *ready.Claim, identity.Owner) {
				return processhost.ErrStale
			}
		} else if !current || activation.HostProcess != identity.Owner {
			return processhost.ErrStale
		}

		return nil
	}, Cleanup: func(owner coremetadata.ProcessIdentity, ready mutationruntime.ProcessDetachedReply) error {
		return awaitDetachedRequestCleanup(command, request, owner, convert(ready))
	}}
	if request.Context != nil {
		launcher.Environment = request.Context.Environment
		launcher.PayloadDir = request.Context.Paths.StateDir
	}
	_, err := launcher.Launch(ctx, request, stdout, stderr)
	return err
}

func runProcessDetachedEntry(route string, command *createCommand, resume *agentCommand) error {
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
	if req.RelaunchContext != nil {
		if req.Context != nil || req.ResumeContext != nil || req.Resume != nil || req.Relaunch == nil || resume == nil {
			return errors.New("process-detached-context-relaunch-only")
		}
		if err = command.bindProcessCreateContext(*req.RelaunchContext); err != nil {
			return err
		}
		resume.bindProcessResumeContext(command, *req.RelaunchContext)
	}
	if req.ResumeContext != nil {
		if req.Context != nil || req.Resume == nil || req.Relaunch != nil || resume == nil {
			return errors.New("process-detached-context-resume-only")
		}
		if err = command.bindProcessCreateContext(*req.ResumeContext); err != nil {
			return err
		}
		resume.bindProcessResumeContext(command, *req.ResumeContext)
	}
	if req.Context != nil {
		if req.Resume != nil || req.Relaunch != nil {
			return errors.New("process-detached-context-create-only")
		}
		if err = command.bindProcessCreateContext(*req.Context); err != nil {
			return err
		}
	}
	if err = bindDetachedCaller(command, req.Caller); err != nil {
		return err
	}
	if req.Context != nil {
		command.processAncestors = nil
		command.processCreatorAncestors = nil
	}
	// Detached creation cannot redirect the Registry to another domain.
	stateDir, pathErr := command.store.stateDir()
	if pathErr != nil {
		return pathErr
	}
	if req.RegistryPath != intmetadata.PathFor(stateDir) {
		return errors.New("process-detached-registry-mismatch")
	}
	if req.Relaunch != nil {
		if resume == nil || req.Resume != nil {
			return errors.New("process-detached-relaunch-unavailable")
		}
		return runProcessDetachedRelaunchOwner(conn, encoder, decoder, req, identity, command, resume)
	}
	if req.Resume != nil {
		if resume == nil {
			return errors.New("process-detached-resume-unavailable")
		}
		return runProcessDetachedResumeOwner(conn, encoder, decoder, req, identity, resume)
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
		_ = encoder.Encode(processDetachedReply{Owner: identity, Binding: result.Binding, Error: cleanup.Error(), ErrorCode: mutationruntime.ProcessCreateErrorCode(cause)})
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
	if req.Context != nil {
		committed, err := command.processCreateCommitted(result)
		if err != nil {
			return fail(err)
		}
		reply.Result = &committed
	}
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
	return mutationruntime.AwaitDetachedProcessCreateCleanup(command.store.snapshot, owner, binding)
}
