package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const reservedLaunchVersion = 1

// createdLaunchRecord is the frozen launch of a reserved process Agent: the
// resolved command and the proofs of the snapshot files it names, never a frame.
type createdLaunchRecord struct {
	Version       int
	Binding       processhost.Binding
	Provider      string
	CreatedWindow bool
	Command       processhost.Command
	Files         map[string]string
	// Starting marks the one start that owns the reservation.
	Starting bool `json:",omitempty"`
	// StartingOwner is the worker that set Starting; only proof that it is gone
	// frees the reservation for another start.
	StartingOwner *coremetadata.ProcessIdentity `json:",omitempty"`
}

func writeCreatedLaunch(path string, record createdLaunchRecord) error {
	return writeDeferredState(path, record)
}

func readCreatedLaunch(path string) (*createdLaunchRecord, error) {
	var record createdLaunchRecord
	found, err := readDeferredState(path, &record)
	if err != nil || !found {
		return nil, err
	}
	return &record, nil
}

// reserveProcessLaunch creates one Pending Claude process Agent and freezes its
// launch; a failure removes both before it returns.
func (c *createCommand) reserveProcessLaunch(ctx context.Context, opts processAgentCreateOptions, operation string) (processAgentCreateResult, error) {
	plan, err := c.planProcessAgent(opts)
	if err != nil {
		return processAgentCreateResult{}, err
	}
	if plan.flags.provider != aiModeClaude {
		return processAgentCreateResult{}, &mutationruntime.AgentCreateRefusal{Reason: "reserve-provider-unsupported", Message: "reserve agent: only a Claude process Agent can be reserved; nothing was created"}
	}
	stateDir, err := c.store.stateDir()
	if err != nil {
		return processAgentCreateResult{}, err
	}
	generation, err := c.mintGeneration()
	if err != nil {
		return processAgentCreateResult{}, err
	}
	result, err := c.reserveProcessAgent(ctx, plan, opts, operation, generation)
	if err != nil {
		return result, err
	}
	result.registryPath = intmetadata.PathFor(stateDir)
	if notice := plan.flags.projectLinks.project.notice(result.Created.name); notice != "" {
		result.Notices = append(result.Notices, notice)
	}
	if err = freezeCreatedLaunch(result.registryPath, plan, result); err != nil {
		return result, c.failReservedLaunch(&result, err)
	}
	return result, nil
}

func freezeCreatedLaunch(registryPath string, plan processAgentCreatePlan, result processAgentCreateResult) error {
	files, err := deferredLaunchFiles(plan.command.Args)
	if err != nil {
		return err
	}
	unlock, err := lockDeferredClaim(mutationruntime.DeferredClaimPath(registryPath, result.Binding.Agent))
	if err != nil {
		return err
	}
	defer unlock()
	path := mutationruntime.CreatedLaunchPath(registryPath, result.Binding.Agent)
	record := createdLaunchRecord{Version: reservedLaunchVersion, Binding: result.Binding, Provider: result.Provider, CreatedWindow: result.createdWindow, Command: plan.command, Files: files}
	if err = writeCreatedLaunch(path, record); err != nil {
		return err
	}
	stored, err := readCreatedLaunch(path)
	if err != nil {
		return err
	}
	if stored == nil || !reflect.DeepEqual(*stored, record) {
		return deferredRefused("reserved launch did not read back as written")
	}
	return nil
}

// failReservedLaunch removes an unstarted reservation and its frozen launch.
func (c *createCommand) failReservedLaunch(result *processAgentCreateResult, cause error) error {
	cleanup := c.failProcessCreate(result, cause)
	unlock, lockErr := lockDeferredClaim(mutationruntime.DeferredClaimPath(result.registryPath, result.Binding.Agent))
	if lockErr != nil {
		return errors.Join(cleanup, lockErr)
	}
	defer unlock()
	return errors.Join(cleanup, mutationruntime.RemoveCreatedLaunch(result.registryPath, result.Binding.Agent))
}

// runProcessDetachedReserve is ReserveAgent's no-provider worker; it keeps the
// reservation only when the caller commits.
func runProcessDetachedReserve(conn net.Conn, encoder *json.Encoder, decoder *json.Decoder, req processDetachedRequest, identity coremetadata.ProcessIdentity, command *createCommand) error {
	reject := func(result *processAgentCreateResult, cause error) error {
		binding := processhost.Binding{}
		if result != nil {
			binding = result.Binding
		}
		_ = encoder.Encode(processDetachedReply{Owner: identity, Binding: binding, Error: cause.Error(), ErrorCode: mutationruntime.ProcessCreateErrorCode(cause)})
		return cause
	}
	request, err := newProcessAgentCreateRequest(req.Options)
	if err != nil {
		return reject(nil, err)
	}
	if len(request.options.Payload) != 0 || request.options.OperatorCreatorClient != "" {
		return reject(nil, errors.New("process-detached-reserve-request-invalid"))
	}
	life, cancel := processForegroundLifetime()
	defer cancel()
	timer := time.AfterFunc(processDetachedStartupTimeout, cancel)
	defer timer.Stop()
	result, err := command.reserveProcessLaunch(life, request.options, req.ReserveOperation)
	if err != nil {
		// A protected Agent survives rollback; the reply keeps its Binding.
		return reject(&result, err)
	}
	rollback := func(cause error) error {
		return reject(&result, command.failReservedLaunch(&result, cause))
	}
	committed, err := command.processCreateCommitted(result)
	if err != nil {
		return rollback(err)
	}
	if err = encoder.Encode(processDetachedReply{Owner: identity, Binding: result.Binding, Phase: "prepared", Result: &committed}); err != nil {
		return rollback(err)
	}
	var commit string
	if err = decoder.Decode(&commit); err != nil {
		return rollback(err)
	}
	if commit != "commit" {
		return rollback(errors.New("process-preparation-commit-invalid"))
	}
	if err = life.Err(); err != nil {
		return rollback(err)
	}
	if err = encoder.Encode("completed"); err != nil {
		return rollback(err)
	}
	return nil
}
