//go:build !unix

package osfocus

// defaultWTRun is a non-Unix no-op. The WindowsTerminalWSLAdapter is for
// projmux running inside WSL, where GOOS=linux and wt.exe is launched through
// WSL interop. Native non-Unix builds must not compile in bash or POSIX
// process group assumptions.
func defaultWTRun(_ string, _ ...string) error {
	return nil
}
