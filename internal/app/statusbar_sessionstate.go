package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

type statusbarSessionStateView struct {
	Session    string
	Source     string
	Autosave   sessionStateEffectiveToggle
	Snapshot   sessionstate.Snapshot
	SessionErr error
	StoreErr   error
	LoadErr    error
}

func statusbarSessionStateToggleText(toggle sessionStateEffectiveToggle) string {
	mode := strings.TrimSpace(string(toggle.Mode))
	if mode == "" {
		mode = string(config.SessionStateToggleOn)
	}
	source := strings.TrimSpace(toggle.Source)
	if source == "" {
		return mode
	}
	return mode + " (" + source + ")"
}

func statusbarSessionStateSourceText(state statusbarSessionStateView) string {
	if source := strings.TrimSpace(state.Source); source != "" {
		return source
	}
	if state.LoadErr == nil {
		return state.Snapshot.SourceLabel()
	}
	return sessionstate.SourceAutosave
}

func statusbarSessionStateSavedText(savedAt, now time.Time) string {
	if savedAt.IsZero() {
		return "-"
	}
	text := savedAt.Local().Format("2006-01-02 15:04:05 MST")
	age := statusbarSessionStateAge(savedAt, now)
	if age >= time.Second {
		text += " (" + usagecmd.FormatBackoffDuration(age.Round(time.Second)) + " ago)"
	}
	return text
}

func statusbarSessionStateAge(savedAt, now time.Time) time.Duration {
	if savedAt.IsZero() || now.IsZero() || savedAt.After(now) {
		return 0
	}
	return now.Sub(savedAt)
}

func statusbarSessionStatePaneCount(snap sessionstate.Snapshot) int {
	count := 0
	for _, window := range snap.Windows {
		count += len(window.Panes)
	}
	return count
}

func statusbarSessionStatePanePreview(savedAt time.Time, windowIndex int, pane sessionstate.Pane) string {
	recipe := pane.Recipe
	kind := strings.TrimSpace(recipe.Kind)
	if kind == "" {
		kind = "unknown"
	}
	detail := ""
	switch kind {
	case sessionstate.RecipeKindAgent:
		detail = strings.TrimSpace(recipe.Agent)
		if resumeID := strings.TrimSpace(recipe.ResumeID); resumeID != "" {
			detail += " resume " + resumeID
		}
		if health := sessionStateResumeHealthText(recipe, savedAt); health != "" {
			if detail != "" {
				detail += " "
			}
			detail += health
		}
		if topic := strings.TrimSpace(recipe.Topic); topic != "" {
			detail += " topic " + topic
		}
	case sessionstate.RecipeKindStartup:
		detail = strings.TrimSpace(recipe.Command)
	case sessionstate.RecipeKindShell:
		detail = filepath.Base(strings.TrimSpace(pane.CWD))
	}
	if detail == "." || detail == string(filepath.Separator) {
		detail = strings.TrimSpace(pane.CWD)
	}
	detail = statusbarSessionStateClean(detail)
	if title := statusbarSessionStateClean(pane.Title); title != "" {
		fallback := kind
		if detail != "" {
			fallback += " " + detail
		}
		return fmt.Sprintf("pane %d.%d  %s  %s", windowIndex, pane.Index, title, fallback)
	}
	if detail == "" {
		return fmt.Sprintf("pane %d.%d  %s", windowIndex, pane.Index, kind)
	}
	return fmt.Sprintf("pane %d.%d  %s  %s", windowIndex, pane.Index, kind, detail)
}

func statusbarSessionStateErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "unknown"
	}
	return statusbarSessionStateClip(msg, 88)
}

func statusbarSessionStateClean(value string) string {
	value = strings.TrimSpace(value)
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func statusbarSessionStateClip(value string, cols int) string {
	value = statusbarSessionStateClean(value)
	if cols <= 0 || projmuxpicker.VisibleLen(value) <= cols {
		return value
	}
	const suffix = "..."
	limit := max(cols-projmuxpicker.VisibleLen(suffix), 1)
	var out strings.Builder
	width := 0
	for _, r := range value {
		rw := projmuxpicker.RuneWidth(r)
		if width+rw > limit {
			break
		}
		out.WriteRune(r)
		width += rw
	}
	return strings.TrimSpace(out.String()) + suffix
}
