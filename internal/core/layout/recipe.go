package layout

import (
	"fmt"
	"strings"
)

// Recipe records how a layout preset pane is classified: a plain shell, an
// Agent, or a declarative startup command.
type Recipe struct {
	Kind     string
	Agent    string
	ResumeID string
	Topic    string
	Command  string
}

const (
	RecipeKindShell   = "shell"
	RecipeKindAgent   = "agent"
	RecipeKindStartup = "startup"
)

// ShellRecipe returns the plain-shell recipe form.
func ShellRecipe() Recipe {
	return Recipe{Kind: RecipeKindShell}
}

// AgentRecipe returns the Agent recipe form with resume metadata.
func AgentRecipe(agent, resumeID, topic string) Recipe {
	return Recipe{
		Kind:     RecipeKindAgent,
		Agent:    agent,
		ResumeID: resumeID,
		Topic:    topic,
	}
}

// StartupRecipe returns the declarative startup command recipe form.
func StartupRecipe(command string) Recipe {
	return Recipe{
		Kind:    RecipeKindStartup,
		Command: strings.TrimSpace(command),
	}
}

// Validate checks that the recipe is one of the supported forms.
func (r Recipe) Validate() error {
	switch r.Kind {
	case RecipeKindShell:
		if r.Agent != "" || r.ResumeID != "" || r.Topic != "" || r.Command != "" {
			return fmt.Errorf("%w: shell recipe cannot include agent or startup metadata", ErrInvalidPreset)
		}
	case RecipeKindAgent:
		if strings.TrimSpace(r.Agent) == "" {
			return fmt.Errorf("%w: agent recipe requires agent", ErrInvalidPreset)
		}
		if r.Command != "" {
			return fmt.Errorf("%w: agent recipe cannot include startup command", ErrInvalidPreset)
		}
	case RecipeKindStartup:
		if strings.TrimSpace(r.Command) == "" {
			return fmt.Errorf("%w: startup recipe requires command", ErrInvalidPreset)
		}
		if r.Agent != "" || r.ResumeID != "" || r.Topic != "" {
			return fmt.Errorf("%w: startup recipe cannot include agent metadata", ErrInvalidPreset)
		}
	case "":
		return fmt.Errorf("%w: recipe kind is required", ErrInvalidPreset)
	default:
		return fmt.Errorf("%w: unsupported recipe kind %q", ErrInvalidPreset, r.Kind)
	}
	return nil
}
