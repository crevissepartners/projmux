package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// An explicit evidence root retains every fixture and observation on failure.
// Ordinary repository runs keep the normal testing temporary-directory policy.
func resourcesTestRoot(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	if root := os.Getenv("SDK_STATUSBAR_RESOURCES_EVIDENCE_ROOT"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("absolute evidence root required")
		}
		var err error
		base, err = os.MkdirTemp(root, "settings-")
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture, observations := filepath.Join(base, "fixture"), filepath.Join(base, "observations")
	for _, path := range []string{fixture, observations} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("retained evidence:", base)
	return fixture, observations
}

type resourcesFile struct {
	Mode                     os.FileMode
	Size, Mtime              int64
	Device, Inode            uint64
	Bytes                    []byte
	ReadErrorType, ReadError string
}

func resourcesInventory(t *testing.T, root string) map[string]resourcesFile {
	t.Helper()
	out := map[string]resourcesFile{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		row := resourcesFile{Mode: info.Mode(), Size: info.Size(), Mtime: info.ModTime().UnixNano()}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			row.Device, row.Inode = uint64(st.Dev), uint64(st.Ino)
		}
		row.Bytes, err = os.ReadFile(path)
		if err != nil {
			row.ReadErrorType, row.ReadError = fmt.Sprintf("%T", err), err.Error()
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = row
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func resourcesRecord(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(writeErr, closeErr)
	}
}

func resourcesError(err error) any {
	if err == nil {
		return nil
	}
	row := map[string]any{"type": fmt.Sprintf("%T", err), "text": err.Error(), "unwrap": resourcesError(errors.Unwrap(err))}
	if p, ok := err.(*os.PathError); ok {
		row["op"], row["path"] = p.Op, p.Path
	}
	if l, ok := err.(*os.LinkError); ok {
		row["op"], row["old"], row["new"] = l.Op, l.Old, l.New
	}
	if e, ok := err.(syscall.Errno); ok {
		row["errno"] = uint64(e)
	}
	return row
}

func TestStatusbarResourcesSetStagesAndErrors(t *testing.T) {
	cause := errors.New("resources designated fault")
	for _, fault := range []string{"unsupported", "path", "save", "prepare", "apply", "success"} {
		t.Run(fault, func(t *testing.T) {
			root, dest := resourcesTestRoot(t)
			paths := config.Paths{ConfigDir: root}
			if fault == "save" {
				if err := os.Mkdir(paths.LiveResourcesFile(), 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := resourcesInventory(t, root)
			supports, resolves, applies := 0, 0, 0
			result, err := (StatusbarResources{
				Supported: func() bool { supports++; return fault != "unsupported" },
				Paths: func() (config.Paths, error) {
					resolves++
					if fault == "path" {
						return config.Paths{}, cause
					}
					return paths, nil
				},
				Apply: func() (ApplyStage, ApplyStage, error) {
					applies++
					got, err := os.ReadFile(paths.LiveResourcesFile())
					if err != nil || string(got) != "on\n" {
						t.Fatal("prepare before save", string(got), err)
					}
					if fault == "prepare" {
						return ApplyStage{Status: ApplyFailed}, ApplyStage{Status: ApplySkipped}, cause
					}
					if fault == "apply" {
						return ApplyStage{Status: ApplyOK}, ApplyStage{Status: ApplyFailed}, cause
					}
					return ApplyStage{Status: ApplyOK}, ApplyStage{Status: ApplyOK}, nil
				},
			}).Set(" ON ") // The low-level CLI still normalizes case.
			after := resourcesInventory(t, root)
			resourcesRecord(t, filepath.Join(dest, "operation.json"), map[string]any{"before": before, "after": after, "result": result, "error": resourcesError(err), "errorType": fmt.Sprintf("%T", err), "unwrap": fmt.Sprint(errors.Unwrap(err)), "sentinelIs": errors.Is(err, cause), "supports": supports, "paths": resolves, "apply": applies})
			if supports != 1 || result.Mode != "on" {
				t.Fatal(result, supports)
			}
			if fault == "unsupported" {
				if err == nil || err.Error() != "live system resources are unavailable on this platform" || resolves != 0 || applies != 0 || result.Failure != fault || result.Saved || !reflect.DeepEqual(before, after) {
					t.Fatal(result, err, resolves, applies)
				}
				return
			}
			if resolves != 1 {
				t.Fatal(resolves)
			}
			if fault == "path" || fault == "save" {
				comparison := make(map[string]resourcesFile, len(after))
				maps.Copy(comparison, after)
				if fault == "save" {
					// Atomic save creates/removes its temp before rename fails.
					// Only the parent size/mtime may change; retain raw above.
					parent := comparison["."]
					parent.Size, parent.Mtime = before["."].Size, before["."].Mtime
					comparison["."] = parent
				}
				if err == nil || result.Failure != "save" || result.Saved || applies != 0 || result.Prepared.Status != ApplySkipped || result.Live.Status != ApplySkipped || !reflect.DeepEqual(before, comparison) {
					t.Fatal(result, err, applies)
				}
				if fault == "path" && (!errors.Is(err, cause) || err.Error() != cause.Error()) {
					t.Fatal(err)
				}
				if fault == "save" {
					var link *os.LinkError
					if !errors.As(err, &link) || link.Op != "rename" || link.New != paths.LiveResourcesFile() {
						t.Fatal(err)
					}
				}
				return
			}
			if !result.Saved || applies != 1 || after[config.LiveResourcesFileName].Mode.Perm() != 0600 || string(after[config.LiveResourcesFileName].Bytes) != "on\n" {
				t.Fatal(result, applies, after)
			}
			if fault == "success" {
				if err != nil || result.Failure != "" {
					t.Fatal(result, err)
				}
				return
			}
			prefix := "reload active status bar: "
			if fault == "prepare" {
				prefix = "update status bar runtime config: "
			}
			if !errors.Is(err, cause) || err.Error() != prefix+cause.Error() || result.Failure != fault {
				t.Fatal(result, err)
			}
		})
	}
}

func TestStatusbarResourcesCLINormalization(t *testing.T) {
	for _, value := range []string{"on", " ON ", "off", "unknown", ""} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			root, _ := resourcesTestRoot(t)
			got, err := (StatusbarResources{Supported: func() bool { return true }, Paths: func() (config.Paths, error) { return config.Paths{ConfigDir: root}, nil }, Apply: func() (ApplyStage, ApplyStage, error) {
				return ApplyStage{Status: ApplyOK}, ApplyStage{Status: ApplySkipped}, nil
			}}).Set(value)
			want := string(config.NormalizeLiveResourcesMode(value))
			raw, readErr := os.ReadFile(filepath.Join(root, config.LiveResourcesFileName))
			if err != nil || readErr != nil || got.Mode != want || strings.TrimSpace(string(raw)) != want {
				t.Fatal(got, err, readErr, string(raw))
			}
		})
	}
}
