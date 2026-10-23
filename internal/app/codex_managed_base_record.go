package app

import (
	"errors"
	"fmt"
	"maps"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// managedCodexRecipeKeys are the annotations a managed base confirmation
// reads or writes: the instruction components and the managed digest/status.
var managedCodexRecipeKeys = []string{
	coremetadata.AnnotationAgentPersona,
	coremetadata.AnnotationAgentPersonaDigest,
	coremetadata.AnnotationAgentInstructionsSource,
	coremetadata.AnnotationAgentGuidanceDigest,
	coremetadata.AnnotationAgentProjectGuidanceDigest,
	coremetadata.AnnotationAgentProjectLinkRulesDigest,
	coremetadata.AnnotationAgentSystemPromptSnapshot,
	mutationruntime.CodexManagedBaseDigest,
	mutationruntime.CodexManagedBaseMode,
	mutationruntime.CodexManagedBaseStatus,
}

func managedCodexRecipe(annotations map[string]string) map[string]string {
	out := map[string]string{}
	for _, key := range managedCodexRecipeKeys {
		if value, ok := annotations[key]; ok {
			out[key] = value
		}
	}
	return out
}

// managedCodexBaseOwner is the exact execution generation and reserved recipe
// that one create/resume/relaunch applied. It is captured after reservation
// and before the provider starts, so a later generation or recipe is never
// confused with this operation's accepted request.
type managedCodexBaseOwner struct {
	binding processhost.Binding
	recipe  map[string]string
}

func captureManagedCodexBaseOwner(registryPath string, binding processhost.Binding) (managedCodexBaseOwner, error) {
	reg, err := intmetadata.NewStore(registryPath).LoadReadOnly()
	if err != nil {
		return managedCodexBaseOwner{}, err
	}
	agent, ok := reg.Agent(binding.Agent)
	if !ok {
		return managedCodexBaseOwner{}, errors.New("managed base Agent missing")
	}
	return managedCodexBaseOwner{binding: binding, recipe: managedCodexRecipe(agent.Metadata.Annotations)}, nil
}

// recordManagedCodexBase confirms an accepted managed base only while the
// Registry still shows the generation, thread and reserved recipe this
// operation applied. A late confirmation is refused as stale and leaves the
// current generation's annotations unchanged; the caller keeps its existing
// failure handling for its own child.
func recordManagedCodexBase(registryPath string, base *mutationruntime.ManagedCodexBase, owner managedCodexBaseOwner, endpoint *codexProcessEndpoint, fresh bool) error {
	if base == nil {
		return nil
	}
	_, _, err := intmetadata.NewStore(registryPath).UpdateConvergent(func(reg *coremetadata.Registry) error {
		return confirmManagedCodexBase(reg, base, owner, endpoint, fresh)
	})
	return err
}

// confirmManagedCodexBase is the Registry transaction body: the ownership
// and recipe check precede any managed annotation write.
func confirmManagedCodexBase(reg *coremetadata.Registry, base *mutationruntime.ManagedCodexBase, owner managedCodexBaseOwner, endpoint *codexProcessEndpoint, fresh bool) error {
	if err := managedCodexBaseCurrent(reg, owner, endpoint); err != nil {
		return err
	}
	return base.Record(reg, owner.binding.Agent, fresh)
}

func managedCodexBaseCurrent(reg *coremetadata.Registry, owner managedCodexBaseOwner, endpoint *codexProcessEndpoint) error {
	if endpoint == nil || endpoint.binding != owner.binding {
		return fmt.Errorf("%w: managed base endpoint does not belong to this operation", processhost.ErrStale)
	}
	want := metadataProcessBinding(owner.binding)
	agent, found := reg.Agent(owner.binding.Agent)
	pane, present := reg.Pane(owner.binding.Pane)
	if !found || !present || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != owner.binding.Pane || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != want || pane.Status.Activation.Process == nil || pane.Status.Activation.Process.Binding != want {
		return fmt.Errorf("%w: managed base generation was replaced", processhost.ErrStale)
	}
	if thread := endpoint.evidence.ThreadID; thread == "" || pane.Status.ProcessSession.ThreadID != thread {
		return fmt.Errorf("%w: managed base thread changed", processhost.ErrStale)
	}
	if !maps.Equal(managedCodexRecipe(agent.Metadata.Annotations), owner.recipe) {
		return fmt.Errorf("%w: managed base recipe changed", processhost.ErrStale)
	}
	return nil
}
