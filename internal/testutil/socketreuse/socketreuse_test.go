package socketreuse

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestMeasureCountsIdentityCollisions(t *testing.T) {
	lstatIdentity := func(path string) (syscall.Stat_t, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return syscall.Stat_t{}, err
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
			return syscall.Stat_t{}, errors.New("probe is not a private socket")
		}
		return *info.Sys().(*syscall.Stat_t), nil
	}
	measured, err := MeasureSameTickReplacement("/tmp", 20, lstatIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Iterations != 20 || measured.Collisions < 0 || measured.Collisions > 20 {
		t.Fatalf("measurement = %+v", measured)
	}
	t.Logf("host collisions: %d/%d", measured.Collisions, measured.Iterations)

	measured, err = MeasureSameTickReplacement("/tmp", 5, func(string) (int, error) { return 1, nil })
	if err != nil || measured != (Measurement{Iterations: 5, Collisions: 5}) {
		t.Fatalf("constant identity = %+v, %v; want every replacement counted", measured, err)
	}
	failed := errors.New("inspect failed")
	if _, err := MeasureSameTickReplacement("/tmp", 5, func(string) (int, error) { return 0, failed }); !errors.Is(err, failed) {
		t.Fatalf("identity error = %v, want it returned", err)
	}
}
