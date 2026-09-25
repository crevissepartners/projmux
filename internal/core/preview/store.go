package preview

import (
	"errors"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/state"
)

var ErrInvalidSessionName = errors.New("invalid session name")

// Store manages file-backed preview selection state keyed by session name.
type Store struct {
	file state.LinesFile
	// afterLoad is a test seam run inside a locked update after the stored
	// rows were parsed.
	afterLoad func()
}

// CycleResult reports the cursor chosen by a persisted cycle operation.
type CycleResult struct {
	Cursor   Cursor
	Selected bool
	Changed  bool
}

// NewStore builds a preview state store for the provided file path.
func NewStore(path string) Store {
	return Store{file: state.NewLinesFile(path)}
}

// NewDefaultStore builds a preview state store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.PreviewStateFile())
}

// Path returns the file path used by this store.
func (s Store) Path() string {
	return s.file.Path()
}

// ReadWindowIndex returns the selected window index for a session.
func (s Store) ReadWindowIndex(sessionName string) (string, bool, error) {
	sessionName, err := validateSessionName(sessionName)
	if err != nil {
		return "", false, err
	}

	rows, err := s.load()
	if err != nil {
		return "", false, err
	}

	for _, row := range rows {
		if row.SessionName == sessionName {
			return row.WindowIndex, true, nil
		}
	}

	return "", false, nil
}

// ReadPaneIndex returns the selected pane index for a session.
func (s Store) ReadPaneIndex(sessionName string) (string, bool, error) {
	sessionName, err := validateSessionName(sessionName)
	if err != nil {
		return "", false, err
	}

	rows, err := s.load()
	if err != nil {
		return "", false, err
	}

	for _, row := range rows {
		if row.SessionName == sessionName {
			if row.PaneIndex != "" {
				return row.PaneIndex, true, nil
			}
			return row.WindowIndex, true, nil
		}
	}

	return "", false, nil
}

// ReadSelection returns the full stored selection row for a session.
func (s Store) ReadSelection(sessionName string) (Selection, bool, error) {
	sessionName, err := validateSessionName(sessionName)
	if err != nil {
		return Selection{}, false, err
	}

	rows, err := s.load()
	if err != nil {
		return Selection{}, false, err
	}

	for _, row := range rows {
		if row.SessionName == sessionName {
			return row, true, nil
		}
	}

	return Selection{}, false, nil
}

// WriteSelection updates the selection row for a session while preserving
// unrelated rows.
func (s Store) WriteSelection(sessionName, windowIndex, paneIndex string) error {
	sessionName, err := validateSessionName(sessionName)
	if err != nil {
		return err
	}
	if err := validateCell(windowIndex); err != nil {
		return err
	}
	if err := validateCell(paneIndex); err != nil {
		return err
	}

	next := Selection{
		SessionName: sessionName,
		WindowIndex: windowIndex,
		PaneIndex:   paneIndex,
	}
	return s.update(func(rows []Selection) ([]string, bool, error) {
		return replaceRow(rows, next), true, nil
	})
}

// Delete removes the persisted preview selection for sessionName. Missing
// selections are treated as a successful no-op.
func (s Store) Delete(sessionName string) error {
	sessionName, err := validateSessionName(sessionName)
	if err != nil {
		return err
	}

	return s.update(func(rows []Selection) ([]string, bool, error) {
		lines := make([]string, 0, len(rows))
		found := false
		for _, row := range rows {
			if row.SessionName == sessionName {
				found = true
				continue
			}
			lines = append(lines, row.line())
		}
		return lines, found, nil
	})
}

// CyclePaneSelection loads a session's stored selection, applies pane cycling,
// and persists the resulting cursor when it changes.
func (s Store) CyclePaneSelection(sessionName string, windows []Window, panes []Pane, direction Direction) (CycleResult, error) {
	return s.cycleSelection(sessionName, windows, panes, direction, cyclePane)
}

// CycleWindowSelection loads a session's stored selection, applies window
// cycling, and persists the resulting cursor when it changes.
func (s Store) CycleWindowSelection(sessionName string, windows []Window, panes []Pane, direction Direction) (CycleResult, error) {
	return s.cycleSelection(sessionName, windows, panes, direction, cycleWindow)
}

// Selection captures the stored preview target for a session.
type Selection struct {
	SessionName string
	WindowIndex string
	PaneIndex   string
}

type cycleFunc func(CycleInputs, Direction) (Cursor, bool, error)

func (s Store) cycleSelection(
	sessionName string,
	windows []Window,
	panes []Pane,
	direction Direction,
	cycle cycleFunc,
) (CycleResult, error) {
	sessionName, err := validateSessionName(sessionName)
	if err != nil {
		return CycleResult{}, err
	}

	// The cursor is computed from the row read under the lock, so a
	// concurrent writer cannot slip between reading and persisting it.
	var result CycleResult
	err = s.update(func(rows []Selection) ([]string, bool, error) {
		result = CycleResult{}
		var selection Selection
		found := false
		for _, row := range rows {
			if row.SessionName == sessionName {
				selection, found = row, true
				break
			}
		}

		cursor, ok, err := cycle(CycleInputs{
			StoredWindowIndex: selection.WindowIndex,
			StoredPaneIndex:   selection.PaneIndex,
			Windows:           windows,
			Panes:             panes,
		}, direction)
		if err != nil || !ok {
			return nil, false, err
		}

		changed := !found || selection.WindowIndex != cursor.WindowIndex || selection.PaneIndex != cursor.PaneIndex
		result = CycleResult{
			Cursor:   cursor,
			Selected: true,
			Changed:  changed,
		}
		if !changed {
			return nil, false, nil
		}
		if err := validateCell(cursor.WindowIndex); err != nil {
			return nil, false, err
		}
		if err := validateCell(cursor.PaneIndex); err != nil {
			return nil, false, err
		}
		return replaceRow(rows, Selection{
			SessionName: sessionName,
			WindowIndex: cursor.WindowIndex,
			PaneIndex:   cursor.PaneIndex,
		}), true, nil
	})
	if err != nil {
		return CycleResult{}, err
	}
	return result, nil
}

// update runs a locked read-modify-write of the stored rows. apply returns the
// next lines and whether to write them.
func (s Store) update(apply func([]Selection) ([]string, bool, error)) error {
	return s.file.Update(func(lines []string) ([]string, bool, error) {
		rows := parseRows(lines)
		if s.afterLoad != nil {
			s.afterLoad()
		}
		return apply(rows)
	})
}

// replaceRow drops the session's old row and appends next at the end.
func replaceRow(rows []Selection, next Selection) []string {
	lines := make([]string, 0, len(rows)+1)
	for _, row := range rows {
		if row.SessionName == next.SessionName {
			continue
		}
		lines = append(lines, row.line())
	}
	return append(lines, next.line())
}

func (s Store) load() ([]Selection, error) {
	lines, err := s.file.Read()
	if err != nil {
		return nil, err
	}
	return parseRows(lines), nil
}

func parseRows(lines []string) []Selection {
	rows := make([]Selection, 0, len(lines))
	for _, line := range lines {
		row, ok := parseSelection(line)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func parseSelection(line string) (Selection, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) < 2 {
		return Selection{}, false
	}
	if strings.TrimSpace(fields[0]) == "" {
		return Selection{}, false
	}

	row := Selection{
		SessionName: fields[0],
		WindowIndex: fields[1],
	}
	if len(fields) >= 3 {
		row.PaneIndex = fields[2]
	}
	return row, true
}

func (s Selection) line() string {
	return strings.Join([]string{s.SessionName, s.WindowIndex, s.PaneIndex}, "\t")
}

func validateSessionName(sessionName string) (string, error) {
	if strings.TrimSpace(sessionName) == "" {
		return "", ErrInvalidSessionName
	}
	if strings.ContainsAny(sessionName, "\t\r\n") {
		return "", ErrInvalidSessionName
	}
	return sessionName, nil
}

func validateCell(value string) error {
	if strings.ContainsAny(value, "\t\r\n") {
		return ErrInvalidSessionName
	}
	return nil
}
