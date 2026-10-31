package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func startReservedRefusal(reason, uid, why string) error {
	return &mutationruntime.AgentCreateRefusal{Reason: reason, Message: "start reserved agent: uid:" + uid + " " + why + "; nothing was started"}
}

// validateReservedLaunch proves a frozen launch is the one the Registry's
// Pending Agent was reserved with before anything in it is executed. It never
// puts the command or its environment in an error.
func validateReservedLaunch(record createdLaunchRecord, reg coremetadata.Registry, uid string) error {
	stale := func(why string) error { return startReservedRefusal("reservation-stale", uid, why) }
	if record.Version != reservedLaunchVersion {
		return stale("has a frozen launch of an unknown version")
	}
	if record.Binding.Agent != uid || record.Provider != aiModeClaude {
		return stale("has a frozen launch that names another Agent or provider")
	}
	command := record.Command
	if !filepath.IsAbs(command.Path) || len(command.Args) == 0 || command.Env == nil {
		return stale("has an incomplete frozen command")
	}
	if validateDeferredClaudePermissionMode(command.Args) != nil {
		return stale("has a frozen command without its permission mode")
	}
	files, err := deferredLaunchFiles(command.Args)
	if err != nil || !reflect.DeepEqual(files, record.Files) {
		return stale("has launch files that changed since it was reserved")
	}
	agent, exists := reg.Agent(uid)
	pane, ambiguous := mutationruntime.ProcessAgentPane(reg, uid)
	if !exists || ambiguous || pane == nil {
		return stale("has no single process Pane")
	}
	_, windowExists := reg.Window(record.Binding.Window)
	_, projectExists := reg.Project(record.Binding.Project)
	if !windowExists || !projectExists || agent.Metadata.OwnerUID() != record.Binding.Window {
		return stale("has lost its Window or Project")
	}
	// The reserve proof compares the prepared reply with the Registry; reusing it
	// keeps one definition of a Pending reservation.
	prepared := mutationruntime.ProcessDetachedReply{Binding: record.Binding, Result: &mutationruntime.ProcessCreateCommitted{Agent: *agent, Pane: *pane}}
	if mutationruntime.VerifyDetachedProcessReserve(func() (coremetadata.Registry, error) { return reg, nil }, prepared) != nil {
		return stale("no longer matches the Registry")
	}
	return nil
}

// claimReservedStart marks the reservation as owned by this worker, under the
// Agent's claim guard, and returns what the start needs.
func (c *createCommand) claimReservedStart(registryPath, uid string, owner coremetadata.ProcessIdentity) (createdLaunchRecord, processAgentCreateResult, error) {
	var result processAgentCreateResult
	unlock, err := lockDeferredClaim(mutationruntime.DeferredClaimPath(registryPath, uid))
	if err != nil {
		return createdLaunchRecord{}, result, err
	}
	defer unlock()
	path := mutationruntime.CreatedLaunchPath(registryPath, uid)
	stored, err := readCreatedLaunch(path)
	if errors.Is(err, processhost.ErrResumeRefused) {
		return createdLaunchRecord{}, result, startReservedRefusal("reservation-stale", uid, "has a damaged or unsafe frozen launch")
	}
	if err != nil {
		return createdLaunchRecord{}, result, err
	}
	if stored == nil {
		return createdLaunchRecord{}, result, startReservedRefusal("reservation-missing", uid, "has no frozen launch")
	}
	record := *stored
	reg, err := c.store.snapshot()
	if err != nil {
		return record, result, err
	}
	if err = validateReservedLaunch(record, reg, uid); err != nil {
		return record, result, err
	}
	if record.Starting && (record.StartingOwner == nil || !mutationruntime.ProcessIdentityAbsent(*record.StartingOwner, localipc.Process)) {
		return record, result, startReservedRefusal("reservation-starting", uid, "is being started by another worker")
	}
	record.Starting, record.StartingOwner = true, &owner
	if err = writeCreatedLaunch(path, record); err != nil {
		return record, result, err
	}
	agent, _ := reg.Agent(uid)
	window, _ := reg.Window(record.Binding.Window)
	project, _ := reg.Project(record.Binding.Project)
	result = processAgentCreateResult{createdWindow: record.CreatedWindow, Created: createResult{kind: coremetadata.KindAgent, uid: uid, name: agent.Metadata.Name, projectName: project.Metadata.Name, windowName: window.Metadata.Name, windowUID: window.Metadata.UID}, Binding: record.Binding, Provider: record.Provider, registryPath: registryPath}
	return record, result, nil
}

// releaseReservedStart gives the reservation back after a start that never
// reached its provider. It frees only a mark this worker set.
func (c *createCommand) releaseReservedStart(registryPath, uid string, owner coremetadata.ProcessIdentity) error {
	unlock, err := lockDeferredClaim(mutationruntime.DeferredClaimPath(registryPath, uid))
	if err != nil {
		return err
	}
	defer unlock()
	path := mutationruntime.CreatedLaunchPath(registryPath, uid)
	record, err := readCreatedLaunch(path)
	if err != nil || record == nil || record.StartingOwner == nil || *record.StartingOwner != owner {
		return err
	}
	record.Starting, record.StartingOwner = false, nil
	return writeCreatedLaunch(path, *record)
}

// removeReservedLaunch deletes the Agent's frozen launch under its claim guard.
func removeReservedLaunch(registryPath, uid string) error {
	unlock, err := lockDeferredClaim(mutationruntime.DeferredClaimPath(registryPath, uid))
	if err != nil {
		return err
	}
	defer unlock()
	return mutationruntime.RemoveCreatedLaunch(registryPath, uid)
}

// startReservedProcessAgent starts the provider of a reserved Agent. kept is
// true when it failed before a provider existed (including a provider start the
// host rejected): the reservation is intact, its Starting mark released, and
// nothing needs rolling back. After the provider starts, a failure rolls the
// Agent back like any failed create.
func (c *createCommand) startReservedProcessAgent(ctx context.Context, uid string, owner coremetadata.ProcessIdentity) (result processAgentCreateResult, kept bool, err error) {
	stateDir, err := c.store.stateDir()
	if err != nil {
		return result, true, err
	}
	path := intmetadata.PathFor(stateDir)
	record, result, err := c.claimReservedStart(path, uid, owner)
	if err != nil {
		return result, true, err
	}
	early := func(cause error) (processAgentCreateResult, bool, error) {
		return result, true, errors.Join(cause, c.releaseReservedStart(path, uid, owner))
	}
	ctx, result.stopRecorder = newProcessOwnerStop(ctx, path, result.Binding)
	executable, err := c.processCreateExecutable()
	if err != nil {
		return early(err)
	}
	supervisorEnv, err := c.processCreateSupervisorEnvironment(record.Command.Env, result.Binding, path)
	if err != nil {
		return early(err)
	}
	host, err := processhost.NewHost(result.Binding.Host, processhost.Command{Path: executable, Args: []string{"internal", "process-host-supervisor"}, Env: supervisorEnv}, c.processCreateTransactions(path), processhost.DefaultLimits())
	if err != nil {
		return early(err)
	}
	handle, err := startProcessClaude(ctx, host, processhost.Launch{Binding: result.Binding, Command: record.Command}, path, nil)
	if err != nil {
		// The rollback of a failed create treats a nil handle and a rejected provider
		// start as an unstarted reservation whose Registry state is untouched; so
		// does a retry. A failure before the supervisor ran left nothing either.
		if handle == nil || handle.ProviderStartRejected(result.Binding) || handle.SupervisorNotStarted(result.Binding) {
			return early(err)
		}
		result.Handle = handle
		return result, false, err
	}
	result.Handle = handle
	// A failure from here on deletes the Agent, so the frozen launch has no
	// further use; removing it now lets a failed removal reach the caller.
	if removeErr := removeReservedLaunch(path, uid); removeErr != nil {
		result.Notices = append(result.Notices, fmt.Sprintf("start reserved agent: the frozen launch of uid:%s could not be removed (%v); projmux delete agent uid:%s removes it", uid, removeErr, uid))
	}
	return result, false, nil
}

// runProcessDetachedStartReserved is StartReservedAgent's owner: it starts the
// frozen launch and then owns the provider like a created process Agent.
func runProcessDetachedStartReserved(conn net.Conn, encoder *json.Encoder, decoder *json.Decoder, req processDetachedRequest, identity coremetadata.ProcessIdentity, command *createCommand) error {
	life, cancel := processForegroundLifetime()
	life = (processOwnerLifetime{}).withContext(life)
	defer cancel()
	startup := life
	startupTimer := time.AfterFunc(processDetachedStartupTimeout, cancel)
	defer startupTimer.Stop()
	result, kept, err := command.startReservedProcessAgent(startup, req.StartAgent, identity)
	fail := func(cause error) error {
		var cleanup error
		switch {
		case kept && result.Binding.Agent != "":
			cleanup = fmt.Errorf("%w; the reservation is kept and can be started again", cause)
		case kept:
			cleanup = cause
		default:
			cleanup = errors.Join(command.failProcessCreate(&result, cause), removeReservedLaunch(result.registryPath, result.Binding.Agent))
		}
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
	if err = command.writeProcessCreateResult(&display, &notices, req.Output, result, true); err != nil {
		return fail(err)
	}
	committed, err := command.processCreateCommitted(result)
	if err != nil {
		return fail(err)
	}
	reply := processDetachedReply{Owner: identity, Binding: result.Binding, Stdout: display.String(), Stderr: notices.String(), Result: &committed}
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
