package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const sessionStateAutosaveEnv = "PROJMUX_SESSIONSTATE_AUTOSAVE"

type sessionStateEffectiveToggle struct {
	Mode   config.SessionStateToggle
	Source string
}

type sessionStateEffectiveInterval struct {
	Duration time.Duration
	Source   string
}

type projectSessionStateEffectiveToggle struct {
	Mode          config.SessionStateToggle
	Source        string
	ProjectMode   config.SessionStateProjectToggle
	ProjectSource string
	Global        sessionStateEffectiveToggle
}

type projectSessionStateIdentity struct {
	Project settingsProjectContext
	Session string
	Err     error
}

func (c *settingsCommand) runSessionStateSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionSessionState)
		if err != nil {
			return err
		}
		result, err := c.runPicker(options)
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		switch action {
		case settingsSessionStateAutosaveDetail:
			if err := c.runSessionStateToggleDetail("Session State - Auto-save", "Settings > Session State > Auto-save > ", func() []intpickercompat.Entry {
				autosave := c.currentSessionStateAutosave()
				interval := c.currentSessionStateAutosaveInterval()
				return c.sessionStateAutosaveDetailEntries(autosave, interval)
			}, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown session state settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runProjectSessionStateSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionProjectSessionState)
		if err != nil {
			return err
		}
		result, err := c.runPicker(options)
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		switch action {
		case settingsProjectSessionStateAutosaveDetail:
			if err := c.runProjectSessionStateAutosaveDetail(stdout, stderr); err != nil {
				return err
			}
		case settingsProjectSessionStateActionsDetail:
			if err := c.runProjectSessionStateActionsDetail(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project session state settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runSessionStateToggleDetail(title, prompt string, entries func() []intpickercompat.Entry, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-sessionstate-detail",
			Entries:    entries(),
			Title:      title,
			Prompt:     prompt,
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case action == settingsSessionStateAutosaveIntervalSet:
			if err := c.runSessionStateAutosaveIntervalTyped(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixSessionState):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown session state detail action: %s", action)
		}
	}
}

func (c *settingsCommand) runProjectSessionStateAutosaveDetail(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-sessionstate-autosave",
			Entries:    c.projectSessionStateAutosaveDetailEntries(),
			Title:      "Session State - Project auto-save",
			Prompt:     "Settings > Project > Session State > Auto-save > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case strings.HasPrefix(action, settingsActionPrefixSessionState):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project session state auto-save action: %s", action)
		}
	}
}

func (c *settingsCommand) runProjectSessionStateActionsDetail(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-sessionstate-actions",
			Entries:    c.projectSessionStateActionsDetailEntries(),
			Title:      "Session State - Project snapshot actions",
			Prompt:     "Settings > Project > Session State > Snapshot actions > ",
			Footer:     projmuxFooter("Enter: action  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case action == settingsProjectSessionStateDelete:
			confirmed, err := c.confirmProjectSessionStateDelete()
			if err != nil {
				return err
			}
			if !confirmed {
				continue
			}
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixSessionState):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project session state snapshot action: %s", action)
		}
	}
}

func (c *settingsCommand) sessionStateRootLabel() string {
	autosave := c.currentSessionStateAutosave()
	interval := c.currentSessionStateAutosaveInterval()
	desc := fmt.Sprintf("autosave %s, interval %s", autosave.Mode, formatSessionStateAutosaveInterval(interval.Duration))
	return settingsLabel(settingsGlyphOpen, settingsColorType, "Session State", desc)
}

func (c *settingsCommand) projectSessionStateRootLabel(ctx settingsProjectContext) string {
	identity := c.projectSessionStateIdentity(ctx)
	desc := "disabled - no project context"
	if identity.Err == nil {
		desc = identity.Session
	}
	return settingsLabel(settingsGlyphOpen, settingsColorType, "Session State", desc)
}

func (c *settingsCommand) projectSessionStateTitle() string {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return "Session State - Project settings unavailable"
	}
	return "Session State - " + identity.Project.Name + " settings"
}

func (c *settingsCommand) sessionStateEntries() []intpickercompat.Entry {
	autosave := c.currentSessionStateAutosave()
	interval := c.currentSessionStateAutosaveInterval()
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Auto-save", string(autosave.Mode)+" - interval "+formatSessionStateAutosaveInterval(interval.Duration)+" - "+autosave.Source),
			Value: settingsSessionStateAutosaveDetail,
		},
		{
			Label: settingsLabelInfo("Storage", "latest snapshot store", "per-session JSON under XDG state"),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Retention", "latest snapshot only", "named snapshots are manual project files"),
			Value: settingsNoopValue,
		},
	}
	return entries
}

func (c *settingsCommand) projectSessionStateEntries() []intpickercompat.Entry {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return []intpickercompat.Entry{
			settingsBackEntry(),
			{
				Label: settingsLabelInfo("Project", "unavailable", identity.Err.Error()),
				Value: settingsNoopValue,
			},
		}
	}

	autosave := c.currentProjectSessionStateAutosave(identity)
	interval := c.currentSessionStateAutosaveInterval()
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Project", identity.Project.Name, ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Project path", identity.Project.Path, identity.Project.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Session identity", identity.Session, "derived from project path"),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Project auto-save", string(autosave.ProjectMode)+" - "+autosave.ProjectSource),
			Value: settingsProjectSessionStateAutosaveDetail,
		},
		{
			Label: settingsLabelInfo("Effective auto-save", string(autosave.Mode), autosave.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Global auto-save", string(autosave.Global.Mode), autosave.Global.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Auto-save interval", formatSessionStateAutosaveInterval(interval.Duration), interval.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Snapshot actions", c.projectSessionStateActionsSummary(identity)),
			Value: settingsProjectSessionStateActionsDetail,
		},
	}
	return entries
}

func (c *settingsCommand) sessionStateAutosaveDetailEntries(autosave sessionStateEffectiveToggle, interval sessionStateEffectiveInterval) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Auto-save", string(autosave.Mode), autosave.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Interval", formatSessionStateAutosaveInterval(interval.Duration)+" - "+interval.Source),
			Value: settingsSessionStateAutosaveIntervalSet,
		},
	}
	entries = append(entries, c.sessionStateToggleEntries("Auto-save", "autosave", autosave.Mode)...)
	return entries
}

func (c *settingsCommand) sessionStateToggleDetailEntries(label, key string, current config.SessionStateToggle, source string) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo(label, string(current), source),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, c.sessionStateToggleEntries(label, key, current)...)
	return entries
}

func (c *settingsCommand) runSessionStateAutosaveIntervalTyped(stdout, stderr io.Writer) error {
	interval := c.currentSessionStateAutosaveInterval()
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-sessionstate-autosave-interval",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: formatSessionStateAutosaveInterval(interval.Duration),
		Title:        "Session State - Auto-save interval",
		Prompt:       "Auto-save interval > ",
		Footer:       projmuxFooter("Enter: save  |  Examples: 30s, 2m, 90  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys:   []string{"enter"},
		Bindings:     settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	value, err := parseSessionStateAutosaveInterval(result.Query)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return nil
	}
	return c.setSessionStateAutosaveInterval(value, stdout)
}

func (c *settingsCommand) projectSessionStateAutosaveDetailEntries() []intpickercompat.Entry {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return []intpickercompat.Entry{
			settingsBackEntry(),
			{
				Label: settingsLabelInfo("Project", "unavailable", identity.Err.Error()),
				Value: settingsNoopValue,
			},
		}
	}
	autosave := c.currentProjectSessionStateAutosave(identity)
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Project auto-save", string(autosave.ProjectMode), autosave.ProjectSource),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Effective auto-save", string(autosave.Mode), autosave.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Global auto-save", string(autosave.Global.Mode), autosave.Global.Source),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, c.projectSessionStateAutosaveToggleEntries(autosave.ProjectMode)...)
	return entries
}

func (c *settingsCommand) projectSessionStateActionsDetailEntries() []intpickercompat.Entry {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return []intpickercompat.Entry{
			settingsBackEntry(),
			{
				Label: settingsLabelInfo("Project", "unavailable", identity.Err.Error()),
				Value: settingsNoopValue,
			},
		}
	}
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Project", identity.Project.Name, identity.Project.Path),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Session identity", identity.Session, "derived from project path"),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, c.projectSessionStateActionEntries(identity)...)
	return entries
}

func (c *settingsCommand) projectSessionStateActionsSummary(identity projectSessionStateIdentity) string {
	parts := []string{"latest/named save"}
	if ok, reason := c.projectSessionStateLiveSessionAvailable(identity.Session); !ok {
		parts = append(parts, "save unavailable: "+reason)
	}
	store, err := c.settingsSessionStateStore()
	if err != nil {
		parts = append(parts, "snapshot unavailable")
		return strings.Join(parts, " - ")
	}
	if _, err := store.Summary(identity.Session); err != nil {
		parts = append(parts, "snapshot missing")
	} else {
		parts = append(parts, "preview/delete available")
	}
	return strings.Join(parts, " - ")
}

func (c *settingsCommand) projectSessionStateActionEntries(identity projectSessionStateIdentity) []intpickercompat.Entry {
	snapshotReady := false
	store, err := c.settingsSessionStateStore()
	if err == nil {
		if _, err := store.Summary(identity.Session); err == nil {
			snapshotReady = true
		}
	}

	saveDesc := "capture live project session as latest"
	saveValue := settingsProjectSessionStateSaveLatest
	namedDesc := "choose a name for the live project session"
	namedValue := settingsProjectSessionStateSaveNamed
	if ok, reason := c.projectSessionStateLiveSessionAvailable(identity.Session); !ok {
		saveDesc = "unavailable - " + reason
		saveValue = settingsNoopValue
		namedDesc = "unavailable - " + reason
		namedValue = settingsNoopValue
	}

	previewDesc := "dry-run only"
	previewValue := settingsProjectSessionStatePreview
	if !snapshotReady {
		previewDesc = "unavailable without a valid snapshot"
		previewValue = settingsNoopValue
	}

	deleteDesc := identity.Session
	deleteValue := settingsProjectSessionStateDelete
	if !snapshotReady {
		deleteDesc = "unavailable without a valid snapshot"
		deleteValue = settingsNoopValue
	}

	return []intpickercompat.Entry{
		{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Save latest snapshot", saveDesc),
			Value: saveValue,
		},
		{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Save named snapshot", namedDesc),
			Value: namedValue,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Preview restore", previewDesc),
			Value: previewValue,
		},
		{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Delete snapshot", deleteDesc),
			Value: deleteValue,
		},
	}
}

func (c *settingsCommand) projectSessionStateAutosaveToggleEntries(current config.SessionStateProjectToggle) []intpickercompat.Entry {
	items := []struct {
		mode config.SessionStateProjectToggle
		desc string
	}{
		{config.SessionStateProjectInherit, "follow global auto-save"},
		{config.SessionStateProjectOn, "enable latest snapshot auto-save for this project"},
		{config.SessionStateProjectOff, "disable latest snapshot auto-save for this project"},
	}
	out := make([]intpickercompat.Entry, 0, len(items))
	for _, item := range items {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		out = append(out, intpickercompat.Entry{
			Label: settingsLabel(glyph, color, "Project auto-save "+string(item.mode), item.desc),
			Value: settingsActionPrefixSessionState + "project-autosave:" + string(item.mode),
		})
	}
	return out
}

func (c *settingsCommand) projectSessionStateLiveSessionAvailable(sessionName string) (bool, string) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return false, "project session identity unavailable"
	}
	if c.tmuxRunner == nil {
		return false, "tmux runner unavailable"
	}
	ok, err := tmuxSessionExists(context.Background(), c.tmuxRunner, sessionName)
	if err != nil {
		return false, err.Error()
	}
	if !ok {
		return false, "live project session not found"
	}
	return true, ""
}

func (c *settingsCommand) sessionStateToggleEntries(label, key string, current config.SessionStateToggle) []intpickercompat.Entry {
	items := []struct {
		mode config.SessionStateToggle
		desc string
	}{
		{config.SessionStateToggleOn, "enable " + strings.ToLower(label)},
		{config.SessionStateToggleOff, "disable " + strings.ToLower(label)},
	}
	out := make([]intpickercompat.Entry, 0, len(items))
	for _, item := range items {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		out = append(out, intpickercompat.Entry{
			Label: settingsLabel(glyph, color, label+" "+string(item.mode), item.desc),
			Value: settingsActionPrefixSessionState + key + ":" + string(item.mode),
		})
	}
	return out
}

func (c *settingsCommand) sessionStateSnapshotEntries() []intpickercompat.Entry {
	sessionName, err := c.currentSettingsSessionName()
	if err != nil {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Snapshot", "missing", err.Error()),
			Value: settingsNoopValue,
		}}
	}
	return c.sessionStateSnapshotEntriesForSession(sessionName)
}

func (c *settingsCommand) sessionStateSnapshotEntriesForSession(sessionName string) []intpickercompat.Entry {
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Snapshot", "invalid", err.Error()),
			Value: settingsNoopValue,
		}}
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		status := "invalid"
		if errors.Is(err, sessionstate.ErrNotFound) {
			status = "missing"
		}
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Snapshot", status, sessionName),
			Value: settingsNoopValue,
		}}
	}
	entries := []intpickercompat.Entry{
		{
			Label: settingsLabelInfo("Snapshot session", snap.Session, ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Snapshot source", snap.SourceLabel(), ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Saved snapshot", statusbarSessionStateSavedText(snap.SavedAt, time.Now()), snap.SavedAt.Format("2006-01-02 15:04:05 MST")),
			Value: settingsNoopValue,
		},
	}
	for _, line := range sessionStatePlainPreviewLines(snap, 100) {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Preview", line, ""),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabelInfo("Windows", fmt.Sprintf("%d", len(snap.Windows)), "snapshot metadata"),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: settingsLabelInfo("Panes", fmt.Sprintf("%d", statusbarSessionStatePaneCount(snap)), "snapshot metadata"),
			Value: settingsNoopValue,
		},
	)
	return entries
}

func (c *settingsCommand) projectSessionStateSnapshotEntriesForSession(sessionName string) []intpickercompat.Entry {
	entries := c.sessionStateSnapshotEntriesForSession(sessionName)
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return entries
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		return entries
	}
	for _, window := range snap.Windows {
		windowName := statusbarSessionStateClean(window.Name)
		if windowName == "" {
			windowName = "window"
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Window", fmt.Sprintf("%d %s", window.Index, windowName), sessionStateCount(len(window.Panes), "pane")),
			Value: settingsNoopValue,
		})
		for _, pane := range window.Panes {
			paneTitle := projectSessionStatePaneTitle(pane)
			entries = append(entries,
				intpickercompat.Entry{
					Label: settingsLabelInfo("Pane", fmt.Sprintf("%d.%d %s", window.Index, pane.Index, paneTitle), ""),
					Value: settingsNoopValue,
				},
				intpickercompat.Entry{
					Label: settingsLabelInfo("Pane cwd", nonEmpty(strings.TrimSpace(pane.CWD), "-"), ""),
					Value: settingsNoopValue,
				},
				intpickercompat.Entry{
					Label: settingsLabelInfo("Pane recipe", projectSessionStateRecipeText(pane.Recipe, snap.SavedAt), ""),
					Value: settingsNoopValue,
				},
			)
		}
	}
	return entries
}

func projectSessionStatePaneTitle(pane sessionstate.Pane) string {
	for _, candidate := range []string{
		pane.Title,
		pane.Recipe.Command,
		pane.Recipe.Topic,
		filepath.Base(pane.CWD),
	} {
		if cleaned := statusbarSessionStateClean(candidate); cleaned != "" {
			return cleaned
		}
	}
	return "pane"
}

func projectSessionStateRecipeText(recipe sessionstate.Recipe, savedAt time.Time) string {
	kind := strings.TrimSpace(recipe.Kind)
	if kind == "" {
		kind = "unknown"
	}
	switch kind {
	case sessionstate.RecipeKindAgent:
		parts := []string{"agent"}
		if agent := strings.TrimSpace(recipe.Agent); agent != "" {
			parts = append(parts, agent)
		}
		if topic := strings.TrimSpace(recipe.Topic); topic != "" {
			parts = append(parts, "topic "+topic)
		}
		if strings.TrimSpace(recipe.ResumeID) != "" {
			parts = append(parts, "resume available")
		} else {
			parts = append(parts, "resume unavailable")
		}
		if source := strings.TrimSpace(recipe.ResumeSource); source != "" {
			parts = append(parts, "source "+source)
		}
		if health := sessionStateResumeHealthText(recipe, savedAt); health != "" {
			parts = append(parts, health)
		}
		return strings.Join(parts, " ")
	case sessionstate.RecipeKindStartup:
		if command := strings.TrimSpace(recipe.Command); command != "" {
			return "startup " + command
		}
		return "startup"
	default:
		return kind
	}
}

func (c *settingsCommand) sessionStateDeleteEntry() intpickercompat.Entry {
	sessionName, err := c.currentSettingsSessionName()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Delete snapshot", "unavailable - "+err.Error()),
			Value: settingsNoopValue,
		}
	}
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Delete snapshot", "unavailable - "+err.Error()),
			Value: settingsNoopValue,
		}
	}
	if _, err := store.Summary(sessionName); err != nil {
		desc := "missing"
		if !errors.Is(err, sessionstate.ErrNotFound) {
			desc = "invalid - " + err.Error()
		}
		return intpickercompat.Entry{
			Label: settingsLabelDim("Delete snapshot", desc),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Delete snapshot", sessionName),
		Value: settingsSessionStateDelete,
	}
}

func (c *settingsCommand) executeSessionStateAction(action string, stdout io.Writer, _ io.Writer) error {
	switch {
	case action == "autosave:on":
		return c.setSessionStateAutosave(config.SessionStateToggleOn)
	case action == "autosave:off":
		return c.setSessionStateAutosave(config.SessionStateToggleOff)
	case strings.HasPrefix(action, "autosave-interval:"):
		value, err := parseSessionStateAutosaveInterval(strings.TrimPrefix(action, "autosave-interval:"))
		if err != nil {
			return err
		}
		return c.setSessionStateAutosaveInterval(value, stdout)
	case action == "sidebar-startup:on":
		return c.setSidebarStartupPicker(config.SessionStateToggleOn)
	case action == "sidebar-startup:off":
		return c.setSidebarStartupPicker(config.SessionStateToggleOff)
	case action == "delete":
		return c.deleteCurrentSessionStateSnapshot()
	case action == "project-save":
		return c.saveProjectLatestSessionStateSnapshot(stdout)
	case action == "project-save-latest":
		return c.saveProjectLatestSessionStateSnapshot(stdout)
	case action == "project-save-named":
		return c.runSaveProjectNamedSessionStateSnapshot(stdout)
	case strings.HasPrefix(action, "project-save-named:"):
		return c.saveProjectNamedSessionStateSnapshot(stdout, strings.TrimPrefix(action, "project-save-named:"))
	case strings.HasPrefix(action, "project-autosave:"):
		return c.setProjectSessionStateAutosave(strings.TrimPrefix(action, "project-autosave:"))
	case action == "project-preview":
		return c.previewProjectSessionStateSnapshot(stdout)
	case action == "project-delete":
		return c.deleteProjectSessionStateSnapshot()
	default:
		return fmt.Errorf("unknown session state settings action: %s", action)
	}
}

func (c *settingsCommand) saveProjectLatestSessionStateSnapshot(stdout io.Writer) error {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return identity.Err
	}
	if ok, reason := c.projectSessionStateLiveSessionAvailable(identity.Session); !ok {
		return fmt.Errorf("save project session snapshot: %s", reason)
	}
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return err
	}
	now := time.Now()
	snap, err := inttmux.NewClient(c.tmuxRunner).SaveSessionSnapshot(context.Background(), store, identity.Session, now)
	if err != nil {
		return fmt.Errorf("save project session snapshot %q: %w", identity.Session, err)
	}
	_, err = fmt.Fprintf(stdout, "saved project session snapshot: %s (%s, %s)\n", snap.Session, sessionStateCount(len(snap.Windows), "window"), sessionStateCount(statusbarSessionStatePaneCount(snap), "pane"))
	return err
}

func (c *settingsCommand) runSaveProjectNamedSessionStateSnapshot(stdout io.Writer) error {
	result, err := c.runPicker(intpickercompat.Options{
		UI:          "settings-project-sessionstate-save-named",
		Entries:     nil,
		AcceptQuery: true,
		Title:       "Save named snapshot",
		Prompt:      "Snapshot name > ",
		Footer:      projmuxFooter("Enter: save  |  Esc/Alt+5/Ctrl+Alt+S: cancel"),
		ExpectKeys:  []string{"enter"},
		Bindings:    settingsCloseBindings(),
	})
	if err != nil {
		if errors.Is(err, errSettingsClosed) {
			return nil
		}
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	name := strings.TrimSpace(result.Query)
	if name == "" {
		return nil
	}
	return c.saveProjectNamedSessionStateSnapshot(stdout, name)
}

func (c *settingsCommand) saveProjectNamedSessionStateSnapshot(stdout io.Writer, name string) error {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return identity.Err
	}
	if ok, reason := c.projectSessionStateLiveSessionAvailable(identity.Session); !ok {
		return fmt.Errorf("save named project session snapshot: %s", reason)
	}
	if c.tmuxRunner == nil {
		return errors.New("save named project session snapshot: tmux runner unavailable")
	}
	now := time.Now()
	client := inttmux.NewClient(c.tmuxRunner)
	snap, err := client.CaptureSessionSnapshot(context.Background(), identity.Session, now)
	if err != nil {
		return fmt.Errorf("capture named project session snapshot %q: %w", identity.Session, err)
	}
	preset := corelayout.FromSnapshot(snap, identity.Project.Path, "Named snapshot saved from live session", corelayout.ModeInheritAutosave)
	if err := corelayout.NewStore(identity.Project.Path).Save(name, preset); err != nil {
		return fmt.Errorf("save named project session snapshot %q: %w", name, err)
	}
	_, err = fmt.Fprintf(stdout, "saved named project session snapshot: %s (%s, %s)\n", name, sessionStateCount(len(snap.Windows), "window"), sessionStateCount(statusbarSessionStatePaneCount(snap), "pane"))
	return err
}

func (c *settingsCommand) previewProjectSessionStateSnapshot(stdout io.Writer) error {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return identity.Err
	}
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return err
	}
	snap, err := store.Load(identity.Session)
	if err != nil {
		return fmt.Errorf("load project session snapshot %q: %w", identity.Session, err)
	}
	for _, line := range sessionStateRestorePreviewLines(snap, time.Now(), 100) {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func (c *settingsCommand) deleteProjectSessionStateSnapshot() error {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return identity.Err
	}
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return err
	}
	if err := store.Delete(identity.Session); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "deleted project session snapshot: "+identity.Session)
	}
	return nil
}

func (c *settingsCommand) currentSessionStateAutosave() sessionStateEffectiveToggle {
	return c.currentSessionStateToggleDefault(sessionStateAutosaveEnv, config.SessionStateToggleOff, func(paths config.Paths) string {
		return paths.SessionStateAutosaveFile()
	})
}

func (c *settingsCommand) currentSessionStateToggle(envName string, file func(config.Paths) string) sessionStateEffectiveToggle {
	return c.currentSessionStateToggleDefault(envName, config.SessionStateToggleOn, file)
}

func (c *settingsCommand) currentSessionStateAutosaveInterval() sessionStateEffectiveInterval {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return sessionStateEffectiveInterval{Duration: defaultSessionStateAutosaveInterval, Source: "default"}
	}
	duration, err := config.LoadSessionStateDurationFileDefault(paths.SessionStateAutosaveIntervalFile(), defaultSessionStateAutosaveInterval)
	if err != nil {
		return sessionStateEffectiveInterval{Duration: defaultSessionStateAutosaveInterval, Source: "default"}
	}
	if _, err := osStat(paths.SessionStateAutosaveIntervalFile()); err == nil {
		return sessionStateEffectiveInterval{Duration: duration, Source: "saved"}
	}
	return sessionStateEffectiveInterval{Duration: duration, Source: "default"}
}

func (c *settingsCommand) currentSessionStateToggleDefault(envName string, fallback config.SessionStateToggle, file func(config.Paths) string) sessionStateEffectiveToggle {
	return sessionStateToggleStateDefault(c.homeDir, c.lookupEnv, envName, fallback, file)
}

func sessionStateToggleState(homeDir func() (string, error), lookupEnv func(string) string, envName string, file func(config.Paths) string) sessionStateEffectiveToggle {
	return sessionStateToggleStateDefault(homeDir, lookupEnv, envName, config.SessionStateToggleOn, file)
}

func sessionStateToggleStateDefault(homeDir func() (string, error), lookupEnv func(string) string, envName string, fallback config.SessionStateToggle, file func(config.Paths) string) sessionStateEffectiveToggle {
	if lookupEnv != nil {
		if raw := strings.TrimSpace(lookupEnv(envName)); raw != "" {
			return sessionStateEffectiveToggle{Mode: config.NormalizeSessionStateToggle(raw), Source: envName + " env"}
		}
	}
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return sessionStateEffectiveToggle{Mode: fallback, Source: "default"}
	}
	mode, err := config.LoadSessionStateToggleFileDefault(file(paths), fallback)
	if err != nil {
		return sessionStateEffectiveToggle{Mode: fallback, Source: "default"}
	}
	if _, err := osStat(file(paths)); err == nil {
		return sessionStateEffectiveToggle{Mode: mode, Source: "saved"}
	}
	return sessionStateEffectiveToggle{Mode: mode, Source: "default"}
}

func sessionStateToggleFileStateDefault(homeDir func() (string, error), lookupEnv func(string) string, fallback config.SessionStateToggle, file func(config.Paths) string) sessionStateEffectiveToggle {
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return sessionStateEffectiveToggle{Mode: fallback, Source: "default"}
	}
	mode, err := config.LoadSessionStateToggleFileDefault(file(paths), fallback)
	if err != nil {
		return sessionStateEffectiveToggle{Mode: fallback, Source: "default"}
	}
	if _, err := osStat(file(paths)); err == nil {
		return sessionStateEffectiveToggle{Mode: mode, Source: "saved"}
	}
	return sessionStateEffectiveToggle{Mode: mode, Source: "default"}
}

func (c *settingsCommand) currentProjectSessionStateAutosave(identity projectSessionStateIdentity) projectSessionStateEffectiveToggle {
	global := c.currentSessionStateAutosave()
	projectMode, projectSource := c.currentProjectSessionStateAutosaveMode(identity.Session)
	effective := projectSessionStateEffectiveToggle{
		Mode:          global.Mode,
		Source:        "global " + global.Source,
		ProjectMode:   projectMode,
		ProjectSource: projectSource,
		Global:        global,
	}
	switch projectMode {
	case config.SessionStateProjectOn:
		effective.Mode = config.SessionStateToggleOn
		effective.Source = "project override"
	case config.SessionStateProjectOff:
		effective.Mode = config.SessionStateToggleOff
		effective.Source = "project override"
	}
	return effective
}

func (c *settingsCommand) currentProjectSessionStateAutosaveMode(sessionName string) (config.SessionStateProjectToggle, string) {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.SessionStateProjectInherit, "default"
	}
	path := paths.ProjectSessionStateAutosaveFile(sessionName)
	mode, err := config.LoadSessionStateProjectToggleFile(path)
	if err != nil {
		return config.SessionStateProjectInherit, "default"
	}
	if _, err := osStat(path); err == nil {
		return mode, "saved"
	}
	return mode, "default"
}

func (c *settingsCommand) setSessionStateAutosave(value config.SessionStateToggle) error {
	return c.setSessionStateToggle(value, func(paths config.Paths) string {
		return paths.SessionStateAutosaveFile()
	}, "sessionstate autosave")
}

func (c *settingsCommand) currentSidebarStartupPicker() sessionStateEffectiveToggle {
	return sessionStateToggleFileStateDefault(c.homeDir, c.lookupEnv, config.SessionStateToggleOff, func(paths config.Paths) string {
		return paths.SidebarStartupPickerFile()
	})
}

func (c *settingsCommand) setSessionStateAutosaveInterval(value time.Duration, stdout io.Writer) error {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveSessionStateDurationFile(paths.SessionStateAutosaveIntervalFile(), value); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "sessionstate autosave interval: %s\n", formatSessionStateAutosaveInterval(value)); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "sessionstate autosave interval: "+formatSessionStateAutosaveInterval(value))
	}
	return nil
}

func (c *settingsCommand) setSidebarStartupPicker(value config.SessionStateToggle) error {
	return c.setSessionStateToggle(value, func(paths config.Paths) string {
		return paths.SidebarStartupPickerFile()
	}, "sidebar startup picker")
}

func (c *settingsCommand) setProjectSessionStateAutosave(value string) error {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return identity.Err
	}
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	mode := config.NormalizeSessionStateProjectToggle(value)
	if err := config.SaveSessionStateProjectToggleFile(paths.ProjectSessionStateAutosaveFile(identity.Session), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "project sessionstate autosave: "+string(mode))
	}
	return nil
}

func parseSessionStateAutosaveInterval(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, errors.New("auto-save interval must not be empty")
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, errors.New("auto-save interval must be positive")
		}
		return time.Duration(seconds) * time.Second, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("auto-save interval %q must be a positive duration like 30s or 2m", raw)
	}
	return duration, nil
}

func formatSessionStateAutosaveInterval(value time.Duration) string {
	if value <= 0 {
		value = defaultSessionStateAutosaveInterval
	}
	if value%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(value/time.Minute))
	}
	if value%time.Second == 0 {
		return fmt.Sprintf("%ds", int(value/time.Second))
	}
	return value.String()
}

func (c *settingsCommand) setSessionStateToggle(value config.SessionStateToggle, file func(config.Paths) string, messageLabel string) error {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	mode := config.NormalizeSessionStateToggle(string(value))
	if err := config.SaveSessionStateToggleFile(file(paths), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", messageLabel+": "+string(mode))
	}
	return nil
}

func (c *settingsCommand) deleteCurrentSessionStateSnapshot() error {
	sessionName, err := c.currentSettingsSessionName()
	if err != nil {
		return err
	}
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return err
	}
	if err := store.Delete(sessionName); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "deleted session snapshot: "+sessionName)
	}
	return nil
}

func (c *settingsCommand) confirmSessionStateDelete() (bool, error) {
	sessionName, err := c.currentSettingsSessionName()
	if err != nil {
		return false, err
	}
	return c.confirmSessionStateDeleteForSession(sessionName, "settings-sessionstate-delete-confirm", "Settings > Session State > Delete snapshot > ")
}

func (c *settingsCommand) confirmProjectSessionStateDelete() (bool, error) {
	identity := c.projectSessionStateIdentity(c.resolveSettingsProjectContext())
	if identity.Err != nil {
		return false, identity.Err
	}
	return c.confirmSessionStateDeleteForSession(identity.Session, "settings-project-sessionstate-delete-confirm", "Settings > Project > Session State > Delete snapshot > ")
}

func (c *settingsCommand) confirmSessionStateDeleteForSession(sessionName, ui, prompt string) (bool, error) {
	options := intpickercompat.Options{
		UI: ui,
		Entries: []intpickercompat.Entry{
			{
				Label: settingsLabelInfo("Delete snapshot", sessionName, "destructive"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsLabel(settingsGlyphBack, settingsColorBack, "Cancel", "keep snapshot"),
				Value: settingsSessionStateConfirmNo,
			},
			{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Yes, delete", "remove latest snapshot"),
				Value: settingsSessionStateConfirmYes,
			},
		},
		Title:      "Delete session snapshot - confirm",
		Prompt:     prompt,
		Footer:     projmuxFooter("Enter: confirm  |  Esc/Alt+5/Ctrl+Alt+S: cancel"),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
	result, err := c.runPicker(options)
	if err != nil {
		if errors.Is(err, errSettingsClosed) {
			return false, nil
		}
		return false, err
	}
	value := strings.TrimSpace(result.Value)
	if result.Key != "enter" || value == "" {
		return false, nil
	}
	return value == settingsSessionStateConfirmYes, nil
}

func (c *settingsCommand) settingsSessionStateStore() (sessionstate.Store, error) {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return sessionstate.Store{}, err
	}
	return sessionstate.NewStore(paths.SessionStateDir()), nil
}

func (c *settingsCommand) currentSettingsSessionName() (string, error) {
	if c.lookupEnv != nil {
		if raw := strings.TrimSpace(c.lookupEnv("PROJMUX_SESSION")); raw != "" {
			return raw, nil
		}
	}
	if c.lookupEnv == nil || strings.TrimSpace(c.lookupEnv("TMUX")) == "" {
		return "", errors.New("no tmux session")
	}
	if c.runOutput == nil {
		return "", errors.New("tmux output runner unavailable")
	}
	output, err := c.runOutput("tmux", "display-message", "-p", "#{session_name}")
	if err != nil {
		return "", fmt.Errorf("resolve current tmux session: %w", err)
	}
	sessionName := strings.TrimSpace(string(output))
	if sessionName == "" {
		return "", errors.New("current tmux session unavailable")
	}
	return sessionName, nil
}

func (c *settingsCommand) projectSessionStateIdentity(ctx settingsProjectContext) projectSessionStateIdentity {
	if !ctx.hasProject() {
		return projectSessionStateIdentity{Project: ctx, Err: errors.New("no project context")}
	}
	homeDir := c.homeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return projectSessionStateIdentity{Project: ctx, Err: fmt.Errorf("resolve home directory: %w", err)}
	}
	sessionName := sessions.NewNamer(home).SessionName(ctx.Path)
	if strings.TrimSpace(sessionName) == "" {
		return projectSessionStateIdentity{Project: ctx, Err: errors.New("project session identity unavailable")}
	}
	return projectSessionStateIdentity{Project: ctx, Session: sessionName}
}

func sessionStateAutosaveEnabled(homeDir func() (string, error), lookupEnv func(string) string) bool {
	return sessionStateToggleEnabledDefault(homeDir, lookupEnv, sessionStateAutosaveEnv, config.SessionStateToggleOff, func(paths config.Paths) string {
		return paths.SessionStateAutosaveFile()
	})
}

func sidebarStartupPickerEnabled(homeDir func() (string, error), lookupEnv func(string) string) bool {
	return sessionStateToggleFileStateDefault(homeDir, lookupEnv, config.SessionStateToggleOff, func(paths config.Paths) string {
		return paths.SidebarStartupPickerFile()
	}).Mode.Enabled()
}

func sessionStateToggleEnabled(homeDir func() (string, error), lookupEnv func(string) string, envName string, file func(config.Paths) string) bool {
	return sessionStateToggleEnabledDefault(homeDir, lookupEnv, envName, config.SessionStateToggleOn, file)
}

func sessionStateToggleEnabledDefault(homeDir func() (string, error), lookupEnv func(string) string, envName string, fallback config.SessionStateToggle, file func(config.Paths) string) bool {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if raw := strings.TrimSpace(lookupEnv(envName)); raw != "" {
		return config.NormalizeSessionStateToggle(raw).Enabled()
	}
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return fallback.Enabled()
	}
	mode, err := config.LoadSessionStateToggleFileDefault(file(paths), fallback)
	if err != nil {
		return fallback.Enabled()
	}
	return mode.Enabled()
}

const (
	settingsSessionStateAutosaveDetail        = settingsActionPrefixSessionState + "view-autosave"
	settingsSessionStateAutosaveIntervalSet   = settingsActionPrefixSessionState + "autosave-interval-set"
	settingsProjectSessionStateAutosaveDetail = settingsActionPrefixSessionState + "project-view-autosave"
	settingsProjectSessionStateActionsDetail  = settingsActionPrefixSessionState + "project-view-actions"
	settingsProjectSessionStateSaveLatest     = settingsActionPrefixSessionState + "project-save-latest"
	settingsProjectSessionStateSaveNamed      = settingsActionPrefixSessionState + "project-save-named"
	settingsProjectSessionStateSave           = settingsActionPrefixSessionState + "project-save"
	settingsProjectSessionStatePreview        = settingsActionPrefixSessionState + "project-preview"
	settingsProjectSessionStateDelete         = settingsActionPrefixSessionState + "project-delete"
	settingsSessionStateConfirmYes            = "sessionstate:confirm-yes"
	settingsSessionStateConfirmNo             = "sessionstate:confirm-no"
)
