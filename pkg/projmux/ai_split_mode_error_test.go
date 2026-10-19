package projmux

import (
	"errors"
	"fmt"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/instructions"
)

func TestAISplitModePathErrorRetainsLegacyContext(t *testing.T) {
	cause := errors.New("path resolver unavailable")
	c := &Client{instructions: instructions.Service{Paths: func() (config.Paths, error) { return config.Paths{}, cause }}}
	_, err := c.SetAISplitMode("codex")
	legacy := fmt.Errorf("save AI split default: %w", cause)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeAISplitModeSaveFailed || err.Error() != legacy.Error() || !errors.Is(err, cause) || !errors.Is(errors.Unwrap(err), cause) {
		t.Fatalf("SDK=%v legacy=%v", err, legacy)
	}
}
