package app

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// startCodexBrokerRuntimeProcess launches one detached broker runtime.
//
// The child is put in its own session and inherits no tmux routing, because it
// outlives the call that started it and must never be attributed to the pane
// that happened to trigger the launch.
func startCodexBrokerRuntimeProcess(executable string, discovery codexbroker.Discovery) error {
	return startCodexBrokerRuntimeProcessForRoute(executable, discovery, codexBrokerEndpointRoute{})
}

func startCodexBrokerRuntimeProcessForRoute(executable string, discovery codexbroker.Discovery, route codexBrokerEndpointRoute) error {
	key, err := route.endpointKey()
	if err != nil {
		return err
	}
	if discovery.Endpoint() != key {
		return syscall.EINVAL
	}
	args := []string{"internal", "codex-broker", "serve", "--state-domain", discovery.Domain()}
	if route != (codexBrokerEndpointRoute{}) {
		args = append(args,
			"--endpoint-state-domain", route.StateDomainID,
			"--endpoint-generation", route.EndpointGenerationID,
		)
		if route.Default {
			args = append(args, "--endpoint-default")
		} else {
			args = append(args, "--endpoint-socket", route.SocketPath)
		}
	}
	// #nosec G204 -- executable is this process's own resolved path and the argv is a fixed internal route plus one validated absolute state domain; it never enters a shell.
	cmd := exec.Command(executable, args...)
	cmd.Env = withoutInheritedTmuxEnvironment(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// The runtime is intentionally not waited on: it is a long-lived singleton
	// whose lifetime is owned by its own idle and drain policy, not by whichever
	// short-lived call happened to start it.
	go func() { _ = cmd.Wait() }()
	return nil
}
