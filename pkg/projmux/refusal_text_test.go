package projmux

import (
	"errors"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

func TestSDKProjectNameExhaustionText(t *testing.T) {
	reg := metadata.NewRegistry()
	reg.NameReservations = append(reg.NameReservations, metadata.NameReservation{Kind: metadata.KindProject, Name: "collision", UID: "reserved"})
	attempts := 0
	m := metadata.Mutator{DirExists: func(string) (bool, error) { return true, nil }, NewUID: func(metadata.Kind) (string, error) { attempts++; return "collision", nil }}
	_, cause := m.RegisterProject(&reg, metadata.RegisterProjectOptions{Root: "/"})
	if !errors.Is(cause, metadata.ErrNameExhausted) || attempts != 100 {
		t.Fatalf("expected bounded name exhaustion, attempts=%d error=%v", attempts, cause)
	}
	cli := mutations.Classify(cause)
	sdk := projectRefusal(cause)
	t.Logf("CLI: %v\nSDK: %v", cli, sdk)
	var refusal *Refusal
	if !errors.As(sdk, &refusal) || refusal.Code != CodeResourceNameExhausted || cli.Error() != sdk.Error() {
		t.Fatalf("CLI=%v SDK=%v", cli, sdk)
	}
}
