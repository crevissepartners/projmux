// Package layout manages project-local tmux layout presets.
package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

const (
	// SchemaVersion is the current project layout preset TOML schema.
	SchemaVersion = 1

	ModeInheritAutosave = "inherit-autosave"
	ModeFreshEachTime   = "fresh-each-time"

	dirName  = ".projmux/layouts"
	fileMode = 0o644
)

var (
	ErrNotFound        = errors.New("layout preset not found")
	ErrInvalidName     = errors.New("invalid layout preset name")
	ErrInvalidPreset   = errors.New("invalid layout preset")
	ErrUnsupportedMode = errors.New("unsupported layout preset mode")

	placeholderPattern = regexp.MustCompile(`\$\{([^}]+)\}`)
)

// Preset is a project-local layout seed stored as TOML.
type Preset struct {
	SchemaVersion int
	Description   string
	Mode          string
	DefaultCWD    string
	Windows       []Window
}

// Window mirrors the reusable tmux session-state window shape.
type Window struct {
	Index           int
	Name            string
	Layout          string
	ActivePaneIndex int
	Panes           []Pane
}

// Pane mirrors the reusable tmux session-state pane shape with a compact
// command field for startup recipes.
type Pane struct {
	Index    int
	CWD      string
	Recipe   sessionstate.Recipe
	Agent    string
	ResumeID string
	Topic    string
	Command  string
}

// Entry is the compact list view for one preset file.
type Entry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Mode        string `json:"mode"`
	Windows     int    `json:"windows"`
	Panes       int    `json:"panes"`
}

// Warning records a preset file that discovery skipped.
type Warning struct {
	Path string
	Err  error
}

// Store discovers and writes layouts below ProjectRoot/.projmux/layouts.
type Store struct {
	ProjectRoot string
}

func NewStore(projectRoot string) Store {
	return Store{ProjectRoot: filepath.Clean(strings.TrimSpace(projectRoot))}
}

func (s Store) Dir() string {
	return filepath.Join(s.ProjectRoot, dirName)
}

func (s Store) Path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir(), name+".toml"), nil
}

func (s Store) List() ([]Entry, []Warning, error) {
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("layout: read presets dir %s: %w", s.Dir(), err)
	}

	var out []Entry
	var warnings []Warning
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".toml")
		preset, err := s.Load(name)
		path := filepath.Join(s.Dir(), entry.Name())
		if err != nil {
			warnings = append(warnings, Warning{Path: path, Err: err})
			continue
		}
		out = append(out, presetEntry(name, path, preset))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, warnings, nil
}

func (s Store) Load(name string) (Preset, error) {
	path, err := s.Path(name)
	if err != nil {
		return Preset{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Preset{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return Preset{}, fmt.Errorf("layout: read preset %s: %w", path, err)
	}
	preset, err := Parse(string(data))
	if err != nil {
		return Preset{}, fmt.Errorf("layout: parse preset %s: %w", path, err)
	}
	if err := preset.Validate(); err != nil {
		return Preset{}, fmt.Errorf("layout: validate preset %s: %w", path, err)
	}
	return preset.Normalize(), nil
}

func (s Store) Save(name string, preset Preset) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	preset = preset.Normalize()
	if err := preset.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return fmt.Errorf("layout: create presets dir %s: %w", s.Dir(), err)
	}
	path, err := s.Path(name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir(), "."+name+".toml.tmp-*")
	if err != nil {
		return fmt.Errorf("layout: create temp preset: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("layout: chmod temp preset: %w", err)
	}
	if _, err := tmp.WriteString(Render(preset)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("layout: write temp preset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("layout: close temp preset: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("layout: rename temp preset: %w", err)
	}
	cleanup = false
	return nil
}

func FromSnapshot(snap sessionstate.Snapshot, projectRoot, description, mode string) Preset {
	p := Preset{
		SchemaVersion: SchemaVersion,
		Description:   strings.TrimSpace(description),
		Mode:          normalizeMode(mode),
		DefaultCWD:    portablePath(snap.DefaultCWD, projectRoot),
		Windows:       make([]Window, 0, len(snap.Windows)),
	}
	for _, window := range snap.Windows {
		out := Window{
			Index:           window.Index,
			Name:            window.Name,
			Layout:          window.Layout,
			ActivePaneIndex: window.ActivePaneIndex,
			Panes:           make([]Pane, 0, len(window.Panes)),
		}
		for _, pane := range window.Panes {
			out.Panes = append(out.Panes, paneFromSnapshot(pane, projectRoot))
		}
		p.Windows = append(p.Windows, out)
	}
	return p.Normalize()
}

func (p Preset) Validate() error {
	if p.SchemaVersion == 0 {
		return fmt.Errorf("%w: schema_version is required", ErrInvalidPreset)
	}
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema_version %d", ErrInvalidPreset, p.SchemaVersion)
	}
	if p.Mode != "" && p.Mode != ModeInheritAutosave && p.Mode != ModeFreshEachTime {
		return fmt.Errorf("%w: %s", ErrUnsupportedMode, p.Mode)
	}
	if err := validatePlaceholders("description", p.Description); err != nil {
		return err
	}
	if err := validatePath("default_cwd", p.DefaultCWD, true); err != nil {
		return err
	}
	for wi, window := range p.Windows {
		if window.Index < 0 {
			return fmt.Errorf("%w: window %d index must be non-negative", ErrInvalidPreset, wi)
		}
		if window.ActivePaneIndex < 0 {
			return fmt.Errorf("%w: window %d active_pane_index must be non-negative", ErrInvalidPreset, wi)
		}
		activePaneFound := len(window.Panes) == 0
		for pi, pane := range window.Panes {
			if pane.Index < 0 {
				return fmt.Errorf("%w: window %d pane %d index must be non-negative", ErrInvalidPreset, wi, pi)
			}
			if pane.Index == window.ActivePaneIndex {
				activePaneFound = true
			}
			if err := validatePath(fmt.Sprintf("window %d pane %d cwd", wi, pi), pane.CWD, false); err != nil {
				return err
			}
			if err := validatePaneRecipe(wi, pi, pane); err != nil {
				return err
			}
		}
		if !activePaneFound {
			return fmt.Errorf("%w: window %d active_pane_index %d does not match a pane index", ErrInvalidPreset, wi, window.ActivePaneIndex)
		}
	}
	return nil
}

func (p Preset) Normalize() Preset {
	p.SchemaVersion = SchemaVersion
	p.Description = strings.TrimSpace(p.Description)
	p.Mode = normalizeMode(p.Mode)
	p.DefaultCWD = strings.TrimSpace(p.DefaultCWD)
	for wi := range p.Windows {
		p.Windows[wi].Name = strings.TrimSpace(p.Windows[wi].Name)
		p.Windows[wi].Layout = strings.TrimSpace(p.Windows[wi].Layout)
		for pi := range p.Windows[wi].Panes {
			pane := &p.Windows[wi].Panes[pi]
			pane.CWD = strings.TrimSpace(pane.CWD)
			pane.Agent = strings.TrimSpace(pane.Agent)
			pane.ResumeID = strings.TrimSpace(pane.ResumeID)
			pane.Topic = strings.TrimSpace(pane.Topic)
			pane.Command = strings.TrimSpace(pane.Command)
			if pane.Command == "" && pane.Recipe.Kind == sessionstate.RecipeKindStartup {
				pane.Command = strings.TrimSpace(pane.Recipe.Command)
			}
			if pane.Recipe.Kind == "" {
				switch {
				case pane.Command != "":
					pane.Recipe = sessionstate.StartupRecipe(pane.Command)
				case pane.Agent != "":
					pane.Recipe = sessionstate.AgentRecipe(pane.Agent, pane.ResumeID, pane.Topic)
				default:
					pane.Recipe = sessionstate.ShellRecipe()
				}
			}
			if pane.Recipe.Kind == sessionstate.RecipeKindAgent {
				pane.Recipe = sessionstate.AgentRecipe(pane.Agent, pane.ResumeID, pane.Topic)
			}
			if pane.Recipe.Kind == sessionstate.RecipeKindStartup && pane.Command != "" {
				pane.Recipe = sessionstate.StartupRecipe(pane.Command)
			}
			if pane.Recipe.Kind == sessionstate.RecipeKindStartup {
				pane.Command = strings.TrimSpace(pane.Recipe.Command)
			}
			if pane.Recipe.Kind == sessionstate.RecipeKindAgent {
				pane.Agent = strings.TrimSpace(pane.Recipe.Agent)
				pane.ResumeID = strings.TrimSpace(pane.Recipe.ResumeID)
				pane.Topic = strings.TrimSpace(pane.Recipe.Topic)
			}
		}
	}
	return p
}

func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if strings.HasSuffix(name, ".toml") {
		return fmt.Errorf("%w: omit .toml suffix in %q", ErrInvalidName, name)
	}
	return nil
}

func Parse(content string) (Preset, error) {
	var p Preset
	section := rootSection
	currentWindow := -1
	currentPane := -1
	ignoredArray := false

	for lineNo, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]"))
			switch name {
			case "windows":
				p.Windows = append(p.Windows, Window{})
				currentWindow = len(p.Windows) - 1
				currentPane = -1
				section = windowSection
				ignoredArray = false
			case "windows.panes":
				if currentWindow < 0 {
					return Preset{}, fmt.Errorf("line %d: windows.panes must follow a windows entry", lineNo+1)
				}
				p.Windows[currentWindow].Panes = append(p.Windows[currentWindow].Panes, Pane{})
				currentPane = len(p.Windows[currentWindow].Panes) - 1
				section = paneSection
				ignoredArray = false
			default:
				section = ignoredSection
				ignoredArray = true
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = ignoredSection
			ignoredArray = true
			continue
		}
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			return Preset{}, fmt.Errorf("line %d: expected key = value", lineNo+1)
		}
		if ignoredArray {
			continue
		}
		key = strings.TrimSpace(key)
		rawValue = strings.TrimSpace(rawValue)
		if key == "" {
			return Preset{}, fmt.Errorf("line %d: empty key", lineNo+1)
		}
		if err := applyValue(&p, section, currentWindow, currentPane, key, rawValue, lineNo+1); err != nil {
			return Preset{}, err
		}
	}
	return p.Normalize(), nil
}

func Render(p Preset) string {
	p = p.Normalize()
	var b strings.Builder
	b.WriteString("schema_version = ")
	b.WriteString(strconv.Itoa(p.SchemaVersion))
	b.WriteString("\n")
	if p.Description != "" {
		b.WriteString("description = ")
		b.WriteString(strconv.Quote(p.Description))
		b.WriteString("\n")
	}
	b.WriteString("mode = ")
	b.WriteString(strconv.Quote(p.Mode))
	b.WriteString("\n")
	if p.DefaultCWD != "" {
		b.WriteString("default_cwd = ")
		b.WriteString(strconv.Quote(p.DefaultCWD))
		b.WriteString("\n")
	}
	for _, window := range p.Windows {
		b.WriteString("\n[[windows]]\n")
		b.WriteString("index = ")
		b.WriteString(strconv.Itoa(window.Index))
		b.WriteString("\n")
		if window.Name != "" {
			b.WriteString("name = ")
			b.WriteString(strconv.Quote(window.Name))
			b.WriteString("\n")
		}
		if window.Layout != "" {
			b.WriteString("layout = ")
			b.WriteString(strconv.Quote(window.Layout))
			b.WriteString("\n")
		}
		b.WriteString("active_pane_index = ")
		b.WriteString(strconv.Itoa(window.ActivePaneIndex))
		b.WriteString("\n")
		for _, pane := range window.Panes {
			b.WriteString("\n[[windows.panes]]\n")
			b.WriteString("index = ")
			b.WriteString(strconv.Itoa(pane.Index))
			b.WriteString("\n")
			b.WriteString("cwd = ")
			b.WriteString(strconv.Quote(pane.CWD))
			b.WriteString("\n")
			switch pane.Recipe.Kind {
			case sessionstate.RecipeKindStartup:
				b.WriteString("command = ")
				b.WriteString(strconv.Quote(strings.TrimSpace(pane.Recipe.Command)))
				b.WriteString("\n")
			case sessionstate.RecipeKindAgent:
				b.WriteString("recipe = ")
				b.WriteString(strconv.Quote(sessionstate.RecipeKindAgent))
				b.WriteString("\n")
				b.WriteString("agent = ")
				b.WriteString(strconv.Quote(pane.Recipe.Agent))
				b.WriteString("\n")
				if pane.Recipe.ResumeID != "" {
					b.WriteString("resume_id = ")
					b.WriteString(strconv.Quote(pane.Recipe.ResumeID))
					b.WriteString("\n")
				}
				if pane.Recipe.Topic != "" {
					b.WriteString("topic = ")
					b.WriteString(strconv.Quote(pane.Recipe.Topic))
					b.WriteString("\n")
				}
			default:
				b.WriteString("recipe = ")
				b.WriteString(strconv.Quote(sessionstate.RecipeKindShell))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func ToSnapshot(p Preset, session, projectRoot string, now time.Time) (sessionstate.Snapshot, error) {
	p = p.Normalize()
	if err := p.Validate(); err != nil {
		return sessionstate.Snapshot{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    session,
		DefaultCWD: expandPath(p.DefaultCWD, projectRoot, session),
		SavedAt:    now,
		Windows:    make([]sessionstate.Window, 0, len(p.Windows)),
	}
	for _, window := range p.Windows {
		out := sessionstate.Window{
			Index:           window.Index,
			Name:            window.Name,
			Layout:          window.Layout,
			ActivePaneIndex: window.ActivePaneIndex,
			Panes:           make([]sessionstate.Pane, 0, len(window.Panes)),
		}
		for _, pane := range window.Panes {
			out.Panes = append(out.Panes, sessionstate.Pane{
				Index:  pane.Index,
				CWD:    expandPath(pane.CWD, projectRoot, session),
				Recipe: pane.Recipe,
			})
		}
		snap.Windows = append(snap.Windows, out)
	}
	if err := snap.Validate(); err != nil {
		return sessionstate.Snapshot{}, err
	}
	return snap, nil
}

type parserSection int

const (
	rootSection parserSection = iota
	windowSection
	paneSection
	ignoredSection
)

func applyValue(p *Preset, section parserSection, currentWindow, currentPane int, key, raw string, lineNo int) error {
	switch section {
	case rootSection:
		switch key {
		case "schema_version":
			n, err := parseIntValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: schema_version: %w", lineNo, err)
			}
			p.SchemaVersion = n
		case "description":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: description: %w", lineNo, err)
			}
			p.Description = value
		case "mode":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: mode: %w", lineNo, err)
			}
			p.Mode = value
		case "default_cwd":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: default_cwd: %w", lineNo, err)
			}
			p.DefaultCWD = value
		}
	case windowSection:
		if currentWindow < 0 {
			return fmt.Errorf("line %d: window field without window", lineNo)
		}
		window := &p.Windows[currentWindow]
		switch key {
		case "index":
			n, err := parseIntValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: window index: %w", lineNo, err)
			}
			window.Index = n
		case "name":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: window name: %w", lineNo, err)
			}
			window.Name = value
		case "layout":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: window layout: %w", lineNo, err)
			}
			window.Layout = value
		case "active_pane_index":
			n, err := parseIntValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: window active_pane_index: %w", lineNo, err)
			}
			window.ActivePaneIndex = n
		}
	case paneSection:
		if currentWindow < 0 || currentPane < 0 {
			return fmt.Errorf("line %d: pane field without pane", lineNo)
		}
		pane := &p.Windows[currentWindow].Panes[currentPane]
		switch key {
		case "index":
			n, err := parseIntValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: pane index: %w", lineNo, err)
			}
			pane.Index = n
		case "cwd":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: pane cwd: %w", lineNo, err)
			}
			pane.CWD = value
		case "recipe":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: pane recipe: %w", lineNo, err)
			}
			pane.Recipe.Kind = value
		case "command":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: pane command: %w", lineNo, err)
			}
			pane.Command = value
			pane.Recipe = sessionstate.StartupRecipe(value)
		case "agent":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: pane agent: %w", lineNo, err)
			}
			pane.Agent = value
		case "resume_id":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: pane resume_id: %w", lineNo, err)
			}
			pane.ResumeID = value
		case "topic":
			value, err := parseStringValue(raw)
			if err != nil {
				return fmt.Errorf("line %d: pane topic: %w", lineNo, err)
			}
			pane.Topic = value
		}
	}
	return nil
}

func validatePaneRecipe(wi, pi int, pane Pane) error {
	for label, value := range map[string]string{
		"agent":     pane.Agent,
		"resume_id": pane.ResumeID,
		"topic":     pane.Topic,
		"command":   pane.Command,
	} {
		if err := validatePlaceholders(fmt.Sprintf("window %d pane %d %s", wi, pi, label), value); err != nil {
			return err
		}
	}
	return pane.Recipe.Validate()
}

func validatePath(label, value string, optional bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%w: %s is required", ErrInvalidPreset, label)
	}
	if err := validatePlaceholders(label, value); err != nil {
		return err
	}
	if strings.HasPrefix(value, "${PROJMUX_CWD}") {
		return nil
	}
	if filepath.IsAbs(value) {
		return nil
	}
	return fmt.Errorf("%w: %s must be absolute or start with ${PROJMUX_CWD}", ErrInvalidPreset, label)
}

func validatePlaceholders(label, value string) error {
	for _, match := range placeholderPattern.FindAllStringSubmatch(value, -1) {
		if len(match) != 2 {
			continue
		}
		switch match[1] {
		case "PROJMUX_CWD", "PROJMUX_SESSION":
		default:
			return fmt.Errorf("%w: %s uses unsupported placeholder ${%s}", ErrInvalidPreset, label, match[1])
		}
	}
	return nil
}

func parseStringValue(value string) (string, error) {
	if !strings.HasPrefix(value, `"`) {
		return "", fmt.Errorf("value must be a quoted string")
	}
	decoded, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("invalid quoted string: %w", err)
	}
	return decoded, nil
}

func parseIntValue(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("value must be an integer")
	}
	return n, nil
}

func stripComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if inString && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if !inString && r == '#' {
			return line[:i]
		}
	}
	return line
}

func normalizeMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return ModeInheritAutosave
	}
	return mode
}

func paneFromSnapshot(pane sessionstate.Pane, projectRoot string) Pane {
	out := Pane{
		Index:  pane.Index,
		CWD:    portablePath(pane.CWD, projectRoot),
		Recipe: pane.Recipe,
	}
	switch pane.Recipe.Kind {
	case sessionstate.RecipeKindStartup:
		out.Command = strings.TrimSpace(pane.Recipe.Command)
	case sessionstate.RecipeKindAgent:
		out.Agent = pane.Recipe.Agent
		out.ResumeID = pane.Recipe.ResumeID
		out.Topic = pane.Recipe.Topic
	}
	return out
}

func portablePath(path, projectRoot string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	projectRoot = filepath.Clean(strings.TrimSpace(projectRoot))
	if path == "" || projectRoot == "" {
		return path
	}
	rel, err := filepath.Rel(projectRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return "${PROJMUX_CWD}"
	}
	return "${PROJMUX_CWD}/" + filepath.ToSlash(rel)
}

func expandPath(path, projectRoot, session string) string {
	path = strings.ReplaceAll(path, "${PROJMUX_SESSION}", session)
	if path == "${PROJMUX_CWD}" {
		return filepath.Clean(projectRoot)
	}
	if after, ok := strings.CutPrefix(path, "${PROJMUX_CWD}/"); ok {
		return filepath.Join(projectRoot, filepath.FromSlash(after))
	}
	return path
}

func presetEntry(name, path string, preset Preset) Entry {
	panes := 0
	for _, window := range preset.Windows {
		panes += len(window.Panes)
	}
	return Entry{
		Name:        name,
		Path:        path,
		Description: preset.Description,
		Mode:        preset.Mode,
		Windows:     len(preset.Windows),
		Panes:       panes,
	}
}
