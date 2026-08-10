package app

import (
	"os"
	"runtime"
	"strings"

	intpsmux "github.com/crevissepartners/projmux/internal/integrations/psmux"
)

const muxBackendEnvVar = "PROJMUX_MUX_BACKEND"

type muxBackend string

const (
	muxBackendTmux  muxBackend = "tmux"
	muxBackendPSMux muxBackend = "psmux"
)

func selectedMuxBackend(lookupEnv func(string) string, goos func() string) muxBackend {
	if lookupEnv != nil {
		switch strings.ToLower(strings.TrimSpace(lookupEnv(muxBackendEnvVar))) {
		case string(muxBackendTmux):
			return muxBackendTmux
		case string(muxBackendPSMux):
			return muxBackendPSMux
		}
	}

	host := runtime.GOOS
	if goos != nil {
		host = goos()
	}
	if strings.EqualFold(strings.TrimSpace(host), "windows") {
		return muxBackendPSMux
	}
	return muxBackendTmux
}

func usePSMuxBackend(lookupEnv func(string) string, goos func() string) bool {
	return selectedMuxBackend(lookupEnv, goos) == muxBackendPSMux
}

func newDefaultPSMuxClient() *intpsmux.Client {
	return intpsmux.NewClient(intpsmux.ExecRunner{}, intpsmux.WithSocketName(defaultAppSocket), intpsmux.WithEnv(os.Getenv))
}
