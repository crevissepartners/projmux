package app

import (
	"github.com/crevissepartners/projmux/internal/app/updatecmd"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// currentReleaseChannelSetting reports the channel this install is judged on
// and whether that answer came from a stored setting. A stored value that this
// binary does not recognise normalizes to the default, matching how the update
// command itself reads the axis.
func (c *settingsCommand) currentReleaseChannelSetting() (channel string, stored bool, err error) {
	path, err := c.globalConfigPath()
	if err != nil {
		return updatecmd.ReleaseChannelStable, false, err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return updatecmd.ReleaseChannelStable, false, err
	}
	if cfg.Update.ReleaseChannel != "" {
		return updatecmd.NormalizeReleaseChannel(cfg.Update.ReleaseChannel), true, nil
	}
	raw := ""
	if c.lookupEnv != nil {
		raw = c.lookupEnv(updatecmd.ReleaseChannelEnv)
	}
	return updatecmd.NormalizeReleaseChannel(raw), false, nil
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
		cfg.Update.ReleaseChannel = updatecmd.NormalizeReleaseChannel(channel)
		return nil
	})
	return err
}

// toggleReleaseChannelSetting flips the opt-in between stable and rc. It
// reports failures by returning them rather than by setting feedback itself:
// the caller already runs it inside the shared Settings mutation boundary, and
// a second boundary nested inside that one only has its message overwritten.
func (c *settingsCommand) toggleReleaseChannelSetting() error {
	channel, _, err := c.currentReleaseChannelSetting()
	if err != nil {
		return err
	}
	next := updatecmd.ReleaseChannelRC
	if channel == updatecmd.ReleaseChannelRC {
		next = updatecmd.ReleaseChannelStable
	}
	return c.setReleaseChannelSetting(next)
}
