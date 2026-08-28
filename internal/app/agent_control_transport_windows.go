//go:build windows

package app

import (
	"context"
	"errors"
)

type codexControlServer struct{ epoch *codexControlEpoch }

func startCodexControlServer(string, *codexControlEpoch) (*codexControlServer, error) {
	return nil, errors.New("exact Agent control transport is unavailable on Windows")
}
func (s *codexControlServer) Close() error {
	if s != nil && s.epoch != nil {
		s.epoch.Revoke()
	}
	return nil
}
func callCodexControl(context.Context, string, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
	return agentControlResponse{}, errors.New("exact Agent control transport is unavailable on Windows")
}
