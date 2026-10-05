package app

import (
	"os"
	"path/filepath"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// updateProcessAgentSession owns both the process mutation and its durable
// conversation pointer. A confirmed processSession is the writer's evidence;
// readers never repair it. History follows the same post-commit rule as tmux.
// update preserves command-injected stores; nil selects the owned disk store.
func updateProcessAgentSession(path string, binding processhost.Binding, observation *coremetadata.AgentSessionObservation, missingOnly, confirmed bool, update func(func(*coremetadata.Registry) error) error, mutation func(*coremetadata.Registry) error) error {
	if update == nil {
		update = func(fn func(*coremetadata.Registry) error) error {
			_, _, err := intmetadata.NewStore(path).UpdateConvergent(fn)
			return err
		}
	}
	var history sessionhistory.Record
	var recordHistory bool
	err := update(func(working *coremetadata.Registry) error {
		if err := mutation(working); err != nil {
			return err
		}
		agent, found := working.Agent(binding.Agent)
		pane, present := working.Pane(binding.Pane)
		if !found || !present || agent.Status.PaneRef != binding.Pane || pane.Metadata.OwnerUID() != binding.Agent || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != metadataProcessBinding(binding) {
			return processhost.ErrStale
		}
		if missingOnly && agent.Status.SessionRef != nil {
			return nil
		}
		record := pane.Status.ProcessSession
		// A failed resumed generation can have no initialized stream of its
		// own while retaining the prior, established conversation.
		if !confirmed && record.History == nil {
			return nil
		}
		if record.ConnectionID == "" || (record.SessionID == "" && record.ThreadID == "") {
			return nil
		}
		obs := coremetadata.AgentSessionObservation{Provider: record.Provider, SessionID: record.SessionID, ThreadID: record.ThreadID}
		if observation != nil {
			if observation.Provider != obs.Provider || observation.SessionID != obs.SessionID || observation.ThreadID != obs.ThreadID {
				return processhost.ErrStale
			}
			obs = *observation
		}
		if obs.Provider == aiModeClaude && obs.TranscriptPath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			projects, err := sessionhistory.ClaudeProjectsDir(os.Getenv("CLAUDE_CONFIG_DIR"), home)
			if err != nil {
				return err
			}
			obs.TranscriptPath = filepath.Join(projects, aisessions.EncodeClaudeProjectPath(agent.Spec.Workspace.CWD), obs.SessionID+".jsonl")
		}
		previous := agent.Status.SessionRef.Clone()
		// Resume of the same conversation retains its original observation and
		// metadata. Only first binding may enrich a previously bare Codex ref.
		if previous != nil && ((previous.Claude != nil && previous.Claude.SessionID == obs.SessionID) || (previous.Codex != nil && previous.Codex.ThreadID == obs.ThreadID && (previous.Codex.Endpoint != nil || obs.Endpoint == nil))) {
			return nil
		}
		bound, changed, err := intmetadata.DefaultMutator().RecordAgentSessionRef(working, binding.Agent, obs)
		if err != nil {
			return err
		}
		history, recordHistory = claudeSessionHistoryRecord(working, binding.Agent, historyConversationChanged(changed, previous, bound.Status.SessionRef), bound.Status.SessionRef)
		return nil
	})
	if err != nil {
		return err
	}
	stateDir := filepath.Dir(filepath.Dir(path))
	creator := &createCommand{store: &resourceStore{stateDir: func() (string, error) { return stateDir, nil }}}
	creator.recordCreateAgentSessionHistory(history, recordHistory, nil)
	return nil
}
