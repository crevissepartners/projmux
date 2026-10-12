package app

import (
	"os"
	"path/filepath"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

	"github.com/crevissepartners/projmux/internal/config"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

var configPaths = mutationruntime.ConfigPaths
var generatedAppConfigDefaultPath = mutationruntime.GeneratedAppConfigDefaultPath

// configFileDisplayPath is where Settings says a central config file lives:
// the file under the resolved config home (XDG_CONFIG_HOME, else ~/.config),
// shortened to ~/... when it sits under $HOME and absolute otherwise. Without a
// config home to resolve it keeps the default ~/.config/projmux spelling.
func configFileDisplayPath(homeDir func() (string, error), lookupEnv func(string) string, name string) string {
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home, err := homeDir()
	if err != nil {
		home = ""
	}
	configHome, err := config.ResolveConfigHome(home, lookupEnv("XDG_CONFIG_HOME"))
	if err != nil {
		return "~/.config/" + config.AppName + "/" + name
	}
	return intrender.PrettyPath(filepath.Join(configHome, config.AppName, name), home, "")
}
