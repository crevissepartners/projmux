package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	sessionStateAutosaveEnv    = "PROJMUX_SESSIONSTATE_AUTOSAVE"
	sessionStateAutorestoreEnv = "PROJMUX_SESSIONSTATE_AUTORESTORE"
)

type sessionStateEffectiveToggle struct {
	Mode   config.SessionStateToggle
	Source string
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
		if action == settingsSessionStateDelete {
			confirmed, err := c.confirmSessionStateDelete()
			if err != nil {
				return err
			}
			if !confirmed {
				continue
			}
		}
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
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
		if action == settingsProjectSessionStateDelete {
			confirmed, err := c.confirmProjectSessionStateDelete()
			if err != nil {
				return err
			}
			if !confirmed {
				continue
			}
		}
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) sessionStateRootLabel() string {
	autosave := c.currentSessionStateAutosave()
	autorestore := c.currentSessionStateAutorestore()
	desc := fmt.Sprintf("autosave %s, startup picker %s", autosave.Mode, autorestore.Mode)
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
		return "Session State - Project restore state unavailable"
	}
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return "Session State - " + identity.Project.Name + " restore state unavailable"
	}
	if _, err := store.Summary(identity.Session); err != nil {
		if errors.Is(err, sessionstate.ErrNotFound) {
			return "Session State - " + identity.Project.Name + " restore state missing"
		}
		return "Session State - " + identity.Project.Name + " restore state invalid"
	}
	return "Session State - " + identity.Project.Name + " restore state saved"
}

func (c *settingsCommand) sessionStateEntries() []intpickercompat.Entry {
	autosave := c.currentSessionStateAutosave()
	autorestore := c.currentSessionStateAutorestore()
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Auto-save", string(autosave.Mode), autosave.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Startup picker", string(autorestore.Mode), autorestore.Source),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, c.sessionStateSnapshotEntries()...)
	entries = append(entries, c.sessionStateToggleEntries("Auto-save", "autosave", autosave.Mode)...)
	entries = append(entries, c.sessionStateToggleEntries("Startup picker", "autorestore", autorestore.Mode)...)
	entries = append(entries, c.sessionStateDeleteEntry())
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

	autosave := c.currentSessionStateAutosave()
	autorestore := c.currentSessionStateAutorestore()
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
			Label: settingsLabelInfo("Auto-save", string(autosave.Mode), autosave.Source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Startup picker", string(autorestore.Mode), autorestore.Source),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, c.projectSessionStateSnapshotEntriesForSession(identity.Session)...)
	entries = append(entries, c.projectSessionStateActionEntries(identity)...)
	entries = append(entries, c.sessionStateToggleEntries("Auto-save", "autosave", autosave.Mode)...)
	entries = append(entries, c.sessionStateToggleEntries("Startup picker", "autorestore", autorestore.Mode)...)
	return entries
}

func (c *settingsCommand) projectSessionStateActionEntries(identity projectSessionStateIdentity) []intpickercompat.Entry {
	snapshotReady := false
	store, err := c.settingsSessionStateStore()
	if err == nil {
		if _, err := store.Summary(identity.Session); err == nil {
			snapshotReady = true
		}
	}

	saveDesc := "capture live project session"
	saveValue := settingsProjectSessionStateSave
	if ok, reason := c.projectSessionStateLiveSessionAvailable(identity.Session); !ok {
		saveDesc = "unavailable - " + reason
		saveValue = settingsNoopValue
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
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Save snapshot", saveDesc),
			Value: saveValue,
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
	switch action {
	case "autosave:on":
		return c.setSessionStateAutosave(config.SessionStateToggleOn)
	case "autosave:off":
		return c.setSessionStateAutosave(config.SessionStateToggleOff)
	case "autorestore:on":
		return c.setSessionStateAutorestore(config.SessionStateToggleOn)
	case "autorestore:off":
		return c.setSessionStateAutorestore(config.SessionStateToggleOff)
	case "delete":
		return c.deleteCurrentSessionStateSnapshot()
	case "project-save":
		return c.saveProjectSessionStateSnapshot(stdout)
	case "project-preview":
		return c.previewProjectSessionStateSnapshot(stdout)
	case "project-delete":
		return c.deleteProjectSessionStateSnapshot()
	default:
		return fmt.Errorf("unknown session state settings action: %s", action)
	}
}

func (c *settingsCommand) saveProjectSessionStateSnapshot(stdout io.Writer) error {
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
	return c.currentSessionStateToggle(sessionStateAutosaveEnv, func(paths config.Paths) string {
		return paths.SessionStateAutosaveFile()
	})
}

func (c *settingsCommand) currentSessionStateAutorestore() sessionStateEffectiveToggle {
	return c.currentSessionStateToggle(sessionStateAutorestoreEnv, func(paths config.Paths) string {
		return paths.SessionStateAutorestoreFile()
	})
}

func (c *settingsCommand) currentSessionStateToggle(envName string, file func(config.Paths) string) sessionStateEffectiveToggle {
	return sessionStateToggleState(c.homeDir, c.lookupEnv, envName, file)
}

func sessionStateToggleState(homeDir func() (string, error), lookupEnv func(string) string, envName string, file func(config.Paths) string) sessionStateEffectiveToggle {
	if lookupEnv != nil {
		if raw := strings.TrimSpace(lookupEnv(envName)); raw != "" {
			return sessionStateEffectiveToggle{Mode: config.NormalizeSessionStateToggle(raw), Source: envName + " env"}
		}
	}
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return sessionStateEffectiveToggle{Mode: config.SessionStateToggleOn, Source: "default"}
	}
	mode, err := config.LoadSessionStateToggleFile(file(paths))
	if err != nil {
		return sessionStateEffectiveToggle{Mode: config.SessionStateToggleOn, Source: "default"}
	}
	if _, err := osStat(file(paths)); err == nil {
		return sessionStateEffectiveToggle{Mode: mode, Source: "saved"}
	}
	return sessionStateEffectiveToggle{Mode: mode, Source: "default"}
}

func (c *settingsCommand) setSessionStateAutosave(value config.SessionStateToggle) error {
	return c.setSessionStateToggle(value, func(paths config.Paths) string {
		return paths.SessionStateAutosaveFile()
	}, "sessionstate autosave")
}

func (c *settingsCommand) setSessionStateAutorestore(value config.SessionStateToggle) error {
	return c.setSessionStateToggle(value, func(paths config.Paths) string {
		return paths.SessionStateAutorestoreFile()
	}, "sessionstate startup picker")
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
	return sessionStateToggleEnabled(homeDir, lookupEnv, sessionStateAutosaveEnv, func(paths config.Paths) string {
		return paths.SessionStateAutosaveFile()
	})
}

func sessionStateAutorestoreEnabled(homeDir func() (string, error), lookupEnv func(string) string) bool {
	return sessionStateToggleEnabled(homeDir, lookupEnv, sessionStateAutorestoreEnv, func(paths config.Paths) string {
		return paths.SessionStateAutorestoreFile()
	})
}

func sessionStateToggleEnabled(homeDir func() (string, error), lookupEnv func(string) string, envName string, file func(config.Paths) string) bool {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if raw := strings.TrimSpace(lookupEnv(envName)); raw != "" {
		return config.NormalizeSessionStateToggle(raw).Enabled()
	}
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return true
	}
	mode, err := config.LoadSessionStateToggleFile(file(paths))
	if err != nil {
		return true
	}
	return mode.Enabled()
}

const (
	settingsProjectSessionStateSave    = settingsActionPrefixSessionState + "project-save"
	settingsProjectSessionStatePreview = settingsActionPrefixSessionState + "project-preview"
	settingsProjectSessionStateDelete  = settingsActionPrefixSessionState + "project-delete"
	settingsSessionStateConfirmYes     = "sessionstate:confirm-yes"
	settingsSessionStateConfirmNo      = "sessionstate:confirm-no"
)
