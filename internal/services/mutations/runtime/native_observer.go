package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// NativeObserverPaths is the process routing contract shared by creators and
// their native observation child. Nil preserves the CLI's omitted-path lane;
// a supplied tuple is complete and never resolved from ambient defaults.
// Endpoint state-domain identity is separate from Paths.StateDir.
type NativeObserverPaths struct {
	Paths        config.Paths
	RegistryPath string
	Executable   string
}

func (p NativeObserverPaths) Validate() error {
	for _, path := range []string{p.Paths.ConfigDir, p.Paths.StateDir, p.RegistryPath, p.Executable} {
		if strings.TrimSpace(path) != path || !filepath.IsAbs(path) {
			return errors.New("native observation requires absolute Config, State, Registry, and executable paths")
		}
	}
	return nil
}

// NativeObserverStartup is an internal service receipt; no public SDK operation
// is enabled by extracting this seam.
type NativeObserverStartup struct {
	Status    string
	Epoch     string
	Reason    string
	Committed bool
}

// NativeObserverStarter retains the exact-binding authority transition and
// startup convergence shared by CLI and future SDK adapters. Transport and
// projection writers remain adapters, including their fence and compensation.
type NativeObserverStarter struct {
	BindingCurrent   func() bool
	SetAuthority     func(source, epoch, reason string) error
	SetAuthorityFrom func(from, source, epoch, reason string) (bool, error)
	Launch           func() NativeObserverStartup
}

func (s NativeObserverStarter) Start(ctx context.Context) NativeObserverStartup {
	stale := NativeObserverStartup{Status: "stale"}
	if ctx.Err() != nil || s.BindingCurrent == nil || s.SetAuthority == nil || s.Launch == nil || !s.BindingCurrent() {
		return stale
	}
	if s.SetAuthority("pending", "", "connecting") != nil {
		return stale
	}
	return s.Converge(s.Launch())
}

// Converge never overwrites a child-committed receipt or a replaced binding.
func (s NativeObserverStarter) Converge(result NativeObserverStartup) NativeObserverStartup {
	stale := NativeObserverStartup{Status: "stale"}
	if result.Committed {
		return result
	}
	if result.Status == "fallback" && result.Reason != "" {
		if !s.BindingCurrent() {
			return stale
		}
		if s.SetAuthority(CodexAuthorityHook, "", result.Reason) != nil && !s.BindingCurrent() {
			return stale
		}
	}
	if result.Status == "retrying" && s.SetAuthorityFrom != nil {
		if _, err := s.SetAuthorityFrom("pending", CodexAuthorityHook, "", result.Reason); err != nil && !s.BindingCurrent() {
			return stale
		}
	}
	return result
}
