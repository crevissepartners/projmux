//go:build darwin

package app

import (
	"fmt"
	"os"
	"syscall"
)

func restartKeyBrokerProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	args := append([]string{executable}, os.Args[1:]...)
	return syscall.Exec(executable, args, os.Environ())
}
