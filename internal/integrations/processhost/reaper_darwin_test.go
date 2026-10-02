package processhost

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestGroupAbsenceRequiresESRCH(t *testing.T) {
	for _, pending := range []error{nil, syscall.EPERM} {
		calls := 0
		err := waitGroupAbsent(123, time.Second, func() error {
			calls++
			if calls < 3 {
				return pending
			}
			return syscall.ESRCH
		})
		if err != nil || calls != 3 {
			t.Fatalf("completed before ESRCH: calls=%d err=%v", calls, err)
		}
	}
	began := time.Now()
	if err := waitGroupAbsent(123, 5*time.Millisecond, func() error { return syscall.EPERM }); err == nil {
		t.Fatal("persistent EPERM treated as absence")
	}
	if time.Since(began) > time.Second {
		t.Fatal("denied observation was not bounded")
	}
	if err := waitGroupAbsent(123, time.Second, func() error { return syscall.EIO }); !errors.Is(err, syscall.EIO) {
		t.Fatalf("unexpected observation failure hidden: %v", err)
	}
}
