package preview

import (
	"errors"
	"strings"

	"github.com/es5h/projmux/internal/config"
	"github.com/es5h/projmux/internal/state"
)

var ErrInvalidSessionName = errors.New("invalid session name")

// Store manages file-backed preview selection state keyed by session name.
type Store struct {
	file state.LinesFile
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

	rows, err := s.load()
	if err != nil {
		return err
	}

	lines := make([]string, 0, len(rows)+1)
	for _, row := range rows {
		if row.SessionName == sessionName {
			continue
		}
		lines = append(lines, row.line())
	}

	lines = append(lines, Selection{
		SessionName: sessionName,
		WindowIndex: windowIndex,
		PaneIndex:   paneIndex,
	}.line())
	return s.file.Write(lines)
}

// Selection captures the stored preview target for a session.
type Selection struct {
	SessionName string
	WindowIndex string
	PaneIndex   string
}

func (s Store) load() ([]Selection, error) {
	lines, err := s.file.Read()
	if err != nil {
		return nil, err
	}

	rows := make([]Selection, 0, len(lines))
	for _, line := range lines {
		row, ok := parseSelection(line)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
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
