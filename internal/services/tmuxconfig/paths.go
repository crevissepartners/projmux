package tmuxconfig
import "github.com/crevissepartners/projmux/internal/config"
// PathsResolver supplies exact captured Paths or the existing CLI resolver.
// Loaders call it in the historical order, once for each original lookup.
type PathsResolver func(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error)
