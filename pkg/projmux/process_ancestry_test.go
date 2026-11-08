package projmux

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestProcessAncestryPublicEqualsLocalIPC(t *testing.T) {
	pid := os.Getpid()
	want, wantErr := localipc.ProcessAncestry(pid)
	got, err := ReadProcessAncestry(pid)
	if (err == nil) != (wantErr == nil) || len(got) != len(want) || len(got) == 0 {
		t.Fatalf("public %v, %v; localipc %v, %v", got, err, want, wantErr)
	}
	for i := range want {
		if got[i] != (core.ProcessIdentity{PID: want[i].PID, OwnerUID: want[i].OwnerUID, Start: want[i].Start}) {
			t.Fatalf("chain[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got[0].PID != pid {
		t.Fatalf("chain starts at %d, want the caller's pid %d", got[0].PID, pid)
	}
}

func TestProcessAncestryFailureIsPartialChainWithRefusal(t *testing.T) {
	cause := errors.New("process identity unavailable")
	for k := 0; k <= 3; k++ {
		partial := make([]metadata.ProcessIdentity, k)
		for i := range partial {
			partial[i] = metadata.ProcessIdentity{PID: 50 - i, OwnerUID: 1000, Start: fmt.Sprint(i + 1)}
		}
		got, err := publicProcessAncestry(50, func(int) ([]metadata.ProcessIdentity, error) { return partial, cause })
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeProcessAncestryUnobservable || refusal.Code != "process-ancestry-unobservable" {
			t.Fatalf("k=%d err = %v, want the unobservable refusal", k, err)
		}
		if !errors.Is(err, cause) || err.Error() != cause.Error() {
			t.Fatalf("k=%d err = %q, want cause %q reachable and as its message", k, err, cause)
		}
		if len(got) != k || (k > 0 && got[0] != (core.ProcessIdentity{PID: 50, OwnerUID: 1000, Start: "1"})) {
			t.Fatalf("k=%d partial chain = %v", k, got)
		}
	}
}

func TestProcessAncestryNonPositivePIDIsEmptyChainAndRefusal(t *testing.T) {
	for _, pid := range []int{0, -3} {
		got, err := ReadProcessAncestry(pid)
		var refusal *Refusal
		if len(got) != 0 || !errors.As(err, &refusal) || refusal.Code != CodeProcessAncestryUnobservable {
			t.Fatalf("ReadProcessAncestry(%d) = %v, %v", pid, got, err)
		}
	}
}

func TestProcessAncestrySuccessCarriesNoError(t *testing.T) {
	got, err := publicProcessAncestry(7, func(int) ([]metadata.ProcessIdentity, error) {
		return []metadata.ProcessIdentity{{PID: 7, OwnerUID: 1, Start: "2"}}, nil
	})
	if err != nil || !reflect.DeepEqual(got, []core.ProcessIdentity{{PID: 7, OwnerUID: 1, Start: "2"}}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestProcessAncestryBoundEqualsInternal(t *testing.T) {
	if ProcessAncestryMaxSteps != localipc.ProcessAncestryMaxSteps || ProcessAncestryMaxSteps != 64 {
		t.Fatalf("ProcessAncestryMaxSteps = %d", ProcessAncestryMaxSteps)
	}
}

func TestProcessAncestryGodocStatesPartialChainIsNotActorEvidence(t *testing.T) {
	src, err := os.ReadFile("process_ancestry.go")
	if err != nil || !strings.Contains(string(src), "A partial chain is not evidence of who acted") {
		t.Fatalf("godoc sentence missing: %v", err)
	}
}
