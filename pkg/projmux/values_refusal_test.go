package projmux

import (
	"errors"
	"fmt"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// A real *Refusal keeps its cause private, so the rows that need one are built
// here rather than from the external test package.
func TestRefusalRowsOfTheValueJudgements(t *testing.T) {
	mismatch := &codexappserver.PolicyMismatchError{Method: "thread/start"}
	refusal := &Refusal{Code: CodeCreateRequestInvalid, message: "refused", cause: fmt.Errorf("relaunch: %w", mismatch)}
	for name, err := range map[string]error{
		"refusal":         refusal,
		"wrapped refusal": fmt.Errorf("outer: %w", refusal),
	} {
		if !errors.Is(err, ErrCodexThreadPolicyMismatch) {
			t.Fatalf("%s: errors.Is false", name)
		}
	}
	plain := &Refusal{Code: CodeCreateRequestInvalid, message: "refused", cause: errors.New("other")}
	if errors.Is(plain, ErrCodexThreadPolicyMismatch) {
		t.Fatal("a refusal with another cause reached the sentinel")
	}

	usage := &metadata.InputError{Op: "op", Detail: "bad input", Cause: errors.New("cause")}
	for name, err := range map[string]error{
		"refusal over usage":         &Refusal{Code: CodeCreateRequestInvalid, message: "refused", cause: usage},
		"wrapped refusal over usage": fmt.Errorf("outer: %w", &Refusal{Code: CodeCreateRequestInvalid, message: "refused", cause: usage}),
		"refusal over plain":         plain,
	} {
		if got, want := IsUsageError(err), metadata.IsUsageError(err); got != want {
			t.Fatalf("%s: public %v internal %v", name, got, want)
		}
	}
	if !IsUsageError(&Refusal{message: "refused", cause: usage}) {
		t.Fatal("a refusal over a usage error is not a usage error")
	}
}
