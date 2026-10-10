package app

import (
	"bytes"
	"context"
	"maps"
	"os"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProjectProfilePreviousRecoveryRefusesWithoutChangingState(t *testing.T) {
	for _, action := range []string{"global-recovery", "prepare-again", "first-input"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			for key, value := range map[string]string{"HOME": root, "XDG_CONFIG_HOME": root + "/config", "XDG_STATE_HOME": root + "/state", "XDG_CACHE_HOME": root + "/cache"} {
				t.Setenv(key, value)
			}
			c := New().agent
			creator := c.rebind.create
			paths, err := configPaths(creator.homeDir, creator.lookupEnv)
			if err != nil {
				t.Fatal(err)
			}
			profiles := profile.NewDefaultStore(paths)
			entry, err := profiles.Write("prior", []byte("provider = \"claude\"\n"))
			if err != nil {
				t.Fatal(err)
			}
			reg := processResumeQueryFixture(t)
			candidate := listResumableProcessAgents(reg, processResumeFilter{})[0]
			source := *candidate.Record.Clone()
			target := source.Binding
			target.HostInstanceID, target.Generation, target.OperationID = "next-host", "next-generation", "next-operation"
			agent, _ := reg.Agent(source.Binding.AgentUID)
			pane, _ := reg.Pane(source.Binding.PaneUID)
			agent.Spec.Workspace.CWD, pane.Spec.CWD = root, root
			old := map[string]string{coremetadata.AnnotationAgentProfile: "prior", coremetadata.AnnotationAgentProfileDigest: entry.Digest}
			agent.Metadata.Annotations = map[string]string{coremetadata.AnnotationAgentModel: "new-model"}
			prior := &deferredLaunchRecord{
				Version: 1, Agent: agent.Metadata.UID, Retired: source,
				OldSpec: agent.Spec, NewSpec: agent.Spec, OldAnnotations: maps.Clone(old), NewAnnotations: maps.Clone(old),
				Command: processhost.Command{Path: "/bin/true", Dir: root, Args: []string{"--permission-mode", "auto"}}, Files: map[string]string{},
			}
			intent := *prior
			intent.NewAnnotations = maps.Clone(agent.Metadata.Annotations)
			intent.Previous, intent.Attempt = prior, &deferredLaunchAttempt{Source: source, Target: target}
			// This is an exactly retired failed prompt writer, with the source
			// conversation/history and both matching Wait receipts for recovery.
			pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{
				Provider: aiModeClaude, Binding: target, SessionID: source.SessionID, ConnectionID: target.OperationID, ResumeState: coremetadata.ProcessResumable,
				History: &coremetadata.ProcessResumeHistory{Binding: source.Binding, SessionID: source.SessionID, InterruptedTurnID: source.TurnID, Expired: source.Pending},
			}
			code := 1
			receipt := &coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationAbnormal, ObservedAt: time.Now().UTC(), PaneUID: target.PaneUID, AgentUID: target.AgentUID, Generation: target.Generation, OperationID: target.OperationID, ExitCode: &code}
			pane.Status.LastTermination, agent.Status.LastTermination = receipt.Clone(), receipt.Clone()
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
			state, err := creator.store.stateDir()
			if err != nil {
				t.Fatal(err)
			}
			store := intmetadata.NewStore(intmetadata.PathFor(state))
			if _, _, err := store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg; return nil }); err != nil {
				t.Fatal(err)
			}
			launchPath := c.deferredStatePath("deferred-launches", agent.Metadata.UID)
			if err := writeDeferredState(launchPath, &intent); err != nil {
				t.Fatal(err)
			}
			opts := processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID}}
			if action == "global-recovery" {
				input := processResumeCandidate{Agent: agent.Clone(), Pane: pane.Clone(), Record: *pane.Status.ProcessSession.Clone()}
				_, pending, err := c.prepareDeferredLaunch(context.Background(), input, opts)
				after, loadErr := store.LoadReadOnly()
				restored, _ := after.Agent(agent.Metadata.UID)
				if err != nil || loadErr != nil || pending == nil || pending.Previous != nil || restored.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != "prior" {
					t.Fatalf("global recovery: pending=%+v err=%v load=%v", pending, err, loadErr)
				}
				return
			}
			path, _ := profiles.Path("prior")
			if err := os.WriteFile(path, []byte("project = \""+profileProjectQ+"\"\nprovider = \"claude\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			beforeRegistry, err := os.ReadFile(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			beforeLaunch, err := os.ReadFile(launchPath)
			if err != nil {
				t.Fatal(err)
			}
			if action == "prepare-again" {
				_, err = c.prepareOwnedClaudeResume(context.Background(), opts)
			} else {
				opts.Prompt = processResumeFirstFrame{Kind: "user", Text: "must not restore foreign profile"}
				_, err = c.resumeProcessAgent(context.Background(), processAgentResumeRequest{options: opts})
			}
			if profile.ReasonOf(err) != profile.ReasonOutOfScope {
				t.Fatalf("scope refusal: %v", err)
			}
			afterRegistry, registryErr := os.ReadFile(store.Path())
			afterLaunch, launchErr := os.ReadFile(launchPath)
			if registryErr != nil || launchErr != nil || !bytes.Equal(beforeRegistry, afterRegistry) || !bytes.Equal(beforeLaunch, afterLaunch) {
				t.Fatalf("scope refusal changed Registry/annotations or Prepared bytes: registry=%v launch=%v", registryErr, launchErr)
			}
		})
	}
}
