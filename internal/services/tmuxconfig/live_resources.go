package tmuxconfig
import ("github.com/crevissepartners/projmux/internal/config"
"github.com/crevissepartners/projmux/internal/systemstatus")
func LoadLiveResourcesMode(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) config.LiveResourcesMode {
	if !systemstatus.Supported() {
		return config.LiveResourcesOff
	}
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return config.LiveResourcesOff
	}
	mode, err := config.LoadLiveResourcesFile(paths.LiveResourcesFile())
	if err != nil {
		return config.LiveResourcesOff
	}
	return mode
}
