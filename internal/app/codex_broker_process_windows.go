//go:build windows

package app

import (
	"errors"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

func startCodexBrokerRuntimeProcess(string, codexbroker.Discovery) error {
	return errors.New("codex broker runtime requires Unix filesystem semantics")
}
