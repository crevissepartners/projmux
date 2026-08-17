package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// canonicalCreateAgent is the spelling the provider shortcuts normalize onto.
const canonicalCreateAgent = "create agent"

// agentWork is one allocated Agent plus its managed Pane, waiting for the
// runtime phase to give the Pane a live tmux binding.
type agentWork struct {
	target     paneTarget
	windowName string
	agent      coremetadata.Agent
	pane       coremetadata.Pane
}

// runResourceAgent answers the canonical resource-backed `create agent` and the
// three provider shortcuts.
//
// It is `create pane --project` with two substitutions, which is the point:
// the Window fan-out, the split anchor, the Window ensure, the operation ledger
// and the rollback are the ones Phase 6 shipped, not a second implementation.
// What differs is the metadata it allocates -- a Window-owned Agent plus the
// Agent-owned managed Pane, rather than a Window-owned shell Pane -- and the
// command the detached split runs, which is the provider launch instead of the
// raw payload.
//
// Three properties are load bearing and are asserted rather than assumed:
//
//   - The Agent is always new. There is no lookup of an existing Agent of the
//     same provider anywhere on this path; rebinding an existing conversation is
//     `agent resume`, which is a different verb with a different cardinality.
//   - The name never depends on the work. The provider id is the only name seed,
//     so the topic, the prompt, and the payload after `--` cannot reach it.
//   - Nothing moves the client. The split goes through the materializer, which
//     owns `-d`; the focus-following legacy split is not on this path at all.
//
// shortcutProvider is empty for the canonical spelling and carries the provider
// for `create codex|claude|antigravity`.
func (c *createCommand) runResourceAgent(shortcutProvider string, args []string, stdout, stderr io.Writer) error {
	spelling := canonicalCreateAgent
	if shortcutProvider != "" {
		spelling = "create " + shortcutProvider
	}

	flags, err := parseResourceCreateFlags(spelling, args, stderr, resourceCreateShape{split: true, provider: true})
	if err != nil {
		return err
	}
	provider, err := c.resolveCreateProvider(spelling, shortcutProvider, flags)
	if err != nil {
		return err
	}
	if c.agents == nil {
		return errors.New("create agent: the provider launcher is not configured")
	}
	// The Settings gate applies to the canonical route too: spelling the command
	// differently does not re-enable a provider the operator switched off. It
	// runs before the store is opened, so a disabled provider costs zero
	// mutations and zero bytes of stdout.
	if err := c.agents.RequireAgentEnabled(provider); err != nil {
		return err
	}
	mode, err := c.resolveProjection(spelling, flags.output)
	if err != nil {
		return err
	}
	labels, err := labelMap(flags.labels)
	if err != nil {
		return MapMetadataError(err)
	}

	var results []createResult
	if err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
		project, err := c.resolveProject(*working, flags)
		if err != nil {
			return err
		}
		if err := c.refuseMissingRoot(project); err != nil {
			return err
		}

		// The launch is constructed before anything is allocated. A missing
		// provider binary is the most likely failure on this route, and it has
		// to land while the operation still owns nothing.
		title, launchArgv, err := c.agents.PlanAgentLaunch(provider, project.Spec.Root, flags.payload)
		if err != nil {
			return err
		}

		// The declared <create, Agent> cell is this route's fan-out cardinality:
		// one Agent per resolved target Window, at least one overall. It is the
		// Agent row rather than the Window row because this route never resolves
		// an existing Agent -- rebinding a conversation is `agent resume` -- so
		// the only Agent count it can fix is the one it produces.
		plan, windows, err := c.resolveSplitTargets(working, mutator, project, flags,
			selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindAgent}, spelling, operationID)
		if err != nil {
			return err
		}

		// Metadata phase. Every Agent and every managed Pane is allocated before
		// the first tmux call, so an explicit --name that collides inside a
		// target Window refuses with zero runtime objects created.
		agents := make([]agentWork, 0, len(plan.targets))
		for _, target := range plan.targets {
			window, ok := working.Window(target.windowUID)
			if !ok {
				return fmt.Errorf("%s: window %q disappeared during preflight", spelling, target.windowUID)
			}
			agent, err := mutator.CreateAgent(working, target.windowUID, coremetadata.CreateAgentOptions{
				// The explicit --name names the Agent. The managed Pane derives
				// its own name from the Agent's, so one flag cannot name two
				// resources.
				Name:        flags.name,
				Provider:    provider,
				Labels:      labels,
				OperationID: operationID,
			})
			if err != nil {
				return MapMetadataError(err)
			}
			pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{
				CWD:    project.Spec.Root,
				Labels: labels,
			}, operationID)
			if err != nil {
				return MapMetadataError(err)
			}
			agents = append(agents, agentWork{
				target:     target,
				windowName: window.Metadata.Name,
				agent:      agent,
				pane:       pane,
			})
		}

		// Runtime phase.
		sessionName, err := c.ensureProjectRuntime(ctx, working, mutator, project, ledger)
		if err != nil {
			return err
		}
		for i := range windows {
			if err := c.materializeWindow(ctx, ledger, project, sessionName, &windows[i]); err != nil {
				return err
			}
		}
		for _, work := range agents {
			anchorPaneID, err := c.ensureAnchorPane(ctx, *working, ledger, project, sessionName, work.target)
			if err != nil {
				return err
			}
			paneID, err := c.runtime.splitPane(ctx, anchorPaneID, flags.placement, project.Spec.Root, launchArgv)
			if paneID != "" {
				ledger.record(runtimePane, paneID, work.pane.Metadata.UID)
				if mirrorErr := c.runtime.mirror.MirrorPane(ctx, paneID, work.pane); mirrorErr != nil {
					return errors.Join(err, mirrorErr)
				}
			}
			if err != nil {
				return err
			}
			// The managed-pane options are what make this pane an agent pane to
			// the statusbar, the attention tracker, and the notification
			// pipeline. They are applied after the pane exists and before the
			// result is reported.
			c.agents.BindManagedAgentPane(paneID, provider, project.Spec.Root, title)
			results = append(results, createResult{
				kind: coremetadata.KindAgent,
				uid:  work.agent.Metadata.UID,
				name: work.agent.Metadata.Name,
				// `-o pane-id` is the managed Pane's raw transport handle, which
				// is what the existing pane launchers and skill bridges consume.
				paneID:      paneID,
				projectName: project.Metadata.Name,
				windowName:  work.windowName,
				windowUID:   work.target.windowUID,
			})
		}
		return nil
	}); err != nil {
		return err
	}
	return c.writeResults(stdout, spelling, mode, coremetadata.KindAgent, results)
}

// resolveCreateProvider fixes the provider of one canonical Agent create.
//
// The canonical spelling requires an explicit `--provider`; the saved split mode
// is deliberately not consulted, because a canonical route whose result depends
// on hidden state is not canonical. A shortcut already names its provider, so
// respelling it is a usage error rather than a silent winner.
func (c *createCommand) resolveCreateProvider(spelling, shortcutProvider string, flags resourceCreateFlags) (string, error) {
	if shortcutProvider == "" {
		return requireCanonicalProvider(spelling, flags.provider)
	}
	if flags.providerSet {
		return "", usageError(fmt.Sprintf(
			"%s already names the provider; drop --provider or use `projmux create agent --provider %s`",
			spelling, strings.TrimSpace(flags.provider)))
	}
	return shortcutProvider, nil
}
