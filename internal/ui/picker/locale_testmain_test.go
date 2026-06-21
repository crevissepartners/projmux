package picker

import (
	"os"
	"testing"
)

// TestMain pins a deterministic default UI locale for the picker test package,
// mirroring internal/app. Several native-interactive render tests assert
// English chrome (search header, titlebar) without resolving a locale of their
// own, so on a host whose ambient LANG is ko_KR they would render Korean and
// diverge from CI (which runs under en-US). Setting the lowest-priority rung
// (LANG=en_US.UTF-8) and clearing the higher rungs keeps the suite
// deterministic while letting any test opt into another locale via t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("PROJMUX_LOCALE")
	os.Unsetenv("LC_ALL")
	os.Unsetenv("LC_MESSAGES")
	os.Setenv("LANG", "en_US.UTF-8")
	os.Exit(m.Run())
}
