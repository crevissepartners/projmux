package settings

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
)

// StatusbarResources shares the legacy supported/save/prepare/live ordering.
// Callers retain their own input validation and route selection policies.
type StatusbarResources struct {
	Supported func() bool
	Paths     func() (config.Paths, error)
	Apply     func() (ApplyStage, ApplyStage, error)
}

// StatusbarResourcesResult carries an explicit failing branch for front ends.
// Failure is empty, unsupported, save, prepare, or apply. It is not a public DTO.
type StatusbarResourcesResult struct {
	Mode     string
	Saved    bool
	Prepared ApplyStage
	Live     ApplyStage
	Failure  string
}

func (s StatusbarResources) Set(value string) (StatusbarResourcesResult, error) {
	out := StatusbarResourcesResult{Prepared: ApplyStage{Status: ApplySkipped}, Live: ApplyStage{Status: ApplySkipped}}
	if !s.Supported() {
		out.Mode = string(config.NormalizeLiveResourcesMode(value))
		out.Failure = "unsupported"
		return out, fmt.Errorf("live system resources are unavailable on this platform")
	}
	mode := config.NormalizeLiveResourcesMode(value)
	out.Mode = string(mode)
	paths, err := s.Paths()
	if err != nil {
		out.Failure = "save"
		return out, err
	}
	if err := config.SaveLiveResourcesFile(paths.LiveResourcesFile(), mode); err != nil {
		out.Failure = "save"
		return out, err
	}
	out.Saved = true
	out.Prepared, out.Live, err = s.Apply()
	if err != nil {
		if out.Prepared.Status == ApplyFailed {
			out.Failure = "prepare"
			return out, fmt.Errorf("update status bar runtime config: %w", err)
		}
		if out.Live.Status == ApplyFailed {
			out.Failure = "apply"
			return out, fmt.Errorf("reload active status bar: %w", err)
		}
		return out, err
	}
	return out, nil
}
