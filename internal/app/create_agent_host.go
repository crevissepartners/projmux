package app

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
)

// resolveNewAgentHost is called only by public CLI new-Agent adapters. Internal
// picker topology and SDK creation retain their explicitly tmux contracts.
func (c *createCommand) resolveNewAgentHost(spelling string, flags *resourceCreateFlags) error {
	if flags.hostSet {
		if flags.host != "tmux" && flags.host != "process" {
			return usageError(fmt.Sprintf("%s --host %q requires tmux or process", spelling, flags.host))
		}
	} else {
		flags.host = string(config.AgentHostTmux)
		paths, err := savedSettingsPaths(c.homeDir, c.lookupEnv)
		if err != nil && !isMissingHome(err) {
			return err
		}
		if err == nil {
			host, err := config.LoadAgentDefaultHostFile(paths.AgentDefaultHostFile())
			if err != nil {
				return err
			}
			flags.host = string(host)
		}
		if flags.host == "process" && !flags.detachSet {
			flags.detach = true
		}
	}
	if flags.detach && flags.host != "process" {
		return usageError(spelling + ": --detach requires --host process; nothing was created")
	}
	return nil
}
