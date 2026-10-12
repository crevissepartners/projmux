package runtime

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/controller"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func ControllerRuntimeMutationKind(write controller.Action) string {
	switch write.Scope {
	case resourcegraph.ObjectSession:
		return "session"
	case resourcegraph.ObjectWindow:
		return "window"
	case resourcegraph.ObjectPane:
		return "pane"
	}
	switch {
	case strings.HasPrefix(write.Target, "$"):
		return "session"
	case strings.HasPrefix(write.Target, "@"):
		return "window"
	default:
		return "pane"
	}
}

func ControllerRuntimeMutationUIDField(kind string) string {
	switch kind {
	case "session":
		return tmuxopts.ProjectUIDSession
	case "window":
		return tmuxopts.WindowUID
	case "pane":
		return tmuxopts.PaneUID
	default:
		return ""
	}
}

type ControllerRuntimeMutationFieldClass string

const (
	ControllerRuntimeMutationManaged      ControllerRuntimeMutationFieldClass = "managed"
	ControllerRuntimeMutationPresentation ControllerRuntimeMutationFieldClass = "presentation-exempt"
	ControllerRuntimeMutationForward      string                              = "forward"
	ControllerRuntimeMutationOwnedReverse string                              = "owned-reverse"
)

var ControllerRuntimeMutationManagedFields = map[string][]string{
	"session": {tmuxopts.ProjectUIDSession, tmuxopts.ProjectNameSession, tmuxopts.ProjectPathSession},
	"window":  {tmuxopts.WindowUID, tmuxopts.AutomaticRenameWindow, tmuxopts.WindowName, "window_name"},
	"pane": {
		tmuxopts.PaneUID, tmuxopts.PaneName, tmuxopts.AgentSessionIDPane, tmuxopts.AgentThreadIDPane,
		tmuxopts.PaneOwnerKind, tmuxopts.PaneOwnerUID, tmuxopts.PaneRole, tmuxopts.AgentUIDPane,
		tmuxopts.AgentProviderPane,
	},
}

var ControllerRuntimeMutationPresentationFields = []string{
	AiPaneTopicOption, AiPaneTopicManualOption, AiPaneStateOption, AiPaneBadgeKindOption, AttentionStateOption,
}

func ControllerRuntimeMutationFieldClassFor(kind, field string) (ControllerRuntimeMutationFieldClass, bool) {
	if slices.Contains(ControllerRuntimeMutationManagedFields[kind], field) {
		return ControllerRuntimeMutationManaged, true
	}
	if kind == "pane" && slices.Contains(ControllerRuntimeMutationPresentationFields, field) {
		return ControllerRuntimeMutationPresentation, true
	}
	return "", false
}

// validateControllerRuntimeMutationArgv is the closed executable grammar for
// the controller adapter. A declared field/effect cannot select a different
// scope, target, option, value, or tmux verb at execution time.
func ValidateControllerRuntimeMutationArgv(write controller.Action, mode string) error {
	if mode != ControllerRuntimeMutationForward && mode != ControllerRuntimeMutationOwnedReverse {
		return fmt.Errorf("controller write %q has unknown execution mode %q", write.Key, mode)
	}
	kind := ControllerRuntimeMutationKind(write)
	class, ok := ControllerRuntimeMutationFieldClassFor(kind, write.Field)
	if !ok {
		return fmt.Errorf("controller write %q field %q is outside the closed %s option set", write.Key, write.Field, kind)
	}
	if write.Field == "window_name" {
		want := []string{"rename-window", "-t", write.Target, "--", write.After}
		if kind != "window" || strings.TrimSpace(write.After) == "" || !slices.Equal(write.Args, want) {
			return fmt.Errorf("controller write %q rename declaration disagrees with executable argv", write.Key)
		}
		return nil
	}
	if write.Field == tmuxopts.AutomaticRenameWindow {
		switch strings.ToLower(strings.TrimSpace(write.After)) {
		case "", "0", "1", "off", "on":
		default:
			return fmt.Errorf("controller write %q automatic-rename value %q is not closed", write.Key, write.After)
		}
	}
	if mode == ControllerRuntimeMutationForward {
		switch write.Field {
		case tmuxopts.AgentSessionIDPane, tmuxopts.AgentThreadIDPane:
			if write.After != "" {
				return fmt.Errorf("controller write %q may only clear the L8 conversation index %q", write.Key, write.Field)
			}
		default:
			if class == ControllerRuntimeMutationManaged && write.Field != ControllerRuntimeMutationUIDField(kind) && write.After == "" {
				return fmt.Errorf("controller write %q may not unset managed field %q outside owned rollback", write.Key, write.Field)
			}
		}
	}
	want := []string{"set-option"}
	switch kind {
	case "window":
		want = append(want, "-w")
	case "pane":
		want = append(want, "-p")
	}
	if write.After == "" {
		want = append(want, "-u", "-t", write.Target, write.Field)
	} else {
		want = append(want, "-t", write.Target)
		if write.Field != tmuxopts.AutomaticRenameWindow && class != ControllerRuntimeMutationPresentation {
			want = append(want, "-q")
		}
		want = append(want, write.Field, write.After)
	}
	if !slices.Equal(write.Args, want) {
		return fmt.Errorf("controller write %q declared %s=%q disagrees with executable argv %v (want %v)", write.Key, write.Field, write.After, write.Args, want)
	}
	return nil
}

func ValidateControllerRuntimeMutationPlanAction(action PlannedRuntimeMutation) error {
	if action.Controller == nil {
		return fmt.Errorf("runtime mutation plan: controller target %q has no typed declared effect", action.Target.ID)
	}
	declared := action.Controller
	write := controller.Action{
		Key: action.Target.UID, Scope: resourcegraph.ObjectKind(declared.Scope), Target: action.Target.ID,
		Field: declared.Field, Before: declared.Before, After: declared.After,
	}
	switch action.Verb {
	case MutationWriteIdentity, MutationWriteOption, MutationWritePresentationOption:
		write.Args = append([]string{"set-option"}, action.Operands...)
	case MutationRenameWindow:
		write.Args = append([]string{"rename-window"}, action.Operands...)
	default:
		return fmt.Errorf("runtime mutation plan: controller effect uses unsupported action %q", action.Verb)
	}
	if ControllerRuntimeMutationKind(write) != action.Target.Kind {
		return fmt.Errorf("runtime mutation plan: controller scope %q disagrees with target kind %q", declared.Scope, action.Target.Kind)
	}
	if err := ValidateControllerRuntimeMutationArgv(write, declared.Mode); err != nil {
		return fmt.Errorf("runtime mutation plan: %w", err)
	}
	class, ok := ControllerRuntimeMutationFieldClassFor(action.Target.Kind, declared.Field)
	if !ok || declared.Class != string(class) {
		return fmt.Errorf("runtime mutation plan: controller field %q has mismatched semantic class %q", declared.Field, declared.Class)
	}
	wantVerb := MutationWriteOption
	switch {
	case declared.Field == ControllerRuntimeMutationUIDField(action.Target.Kind):
		wantVerb = MutationWriteIdentity
	case declared.Field == "window_name":
		wantVerb = MutationRenameWindow
	case class == ControllerRuntimeMutationPresentation:
		wantVerb = MutationWritePresentationOption
	}
	if action.Verb != wantVerb {
		return fmt.Errorf("runtime mutation plan: controller field %q requires action %q, got %q", declared.Field, wantVerb, action.Verb)
	}
	uidField := ControllerRuntimeMutationUIDField(action.Target.Kind)
	if uidField == "" || strings.TrimSpace(action.Target.UID) == "" {
		return errors.New("runtime mutation plan: controller action has no exact kind UID")
	}
	return nil
}
