package candidates

import (
	"path"
	"runtime"
	"strings"
)

// MatchKey returns the identity key that decides whether two path spellings
// name the same directory.
//
// It has exactly two callers by contract: candidate exact-match, and the legacy
// path pin -> Project uid migration. Both are questions about a *path*, which is
// why folding spellings is safe there. Managed identity is never derived from
// this key: a Project uid is minted once and stored, and no amount of path
// agreement mints a second one or merges two.
//
// On a case-sensitive filesystem the key is CanonicalPath: symlink-resolved when
// possible, lexically cleaned when not. On Windows the same resolution runs
// first and the result is then folded on the three axes where two spellings of
// one directory legitimately differ -- separator, case, and drive letter -- so
// `C:\Users\dev\src` and `c:/users/dev/src` are one candidate rather than two.
func MatchKey(p string) string {
	return MatchKeyFor(runtime.GOOS, p)
}

// MatchKeyFor is MatchKey for an explicitly named target OS.
//
// The Windows folding rules are lexical, so this is the seam that makes the
// Windows behaviour assertable from a Linux test run instead of only on a
// Windows host. Callers in product code use MatchKey; goldens name the OS.
func MatchKeyFor(goos, p string) string {
	key := CanonicalPath(p)
	if key == "" {
		return ""
	}
	if goos != "windows" {
		return key
	}
	key = strings.ReplaceAll(key, `\`, "/")
	key = strings.ToLower(key)
	// path.Clean, not filepath.Clean: the slash form is already normalized and a
	// Linux test run must fold it the same way a Windows host does.
	if unc := strings.HasPrefix(key, "//"); unc {
		return "//" + strings.TrimPrefix(path.Clean(key), "/")
	}
	return path.Clean(key)
}
