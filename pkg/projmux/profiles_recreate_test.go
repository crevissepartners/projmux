package projmux_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSDKProfileRecreateRestoresMissingInstructionsOriginal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	c := clientAt(t, dir)
	ctx := context.Background()
	original := []byte("# original\ninstructions = \"missing-instructions\"\nmodel = \"opus\"\n")
	next := append([]byte("project = \"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa\"\n"), original...)
	path := filepath.Join(dir, "profiles", "candidate.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{original, next} {
		if _, err := c.ParseProfile(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, "marker")
	if err := os.WriteFile(marker, []byte("unrelated"), 0640); err != nil {
		t.Fatal(err)
	}
	markersBefore := profileTree(t, marker)
	before, err := c.ReadProfile(ctx, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.RecreateProfile(ctx, "candidate", next, true)
	if codeOf(t, err) != projmux.CodeProfileInstructionsNotFound {
		t.Fatal(err)
	}
	after, err := c.ReadProfile(ctx, "candidate")
	if err != nil || !bytes.Equal(after.Content, original) || after.Digest != before.Digest {
		t.Fatalf("original lost: %+v %v", after, err)
	}
	if !reflect.DeepEqual(markersBefore, profileTree(t, marker)) {
		t.Fatal("unrelated file changed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestSDKProfileRecreateRestoresRoleClaimedOriginal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	c := clientAt(t, dir)
	original := []byte("# keep exact\nroles=[\"claimed\"]\n")
	next := append([]byte("project=\"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa\"\n"), original...)
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"candidate", "other"} {
		if err := os.WriteFile(filepath.Join(dir, "profiles", name+".toml"), original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := profileTree(t, filepath.Join(dir, "profiles", "other.toml"))
	_, err := c.RecreateProfile(context.Background(), "candidate", next, true)
	if codeOf(t, err) != projmux.CodeProfileRoleClaimed {
		t.Fatal(err)
	}
	after, err := c.ReadProfile(context.Background(), "candidate")
	if err != nil || !bytes.Equal(after.Content, original) {
		t.Fatalf("lost original: %v", err)
	}
	if !reflect.DeepEqual(before, profileTree(t, filepath.Join(dir, "profiles", "other.toml"))) {
		t.Fatal("other profile changed")
	}
}

func TestSDKProfileRecreateChangesScope(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	c := clientAt(t, dir)
	ctx := context.Background()
	global := []byte("model=\"opus\"\n")
	scoped := []byte("project=\"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa\"\nmodel=\"opus\"\n")
	if _, err := c.WriteProfile(ctx, "candidate", global); err != nil {
		t.Fatal(err)
	}
	for _, next := range [][]byte{scoped, global} {
		if _, err := c.WriteProfile(ctx, "candidate", next); codeOf(t, err) != projmux.CodeProfileScopeChanged {
			t.Fatal(err)
		}
		got, err := c.RecreateProfile(ctx, "candidate", next, true)
		if err != nil {
			t.Fatal(err)
		}
		read, err := c.ReadProfile(ctx, "candidate")
		if err != nil || !bytes.Equal(read.Content, next) || got.Digest != read.Digest || got.Size != int64(len(next)) {
			t.Fatalf("result %+v read %+v %v", got, read, err)
		}
		info, err := os.Stat(got.Path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("mode %v %v", info, err)
		}
	}
	files, err := os.ReadDir(filepath.Join(dir, "profiles"))
	if err != nil || len(files) != 1 {
		t.Fatalf("temp residue %v %v", files, err)
	}
}

func TestSDKProfileRecreateAdmissionRefusals(t *testing.T) {
	for _, scenario := range []string{"cancel", "name", "reserved", "confirm", "size", "missing", "parse", "directory", "oversize-original", "outside-symlink", "dangling-symlink", "path"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "config")
			c := clientAt(t, dir)
			ctx := context.Background()
			name := "candidate"
			confirm := true
			next := []byte("model=\"opus\"")
			path := filepath.Join(dir, "profiles", "candidate.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("# original\n"), 0600); err != nil {
				t.Fatal(err)
			}
			want := projmux.CodeProfileNotFound
			switch scenario {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				name = "../bad"
				confirm = false
			case "name":
				name = "../bad"
				confirm = false
				want = projmux.CodeProfileNameInvalid
			case "reserved":
				name = "none"
				confirm = false
				want = projmux.CodeProfileNameReserved
			case "confirm":
				confirm = false
				next = bytes.Repeat([]byte(" "), 65537)
				want = projmux.CodeProfileConfirmationRequired
			case "size":
				next = bytes.Repeat([]byte(" "), 65537)
				want = projmux.CodeProfileTooLarge
			case "missing":
				name = "missing"
			case "parse":
				if err := os.WriteFile(path, []byte("model="), 0600); err != nil {
					t.Fatal(err)
				}
				want = projmux.CodeProfileSyntaxInvalid
			case "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversize-original":
				if err := os.WriteFile(path, bytes.Repeat([]byte(" "), 65537), 0600); err != nil {
					t.Fatal(err)
				}
				want = projmux.CodeProfileTooLarge
			case "outside-symlink", "dangling-symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(root, "target.toml")
				if scenario == "outside-symlink" {
					if err := os.WriteFile(target, []byte("# target"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "dangling-symlink" {
					target = "missing.toml"
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}

			case "path":
				dir = filepath.Join(root, "blocked")
				if err := os.WriteFile(dir, nil, 0600); err != nil {
					t.Fatal(err)
				}
				c = clientAt(t, dir)
			}
			before := profileTree(t, root)
			got, err := c.RecreateProfile(ctx, name, next, confirm)
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if scenario == "path" || scenario == "outside-symlink" {
				if err == nil {
					t.Fatal("expected path error")
				}
			} else if codeOf(t, err) != want {
				t.Fatal(err)
			}
			if got != (core.ProfileWriteResult{}) {
				t.Fatalf("partial result %+v", got)
			}
			if !reflect.DeepEqual(before, profileTree(t, root)) {
				t.Fatal("admission changed files")
			}
		})
	}
}

func TestSDKProfileRecreateSymlinkMatchesStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	c := clientAt(t, dir)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("instructions=\"missing\"\n")
	target := filepath.Join(dir, "profiles", "target.toml")
	link := filepath.Join(dir, "profiles", "candidate.toml")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.toml", link); err != nil {
		t.Fatal(err)
	}
	before := profileTree(t, target)
	_, err := c.RecreateProfile(ctx, "candidate", []byte("instructions=\"missing\"\nmodel=\"opus\""), true)
	if codeOf(t, err) != projmux.CodeProfileInstructionsNotFound {
		t.Fatal(err)
	}
	got, err := c.ReadProfile(ctx, "candidate")
	if err != nil || !bytes.Equal(got.Content, original) {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("restored file %v %v", info, err)
	}
	if !reflect.DeepEqual(before, profileTree(t, target)) {
		t.Fatal("symlink target changed")
	}
}

func TestSDKProfileRecreateRestoreFailureCauses(t *testing.T) {
	// Filesystem faults are injected into the facade projection in the test
	// binary. Real snapshot retention and destructive boundaries are covered by
	// TestStoreRecreateReportsRestoreFailure, without races or global hooks.
	writeErr := &os.PathError{Op: "write", Path: "candidate.toml", Err: fs.ErrPermission}
	restoreErr := &os.LinkError{Op: "rename", Old: ".candidate.restore-123", New: "candidate.toml", Err: fs.ErrExist}
	err := projmux.ProfileRestoreRefusalForTest(writeErr, restoreErr)
	var refusal *projmux.Refusal
	var write *os.PathError
	var restore *os.LinkError
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProfileRestoreFailed || refusal.Name != "candidate" {
		t.Fatalf("refusal %v", err)
	}
	if !errors.Is(err, writeErr) || !errors.Is(err, restoreErr) || !errors.As(err, &write) || write != writeErr || !errors.As(err, &restore) || restore != restoreErr {
		t.Fatalf("causes lost %v", err)
	}
	if !strings.Contains(err.Error(), ".candidate.restore-123") || !strings.Contains(err.Error(), "not restored") {
		t.Fatalf("snapshot recovery message %v", err)
	}
}

func TestSDKProfileRecreateRestoresNonstandardOriginalMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	c := clientAt(t, dir)
	ctx := context.Background()
	original := []byte("# exact original\ninstructions=\"missing\"\n")
	path := filepath.Join(dir, "profiles", "candidate.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := c.ReadProfile(ctx, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.RecreateProfile(ctx, "candidate", append([]byte("project=\"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa\"\n"), original...), true)
	if codeOf(t, err) != projmux.CodeProfileInstructionsNotFound {
		t.Fatal(err)
	}
	after, err := c.ReadProfile(ctx, "candidate")
	if err != nil || !bytes.Equal(after.Content, original) || after.Digest != before.Digest {
		t.Fatalf("original lost %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("restored mode %v %v", info, err)
	}
	result, err := c.RecreateProfile(ctx, "candidate", []byte("model=\"opus\""), true)
	if err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(result.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("new mode %v %v", info, err)
	}
}
