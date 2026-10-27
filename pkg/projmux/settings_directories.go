package projmux

import (
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// SettingsDirectories returns the profiles and personas directories beneath
// configDir, which is the already resolved projmux configuration directory.
// It applies filepath.Join's lexical cleaning, including for empty or relative
// inputs, without making paths absolute or expanding environment variables.
// It does not access the environment, filesystem, tmux, or Registry, and does
// not guarantee that the directories exist or are accessible.
func SettingsDirectories(configDir string) (profilesDir, personasDir string) {
	return filepath.Join(configDir, config.ProfilesDirName), filepath.Join(configDir, config.PersonasDirName)
}

// SettingsInputPaths returns the guidance and Project prompt input paths
// beneath configDir, the already resolved projmux configuration directory,
// with the same lexical cleaning as SettingsDirectories. An empty projectUID
// returns the five global paths and leaves the two Project paths empty;
// otherwise projectUID must be a bare Project UID, and one that is not is a
// CodeSelectorInvalid refusal with the zero result. Like SettingsDirectories
// it accesses no environment, filesystem, tmux or Registry and does not
// guarantee that any path exists or is readable.
func SettingsInputPaths(configDir, projectUID string) (core.SettingsInputPaths, error) {
	if projectUID != "" && !metadata.IsProjectUIDShaped(projectUID) {
		return core.SettingsInputPaths{}, &Refusal{Code: CodeSelectorInvalid, message: "SettingsInputPaths projectUID must be empty or a bare Project UID"}
	}
	guidance := filepath.Join(configDir, agentguidance.FileName)
	out := core.SettingsInputPaths{
		GuidanceFile:                 guidance,
		GuidancePolicyFile:           filepath.Join(configDir, agentguidance.SettingsFileName),
		GuidanceFragmentsDir:         filepath.Join(configDir, agentguidance.FragmentsDirName),
		GuidanceMigrationReceiptFile: filepath.Join(configDir, agentguidance.MigrationFileName),
		GuidanceMigrationBackupFile:  guidance + ".bak",
	}
	if projectUID != "" {
		out.ProjectGuidanceFile = filepath.Join(configDir, agentguidance.ProjectDirName, projectUID+".md")
		out.ProjectLinkRulesFile = filepath.Join(configDir, projectlinks.DirName, projectUID+projectlinks.FileExt)
	}
	return out, nil
}
