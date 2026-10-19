//go:build darwin && !cgo

package projmux

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/systemstatus"
)

func TestResourcesSupportedBuild(t *testing.T) {
	if got := ResourcesSupported(); got != false || got != systemstatus.Supported() {
		t.Fatalf("ResourcesSupported() = %v, want false and internal authority parity", got)
	}
}
