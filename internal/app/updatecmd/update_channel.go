package updatecmd

import (
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// StoredReleaseChannel reads the persisted release-channel opt-in.
//
// The second result is the whole point of the function: it reports whether the
// setting exists at all, not what it says. Only a setting that exists outranks
// PROJMUX_RELEASE_CHANNEL, so an install that has never touched the toggle
// keeps the environment as its fallback and an install that has touched it is
// answered by what the user chose — including when the user chose stable.
//
// Every failure to read is reported as "not stored" rather than as stable. The
// difference matters: a stable answer would silently kill an environment
// opt-in on a transient read error, while "not stored" leaves the environment
// exactly where it was.
func StoredReleaseChannel(lookupEnv func(string) string, homeDir func() (string, error)) (string, bool) {
	path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		return "", false
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return "", false
	}
	if cfg.Update.ReleaseChannel == "" {
		return "", false
	}
	return cfg.Update.ReleaseChannel, true
}

// NewReleaseChannelSource builds the resolver New binds to the
// releaseChannelSource seam: the stored setting when there is one, and the
// environment until there is. Both readings happen per call, so a toggle takes
// effect on the next judgment without the process being restarted.
func NewReleaseChannelSource(lookupEnv func(string) string, homeDir func() (string, error)) func() string {
	return func() string {
		if channel, ok := StoredReleaseChannel(lookupEnv, homeDir); ok {
			return channel
		}
		if lookupEnv == nil {
			return ""
		}
		return lookupEnv(ReleaseChannelEnv)
	}
}
