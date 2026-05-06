package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// LimitsEnvVar is the environment variable that, when set, points to a JSON
// file overriding the hardcoded model limits. The JSON layout mirrors
// LimitsFile below, e.g.:
//
//	{
//	  "claude": {"5h": 1000000, "weekly": 20000000},
//	  "codex":  {"5h": 500000,  "weekly": 10000000}
//	}
const LimitsEnvVar = "PROJMUX_USAGE_LIMITS_PATH"

// Default limits — placeholders until the real numbers are pulled from each
// vendor's API.
//
// TODO(usage): pull real limits from the Anthropic / OpenAI APIs (or, for
// Claude Code, surface them via `claude usage` once that lands). The values
// below are deliberately round numbers chosen as conservative ballpark caps
// so a 0%-or-100% status segment doesn't render as missing data.
var DefaultLimits = Limits{
	"claude": {Window5h: 1_000_000, WindowWeekly: 20_000_000},
	"codex":  {Window5h: 500_000, WindowWeekly: 10_000_000},
}

// ModelLimits is a per-window limit row for a single model.
type ModelLimits map[Window]int64

// For returns the limit for the given window, or 0 when unknown.
func (m ModelLimits) For(w Window) int64 {
	if m == nil {
		return 0
	}
	return m[w]
}

// Limits is the model-keyed limit table the aggregator consumes.
type Limits map[string]ModelLimits

// For returns the ModelLimits row for the model name, or nil.
func (l Limits) For(model string) ModelLimits {
	if l == nil {
		return nil
	}
	return l[strings.ToLower(model)]
}

// LoadLimits returns the effective limits, applying the override file at
// path on top of the defaults. An empty path or a missing file returns the
// defaults verbatim with no error so the env var stays optional.
func LoadLimits(path string) (Limits, error) {
	merged := cloneLimits(DefaultLimits)
	if strings.TrimSpace(path) == "" {
		return merged, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return merged, nil
		}
		return nil, fmt.Errorf("usage: read limits %s: %w", path, err)
	}
	override := Limits{}
	if err := json.Unmarshal(data, &override); err != nil {
		return nil, fmt.Errorf("usage: parse limits %s: %w", path, err)
	}
	for model, row := range override {
		key := strings.ToLower(strings.TrimSpace(model))
		if key == "" {
			continue
		}
		dst, ok := merged[key]
		if !ok || dst == nil {
			dst = ModelLimits{}
		}
		for w, v := range row {
			if v < 0 {
				continue
			}
			dst[w] = v
		}
		merged[key] = dst
	}
	return merged, nil
}

func cloneLimits(in Limits) Limits {
	out := make(Limits, len(in))
	for model, row := range in {
		copyRow := make(ModelLimits, len(row))
		for w, v := range row {
			copyRow[w] = v
		}
		out[model] = copyRow
	}
	return out
}
