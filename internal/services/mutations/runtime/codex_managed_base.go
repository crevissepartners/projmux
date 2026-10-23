package runtime

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const CodexManagedBaseDigest = "projmux.io/codex-managed-base-digest"
const CodexManagedBaseMode = "projmux.io/codex-managed-base-mode"
const CodexManagedBaseStatus = "projmux.io/codex-managed-base-status"

// ManagedCodexBase keeps the last confirmed complete recipe separate from
// component annotations. Preparing a snapshot does not publish it as applied.
type ManagedCodexBase struct {
	Path                string
	Instructions        *codexappserver.ManagedInstructions
	Digest              string
	Annotations         map[string]string
	Fallback            bool
	PreviousDigest      string
	PreviousAnnotations map[string]string
}

func managedBaseStore(paths config.Paths) projectlinks.SnapshotStore {
	return projectlinks.NewSnapshotStore(filepath.Join(paths.StateDir, "codex-managed-base"))
}

func PrepareManagedCodexBase(paths config.Paths, uid, text string, annotations map[string]string, preparationErr error) (*ManagedCodexBase, error) {
	store := managedBaseStore(paths)
	out := &ManagedCodexBase{Annotations: maps.Clone(annotations)}
	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	if preparationErr == nil {
		// An explicitly disabled/empty managed composition still replaces the
		// provider base. Never omit the field and silently resurrect its defaults.
		if text == "" {
			text = "\n"
		}
		snapshot, err := store.WriteSnapshot([]byte(text))
		if err == nil {
			out.Digest = snapshot.Digest
			out.Path = snapshot.Path
		} else {
			preparationErr = err
		}
	}
	if preparationErr != nil {
		previous := annotations[CodexManagedBaseDigest]
		path, content, err := store.ReadRecordedSnapshot(previous)
		if err != nil {
			return nil, fmt.Errorf("codex-managed-base-unavailable: no confirmed managed instructions; refusing provider defaults: %w", errors.Join(preparationErr, err))
		}
		text, out.Digest, out.Fallback = string(content), previous, true
		out.Path = path
	}
	out.PreviousDigest = annotations[CodexManagedBaseDigest]
	out.PreviousAnnotations = maps.Clone(annotations)
	previous := ""
	if _, content, e := store.ReadRecordedSnapshot(out.PreviousDigest); e == nil {
		previous = string(content)
	}
	out.Instructions = &codexappserver.ManagedInstructions{Base: text, Previous: previous, Legacy: annotations[CodexManagedBaseMode] != "base" && annotations[CodexManagedBaseDigest] != out.Digest}
	return out, nil
}

// PlanManagedCodexBase uses the same assembly components as create on every
// restart. If any component fails, the entire previous recipe is retained.
func (c *GuidanceService) PlanManagedCodexBase(project metadata.Project, uid string, annotations map[string]string, settings AgentSettingsLaunch, process ...bool) (*ManagedCodexBase, error) {
	paths, err := c.Paths()
	if err != nil {
		return nil, err
	}
	guidance := c.PlanCodexAgentGuidance()
	if len(process) > 0 && process[0] {
		guidance = c.PlanProcessAgentGuidance()
	}
	guidance.Recorded = annotations[metadata.AnnotationAgentGuidanceDigest]
	links := c.PlanCodexProjectLinks(project)
	links.Recorded = annotations[metadata.AnnotationAgentProjectLinkRulesDigest]
	links.Project.Recorded = annotations[metadata.AnnotationAgentProjectGuidanceDigest]
	instructions := ""
	name := settings.Resolution.New.Instructions.Value
	var instructionsErr error
	if name != "" {
		loaded, e := persona.NewDefaultStore(paths).Load(name)
		instructionsErr = e
		// The base must carry the bytes whose digest the recipe records. A file
		// replaced after settings resolution is a component failure, so the
		// whole previous recipe is retained instead of mixing two reads.
		if e == nil && persona.Digest(loaded.Content) != settings.Resolution.InstructionsDigest {
			instructionsErr = fmt.Errorf("codex-managed-base-instructions-changed: instructions %q changed after settings resolution; the new content is not applied", name)
		} else if e == nil {
			instructions = string(loaded.Content)
		}
	}
	text := guidance.CodexDeveloperInstructions(uid, links.DeveloperInstructions(instructions))
	preparationErr := errors.Join(guidance.Unavailable, links.Unavailable, links.Project.Unavailable, instructionsErr, settings.InstructionsErr, settings.SnapshotErr)
	next := settings.LaunchAnnotations(annotations)
	next = guidance.ResumeLaunchAnnotations(links.ResumeLaunchAnnotations(next))
	base, err := PrepareManagedCodexBase(paths, uid, text, annotations, preparationErr)
	if err != nil {
		return nil, err
	}
	if !base.Fallback {
		base.Annotations = next
	}
	return base, nil
}

func (b *ManagedCodexBase) Notice() string {
	if b == nil {
		return ""
	}
	if b.Fallback || b.Instructions.Fallback {
		return "codex-managed-base-not-refreshed: continuing with the last confirmed managed instructions"
	}
	if b.Instructions.Shared {
		return "codex-managed-base-not-refreshed: application of the current base override on the shared provider is unconfirmed"
	}
	if b.Instructions.Legacy {
		return "codex-managed-base-legacy-context: current managed context appended; historical developer instructions remain; conflict resolution by the model is unverified"
	}
	return ""
}

func (b *ManagedCodexBase) Record(reg *metadata.Registry, uid string, fresh bool) error {
	if b == nil {
		return nil
	}
	if b.Instructions.Fallback {
		b.Fallback = true
		b.Digest = b.PreviousDigest
		b.Annotations = b.PreviousAnnotations
	}
	agent, ok := reg.Agent(uid)
	if !ok {
		return errors.New("managed base Agent missing")
	}
	// A prepared or rejected request is never promoted to a confirmed recipe.
	if !b.Instructions.Accepted {
		return nil
	}
	previous := maps.Clone(agent.Metadata.Annotations)
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	if !fresh && !b.Instructions.Shared {
		for _, key := range []string{metadata.AnnotationAgentPersona, metadata.AnnotationAgentPersonaDigest, metadata.AnnotationAgentInstructionsSource, metadata.AnnotationAgentGuidanceDigest, metadata.AnnotationAgentProjectGuidanceDigest, metadata.AnnotationAgentProjectLinkRulesDigest, metadata.AnnotationAgentSystemPromptSnapshot} {
			SetOrDelete(agent.Metadata.Annotations, key, b.Annotations[key])
		}
	}
	status := "base-delivered"
	if b.Fallback {
		status = "not-refreshed-fallback"
	}
	if b.Instructions.Shared {
		status = "base-unconfirmed"
	}
	if b.Instructions.Warm {
		status = "not-refreshed-loaded-thread"
	}
	if fresh {
		agent.Metadata.Annotations[CodexManagedBaseMode] = "base"
	}
	if !b.Instructions.Shared {
		agent.Metadata.Annotations[CodexManagedBaseDigest] = b.Digest
	} else if v := previous[CodexManagedBaseDigest]; v != "" {
		agent.Metadata.Annotations[CodexManagedBaseDigest] = v
	}
	agent.Metadata.Annotations[CodexManagedBaseStatus] = status
	return nil
}

// WithCodexBaseFile prepares the complete private base for a plain TUI launch.
// Only its path reaches argv; launch delivery is not provider/model confirmation.
func (l AgentGuidanceLaunch) WithCodexBaseFile(paths config.Paths, links ProjectLinksLaunch, role string) AgentGuidanceLaunch {
	managed, err := PrepareManagedCodexBase(paths, "", l.DeveloperInstructions(links.DeveloperInstructions(role)), nil, errors.Join(l.Unavailable, links.Unavailable, links.Project.Unavailable))
	if err != nil {
		l.Unavailable = err
		return l
	}
	l.SystemPromptFile, l.CodexBaseFile = managed.Path, true
	return l
}
