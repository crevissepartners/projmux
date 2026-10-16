package agentmessages

import (
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// ReleaseOptions freezes the internal held-message helper's entire routing
// context. It carries no provider credentials or active Pane environment.
type ReleaseOptions struct{ Executable, ConfigDir, StateDir, HomeDir, SocketPath, Path string }

func LaunchRelease(o ReleaseOptions, agent string) error {
	if !message.ValidRef(agent) {
		return errors.New("invalid held-message release Agent")
	}
	for _, path := range []string{o.Executable, o.ConfigDir, o.StateDir, o.HomeDir} {
		if !filepath.IsAbs(path) {
			return errors.New("held-message release requires absolute executable and paths")
		}
	}
	if o.SocketPath != "" && !filepath.IsAbs(o.SocketPath) {
		return errors.New("held-message release socket must be absolute")
	}
	// #nosec G204 -- the caller explicitly supplies this build's absolute executable;
	// the internal route is fixed and the exact Agent reference was validated.
	cmd := exec.Command(o.Executable, "internal", "agent-message-release", "--agent", "uid:"+agent, "--config-dir", o.ConfigDir, "--state-dir", o.StateDir, "--socket-path", o.SocketPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = []string{"HOME=" + o.HomeDir, "PATH=" + o.Path, "XDG_CONFIG_HOME=" + filepath.Dir(o.ConfigDir), "XDG_STATE_HOME=" + filepath.Dir(o.StateDir)}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
