package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// The launch outlives a claim. It carries resolved arguments and snapshot
// proofs, never a first frame or the caller's environment.
type deferredLaunchRecord struct {
	Version                        int
	Agent                          string
	Retired                        coremetadata.ProcessSessionRecord
	OldSpec, NewSpec               coremetadata.AgentSpec
	OldAnnotations, NewAnnotations map[string]string
	Command                        processhost.Command
	Files                          map[string]string
	Model, Effort                  string
	Attempt                        *deferredLaunchAttempt
	Previous                       *deferredLaunchRecord
}

type deferredLaunchAttempt struct {
	Source coremetadata.ProcessSessionRecord
	Target coremetadata.ProcessBinding
}

func deferredRefused(detail string) error {
	return fmt.Errorf("%s: %w: %s", processResumeRefused, processhost.ErrResumeRefused, detail)
}

func (c *agentCommand) deferredStatePath(kind, uid string) string {
	path := c.deferredClaimPath(uid)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(path)), kind, filepath.Base(path))
}

// Sidecars are bounded, private, regular files owned by this OS user. The
// persistent claim lock is the only writer lock for an Agent's sidecars.
func readDeferredState(path string, value any) (bool, error) {
	if path == "" {
		return false, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return false, deferredRefused("unsafe deferred state")
	}
	file, err := os.Open(path) // #nosec G304 -- private state path derived from the Agent digest.
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	err = errors.Join(err, file.Close())
	if err != nil {
		return false, err
	}
	if len(data) > 65536 || json.Unmarshal(data, value) != nil {
		return false, deferredRefused("damaged deferred state")
	}
	return true, nil
}

func writeDeferredState(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 65536 || path == "" {
		return deferredRefused("deferred state exceeds its bound or has no store")
	}
	if err = localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".deferred-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDeferredDirectory(path)
}

func syncDeferredDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path)) // #nosec G304 -- private state parent.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func removeDeferredState(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDeferredDirectory(path)
}

func (c *agentCommand) readDeferredLaunch(uid string) (*deferredLaunchRecord, error) {
	var record deferredLaunchRecord
	found, err := readDeferredState(c.deferredStatePath("deferred-launches", uid), &record)
	if err != nil || !found {
		return nil, err
	}
	if record.Version != 1 || record.Agent != uid || record.Retired.Provider != aiModeClaude || record.Retired.SessionID == "" || record.Retired.Binding.AgentUID != uid || record.Command.Env != nil || record.Command.Path == "" || record.Command.Dir == "" {
		return nil, deferredRefused("damaged deferred launch")
	}
	if record.Previous != nil && (record.Previous.Previous != nil || record.Previous.Agent != uid || record.Previous.Version != 1 || record.Previous.Retired.Provider != aiModeClaude || record.Previous.Retired.Binding.AgentUID != uid || record.Previous.Retired.SessionID == "" || record.Previous.Command.Env != nil || record.Previous.Command.Path == "" || record.Previous.Command.Dir == "" || record.Attempt == nil) {
		return nil, deferredRefused("damaged relaunch intent")
	}
	return &record, nil
}

func deferredLaunchDigest(record *deferredLaunchRecord) string {
	if record == nil {
		return ""
	}
	data, _ := json.Marshal(record)
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func deferredSnapshotDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", deferredRefused("launch snapshot is not a regular file")
	}
	file, err := os.Open(path) // #nosec G304 -- resolved launch snapshot, never provider input.
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	err = errors.Join(err, file.Close())
	return fmt.Sprintf("%x", hash.Sum(nil)), err
}

// Claude's resolved grammar has two file-valued flags. Workspace roots are
// directories; resume IDs, model/effort and stream arguments are literals.
func deferredLaunchFiles(args []string) (map[string]string, error) {
	files := map[string]string{}
	for i := 0; i < len(args); i++ {
		arg, path := args[i], ""
		for _, flag := range []string{"--settings", "--append-system-prompt-file"} {
			if arg == flag {
				if i+1 >= len(args) {
					return nil, deferredRefused("missing snapshot path")
				}
				i++
				path = args[i]
			}
			if after, ok := strings.CutPrefix(arg, flag+"="); ok {
				path = after
			}
		}
		if path != "" {
			digest, err := deferredSnapshotDigest(path)
			if err != nil {
				return nil, err
			}
			files[path] = digest
		}
	}
	return files, nil
}

func (record *deferredLaunchRecord) validateFiles() error {
	files, err := deferredLaunchFiles(record.Command.Args)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(files, record.Files) {
		return deferredRefused("launch snapshot changed; configuration retained")
	}
	return nil
}

func (record *deferredLaunchRecord) matches(candidate processResumeCandidate) bool {
	return record.Agent == candidate.Agent.Metadata.UID && processResumeRecordEqual(&record.Retired, &candidate.Record) && reflect.DeepEqual(candidate.Agent.Spec, record.NewSpec) && reflect.DeepEqual(candidate.Agent.Metadata.Annotations, record.NewAnnotations)
}

// Called under the existing claim guard. Replay can finish only the exact
// prepared old->new CAS, never overwrite an unrelated recipe or conversation.
func (c *agentCommand) reconcileDeferredLaunch(ctx context.Context, record *deferredLaunchRecord) error {
	if record == nil {
		return nil
	}
	if record.Previous != nil {
		return c.reconcilePromptRelaunch(record)
	}
	if err := c.reconcileDeferredAttempt(record); err != nil {
		return err
	}
	_, err := c.rebind.create.store.update(func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		pane, ambiguous := processResumePane(*reg, record.Agent)
		agent, found := reg.Agent(record.Agent)
		if ambiguous || !found || pane == nil || agent.Status.Phase != coremetadata.PhaseOffline || !processResumeRecordEqual(pane.Status.ProcessSession, &record.Retired) || !coremetadata.MatchesProcessWait(record.Retired.Binding, pane.Status.LastTermination) {
			return deferredRefused("prepared relaunch conversation changed")
		}
		if reflect.DeepEqual(agent.Spec, record.NewSpec) && reflect.DeepEqual(agent.Metadata.Annotations, record.NewAnnotations) {
			return nil
		}
		if !reflect.DeepEqual(agent.Spec, record.OldSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.OldAnnotations) {
			return deferredRefused("prepared relaunch recipe changed")
		}
		agent.Spec, agent.Metadata.Annotations = record.NewSpec, maps.Clone(record.NewAnnotations)
		return nil
	})
	return err
}

// Caller holds the claim guard. The witness is intent, never live authority.
func (c *agentCommand) prepareDeferredAttempt(candidate processResumeCandidate, binding processhost.Binding, record *deferredLaunchRecord) error {
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return err
	}
	sourceRecipe := record
	if record.Previous != nil {
		sourceRecipe = record.Previous
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(sourceRecipe) {
		return deferredRefused("launch changed before reservation")
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || !coremetadata.MatchesProcessWait(candidate.Record.Binding, pane.Status.LastTermination) || !coremetadata.MatchesProcessWait(candidate.Record.Binding, agent.Status.LastTermination) || !sourceRecipe.matches(candidate) || !processResumeRecordEqual(pane.Status.ProcessSession, &candidate.Record) || !reflect.DeepEqual(agent.Spec, sourceRecipe.NewSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, sourceRecipe.NewAnnotations) {
		return deferredRefused("attempt source changed")
	}
	target, source := metadataProcessBinding(binding), candidate.Record.Binding
	if target.AgentUID != source.AgentUID || target.PaneUID != source.PaneUID || target.ProjectUID != source.ProjectUID || target.WindowUID != source.WindowUID || target.HostInstanceID == source.HostInstanceID || target.Generation == source.Generation || target.OperationID == source.OperationID {
		return deferredRefused("attempt requires exact owner chain and fresh target")
	}
	record.Attempt = &deferredLaunchAttempt{Source: *candidate.Record.Clone(), Target: target}
	return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), record)
}

// A failed attempt can readdress only its exact Wait. Registry locks are
// released before the attention CAS; no old cursor/control becomes authority.
func (c *agentCommand) reconcileDeferredAttempt(record *deferredLaunchRecord) error {
	a := record.Attempt
	if a == nil {
		return nil
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || pane.Status.ProcessSession == nil || !reflect.DeepEqual(agent.Spec, record.NewSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.NewAnnotations) {
		return deferredRefused("attempt recipe changed")
	}
	s := pane.Status.ProcessSession
	if a.Target.HostInstanceID == a.Source.Binding.HostInstanceID || a.Target.Generation == a.Source.Binding.Generation || a.Target.OperationID == a.Source.Binding.OperationID || (!processResumeRecordEqual(&record.Retired, &a.Source) && !processResumeRecordEqual(&record.Retired, s)) {
		return deferredRefused("attempt source/target differs")
	}
	if processResumeRecordEqual(s, &a.Source) {
		return nil
	}
	history := a.Source.History
	if history == nil || a.Source.TurnID != "" || len(a.Source.Pending) > 0 {
		history = &coremetadata.ProcessResumeHistory{Binding: a.Source.Binding, SessionID: a.Source.SessionID, InterruptedTurnID: a.Source.TurnID, Expired: a.Source.Pending}
	}
	if a.Source.Provider != aiModeClaude || a.Source.Binding.AgentUID != record.Agent || a.Target.AgentUID != record.Agent || a.Target.PaneUID != a.Source.Binding.PaneUID || a.Target.ProjectUID != a.Source.Binding.ProjectUID || a.Target.WindowUID != a.Source.Binding.WindowUID || s.Binding != a.Target || s.Provider != aiModeClaude || s.SessionID != a.Source.SessionID || s.SessionID != record.Retired.SessionID || s.ConnectionID != a.Target.OperationID || !reflect.DeepEqual(s.History, history) || agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || s.ResumeState != coremetadata.ProcessResumable || !coremetadata.MatchesProcessWait(s.Binding, pane.Status.LastTermination) || !coremetadata.MatchesProcessWait(s.Binding, agent.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
		return deferredRefused("attempt lacks exact failed writer/History/Wait proof")
	}
	attention := newProcessAttentionStore(filepath.Dir(filepath.Dir(c.messagePaths.registryPath)))
	err = attention.update(func(records map[string]processAttentionRecord) error {
		old, exists := records[a.Target.PaneUID]
		if !exists {
			return nil
		}
		if old.Provider != aiModeClaude || (old.Binding != processSchemaBinding(a.Source.Binding) && old.Binding != processSchemaBinding(a.Target)) {
			return deferredRefused("attempt attention source differs")
		}
		records[a.Target.PaneUID] = processAttentionRecord{Binding: processSchemaBinding(a.Target), Provider: aiModeClaude, Terminal: true, Pending: map[string]processAttentionPending{}}
		return nil
	})
	if err != nil {
		return err
	}
	record.Retired = *s.Clone()
	return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), record)
}

// Digest reads are outside the lock. The lock then checks the exact sidecar,
// claim authority and recipe CAS again before the resume planner can use it.
func (c *agentCommand) prepareDeferredLaunch(ctx context.Context, candidate processResumeCandidate, opts processAgentResumeOptions) (processResumeCandidate, *deferredLaunchRecord, error) {
	return c.prepareDeferredLaunchMode(ctx, candidate, opts, false)
}

// Explicit replacement validates its new planner files, never reuses old Args.
func (c *agentCommand) prepareDeferredLaunchMode(ctx context.Context, candidate processResumeCandidate, opts processAgentResumeOptions, replacement bool) (processResumeCandidate, *deferredLaunchRecord, error) {
	record, err := c.readDeferredLaunch(candidate.Agent.Metadata.UID)
	if err != nil || record == nil {
		return candidate, record, err
	}
	if !replacement {
		if err = record.validateFiles(); err != nil {
			return candidate, nil, err
		}
	}
	if opts.Model != "" && opts.Model != record.Model || opts.Effort != "" && opts.Effort != record.Effort {
		return candidate, nil, deferredRefused("pending relaunch configuration is fixed; use agent relaunch to change it")
	}
	unlock, err := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if err != nil {
		return candidate, nil, err
	}
	defer func() { unlock() }()
	if err = c.checkDeferredClaim(record.Agent, opts.claim); err != nil {
		return candidate, nil, err
	}
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return candidate, nil, err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return candidate, nil, deferredRefused("prepared launch changed")
	}
	if err = c.reconcileDeferredLaunch(ctx, record); err != nil {
		return candidate, nil, err
	}
	request, err := newProcessAgentResumeRequest(opts)
	if err != nil {
		return candidate, nil, err
	}
	record, err = c.readDeferredLaunch(record.Agent)
	if err != nil {
		return candidate, nil, err
	}
	unlock()
	unlock = func() {}
	if !replacement {
		if err = record.validateFiles(); err != nil {
			return candidate, nil, err
		}
	}
	if opts.Model != "" && opts.Model != record.Model || opts.Effort != "" && opts.Effort != record.Effort {
		return candidate, nil, deferredRefused("recovered launch override differs")
	}
	nextUnlock, lockErr := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if lockErr != nil {
		return candidate, nil, lockErr
	}
	unlock = nextUnlock
	if err = c.checkDeferredClaim(record.Agent, opts.claim); err != nil {
		return candidate, nil, err
	}
	current, err = c.readDeferredLaunch(record.Agent)
	if err != nil {
		return candidate, nil, err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return candidate, nil, deferredRefused("launch changed during snapshot validation")
	}
	candidate, err = c.processResumeCandidate(request)
	if err == nil && !record.matches(candidate) {
		err = deferredRefused("prepared launch proof changed")
	}
	return candidate, record, err
}

func (c *agentCommand) frozenDeferredCommand(candidate processResumeCandidate, record *deferredLaunchRecord) (processhost.Command, error) {
	ai, ok := c.ai.(*aiCommand)
	if !ok {
		return processhost.Command{}, deferredRefused("process launcher unavailable")
	}
	if err := ai.RequireAgentEnabled(aiModeClaude); err != nil {
		return processhost.Command{}, err
	}
	fresh, err := ai.PlanProcessClaudeCommand(candidate.Agent.Spec.Workspace, processClaudeLaunchOptions{})
	if err != nil {
		return fresh, err
	}
	if fresh.Path != record.Command.Path {
		return fresh, deferredRefused("resolved provider path changed")
	}
	command := record.Command
	command.Env = fresh.Env
	return command, nil
}

// Success consumes only this prepared launch after verified same-session init.
// Failure retains the new recipe and readdresses only an exactly reaped writer.
func (c *agentCommand) finishDeferredLaunch(record *deferredLaunchRecord, result processAgentResumeResult, success bool) error {
	unlock, err := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if err != nil {
		return err
	}
	defer unlock()
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return deferredRefused("launch changed during provider start")
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || pane.Status.ProcessSession == nil || !reflect.DeepEqual(agent.Spec, record.NewSpec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.NewAnnotations) {
		return deferredRefused("new launch recipe changed")
	}
	session := pane.Status.ProcessSession
	if session.SessionID != record.Retired.SessionID || session.Provider != aiModeClaude {
		return deferredRefused("new launch conversation differs")
	}
	if success {
		if session.Binding != metadataProcessBinding(result.Binding) || pane.Status.Activation.Process == nil {
			return deferredRefused("new launch has no exact init")
		}
		return removeDeferredState(c.deferredStatePath("deferred-launches", record.Agent))
	}
	if processResumeRecordEqual(session, &record.Retired) {
		return nil
	}
	if session.Binding != metadataProcessBinding(result.Binding) || agent.Status.Phase != coremetadata.PhaseOffline || session.ResumeState != coremetadata.ProcessResumable || !coremetadata.MatchesProcessWait(session.Binding, pane.Status.LastTermination) {
		return deferredRefused("failed launch has no exact Wait; inspect Agent before reclaiming")
	}
	return c.reconcileDeferredAttempt(record)
}

// Prompt intents never grant writer authority. Exact failed reservation proof
// precedes configuration rollback and restoration of the immediate prior L.
func (c *agentCommand) reconcilePromptRelaunch(record *deferredLaunchRecord) error {
	prior := record.Previous
	a := record.Attempt
	if a == nil || a.Source.Provider != aiModeClaude || a.Source.Binding.AgentUID != record.Agent || a.Target.AgentUID != record.Agent || a.Target.PaneUID != a.Source.Binding.PaneUID || a.Target.ProjectUID != a.Source.Binding.ProjectUID || a.Target.WindowUID != a.Source.Binding.WindowUID || a.Target.HostInstanceID == a.Source.Binding.HostInstanceID || a.Target.Generation == a.Source.Binding.Generation || a.Target.OperationID == a.Source.Binding.OperationID {
		return deferredRefused("prompt planned target differs")
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || pane.Status.ProcessSession == nil || prior.Previous != nil || !processResumeRecordEqual(&prior.Retired, &record.Attempt.Source) || !reflect.DeepEqual(prior.NewSpec, record.OldSpec) || !reflect.DeepEqual(prior.NewAnnotations, record.OldAnnotations) {
		return deferredRefused("prompt intent source differs")
	}
	old := reflect.DeepEqual(agent.Spec, record.OldSpec) && reflect.DeepEqual(agent.Metadata.Annotations, record.OldAnnotations)
	if processResumeRecordEqual(pane.Status.ProcessSession, &record.Attempt.Source) {
		if !old || agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || !coremetadata.MatchesProcessWait(prior.Retired.Binding, pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
			return deferredRefused("prompt source rollback lacks exact Wait")
		}
		return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), prior)
	} else {
		proof := *record
		if old {
			proof.NewSpec, proof.NewAnnotations = record.OldSpec, record.OldAnnotations
		}
		// Reuse the complete owner65 History, full binding, both Wait and attention CAS.
		if err = c.reconcileDeferredAttempt(&proof); err != nil {
			return err
		}
		record.Retired = proof.Retired
		_, err = c.rebind.create.store.update(func(current *coremetadata.Registry) error {
			a, ok := current.Agent(record.Agent)
			p, multiple := processResumePane(*current, record.Agent)
			if !ok || multiple || p == nil || !processResumeRecordEqual(p.Status.ProcessSession, &proof.Retired) || !reflect.DeepEqual(a.Spec, proof.NewSpec) || !reflect.DeepEqual(a.Metadata.Annotations, proof.NewAnnotations) {
				return deferredRefused("prompt rollback source changed")
			}
			a.Spec, a.Metadata.Annotations = record.OldSpec, maps.Clone(record.OldAnnotations)
			return nil
		})
		if err != nil {
			return err
		}
	}
	prior.Retired, prior.Attempt = record.Retired, record.Attempt
	return writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), prior)
}

func (c *agentCommand) restorePromptRelaunch(record *deferredLaunchRecord) error {
	unlock, err := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if err != nil {
		return err
	}
	defer unlock()
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return deferredRefused("prompt intent changed during start")
	}
	return c.reconcilePromptRelaunch(record)
}
