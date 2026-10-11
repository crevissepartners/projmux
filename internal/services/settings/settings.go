package settings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// InputError classifies invalid settings without depending on a front end.
type InputError struct{ Code, Message string }

func (e *InputError) Error() string            { return e.Message }
func (e *InputError) MetadataUsageError() bool { return true }
func usageError(message string) error {
	return &InputError{Code: "config-value-invalid", Message: message}
}

var errUnknownAIProvider = errors.New("unknown AI agent provider")

func configPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home, err := homeDir()
	if err != nil {
		return config.Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return config.Homes{HomeDir: home, ConfigHome: lookupEnv("XDG_CONFIG_HOME"), StateHome: lookupEnv("XDG_STATE_HOME")}.Paths()
}
func questionAnsweringFromPaths(paths config.Paths) config.AgentQuestionAnswering {
	v, _ := config.LoadAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile())
	return v
}
func approvalAnsweringFromPaths(paths config.Paths) config.AgentApprovalAnswering {
	v, _ := config.LoadAgentApprovalAnsweringFile(paths.AgentApprovalAnsweringFile())
	return v
}
func aiEnabledAgentsContains(values []config.AIAgentProvider, provider config.AIAgentProvider) bool {
	return slices.Contains(values, provider)
}

// The central settings are the settings every front (TUI, web, CLI) shares:
// `[ui] locale` in config.toml, the agent-question-answering file, and the
// agent-question-window-seconds file. These functions read, validate, and
// store them from the home and env resolvers alone; they never read a front
// (TUI) layer setting. A front adds only its own display on top, such as
// output lines or a tmux display-message.

// LoadCentralLocaleSetting returns the saved `[ui] locale` (auto when unset)
// and the config.toml path it came from.
func LoadCentralLocaleSetting(homeDir func() (string, error), lookupEnv func(string) string) (setting string, source string, err error) {
	path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		return i18n.LocaleSettingAuto, "", err
	}
	return LoadLocale(path)
}

func LoadLocale(path string) (setting string, source string, err error) {
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return i18n.LocaleSettingAuto, path, err
	}
	setting = strings.TrimSpace(cfg.UI.Locale)
	if setting == "" {
		setting = i18n.LocaleSettingAuto
	}
	return setting, path, nil
}

// SaveCentralLocale validates value before touching any file, then stores it
// as `[ui] locale`. It returns the trimmed value it saved. An unsupported value
// is a usage error; path and write failures are not.
func SaveCentralLocale(homeDir func() (string, error), lookupEnv func(string) string, value string) (string, error) {
	if err := validateLocale(value); err != nil {
		return "", err
	}
	path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		return "", err
	}
	return SaveLocale(path, value)
}

func SaveLocale(path, value string) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateLocale(value); err != nil {
		return "", err
	}
	_, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error { cfg.UI.Locale = value; return nil })
	if err != nil {
		return "", err
	}
	return value, nil
}

// LoadCentralAgentQuestionAnswering and LoadCentralAgentQuestionWindowSeconds
// read the same files, through the same loaders, the question hook reads.
func LoadCentralAgentQuestionAnswering(homeDir func() (string, error), lookupEnv func(string) string) config.AgentQuestionAnswering {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.AgentQuestionAnsweringClaude
	}
	return questionAnsweringFromPaths(paths)
}

func LoadCentralAgentQuestionWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string) int {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultAgentQuestionWindowSeconds
	}
	seconds, _ := config.LoadAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile())
	return seconds
}

// SaveQuestionAnswering writes the central answering file and
// returns the normalized way it now holds.
func SaveQuestionAnswering(paths config.Paths, way config.AgentQuestionAnswering) (config.AgentQuestionAnswering, error) {
	if err := config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), way); err != nil {
		return "", err
	}
	return config.NormalizeAgentQuestionAnswering(string(way)), nil
}

// SaveQuestionWindow writes the central window file. An
// out-of-range value is refused and nothing is written.
func SaveQuestionWindow(paths config.Paths, seconds int) error {
	return config.SaveAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile(), seconds)
}

// LoadCentralAgentApprovalAnswering and LoadCentralAgentApprovalWindowSeconds
// read the same files, through the same loaders, the permission hook reads.
func LoadCentralAgentApprovalAnswering(homeDir func() (string, error), lookupEnv func(string) string) config.AgentApprovalAnswering {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.AgentApprovalAnsweringClaude
	}
	return approvalAnsweringFromPaths(paths)
}

func LoadCentralAgentApprovalWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string) int {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultAgentApprovalWindowSeconds
	}
	seconds, _ := config.LoadAgentApprovalWindowSecondsFile(paths.AgentApprovalWindowSecondsFile())
	return seconds
}

// SaveApprovalAnswering writes the central approval answering file.
func SaveApprovalAnswering(paths config.Paths, way config.AgentApprovalAnswering) error {
	return config.SaveAgentApprovalAnsweringFile(paths.AgentApprovalAnsweringFile(), way)
}

// SaveApprovalWindow writes the central approval window
// file. An out-of-range value is refused and nothing is written.
func SaveApprovalWindow(paths config.Paths, seconds int) error {
	return config.SaveAgentApprovalWindowSecondsFile(paths.AgentApprovalWindowSecondsFile(), seconds)
}

func KnownAIProviderList() string {
	known := config.KnownAIAgentProviders()
	names := make([]string, 0, len(known))
	for _, provider := range known {
		names = append(names, string(provider))
	}
	return strings.Join(names, ", ")
}

func KnownAIProvider(value string) (config.AIAgentProvider, error) {
	normalized := config.NormalizeAIEnabledAgents([]string{value})
	if len(normalized) != 1 {
		return "", fmt.Errorf("%w: %s", errUnknownAIProvider, value)
	}
	return normalized[0], nil
}

func ReadAIEnabledAgentsPolicy(homeDir func() (string, error), lookupEnv func(string) string) (string, []config.AIAgentProvider, error) {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return "", nil, err
	}
	path := paths.AIEnabledAgentsFile()
	current, err := config.LoadAIEnabledAgentsFile(path)
	if err != nil {
		return "", nil, err
	}
	return path, current, nil
}

func SetAIEnabledAgent(homeDir func() (string, error), lookupEnv func(string) string, provider config.AIAgentProvider, enabled bool) error {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return err
	}
	return SetProvider(paths, provider, enabled)
}

func KnownAgentQuestionAnswering(value string) (config.AgentQuestionAnswering, error) {
	known := []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux}
	names := make([]string, 0, len(known))
	for _, way := range known {
		names = append(names, string(way))
	}
	if strings.TrimSpace(value) == "" {
		return "", usageError("config agent-questions --answering requires a way: " + strings.Join(names, ", "))
	}
	typed := config.AgentQuestionAnswering(strings.ToLower(strings.TrimSpace(value)))
	for _, way := range known {
		if typed == way {
			return way, nil
		}
	}
	return "", usageError(fmt.Sprintf("config agent-questions --answering: unknown way %q; known ways: %s", value, strings.Join(names, ", ")))
}

func AgentQuestionWindowSpellings() string {
	return fmt.Sprintf("%d..%d seconds or %s", config.MinAgentQuestionWindowSeconds, config.MaxAgentQuestionWindowSeconds, config.AgentQuestionWindowUnlimitedWord)
}

func ParseAgentQuestionWindow(raw string) (int, error) {
	text := strings.TrimSpace(raw)
	if strings.EqualFold(text, config.AgentQuestionWindowUnlimitedWord) {
		return config.UnlimitedAgentQuestionWindowSeconds, nil
	}
	seconds, err := strconv.Atoi(text)
	if err != nil || seconds < config.MinAgentQuestionWindowSeconds || seconds > config.MaxAgentQuestionWindowSeconds {
		return 0, fmt.Errorf("agent question window %q must be %d..%d seconds", raw, config.MinAgentQuestionWindowSeconds, config.MaxAgentQuestionWindowSeconds)
	}
	return seconds, nil
}

func AgentQuestionWindowWord(seconds int) string {
	if seconds == config.UnlimitedAgentQuestionWindowSeconds {
		return config.AgentQuestionWindowUnlimitedWord
	}
	return strconv.Itoa(seconds)
}

func KnownAgentApprovalAnswering(value string) (config.AgentApprovalAnswering, error) {
	known := []config.AgentApprovalAnswering{config.AgentApprovalAnsweringClaude, config.AgentApprovalAnsweringProjmux}
	names := make([]string, 0, len(known))
	for _, way := range known {
		names = append(names, string(way))
	}
	if strings.TrimSpace(value) == "" {
		return "", usageError("config agent-approvals --answering requires a way: " + strings.Join(names, ", "))
	}
	typed := config.AgentApprovalAnswering(strings.ToLower(strings.TrimSpace(value)))
	for _, way := range known {
		if typed == way {
			return way, nil
		}
	}
	return "", usageError(fmt.Sprintf("config agent-approvals --answering: unknown way %q; known ways: %s", value, strings.Join(names, ", ")))
}

// Service executes only the four existing central settings at captured paths.
type Service struct {
	Paths func() (config.Paths, error)
	// Optional policy writers let fronts retain their central settings adapters.
	// Nil writers use the same file services directly at the captured paths.
	QuestionAnswering func(config.AgentQuestionAnswering) (config.AgentQuestionAnswering, error)
	QuestionWindow    func(int) error
	ApprovalAnswering func(config.AgentApprovalAnswering) error
	ApprovalWindow    func(int) error
}
type ProviderState struct {
	ID      string
	Enabled bool
}
type Value struct {
	Key       string
	Providers []ProviderState
	Locale    string
	Source    string
	Answering string
	Window    string
}
type Update struct {
	Key       string
	Provider  string
	Enabled   bool
	Locale    string
	Answering *string
	Window    *string
}

func (s Service) Get(ctx context.Context, key string) (Value, error) {
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	p, err := s.Paths()
	if err != nil {
		switch key {
		case "providers":
			out := Value{Key: key, Providers: make([]ProviderState, 0)}
			for _, p := range config.KnownAIAgentProviders() {
				out.Providers = append(out.Providers, ProviderState{ID: string(p), Enabled: aiEnabledAgentsContains(config.DefaultAIEnabledAgents, p)})
			}
			return out, nil
		case "agent-questions":
			return Value{Key: key, Answering: string(config.AgentQuestionAnsweringClaude), Window: AgentQuestionWindowWord(config.DefaultAgentQuestionWindowSeconds)}, nil
		case "agent-approvals":
			return Value{Key: key, Answering: string(config.AgentApprovalAnsweringClaude), Window: strconv.Itoa(config.DefaultAgentApprovalWindowSeconds)}, nil
		}
		return Value{}, err
	}
	return readValue(p, key)
}
func readValue(p config.Paths, key string) (Value, error) {
	out := Value{Key: key}
	switch key {
	case "providers":
		agents, err := config.LoadAIEnabledAgentsFile(p.AIEnabledAgentsFile())
		if err != nil {
			agents = append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
		}
		out.Providers = make([]ProviderState, 0)
		for _, a := range config.KnownAIAgentProviders() {
			out.Providers = append(out.Providers, ProviderState{ID: string(a), Enabled: aiEnabledAgentsContains(agents, a)})
		}
	case "locale":
		v, path, err := LoadLocale(filepath.Join(p.ConfigDir, "config.toml"))
		if err != nil {
			return out, err
		}
		out.Locale = v
		out.Source = path
	case "agent-questions":
		v, _ := config.LoadAgentQuestionAnsweringFile(p.AgentQuestionAnsweringFile())
		w, _ := config.LoadAgentQuestionWindowSecondsFile(p.AgentQuestionWindowSecondsFile())
		out.Answering = string(v)
		out.Window = AgentQuestionWindowWord(w)
	case "agent-approvals":
		v, _ := config.LoadAgentApprovalAnsweringFile(p.AgentApprovalAnsweringFile())
		w, _ := config.LoadAgentApprovalWindowSecondsFile(p.AgentApprovalWindowSecondsFile())
		out.Answering = string(v)
		out.Window = strconv.Itoa(w)
	default:
		return out, &InputError{Code: "config-key-invalid", Message: "unknown config key: " + key}
	}
	return out, nil
}
func (s Service) Set(ctx context.Context, u Update) (Value, error) {
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	// Validate every supplied policy value before the first write.
	var question config.AgentQuestionAnswering
	var approval config.AgentApprovalAnswering
	var seconds int
	var err error
	switch u.Key {
	case "providers":
		flagName := "--disable"
		if u.Enabled {
			flagName = "--enable"
		}
		if strings.TrimSpace(u.Provider) == "" {
			return Value{}, usageError(fmt.Sprintf("config providers %s requires a provider id: %s", flagName, KnownAIProviderList()))
		}
		provider, e := KnownAIProvider(u.Provider)
		if e != nil {
			return Value{}, usageError(fmt.Sprintf("config providers %s: unknown provider %q; known providers: %s", flagName, u.Provider, KnownAIProviderList()))
		}
		p, e := s.Paths()
		if e != nil {
			return Value{}, e
		}
		if e = SetProvider(p, provider, u.Enabled); e != nil {
			return Value{}, e
		}
		agents, e := config.LoadAIEnabledAgentsFile(p.AIEnabledAgentsFile())
		if e != nil {
			return Value{}, e
		}
		out := Value{Key: u.Key, Providers: make([]ProviderState, 0)}
		for _, a := range config.KnownAIAgentProviders() {
			out.Providers = append(out.Providers, ProviderState{ID: string(a), Enabled: aiEnabledAgentsContains(agents, a)})
		}
		return out, nil
	case "locale":
		if strings.TrimSpace(u.Locale) == "" {
			return Value{}, usageError("config locale --set requires a locale setting")
		}
	case "agent-questions":
		if u.Answering != nil {
			question, err = KnownAgentQuestionAnswering(*u.Answering)
			if err != nil {
				return Value{}, err
			}
		}
		if u.Window != nil {
			if strings.TrimSpace(*u.Window) == "" {
				return Value{}, usageError("config agent-questions --window requires a value: " + AgentQuestionWindowSpellings())
			}
			seconds, err = ParseAgentQuestionWindow(*u.Window)
			if err != nil {
				return Value{}, usageError(fmt.Sprintf("config agent-questions --window: invalid window %q; want %s", *u.Window, AgentQuestionWindowSpellings()))
			}
		}
	case "agent-approvals":
		if u.Answering != nil {
			approval, err = KnownAgentApprovalAnswering(*u.Answering)
			if err != nil {
				return Value{}, err
			}
		}
		if u.Window != nil {
			seconds, err = strconv.Atoi(strings.TrimSpace(*u.Window))
			if err != nil || seconds < config.MinAgentApprovalWindowSeconds || seconds > config.MaxAgentApprovalWindowSeconds {
				return Value{}, usageError(fmt.Sprintf("config agent-approvals --window: invalid window %q; want %d..%d seconds", *u.Window, config.MinAgentApprovalWindowSeconds, config.MaxAgentApprovalWindowSeconds))
			}
		}
	default:
		return Value{}, &InputError{Code: "config-key-invalid", Message: "unknown config key: " + u.Key}
	}
	p, err := s.Paths()
	if err != nil {
		return Value{}, err
	}
	switch u.Key {
	case "locale":
		_, err = SaveLocale(filepath.Join(p.ConfigDir, "config.toml"), u.Locale)
	case "agent-questions":
		if u.Answering != nil {
			if s.QuestionAnswering != nil {
				_, err = s.QuestionAnswering(question)
			} else {
				_, err = SaveQuestionAnswering(p, question)
			}
		}
		if err == nil && u.Window != nil {
			if s.QuestionWindow != nil {
				err = s.QuestionWindow(seconds)
			} else {
				err = SaveQuestionWindow(p, seconds)
			}
		}
	case "agent-approvals":
		if u.Answering != nil {
			if s.ApprovalAnswering != nil {
				err = s.ApprovalAnswering(approval)
			} else {
				err = SaveApprovalAnswering(p, approval)
			}
		}
		if err == nil && u.Window != nil {
			if s.ApprovalWindow != nil {
				err = s.ApprovalWindow(seconds)
			} else {
				err = SaveApprovalWindow(p, seconds)
			}
		}
	}
	if err != nil {
		return Value{}, err
	}
	return readValue(p, u.Key)
}
func SetProvider(p config.Paths, provider config.AIAgentProvider, enabled bool) error {
	current, err := config.LoadAIEnabledAgentsFile(p.AIEnabledAgentsFile())
	if err != nil {
		return err
	}
	if aiEnabledAgentsContains(current, provider) == enabled {
		return nil
	}
	members := map[config.AIAgentProvider]bool{}
	for _, a := range current {
		members[a] = true
	}
	members[provider] = enabled
	next := make([]config.AIAgentProvider, 0)
	for _, a := range config.KnownAIAgentProviders() {
		if members[a] {
			next = append(next, a)
		}
	}
	return config.SaveAIEnabledAgentsFile(p.AIEnabledAgentsFile(), next)
}

func validateLocale(value string) error {
	value = strings.TrimSpace(value)
	switch value {
	case i18n.LocaleSettingAuto, string(i18n.FallbackLocale), "ko-KR":
		return nil
	default:
		return usageError("unsupported locale setting: " + value)
	}
}
