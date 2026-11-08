package localipc

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// legacyProcessAncestry is the body processCreatorAncestry had before the walk
// moved here, kept as the oracle of TestProcessAncestryEqualsLegacyBody.
func legacyProcessAncestry(pid int, read func(int) (coremetadata.ProcessIdentity, int, error)) ([]coremetadata.ProcessIdentity, error) {
	chain := make([]coremetadata.ProcessIdentity, 0, 8)
	for range 64 {
		identity, parent, err := read(pid)
		if err != nil {
			return chain, err
		}
		chain = append(chain, identity)
		if parent <= 1 || parent == pid {
			break
		}
		pid = parent
	}
	return chain, nil
}

func ancestryFixture(parents map[int]int, failAt map[int]error) func(int) (coremetadata.ProcessIdentity, int, error) {
	return func(pid int) (coremetadata.ProcessIdentity, int, error) {
		if err, ok := failAt[pid]; ok {
			return coremetadata.ProcessIdentity{}, 0, err
		}
		parent, ok := parents[pid]
		if !ok {
			return coremetadata.ProcessIdentity{}, 0, ErrProcessAbsent
		}
		return coremetadata.ProcessIdentity{PID: pid, OwnerUID: 1000, Start: fmt.Sprint(pid * 7)}, parent, nil
	}
}

func TestProcessAncestryEqualsLegacyBody(t *testing.T) {
	long := map[int]int{}
	for pid := 100; pid < 200; pid++ {
		long[pid] = pid + 1
	}
	boom := errors.New("boom")
	for _, tc := range []struct {
		name    string
		parents map[int]int
		failAt  map[int]error
		start   int
	}{
		{"to init", map[int]int{10: 5, 5: 2, 2: 1}, nil, 10},
		{"single", map[int]int{10: 1}, nil, 10},
		{"self parent", map[int]int{10: 5, 5: 5}, nil, 10},
		{"parent zero", map[int]int{10: 0}, nil, 10},
		{"second step fails", map[int]int{10: 5, 5: 2, 2: 1}, map[int]error{5: boom}, 10},
		{"first step fails", map[int]int{10: 5}, map[int]error{10: boom}, 10},
		{"absent parent", map[int]int{10: 5}, nil, 10},
		{"chain longer than bound", long, nil, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := ancestryFixture(tc.parents, tc.failAt)
			want, wantErr := legacyProcessAncestry(tc.start, read)
			got, gotErr := processAncestry(tc.start, read)
			if !reflect.DeepEqual(got, want) || !errors.Is(gotErr, wantErr) && gotErr != wantErr {
				t.Fatalf("processAncestry = %v, %v; legacy %v, %v", got, gotErr, want, wantErr)
			}
		})
	}
}

func TestProcessAncestryFailureReturnsPartialChain(t *testing.T) {
	boom := errors.New("boom")
	parents := map[int]int{10: 9, 9: 8, 8: 7, 7: 6, 6: 1}
	for k := 0; k < 5; k++ {
		pid := 10 - k
		got, err := processAncestry(10, ancestryFixture(parents, map[int]error{pid: boom}))
		if !errors.Is(err, boom) || len(got) != k {
			t.Fatalf("fail at step %d: len=%d err=%v", k, len(got), err)
		}
		for i, identity := range got {
			if identity.PID != 10-i {
				t.Fatalf("fail at step %d: chain[%d]=%d, want nearest first", k, i, identity.PID)
			}
		}
	}
}

func TestProcessAncestryBoundsTheChainAtSixtyFourWithoutError(t *testing.T) {
	parents := map[int]int{}
	for pid := 100; pid <= 100+ProcessAncestryMaxSteps; pid++ { // 65 processes
		parents[pid] = pid + 1
	}
	got, err := processAncestry(100, ancestryFixture(parents, nil))
	if err != nil || len(got) != 64 {
		t.Fatalf("len=%d err=%v, want 64 and no error", len(got), err)
	}
}

func TestProcessAncestryNonPositivePIDIsAnEmptyChainWithError(t *testing.T) {
	for _, pid := range []int{0, -1} {
		got, err := ProcessAncestry(pid)
		if err == nil || got == nil || len(got) != 0 {
			t.Fatalf("ProcessAncestry(%d) = %v, %v", pid, got, err)
		}
	}
}

func TestProcessAncestryOfThisProcessStartsWithItself(t *testing.T) {
	got, err := ProcessAncestry(os.Getpid())
	if err != nil || len(got) == 0 || got[0].PID != os.Getpid() {
		t.Fatalf("ProcessAncestry(self) = %v, %v", got, err)
	}
}
