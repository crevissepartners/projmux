package app

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func markActivationFailureCloseOnExec(fd int) {
	syscall.CloseOnExec(fd)
}

func execCommittedActivation(argv []string, argv0 string, spec superviseSpec) error {
	if len(argv) == 0 {
		return errors.New("no provider command to exec")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	execArgv := append([]string(nil), argv...)
	if argv0 != "" {
		execArgv[0] = argv0
	}
	// #nosec G204 -- this dynamic executable is the existing managed Pane/provider
	// contract input, resolved by exec.LookPath above and passed with its exact
	// argv directly to Exec without a shell.
	return syscall.Exec(path, execArgv, committedActivationEnvironment(os.Environ(), spec))
}

func committedActivationEnvironment(inherited []string, spec superviseSpec) []string {
	if !spec.ClaudeRegistration {
		return append(inherited, activationEnvironment(spec)...)
	}
	// Exec accepts duplicate environment keys, while provider and Go readers
	// disagree about which wins. A managed parent may already carry its own
	// private activation envelope; it cannot remain authority for this child.
	keys := map[string]bool{}
	for _, value := range activationEnvironment(spec) {
		key, _, _ := strings.Cut(value, "=")
		keys[key] = true
	}
	var env []string
	for _, value := range inherited {
		key, _, _ := strings.Cut(value, "=")
		if !keys[key] {
			env = append(env, value)
		}
	}
	return append(env, activationEnvironment(spec)...)
}
