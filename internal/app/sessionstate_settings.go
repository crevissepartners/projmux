package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
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

func (c *settingsCommand) sessionStateRootLabel() string {
	autosave := c.currentSessionStateAutosave()
	autorestore := c.currentSessionStateAutorestore()
	desc := fmt.Sprintf("autosave %s, autorestore %s", autosave.Mode, autorestore.Mode)
	return settingsLabel(settingsGlyphOpen, settingsColorType, "Session State", desc)
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
			Label: settingsLabelInfo("Auto-restore", string(autorestore.Mode), autorestore.Source),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, c.sessionStateSnapshotEntries()...)
	entries = append(entries, c.sessionStateToggleEntries("Auto-save", "autosave", autosave.Mode)...)
	entries = append(entries, c.sessionStateToggleEntries("Auto-restore", "autorestore", autorestore.Mode)...)
	entries = append(entries, c.sessionStateDeleteEntry())
	return entries
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
	store, err := c.settingsSessionStateStore()
	if err != nil {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Snapshot", "invalid", err.Error()),
			Value: settingsNoopValue,
		}}
	}
	summary, err := store.Summary(sessionName)
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
	return []intpickercompat.Entry{
		{
			Label: settingsLabelInfo("Snapshot session", summary.Session, ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Saved at", summary.SavedAt.Format("2006-01-02 15:04:05 MST"), ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Windows", fmt.Sprintf("%d", summary.WindowCount), ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Panes", fmt.Sprintf("%d", summary.PaneCount), ""),
			Value: settingsNoopValue,
		},
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

func (c *settingsCommand) executeSessionStateAction(action string, _ io.Writer, _ io.Writer) error {
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
	default:
		return fmt.Errorf("unknown session state settings action: %s", action)
	}
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
	}, "sessionstate autorestore")
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
	options := intpickercompat.Options{
		UI: "settings-sessionstate-delete-confirm",
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
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Yes, delete", "remove saved snapshot"),
				Value: settingsSessionStateConfirmYes,
			},
		},
		Title:      "Delete session snapshot - confirm",
		Prompt:     "Settings > Session State > Delete snapshot > ",
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

func sessionStateAutosaveEnabled(homeDir func() (string, error), lookupEnv func(string) string) bool {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if raw := strings.TrimSpace(lookupEnv(sessionStateAutosaveEnv)); raw != "" {
		return config.NormalizeSessionStateToggle(raw).Enabled()
	}
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return true
	}
	mode, err := config.LoadSessionStateToggleFile(paths.SessionStateAutosaveFile())
	if err != nil {
		return true
	}
	return mode.Enabled()
}

const (
	settingsSessionStateConfirmYes = "sessionstate:confirm-yes"
	settingsSessionStateConfirmNo  = "sessionstate:confirm-no"
)
