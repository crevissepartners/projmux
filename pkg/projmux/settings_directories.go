package projmux

import (
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
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
