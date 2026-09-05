package hooks

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/theme"
)

const projectConfigRelativePath = ".projmux/config.toml"

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type ProjectConfig struct {
	StartupRun string
	Hooks      map[Event]string
	Env        map[string]string
	Theme      theme.ThemeConfig
	UI         UIConfig
	AI         AIConfig
	Update     UpdateConfig
}

const LegacyKubeConfigDiagnostic = "legacy [kube] support was removed; manually move context to [env] KUBE_CONTEXT and namespace to [env] KUBE_NAMESPACE, then remove [kube]; original config was not changed"

type UIConfig struct {
	Locale     string
	NativeKeys *bool
}

// UpdateConfig holds the [update] section. ReleaseChannel is the persisted
// release-channel opt-in: an empty value means the setting was never written,
// which is what lets the environment stay a fallback for an install that has
// never touched the toggle. The value is stored verbatim and interpreted
// fail-closed by the reader, so a channel this binary does not recognise is
// treated as the default rather than rejected at parse time.
type UpdateConfig struct {
	ReleaseChannel string
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
			if next == "kube" {
				return ProjectConfig{}, errors.New(LegacyKubeConfigDiagnostic)
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
	case "startup", "env", "theme", "ui", "ai", "update":
		return true
	}
	if eventName, ok := strings.CutPrefix(section, "hooks."); ok {
		return normalizeEvent(Event(eventName)) != ""
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
	case "theme":
		if err := applyProjectThemeConfigValue(&cfg.Theme, key, value, lineNo); err != nil {
			return err
		}
	case "ui":
		switch key {
		case "locale":
			cfg.UI.Locale = value
		case "native_keys":
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("line %d: invalid ui native_keys %q: %w", lineNo, value, err)
			}
			cfg.UI.NativeKeys = &enabled
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
	case "update":
		switch key {
		case "release_channel":
			cfg.Update.ReleaseChannel = value
		default:
			return fmt.Errorf("line %d: unsupported update key %q", lineNo, key)
		}
	default:
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
	if section == "ui" && key == "native_keys" {
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
	cfg.Theme.Normalize()
	cfg.UI.Locale = strings.TrimSpace(cfg.UI.Locale)
	cfg.AI.ResumePickerLimit = ClampAIResumePickerLimit(cfg.AI.ResumePickerLimit)
	cfg.AI.ResumeScanDepth = ClampAIResumeScanDepth(cfg.AI.ResumeScanDepth)
	cfg.Update.ReleaseChannel = strings.TrimSpace(cfg.Update.ReleaseChannel)
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
	return nil
}

func writeProjectConfigFile(path string, cfg ProjectConfig) error {
	return writeProjectConfigFileMode(path, cfg, 0o644)
}

func writePrivateProjectConfigFile(path string, cfg ProjectConfig) error {
	return writeProjectConfigFileMode(path, cfg, 0o600)
}

func writeProjectConfigFileMode(path string, cfg ProjectConfig, mode os.FileMode) error {
	return writeProjectConfigFileModeWithOps(path, cfg, mode, defaultProjectConfigFileOps())
}

type projectConfigTempFile interface {
	Name() string
	WriteString(string) (int, error)
	Close() error
}

type projectConfigFileOps struct {
	lstat        func(string) (os.FileInfo, error)
	evalSymlinks func(string) (string, error)
	stat         func(string) (os.FileInfo, error)
	mkdirAll     func(string, os.FileMode) error
	createTemp   func(string, string) (projectConfigTempFile, error)
	chown        func(string, int, int) error
	chmod        func(string, os.FileMode) error
	rename       func(string, string) error
	remove       func(string) error
}

func defaultProjectConfigFileOps() projectConfigFileOps {
	return projectConfigFileOps{
		lstat:        os.Lstat,
		evalSymlinks: filepath.EvalSymlinks,
		stat:         os.Stat,
		mkdirAll:     os.MkdirAll,
		createTemp: func(dir, pattern string) (projectConfigTempFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		chown:  os.Chown,
		chmod:  os.Chmod,
		rename: os.Rename,
		remove: os.Remove,
	}
}

func writeProjectConfigFileModeWithOps(path string, cfg ProjectConfig, mode os.FileMode, ops projectConfigFileOps) error {
	writePath, existing, err := resolveProjectConfigWritePath(path, ops)
	if err != nil {
		return err
	}

	dir := filepath.Dir(writePath)
	if err := ops.mkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := ops.createTemp(dir, "config.toml.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = ops.remove(tmpName)
	}()

	if _, err := tmp.WriteString(renderProjectConfig(cfg)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	writeMode := mode
	if existing != nil {
		writeMode = existing.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
		if uid, gid, ok := projectConfigFileOwner(existing); ok {
			if err := ops.chown(tmpName, uid, gid); err != nil {
				return err
			}
		}
	}
	// Chown may clear set-ID bits, so restore the complete supported mode after
	// ownership has been applied. Missing destinations retain the public 0644
	// and private 0600 writer defaults.
	if err := ops.chmod(tmpName, writeMode); err != nil {
		return err
	}
	return ops.rename(tmpName, writePath)
}

func resolveProjectConfigWritePath(path string, ops projectConfigFileOps) (string, os.FileInfo, error) {
	info, err := ops.lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil, nil
		}
		return "", nil, err
	}

	writePath := path
	resolvedSymlink := info.Mode()&os.ModeSymlink != 0
	if resolvedSymlink {
		writePath, err = ops.evalSymlinks(path)
		if err != nil {
			// Preserve the historical recovery behaviour for a dangling link:
			// replacing the link path produces a healthy regular config file.
			if errors.Is(err, os.ErrNotExist) {
				return path, nil, nil
			}
			return "", nil, err
		}
	}

	existing, err := ops.stat(writePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !resolvedSymlink {
			return writePath, nil, nil
		}
		return "", nil, err
	}
	return writePath, existing, nil
}

// projectConfigFileOwner extracts Unix-like uid/gid metadata without making
// the writer platform-specific. Platforms whose FileInfo.Sys value does not
// expose those fields keep the atomic mode contract and skip ownership work.
func projectConfigFileOwner(info os.FileInfo) (int, int, bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, 0, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	uid := value.FieldByName("Uid")
	gid := value.FieldByName("Gid")
	if !uid.IsValid() || !gid.IsValid() || !uid.CanUint() || !gid.CanUint() {
		return 0, 0, false
	}
	uidValue, err := strconv.Atoi(strconv.FormatUint(uid.Uint(), 10))
	if err != nil {
		return 0, 0, false
	}
	gidValue, err := strconv.Atoi(strconv.FormatUint(gid.Uint(), 10))
	if err != nil {
		return 0, 0, false
	}
	return uidValue, gidValue, true
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
	if cfg.Theme.HasContent() {
		sections = append(sections, renderThemeConfigSection(cfg.Theme))
	}
	if cfg.UI.Locale != "" || cfg.UI.NativeKeys != nil {
		var b strings.Builder
		b.WriteString("[ui]\n")
		if cfg.UI.Locale != "" {
			fmt.Fprintf(&b, "locale = %s\n", strconv.Quote(strings.TrimSpace(cfg.UI.Locale)))
		}
		if cfg.UI.NativeKeys != nil {
			fmt.Fprintf(&b, "native_keys = %t\n", *cfg.UI.NativeKeys)
		}
		sections = append(sections, b.String())
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
	if strings.TrimSpace(cfg.Update.ReleaseChannel) != "" {
		sections = append(sections, fmt.Sprintf("[update]\nrelease_channel = %s\n", strconv.Quote(strings.TrimSpace(cfg.Update.ReleaseChannel))))
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n")
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
