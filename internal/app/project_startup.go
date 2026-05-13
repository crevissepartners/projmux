package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	projectStartupKindLatest = "latest"
	projectStartupKindNamed  = "named"
	projectStartupKindEmpty  = "empty"
	projectStartupKindBack   = "back"

	projectStartupValueLatest = "latest"
	projectStartupValueEmpty  = "empty"
	projectStartupValueNamed  = "named:"
)

var errProjectStartupBack = errors.New("project startup back")

type switchProjectTrustAuthorizer interface {
	AuthorizeProjectHooks(ctx context.Context, cwd string) (bool, error)
}

type switchSessionSnapshotRestorer interface {
	RestoreSessionSnapshot(ctx context.Context, snap sessionstate.Snapshot, cwd, source string) error
}

type projectStartupCandidate struct {
	Kind        string
	Name        string
	Label       string
	Description string
}

func (c *switchCommand) openProjectTarget(ctx context.Context, target, sessionName string) error {
	exists, err := c.switchSessionExists(ctx, sessionName)
	if err != nil {
		return err
	}
	if exists {
		return c.openProjectSession(ctx, sessionName)
	}
	mode := projectStartupCandidate{Kind: projectStartupKindEmpty}
	if sessionStateAutorestoreEnabled(c.homeDir, c.lookupEnv) {
		mode = c.pickProjectStartupMode(sessionName, target)
	}
	if mode.Kind == projectStartupKindBack {
		return errProjectStartupBack
	}
	if trusted, err := c.authorizeProjectOpen(ctx, target); err != nil {
		return err
	} else if !trusted {
		return nil
	}
	switch mode.Kind {
	case projectStartupKindLatest:
		return c.restoreProjectLatestSnapshot(ctx, sessionName, target)
	case projectStartupKindNamed:
		return c.restoreProjectNamedSnapshot(ctx, sessionName, target, mode.Name)
	default:
		return c.ensureAndOpenProjectSession(ctx, sessionName, target)
	}
}

func (c *switchCommand) authorizeProjectOpen(ctx context.Context, target string) (bool, error) {
	authorizer, ok := c.sessions.(switchProjectTrustAuthorizer)
	if !ok || authorizer == nil {
		return true, nil
	}
	trusted, err := authorizer.AuthorizeProjectHooks(ctx, target)
	if err != nil {
		return false, err
	}
	return trusted, nil
}

func (c *switchCommand) pickProjectStartupMode(sessionName, target string) projectStartupCandidate {
	candidates := c.projectStartupCandidates(sessionName, target)
	if len(candidates) == 0 {
		return projectStartupCandidate{Kind: projectStartupKindEmpty}
	}
	result, err := runPickerOptionBackend(c.lookupEnv, c.nativePicker, c.runner, projectStartupPickerOptions(candidates))
	if err != nil {
		return projectStartupCandidate{Kind: projectStartupKindEmpty}
	}
	if candidate, ok := projectStartupCandidateFromValue(result.Value); ok {
		return candidate
	}
	return projectStartupCandidate{Kind: projectStartupKindEmpty}
}

func (c *switchCommand) projectStartupCandidates(sessionName, target string) []projectStartupCandidate {
	var candidates []projectStartupCandidate
	if store, err := c.projectStartupSessionStateStore(); err == nil {
		if summary, err := store.Summary(sessionName); err == nil && summary.Source != sessionstate.SourceFresh {
			candidates = append(candidates, projectStartupCandidate{
				Kind:        projectStartupKindLatest,
				Label:       "Latest snapshot",
				Description: projectStartupDescription("auto-saved", summary.SavedAt, summary.WindowCount, summary.PaneCount),
			})
		}
	}
	if store := corelayout.NewStore(target); strings.TrimSpace(store.ProjectRoot) != "" {
		if entries, _, err := store.List(); err == nil {
			for _, entry := range entries {
				candidates = append(candidates, projectStartupCandidate{
					Kind:        projectStartupKindNamed,
					Name:        entry.Name,
					Label:       "Named snapshot",
					Description: namedSnapshotDescription(entry),
				})
			}
		}
	}
	if len(candidates) == 0 {
		return []projectStartupCandidate{emptyProjectStartupCandidate(), backProjectStartupCandidate()}
	}
	candidates = append(candidates, emptyProjectStartupCandidate())
	candidates = append(candidates, backProjectStartupCandidate())
	return candidates
}

func emptyProjectStartupCandidate() projectStartupCandidate {
	return projectStartupCandidate{
		Kind:        projectStartupKindEmpty,
		Label:       "Empty session",
		Description: "start without restoring a snapshot",
	}
}

func namedSnapshotDescription(entry corelayout.Entry) string {
	parts := []string{entry.Name}
	if savedAt := namedSnapshotSavedAt(entry); !savedAt.IsZero() {
		parts = append(parts, projectStartupSavedAtText(savedAt))
	}
	if strings.TrimSpace(entry.Description) != "" {
		parts = append(parts, strings.TrimSpace(entry.Description))
	}
	parts = append(parts, sessionStateCount(entry.Windows, "window"), sessionStateCount(entry.Panes, "pane"))
	return strings.Join(parts, ", ")
}

func namedSnapshotSavedAt(entry corelayout.Entry) time.Time {
	if strings.TrimSpace(entry.Path) == "" {
		return time.Time{}
	}
	info, err := os.Stat(entry.Path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func projectStartupDescription(source string, savedAt time.Time, windows, panes int) string {
	parts := []string{}
	if savedText := projectStartupSavedAtText(savedAt); savedText != "" {
		parts = append(parts, savedText)
	}
	if strings.TrimSpace(source) != "" {
		parts = append(parts, strings.TrimSpace(source))
	}
	parts = append(parts, sessionStateCount(windows, "window"), sessionStateCount(panes, "pane"))
	return strings.Join(parts, ", ")
}

func projectStartupSavedAtText(savedAt time.Time) string {
	if savedAt.IsZero() {
		return ""
	}
	return "saved " + savedAt.UTC().Format("2006-01-02 15:04:05 MST")
}

func backProjectStartupCandidate() projectStartupCandidate {
	return projectStartupCandidate{
		Kind:        projectStartupKindBack,
		Label:       "Back",
		Description: "return to projects",
	}
}

func projectStartupPickerOptions(candidates []projectStartupCandidate) intpickercompat.Options {
	entries := make([]intpickercompat.Entry, 0, len(candidates))
	for _, candidate := range candidates {
		entries = append(entries, intpickercompat.Entry{
			Label:     projectStartupPickerLabel(candidate),
			Value:     projectStartupPickerValue(candidate),
			SearchKey: strings.TrimSpace(candidate.Label + " " + candidate.Name + " " + candidate.Description),
		})
	}
	return intpickercompat.Options{
		UI:            "project-startup",
		Prompt:        "Start project > ",
		Header:        "Start project",
		Footer:        "Enter: start  |  Back row: projects  |  Esc: empty session",
		Entries:       entries,
		Bindings:      settingsCloseBindings(),
		DisableSearch: true,
	}
}

func projectStartupPickerLabel(candidate projectStartupCandidate) string {
	switch candidate.Kind {
	case projectStartupKindLatest:
		return settingsLabel(settingsGlyphOpen, settingsColorType, "Latest snapshot", candidate.Description)
	case projectStartupKindNamed:
		return settingsLabel(settingsGlyphOpen, settingsColorType, "Named snapshot", candidate.Description)
	case projectStartupKindEmpty:
		return settingsLabel(settingsGlyphBack, settingsColorBack, "Empty session", candidate.Description)
	case projectStartupKindBack:
		return settingsLabel(settingsGlyphBack, settingsColorBack, "Back", candidate.Description)
	default:
		return settingsLabel(settingsGlyphInfo, settingsColorInfo, candidate.Label, candidate.Description)
	}
}

func projectStartupPickerValue(candidate projectStartupCandidate) string {
	switch candidate.Kind {
	case projectStartupKindLatest:
		return projectStartupValueLatest
	case projectStartupKindNamed:
		return projectStartupValueNamed + candidate.Name
	case projectStartupKindEmpty:
		return projectStartupValueEmpty
	case projectStartupKindBack:
		return settingsBackValue
	default:
		return ""
	}
}

func projectStartupCandidateFromValue(value string) (projectStartupCandidate, bool) {
	value = strings.TrimSpace(value)
	switch {
	case value == projectStartupValueLatest:
		return projectStartupCandidate{Kind: projectStartupKindLatest}, true
	case value == projectStartupValueEmpty:
		return projectStartupCandidate{Kind: projectStartupKindEmpty}, true
	case value == settingsBackValue:
		return projectStartupCandidate{Kind: projectStartupKindBack}, true
	case strings.HasPrefix(value, projectStartupValueNamed):
		name := strings.TrimSpace(strings.TrimPrefix(value, projectStartupValueNamed))
		if name == "" {
			return projectStartupCandidate{}, false
		}
		return projectStartupCandidate{Kind: projectStartupKindNamed, Name: name}, true
	default:
		return projectStartupCandidate{}, false
	}
}

func (c *switchCommand) restoreProjectLatestSnapshot(ctx context.Context, sessionName, target string) error {
	store, err := c.projectStartupSessionStateStore()
	if err != nil {
		return err
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		return err
	}
	return c.restoreProjectSnapshot(ctx, snap, target, sessionstate.SourceAutosave)
}

func (c *switchCommand) restoreProjectNamedSnapshot(ctx context.Context, sessionName, target, name string) error {
	preset, err := corelayout.NewStore(target).Load(name)
	if err != nil {
		return err
	}
	snap, err := corelayout.ToSnapshot(preset, sessionName, target, c.projectStartupNow())
	if err != nil {
		return err
	}
	source := layoutPresetSource(name, preset)
	if err := c.restoreProjectSnapshot(ctx, snap, target, source); err != nil {
		return err
	}
	if source == sessionstate.SourceFresh {
		if store, err := c.projectStartupSessionStateStore(); err == nil {
			_ = store.Delete(sessionName)
		}
	}
	return nil
}

func (c *switchCommand) restoreProjectSnapshot(ctx context.Context, snap sessionstate.Snapshot, target, source string) error {
	restorer, ok := c.sessions.(switchSessionSnapshotRestorer)
	if !ok || restorer == nil {
		return errors.New("switch session snapshot restorer is not configured")
	}
	if err := restorer.RestoreSessionSnapshot(ctx, snap, target, source); err != nil {
		return err
	}
	return c.openProjectSession(ctx, snap.Session)
}

func (c *switchCommand) ensureAndOpenProjectSession(ctx context.Context, sessionName, target string) error {
	if err := c.sessions.EnsureSession(ctx, sessionName, target); err != nil {
		return fmt.Errorf("ensure tmux session %q: %w", sessionName, err)
	}
	return c.openProjectSession(ctx, sessionName)
}

func (c *switchCommand) openProjectSession(ctx context.Context, sessionName string) error {
	if err := c.sessions.OpenSession(ctx, sessionName); err != nil {
		return fmt.Errorf("open tmux session %q: %w", sessionName, err)
	}
	return nil
}

func (c *switchCommand) projectStartupSessionStateStore() (sessionstate.Store, error) {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return sessionstate.Store{}, err
	}
	return sessionstate.NewStore(paths.SessionStateDir()), nil
}

func (c *switchCommand) projectStartupNow() time.Time {
	return time.Now()
}
