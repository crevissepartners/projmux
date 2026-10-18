package projmux_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestValidateProfileNameParity(t *testing.T) {
	for _, name := range []string{
		"", "worker", "worker-1", "a.b_c", "none", "None", "NONE",
		".hidden", "-flag", ".", "..", " leading", "trailing ", "a b",
		"a/b", `a\b`, "a:b", "a\tb", "a\nb", "a\x00b", "a\x7fb",
		"프로필", "café", "a\xffb", strings.Repeat("a", 127),
		strings.Repeat("a", 128), strings.Repeat("a", 129),
		strings.Repeat("é", 64), strings.Repeat("é", 65),
	} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			want := profile.ValidateName(name)
			got := projmux.ValidateProfileName(name)
			if (got == nil) != (want == nil) {
				t.Fatalf("input %q: public=%v core=%v", name, got, want)
			}
			if want == nil {
				return
			}
			var coreError *profile.Error
			var refusal *projmux.Refusal
			if !errors.As(want, &coreError) || !errors.As(got, &refusal) {
				t.Fatalf("input %q: public=%T core=%T", name, got, want)
			}
			if string(refusal.Code) != coreError.Reason || refusal.Name != coreError.Name || got.Error() != want.Error() {
				t.Fatalf("input %q: public=(%s, %q, %q) core=(%s, %q, %q)",
					name, refusal.Code, refusal.Name, got.Error(), coreError.Reason, coreError.Name, want.Error())
			}
		})
	}
}

func TestValidateProfileNameNoAccess(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%t", blocked), func(t *testing.T) {
			root := t.TempDir()
			// A regular file as an ancestor makes HOME/XDG/store paths
			// inaccessible even when the test runs with elevated privileges.
			if err := os.WriteFile(filepath.Join(root, "blocker"), []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			base := root
			if blocked {
				base = filepath.Join(root, "blocker")
			}
			for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PATH"} {
				t.Setenv(key, filepath.Join(base, key))
			}
			before := profileNameTree(t, root)
			environment := os.Environ()
			// No Client is constructed. Both success and refusal must remain
			// independent of unavailable storage and executable paths.
			if err := projmux.ValidateProfileName("worker"); err != nil {
				t.Fatal(err)
			}
			for name, code := range map[string]projmux.Code{
				"none":   projmux.CodeProfileNameReserved,
				"../bad": projmux.CodeProfileNameInvalid,
			} {
				var refusal *projmux.Refusal
				if err := projmux.ValidateProfileName(name); !errors.As(err, &refusal) || refusal.Code != code || refusal.Name != name {
					t.Fatalf("input %q: %v", name, err)
				}
			}
			if !reflect.DeepEqual(before, profileNameTree(t, root)) {
				t.Fatal("name validation created or changed storage")
			}
			if !reflect.DeepEqual(environment, os.Environ()) {
				t.Fatal("name validation changed the environment")
			}
		})
	}
}

func profileNameTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%v %v", info.Mode(), info.ModTime())
		if !entry.IsDir() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] += string(content)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ExampleValidateProfileName() {
	fmt.Println(projmux.ValidateProfileName("worker"))
	var refusal *projmux.Refusal
	if err := projmux.ValidateProfileName("none"); errors.As(err, &refusal) {
		fmt.Println(refusal.Code, refusal.Name)
	}
	// Output:
	// <nil>
	// profile-name-reserved none
}
