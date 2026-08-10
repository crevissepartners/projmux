//go:build !darwin

package app

import "errors"

func restartKeyBrokerProcess() error {
	return errors.New("native key broker restart is only available on macOS")
}
