package hooks

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/theme"
)

const projectConfigRelativePath = ".projmux/config.toml"

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// insertFileTextNamePattern bounds `[insert_file_text.<name>]` source names to a
// filename-safe, binding-safe identifier set.
var insertFileTextNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

const insertFileTextSectionPrefix = "insert_file_text."

type ProjectConfig struct {
	StartupRun     string
	Hooks          map[Event]string
	Env            map[string]string
	Kube           KubeConfig
	Theme          theme.ThemeConfig
	UI             UIConfig
	AI             AIConfig
	InsertFileText map[string]InsertFileTextSource
}

// InsertFileTextSource describes one `[insert_file_text.<name>]` source: a text
// file whose (optionally trimmed) contents are inserted into the active pane.
type InsertFileTextSource struct {
	// Path is the source file. A leading `~` is expanded at read time, not here.
	Path string
	// Trim reports whether surrounding whitespace is stripped before insert.
	// Defaults to true; the parser seeds new sources with Trim=true so an
	// unspecified `trim` behaves as trim-on.
	Trim bool
}

// ValidateInsertFileTextName reports whether name is a supported
// `[insert_file_text.<name>]` source identifier.
func ValidateInsertFileTextName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("insert_file_text source name is required")
	}
	if !insertFileTextNamePattern.MatchString(name) {
		return fmt.Errorf("invalid insert_file_text source name %q", name)
	}
	return nil
}

type KubeConfig struct {
	Context   string
	Namespace string
}

type UIConfig struct {
	Locale string
}

// AIConfig holds the [ai] section. It is intentionally extensible: Phase 1
// added ResumePickerLimit; Phase 2 adds ResumeScanDepth alongside it.
type AIConfig struct {
	// ResumePickerLimit caps how many recent sessions the AI resume picker
	// lists. Zero means unset (callers fall back to their own default). Stored
	// values are clamped to [AIResumePickerLimitMin, AIResumePickerLimitMax].
	ResumePickerLimit int

	// ResumeScanDepth widens the AI resume picker to include sessions started in
	// directories up to N levels below the current cwd. Zero means unset, which
	// is identical to the historical exact-cwd behaviour (depth 0). Stored
	// values are clamped to [0, AIResumeScanDepthMax].
	ResumeScanDepth int
}

// AIResumePickerLimit bounds. A configured value outside this range is clamped
// on write (normalizeProjectConfig); zero stays zero and means "not set".
const (
	AIResumePickerLimitMin = 1
	AIResumePickerLimitMax = 100
)

// AIResumeScanDepth bounds. Depth 0 is the default exact-cwd behaviour; a
// configured value above the max is clamped on write. Unlike the picker limit,
// zero is a meaningful value (exact cwd) as well as the "unset" sentinel — both
// resolve to the same behaviour, so render simply omits a zero depth.
const (
	AIResumeScanDepthMin = 0
	AIResumeScanDepthMax = 8
)

// ClampAIResumePickerLimit constrains a configured (non-zero) limit to the
// supported range. Zero is preserved so it keeps meaning "unset".
func ClampAIResumePickerLimit(limit int) int {
	if limit == 0 {
		return 0
	}
	if limit < AIResumePickerLimitMin {
		return AIResumePickerLimitMin
	}
	if limit > AIResumePickerLimitMax {
		return AIResumePickerLimitMax
	}
	return limit
}

// ClampAIResumeScanDepth constrains a configured scan depth to the supported
// range. Negative depths collapse to zero (unset/exact cwd); oversized depths
// clamp to AIResumeScanDepthMax.
func ClampAIResumeScanDepth(depth int) int {
	if depth <= AIResumeScanDepthMin {
		return AIResumeScanDepthMin
	}
	if depth > AIResumeScanDepthMax {
		return AIResumeScanDepthMax
	}
	return depth
}

func (c ProjectConfig) SessionEnv() map[string]string {
	env := map[string]string{}
	maps.Copy(env, c.Env)
	if c.Kube.Context != "" {
		env["PROJMUX_KUBE_CONTEXT"] = c.Kube.Context
		env["KUBE_CONTEXT"] = c.Kube.Context
	}
	if c.Kube.Namespace != "" {
		env["PROJMUX_KUBE_NAMESPACE"] = c.Kube.Namespace
		env["KUBE_NAMESPACE"] = c.Kube.Namespace
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func (c ProjectConfig) hookRun(event Event) string {
	if c.Hooks == nil {
		return ""
	}
	return strings.TrimSpace(c.Hooks[event])
}

func (c ProjectConfig) hasEventSurface(event Event) bool {
	return c.hookRun(event) != ""
}

func (c ProjectConfig) hasSessionEnv() bool {
	return len(c.SessionEnv()) > 0
}

func (c ProjectConfig) relevantForEvent(event Event) bool {
	return c.hasEventSurface(event)
}

func ParseProjectConfig(content string) (ProjectConfig, error) {
	cfg := ProjectConfig{
		Hooks: map[Event]string{},
		Env:   map[string]string{},
	}
	section := ""
	for lineNo, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(stripConfigComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			next := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if next == "" {
				return ProjectConfig{}, fmt.Errorf("line %d: empty section", lineNo+1)
			}
			if !isSupportedProjectConfigSection(next) {
				return ProjectConfig{}, fmt.Errorf("line %d: unsupported section %q", lineNo+1, next)
			}
			section = next
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ProjectConfig{}, fmt.Errorf("line %d: expected key = \"value\"", lineNo+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return ProjectConfig{}, fmt.Errorf("line %d: empty key", lineNo+1)
		}
		decoded, err := parseProjectConfigValue(section, key, value)
		if err != nil {
			return ProjectConfig{}, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		if err := applyProjectConfigValue(&cfg, section, key, decoded, lineNo+1); err != nil {
			return ProjectConfig{}, err
		}
	}
	if len(cfg.Hooks) == 0 {
		cfg.Hooks = nil
	}
	if len(cfg.Env) == 0 {
		cfg.Env = nil
	}
	if len(cfg.InsertFileText) == 0 {
		cfg.InsertFileText = nil
	}
	return cfg, nil
}

func UpdateProjectConfig(path string, update func(*ProjectConfig) error) (ProjectConfig, error) {
	cfg := ProjectConfig{
		Hooks: map[Event]string{},
		Env:   map[string]string{},
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return ProjectConfig{}, err
		}
	} else {
		cfg, err = ParseProjectConfig(string(content))
		if err != nil {
			return ProjectConfig{}, err
		}
	}
	if cfg.Hooks == nil {
		cfg.Hooks = map[Event]string{}
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	if update != nil {
		if err := update(&cfg); err != nil {
			return ProjectConfig{}, err
		}
	}
	if err := validateProjectConfig(cfg); err != nil {
		return ProjectConfig{}, err
	}
	normalizeProjectConfig(&cfg)
	if err := writeProjectConfigFile(path, cfg); err != nil {
		return ProjectConfig{}, err
	}
	return cfg, nil
}

func ValidateProjectEnvKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("env key is required")
	}
	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid env key %q", key)
	}
	return nil
}

func loadProjectConfig(path string) (ProjectConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return ProjectConfig{}, err
	}
	return ParseProjectConfig(string(content))
}

func isSupportedProjectConfigSection(section string) bool {
	switch section {
	case "startup", "env", "kube", "theme", "ui", "ai":
		return true
	}
	if eventName, ok := strings.CutPrefix(section, "hooks."); ok {
		return normalizeEvent(Event(eventName)) != ""
	}
	if name, ok := strings.CutPrefix(section, insertFileTextSectionPrefix); ok {
		return ValidateInsertFileTextName(name) == nil
	}
	return false
}

func applyProjectConfigValue(cfg *ProjectConfig, section, key, value string, lineNo int) error {
	switch section {
	case "startup":
		if key != "run" {
			return fmt.Errorf("line %d: unsupported startup key %q", lineNo, key)
		}
		cfg.StartupRun = value
	case "env":
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("line %d: invalid env key %q", lineNo, key)
		}
		cfg.Env[key] = value
	case "kube":
		switch key {
		case "context":
			cfg.Kube.Context = value
		case "namespace":
			cfg.Kube.Namespace = value
		default:
			return fmt.Errorf("line %d: unsupported kube key %q", lineNo, key)
		}
	case "theme":
		if err := applyProjectThemeConfigValue(&cfg.Theme, key, value, lineNo); err != nil {
			return err
		}
	case "ui":
		switch key {
		case "locale":
			cfg.UI.Locale = value
		default:
			return fmt.Errorf("line %d: unsupported ui key %q", lineNo, key)
		}
	case "ai":
		switch key {
		case "resume_picker_limit":
			limit, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("line %d: invalid ai resume_picker_limit %q: %w", lineNo, value, err)
			}
			cfg.AI.ResumePickerLimit = limit
		case "resume_scan_depth":
			depth, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("line %d: invalid ai resume_scan_depth %q: %w", lineNo, value, err)
			}
			cfg.AI.ResumeScanDepth = depth
		default:
			return fmt.Errorf("line %d: unsupported ai key %q", lineNo, key)
		}
	default:
		if name, ok := strings.CutPrefix(section, insertFileTextSectionPrefix); ok {
			return applyInsertFileTextConfigValue(cfg, name, key, value, lineNo)
		}
		eventName, ok := strings.CutPrefix(section, "hooks.")
		if !ok || key != "run" {
			return fmt.Errorf("line %d: unsupported key %q in section %q", lineNo, key, section)
		}
		event := normalizeEvent(Event(eventName))
		if event == "" {
			return fmt.Errorf("line %d: unsupported hook event %q", lineNo, eventName)
		}
		cfg.Hooks[event] = value
	}
	return nil
}

func applyInsertFileTextConfigValue(cfg *ProjectConfig, name, key, value string, lineNo int) error {
	if err := ValidateInsertFileTextName(name); err != nil {
		return fmt.Errorf("line %d: %w", lineNo, err)
	}
	if cfg.InsertFileText == nil {
		cfg.InsertFileText = map[string]InsertFileTextSource{}
	}
	// Seed new sources with the trim-on default so an unspecified `trim` key
	// resolves to true rather than the bool zero value.
	source, ok := cfg.InsertFileText[name]
	if !ok {
		source = InsertFileTextSource{Trim: true}
	}
	switch key {
	case "path":
		source.Path = value
	case "trim":
		trim, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("line %d: invalid insert_file_text trim %q: %w", lineNo, value, err)
		}
		source.Trim = trim
	default:
		return fmt.Errorf("line %d: unsupported insert_file_text key %q", lineNo, key)
	}
	cfg.InsertFileText[name] = source
	return nil
}

func applyProjectThemeConfigValue(cfg *theme.ThemeConfig, key, value string, lineNo int) error {
	switch key {
	case "preset":
		cfg.Preset = value
	case "background":
		cfg.Background = value
	case "surface":
		cfg.Surface = value
	case "status_background":
		cfg.StatusBackground = value
	case "surface_active":
		cfg.SurfaceActive = value
	case "chrome_foreground":
		cfg.ChromeForeground = value
	case "text_primary":
		cfg.TextPrimary = value
	case "foreground":
		cfg.Foreground = value
	case "muted":
		cfg.Muted = value
	case "accent":
		cfg.Accent = value
	case "critical":
		cfg.Critical = value
	case "warning":
		cfg.Warning = value
	case "progress":
		cfg.Progress = value
	case "success":
		cfg.Success = value
	case "action_required":
		cfg.ActionRequired = value
	case "pane_active_bg":
		cfg.PaneActiveBg = value
	case "focus":
		cfg.Focus = value
	case "font_family", "font_size":
		// Deprecated theme font keys (removed in Phase 1b). They never applied
		// to the terminal, so leftover keys are accepted for backward
		// compatibility but ignored rather than stored or resolved.
	default:
		return fmt.Errorf("line %d: unsupported theme key %q", lineNo, key)
	}
	return nil
}

func parseProjectConfigValue(section, key, value string) (string, error) {
	if strings.HasPrefix(section, insertFileTextSectionPrefix) && key == "trim" {
		// trim is a bare boolean (no quotes), e.g.
		//   [insert_file_text.latest_screenshot]
		//   trim = true
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("value must be a boolean")
		}
		if _, err := strconv.ParseBool(value); err != nil {
			return "", fmt.Errorf("invalid boolean value: %w", err)
		}
		return value, nil
	}
	if section == "ai" && (key == "resume_picker_limit" || key == "resume_scan_depth") {
		// resume_picker_limit and resume_scan_depth are bare integers (no
		// quotes), e.g.
		//   [ai]
		//   resume_picker_limit = 50
		//   resume_scan_depth = 2
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("value must be an integer")
		}
		if _, err := strconv.Atoi(value); err != nil {
			return "", fmt.Errorf("invalid integer value: %w", err)
		}
		return value, nil
	}
	if section == "theme" && key == "font_size" && !strings.HasPrefix(strings.TrimSpace(value), "\"") {
		// font_size is a deprecated, ignored key (Phase 1b). It used to be
		// written as a bare integer, so tolerate that form here to keep old
		// configs loadable; applyProjectThemeConfigValue discards the value.
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("value must be a quoted string or integer")
		}
		if _, err := strconv.Atoi(value); err != nil {
			return "", fmt.Errorf("invalid integer value: %w", err)
		}
		return value, nil
	}
	return parseQuotedConfigString(value)
}

func parseQuotedConfigString(value string) (string, error) {
	if !strings.HasPrefix(value, "\"") {
		return "", fmt.Errorf("value must be a quoted string")
	}
	decoded, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("invalid quoted string: %w", err)
	}
	return decoded, nil
}

func stripConfigComment(line string) string {
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

func mergeConfigEnv(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	merged := map[string]string{}
	maps.Copy(merged, base)
	maps.Copy(merged, overlay)
	return merged
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeProjectConfig(cfg *ProjectConfig) {
	cfg.StartupRun = strings.TrimSpace(cfg.StartupRun)
	if len(cfg.Hooks) == 0 {
		cfg.Hooks = nil
	}
	if len(cfg.Env) == 0 {
		cfg.Env = nil
	}
	cfg.Kube.Context = strings.TrimSpace(cfg.Kube.Context)
	cfg.Kube.Namespace = strings.TrimSpace(cfg.Kube.Namespace)
	cfg.Theme.Normalize()
	cfg.UI.Locale = strings.TrimSpace(cfg.UI.Locale)
	cfg.AI.ResumePickerLimit = ClampAIResumePickerLimit(cfg.AI.ResumePickerLimit)
	cfg.AI.ResumeScanDepth = ClampAIResumeScanDepth(cfg.AI.ResumeScanDepth)
	for name, source := range cfg.InsertFileText {
		source.Path = strings.TrimSpace(source.Path)
		cfg.InsertFileText[name] = source
	}
	if len(cfg.InsertFileText) == 0 {
		cfg.InsertFileText = nil
	}
}

func validateProjectConfig(cfg ProjectConfig) error {
	for key := range cfg.Env {
		if err := ValidateProjectEnvKey(key); err != nil {
			return err
		}
	}
	for event := range cfg.Hooks {
		if normalizeEvent(event) == "" {
			return fmt.Errorf("unsupported hook event %q", event)
		}
	}
	for name, source := range cfg.InsertFileText {
		if err := ValidateInsertFileTextName(name); err != nil {
			return err
		}
		if strings.TrimSpace(source.Path) == "" {
			return fmt.Errorf("insert_file_text source %q requires a path", name)
		}
	}
	return nil
}

func writeProjectConfigFile(path string, cfg ProjectConfig) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "config.toml.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(renderProjectConfig(cfg)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func renderProjectConfig(cfg ProjectConfig) string {
	var sections []string
	for _, event := range SupportedEvents {
		run := ""
		if cfg.Hooks != nil {
			run = strings.TrimSpace(cfg.Hooks[event])
		}
		if run == "" {
			continue
		}
		sections = append(sections, fmt.Sprintf("[hooks.%s]\nrun = %s\n", event, strconv.Quote(run)))
	}
	if strings.TrimSpace(cfg.StartupRun) != "" {
		sections = append(sections, fmt.Sprintf("[startup]\nrun = %s\n", strconv.Quote(strings.TrimSpace(cfg.StartupRun))))
	}
	if len(cfg.Env) > 0 {
		var b strings.Builder
		b.WriteString("[env]\n")
		for _, key := range sortedEnvKeys(cfg.Env) {
			b.WriteString(key)
			b.WriteString(" = ")
			b.WriteString(strconv.Quote(cfg.Env[key]))
			b.WriteString("\n")
		}
		sections = append(sections, b.String())
	}
	if cfg.Kube.Context != "" || cfg.Kube.Namespace != "" {
		var b strings.Builder
		b.WriteString("[kube]\n")
		if cfg.Kube.Context != "" {
			b.WriteString("context = ")
			b.WriteString(strconv.Quote(cfg.Kube.Context))
			b.WriteString("\n")
		}
		if cfg.Kube.Namespace != "" {
			b.WriteString("namespace = ")
			b.WriteString(strconv.Quote(cfg.Kube.Namespace))
			b.WriteString("\n")
		}
		sections = append(sections, b.String())
	}
	if cfg.Theme.HasContent() {
		sections = append(sections, renderThemeConfigSection(cfg.Theme))
	}
	if cfg.UI.Locale != "" {
		sections = append(sections, fmt.Sprintf("[ui]\nlocale = %s\n", strconv.Quote(strings.TrimSpace(cfg.UI.Locale))))
	}
	if cfg.AI.ResumePickerLimit != 0 || cfg.AI.ResumeScanDepth != 0 {
		var b strings.Builder
		b.WriteString("[ai]\n")
		if cfg.AI.ResumePickerLimit != 0 {
			fmt.Fprintf(&b, "resume_picker_limit = %d\n", cfg.AI.ResumePickerLimit)
		}
		if cfg.AI.ResumeScanDepth != 0 {
			fmt.Fprintf(&b, "resume_scan_depth = %d\n", cfg.AI.ResumeScanDepth)
		}
		sections = append(sections, b.String())
	}
	for _, name := range sortedInsertFileTextNames(cfg.InsertFileText) {
		source := cfg.InsertFileText[name]
		var b strings.Builder
		fmt.Fprintf(&b, "[%s%s]\n", insertFileTextSectionPrefix, name)
		b.WriteString("path = ")
		b.WriteString(strconv.Quote(strings.TrimSpace(source.Path)))
		b.WriteString("\n")
		// trim defaults to true, so only the opt-out is persisted; a rendered
		// config round-trips back to the same source.
		if !source.Trim {
			b.WriteString("trim = false\n")
		}
		sections = append(sections, b.String())
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n")
}

func sortedInsertFileTextNames(sources map[string]InsertFileTextSource) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func renderThemeConfigSection(cfg theme.ThemeConfig) string {
	cfg.Normalize()
	var b strings.Builder
	b.WriteString("[theme]\n")
	for _, field := range []struct {
		key   string
		value string
	}{
		{"preset", cfg.Preset},
		{"background", cfg.Background},
		{"surface", cfg.Surface},
		{"status_background", cfg.StatusBackground},
		{"surface_active", cfg.SurfaceActive},
		{"chrome_foreground", cfg.ChromeForeground},
		{"text_primary", cfg.TextPrimary},
		{"foreground", cfg.Foreground},
		{"muted", cfg.Muted},
		{"accent", cfg.Accent},
		{"critical", cfg.Critical},
		{"warning", cfg.Warning},
		{"progress", cfg.Progress},
		{"success", cfg.Success},
		{"action_required", cfg.ActionRequired},
		{"pane_active_bg", cfg.PaneActiveBg},
		{"focus", cfg.Focus},
	} {
		if field.value == "" {
			continue
		}
		b.WriteString(field.key)
		b.WriteString(" = ")
		b.WriteString(strconv.Quote(field.value))
		b.WriteString("\n")
	}
	return b.String()
}

type projectConfigFile struct {
	repo string
	rel  string
	path string
}

func discoverProjectConfig(cwd string) projectConfigFile {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return projectConfigFile{}
	}
	repo, err := filepath.Abs(cwd)
	if err != nil {
		return projectConfigFile{}
	}
	path := filepath.Join(repo, projectConfigRelativePath)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return projectConfigFile{}
	}
	return projectConfigFile{
		repo: repo,
		rel:  projectConfigRelativePath,
		path: path,
	}
}
