//go:build windows

package osfocus

import "testing"

func TestWindowsTerminalWSLAdapter_DefaultRunIsNativeWindowsNoop(t *testing.T) {
	t.Parallel()

	a := WindowsTerminalWSLAdapter{}
	if err := a.Focus(Target{Session: "native-windows"}); err != nil {
		t.Fatalf("Focus() with native Windows default runner returned %v, want nil", err)
	}
}
