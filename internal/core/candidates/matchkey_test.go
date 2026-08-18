package candidates

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMatchKeyForCompatibilityTable is the cross-platform contract of the path
// identity key, frozen as one table so a Linux CI run asserts the Windows answers.
//
// The two columns are two different questions. On a case-sensitive filesystem two
// spellings are the same directory only when they are the same path; on Windows
// they are also the same when they differ by separator, case, or drive-letter case.
// Nothing in either column is allowed to be a statement about managed identity: a
// Project uid is minted once and stored, and MatchKey is consulted only for
// candidate exact-match and for legacy path pin migration.
func TestMatchKeyForCompatibilityTable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		path        string
		wantLinux   string
		wantWindows string
	}{
		{
			name:        "posix absolute path",
			path:        "/srv/work/app",
			wantLinux:   "/srv/work/app",
			wantWindows: "/srv/work/app",
		},
		{
			name:        "posix trailing separator",
			path:        "/srv/work/app/",
			wantLinux:   "/srv/work/app",
			wantWindows: "/srv/work/app",
		},
		{
			name:        "posix case is significant",
			path:        "/srv/Work/App",
			wantLinux:   "/srv/Work/App",
			wantWindows: "/srv/work/app",
		},
		{
			name:        "windows backslash separators",
			path:        `C:\Users\dev\src\app`,
			wantLinux:   `C:\Users\dev\src\app`,
			wantWindows: "c:/users/dev/src/app",
		},
		{
			name:        "windows lowercase drive and forward slashes",
			path:        "c:/users/dev/src/app",
			wantLinux:   "c:/users/dev/src/app",
			wantWindows: "c:/users/dev/src/app",
		},
		{
			// On a case-sensitive host a Windows path is opaque text: nothing in
			// it is a separator, so cleaning leaves it exactly as written. That is
			// the honest answer rather than a bug -- the folding column is what
			// Windows hosts and Windows goldens use.
			name:        "windows mixed separators and dot segment",
			path:        `C:\Users\dev\src\.\app\`,
			wantLinux:   `C:\Users\dev\src\.\app\`,
			wantWindows: "c:/users/dev/src/app",
		},
		{
			name:        "windows UNC share",
			path:        `\\Server\Share\Repo`,
			wantLinux:   `\\Server\Share\Repo`,
			wantWindows: "//server/share/repo",
		},
		{
			name:        "empty path has no key",
			path:        "   ",
			wantLinux:   "",
			wantWindows: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchKeyFor("linux", tc.path); got != tc.wantLinux {
				t.Fatalf("MatchKeyFor(linux, %q) = %q, want %q", tc.path, got, tc.wantLinux)
			}
			if got := MatchKeyFor("darwin", tc.path); got != tc.wantLinux {
				t.Fatalf("MatchKeyFor(darwin, %q) = %q, want the case-sensitive answer %q", tc.path, got, tc.wantLinux)
			}
			if got := MatchKeyFor("windows", tc.path); got != tc.wantWindows {
				t.Fatalf("MatchKeyFor(windows, %q) = %q, want %q", tc.path, got, tc.wantWindows)
			}
		})
	}
}

// TestMatchKeyUsesTheRunningOS keeps the convenience wrapper honest: product code
// calls MatchKey and must get its own platform's rules, not the goldens'.
func TestMatchKeyUsesTheRunningOS(t *testing.T) {
	t.Parallel()

	const path = "/srv/Work/App"
	if got, want := MatchKey(path), MatchKeyFor(runtime.GOOS, path); got != want {
		t.Fatalf("MatchKey(%q) = %q, want %q", path, got, want)
	}
}

// TestMatchKeyResolvesSymlinksBeforeFolding pins the composition order. Resolution
// runs first, so a symlinked spelling and its real path are one key -- and on
// Windows the folding then applies to what resolution returned.
func TestMatchKeyResolvesSymlinksBeforeFolding(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got, want := MatchKeyFor("linux", link), MatchKeyFor("linux", real); got != want {
		t.Fatalf("MatchKeyFor(linux, link) = %q, want the real path key %q", got, want)
	}
}
