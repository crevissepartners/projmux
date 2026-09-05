package app

import (
	"io"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// storedUpdateReleaseChannel reads the persisted release-channel opt-in.
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
func storedUpdateReleaseChannel(lookupEnv func(string) string, homeDir func() (string, error)) (string, bool) {
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

// updateReleaseChannelSource builds the resolver newUpdateCommand binds to the
// releaseChannelSource seam: the stored setting when there is one, and the
// environment until there is. Both readings happen per call, so a toggle takes
// effect on the next judgment without the process being restarted.
func updateReleaseChannelSource(lookupEnv func(string) string, homeDir func() (string, error)) func() string {
	return func() string {
		if channel, ok := storedUpdateReleaseChannel(lookupEnv, homeDir); ok {
			return channel
		}
		if lookupEnv == nil {
			return ""
		}
		return lookupEnv(updateReleaseChannelEnv)
	}
}

// currentReleaseChannelSetting reports the channel this install is judged on
// and whether that answer came from a stored setting. A stored value that this
// binary does not recognise normalizes to the default, matching how the update
// command itself reads the axis.
func (c *settingsCommand) currentReleaseChannelSetting() (channel string, stored bool, err error) {
	path, err := c.globalConfigPath()
	if err != nil {
		return updateReleaseChannelStable, false, err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return updateReleaseChannelStable, false, err
	}
	if cfg.Update.ReleaseChannel != "" {
		return normalizeUpdateReleaseChannel(cfg.Update.ReleaseChannel), true, nil
	}
	raw := ""
	if c.lookupEnv != nil {
		raw = c.lookupEnv(updateReleaseChannelEnv)
	}
	return normalizeUpdateReleaseChannel(raw), false, nil
}

// setReleaseChannelSetting persists an explicit channel choice. It is called
// only from the toggle, so the key appears the first time the user actually
// changes the value and never merely from opening the row.
func (c *settingsCommand) setReleaseChannelSetting(channel string) error {
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	_, err = hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		cfg.Update.ReleaseChannel = normalizeUpdateReleaseChannel(channel)
		return nil
	})
	return err
}

// toggleReleaseChannelSetting flips the opt-in between stable and rc.
func (c *settingsCommand) toggleReleaseChannelSetting(stdout, stderr io.Writer) error {
	channel, _, err := c.currentReleaseChannelSetting()
	if err != nil {
		c.setSettingsFeedback("Release channel failed", err.Error())
		return nil
	}
	next := updateReleaseChannelRC
	if channel == updateReleaseChannelRC {
		next = updateReleaseChannelStable
	}
	return c.runSettingsMutation("Release channel", stdout, stderr, func(io.Writer, io.Writer) error {
		return c.setReleaseChannelSetting(next)
	})
}
