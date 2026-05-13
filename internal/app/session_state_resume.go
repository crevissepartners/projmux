package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

const sessionStateResumeStaleAfter = 24 * time.Hour

type sessionStateResumeHealth struct {
	Status     string
	Confidence string
	Source     string
	UpdatedAt  string
	Reason     string
}

func sessionStateRecipeResumeHealth(recipe sessionstate.Recipe, savedAt time.Time) sessionStateResumeHealth {
	if recipe.Kind != sessionstate.RecipeKindAgent {
		return sessionStateResumeHealth{}
	}
	source := strings.TrimSpace(recipe.ResumeSource)
	updatedAt := strings.TrimSpace(recipe.ResumeUpdatedAt)
	if strings.TrimSpace(recipe.ResumeID) == "" {
		return sessionStateResumeHealth{
			Status:     "unavailable",
			Confidence: "none",
			Source:     nonEmpty(source, "unknown"),
			UpdatedAt:  nonEmpty(updatedAt, "unknown"),
			Reason:     "resume id missing",
		}
	}

	health := sessionStateResumeHealth{
		Status:     "available",
		Confidence: sessionStateResumeConfidence(source),
		Source:     nonEmpty(source, "unknown"),
		UpdatedAt:  nonEmpty(updatedAt, "unknown"),
	}
	if updatedAt == "" {
		health.Status = "stale"
		health.Reason = "resume refresh timestamp missing"
		return health
	}
	parsed, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		health.Status = "stale"
		health.Reason = "resume refresh timestamp invalid"
		return health
	}
	if !savedAt.IsZero() && savedAt.Sub(parsed) > sessionStateResumeStaleAfter {
		health.Status = "stale"
		health.Reason = fmt.Sprintf("resume metadata older than %s", sessionStateResumeStaleAfter)
	}
	return health
}

func sessionStateResumeConfidence(source string) string {
	switch strings.TrimSpace(source) {
	case "session-id", "hook":
		return "high"
	case "claude-transcript", "codex-log":
		return "medium"
	case "":
		return "low"
	default:
		return "low"
	}
}

func sessionStateResumeHealthText(recipe sessionstate.Recipe, savedAt time.Time) string {
	health := sessionStateRecipeResumeHealth(recipe, savedAt)
	if health.Status == "" {
		return ""
	}
	parts := []string{
		"status " + health.Status,
		"confidence " + health.Confidence,
		"source " + health.Source,
		"updated " + health.UpdatedAt,
	}
	if health.Reason != "" {
		parts = append(parts, health.Reason)
	}
	return strings.Join(parts, " ")
}
