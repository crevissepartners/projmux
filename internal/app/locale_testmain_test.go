package app

import (
	"os"
	"testing"
)

// TestMain pins a deterministic default UI locale for the whole app test
// package. Many tests assert English chrome strings without resolving a locale
// of their own; on a developer machine whose ambient LANG is ko_KR those tests
// would otherwise render Korean (the localization now works through the shared
// picker choke point) and diverge from CI, which runs under en-US.
//
// It sets the LOWEST-priority locale rung (LANG=en_US.UTF-8) and clears the
// higher-priority rungs (PROJMUX_LOCALE, LC_ALL, LC_MESSAGES) rather than
// pinning PROJMUX_LOCALE. Tests that intentionally exercise ko-KR override LANG
// locally via t.Setenv("LANG", "ko_KR.UTF-8"), which still wins because the
// higher rungs stay cleared. This makes the suite locale-deterministic on any
// host while leaving each test free to opt into another locale explicitly.
func TestMain(m *testing.M) {
	os.Unsetenv("PROJMUX_LOCALE")
	os.Unsetenv("LC_ALL")
	os.Unsetenv("LC_MESSAGES")
	os.Setenv("LANG", "en_US.UTF-8")
	os.Exit(m.Run())
}
