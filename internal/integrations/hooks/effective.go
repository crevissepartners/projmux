package hooks

import (
	"os"
	"sort"
	"strings"
)

// EffectiveSource names the axis a merged config entry came from. The four
// labels intentionally mirror the settings popup design language so the UI
// and any future programmatic consumer (hook CLI, doctor) read the same.
type EffectiveSource string

const (
	// EffectiveSourceProject means the resolved value was defined in the
	// project-local config (.projmux/config.toml). A project value also wins
	// when the same key was defined globally — Phase B merge policy.
	EffectiveSourceProject EffectiveSource = "project"
	// EffectiveSourceGlobal means the resolved value was defined in the
	// global config (~/.config/projmux/config.toml) and was not overridden
	// by a project value.
	EffectiveSourceGlobal EffectiveSource = "global"
	// EffectiveSourceMerged is a section-level label used when the section
	// contains keys from both axes (e.g. some env vars come from global,
	// others from project). Individual rows still carry one of the three
	// single-axis labels.
	EffectiveSourceMerged EffectiveSource = "merged"
	// EffectiveSourceDefault means the value is unset on both axes and
	// falls back to projmux's built-in default (typically empty).
	EffectiveSourceDefault EffectiveSource = "default"
)

// EffectiveEntry is a single key-value pair in the merged effective view,
// annotated with the source axis the value was resolved from.
type EffectiveEntry struct {
	Key    string
	Value  string
	Source EffectiveSource
}

// EffectiveSection is the merge result for one config section, with a
// section-level source label that summarizes which axes contributed.
type EffectiveSection struct {
	Name    string
	Source  EffectiveSource
	Entries []EffectiveEntry
}

// EffectiveConfig is the full merged view of the declarative config sections
// ([env] / [startup] / [hooks.*]).
type EffectiveConfig struct {
	Env     EffectiveSection
	Startup EffectiveSection
	Hooks   EffectiveSection
}

// Sections returns the merged sections in stable display order.
func (c EffectiveConfig) Sections() []EffectiveSection {
	return []EffectiveSection{c.Env, c.Startup, c.Hooks}
}

// MergeEffective merges global + project config into a single effective view
// with per-entry source labels. The merge policy is "project wins" — when
// the same key is defined on both axes, the project value is kept and the
// resulting entry is labelled EffectiveSourceProject. The function is pure;
// it does not read the filesystem.
func MergeEffective(global, project ProjectConfig) EffectiveConfig {
	return EffectiveConfig{
		Env:     mergeEffectiveEnv(global, project),
		Startup: mergeEffectiveStartup(global, project),
		Hooks:   mergeEffectiveHooks(global, project),
	}
}

func mergeEffectiveEnv(global, project ProjectConfig) EffectiveSection {
	section := EffectiveSection{Name: "env"}
	keys := map[string]struct{}{}
	for k := range global.Env {
		keys[k] = struct{}{}
	}
	for k := range project.Env {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, key := range sorted {
		projectValue, hasProject := project.Env[key]
		globalValue, hasGlobal := global.Env[key]
		switch {
		case hasProject:
			// Project wins over global on conflict.
			section.Entries = append(section.Entries, EffectiveEntry{
				Key:    key,
				Value:  projectValue,
				Source: EffectiveSourceProject,
			})
		case hasGlobal:
			section.Entries = append(section.Entries, EffectiveEntry{
				Key:    key,
				Value:  globalValue,
				Source: EffectiveSourceGlobal,
			})
		}
	}
	section.Source = summarizeSectionSource(section.Entries)
	return section
}

func mergeEffectiveStartup(global, project ProjectConfig) EffectiveSection {
	section := EffectiveSection{Name: "startup"}
	section.Entries = append(section.Entries,
		resolveScalarEntry("run", project.StartupRun, global.StartupRun),
	)
	section.Source = summarizeSectionSource(section.Entries)
	return section
}

// mergeEffectiveHooks resolves each supported lifecycle event with the same
// project-wins policy as the data sections. Unlike [env] / [startup],
// each event resolves to a single `run` value sourced from exactly one axis —
// so only EffectiveSourceProject and EffectiveSourceGlobal labels appear at
// the row level. Events that are unset on both axes are omitted entirely to
// keep the popup focused on active hooks (the popup is already information
// dense). The section-level label still uses summarizeSectionSource so the
// header reads (project), (global), or (merged) when both axes contribute
// hook entries for different events.
func mergeEffectiveHooks(global, project ProjectConfig) EffectiveSection {
	section := EffectiveSection{Name: "hooks"}
	for _, event := range SupportedEvents {
		projectRun := strings.TrimSpace(project.hookRun(event))
		globalRun := strings.TrimSpace(global.hookRun(event))
		switch {
		case projectRun != "":
			// Project wins on conflict — the row is unambiguously sourced from
			// the project config even if the global config also defines it.
			section.Entries = append(section.Entries, EffectiveEntry{
				Key:    string(event),
				Value:  projectRun,
				Source: EffectiveSourceProject,
			})
		case globalRun != "":
			section.Entries = append(section.Entries, EffectiveEntry{
				Key:    string(event),
				Value:  globalRun,
				Source: EffectiveSourceGlobal,
			})
		}
		// Both axes empty → row omitted (declarative absence; the lifecycle
		// is simply not wired up).
	}
	section.Source = summarizeSectionSource(section.Entries)
	return section
}

// resolveScalarEntry resolves a single-valued field with project-wins policy.
// An empty string is treated as "unset" on either axis — this matches how
// the project config parser normalizes trimmed-empty values.
func resolveScalarEntry(key, projectValue, globalValue string) EffectiveEntry {
	projectValue = strings.TrimSpace(projectValue)
	globalValue = strings.TrimSpace(globalValue)
	switch {
	case projectValue != "":
		return EffectiveEntry{Key: key, Value: projectValue, Source: EffectiveSourceProject}
	case globalValue != "":
		return EffectiveEntry{Key: key, Value: globalValue, Source: EffectiveSourceGlobal}
	default:
		return EffectiveEntry{Key: key, Value: "", Source: EffectiveSourceDefault}
	}
}

// summarizeSectionSource returns the section-level label by inspecting the
// per-entry sources. A section is "merged" when at least one project entry
// AND at least one global entry are present. Otherwise the dominant axis
// becomes the section label; if no entries resolved at all (every scalar
// fell back to default), the section is labelled "default".
func summarizeSectionSource(entries []EffectiveEntry) EffectiveSource {
	hasProject, hasGlobal, hasResolved := false, false, false
	for _, e := range entries {
		switch e.Source {
		case EffectiveSourceProject:
			hasProject = true
			hasResolved = true
		case EffectiveSourceGlobal:
			hasGlobal = true
			hasResolved = true
		}
	}
	switch {
	case hasProject && hasGlobal:
		return EffectiveSourceMerged
	case hasProject:
		return EffectiveSourceProject
	case hasGlobal:
		return EffectiveSourceGlobal
	case !hasResolved:
		return EffectiveSourceDefault
	default:
		return EffectiveSourceDefault
	}
}

// LoadProjectConfigFile reads + parses a config.toml file. A missing file
// resolves to an empty ProjectConfig, matching the policy used everywhere
// else in the settings UI — "no file" is not an error.
func LoadProjectConfigFile(path string) (ProjectConfig, error) {
	if strings.TrimSpace(path) == "" {
		return ProjectConfig{}, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectConfig{}, nil
		}
		return ProjectConfig{}, err
	}
	return ParseProjectConfig(string(content))
}

// sensitiveEnvKeyPattern lists the case-insensitive substrings that flag an
// env key as carrying credentials. Matching keys have their value replaced
// with the redaction sentinel in display contexts. The list is intentionally
// conservative — projmux is a tmux launcher, so over-redacting is preferable
// to leaking a token into a settings popup.
var sensitiveEnvKeyPattern = []string{
	"TOKEN",
	"SECRET",
	"KEY",
	"PASSWORD",
	"PASSWD",
	"CREDENTIAL",
}

// SensitiveRedaction is the display sentinel substituted for sensitive
// env values. Exported so the settings UI and any future formatter share
// one constant.
const SensitiveRedaction = "<redacted>"

// IsSensitiveEnvKey reports whether key looks like it carries a credential
// and should be redacted in display. Match is case-insensitive on substrings
// — KEY matches API_KEY and OPENAI_API_KEY but also AES_KEY_FILE, which is
// the conservative trade-off.
func IsSensitiveEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return false
	}
	for _, needle := range sensitiveEnvKeyPattern {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}
