package agentguidance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func projectEditCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *EditError
	if !errors.As(err, &e) || e.Code != "agent-guidance-"+want {
		t.Fatalf("want %s, got %v", want, err)
	}
}
func TestProjectEditingRawEffectiveAndRecovery(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		present bool
		content []byte
	}{
		{"absent", false, nil}, {"empty", true, []byte{}}, {"whitespace", true, []byte(" \t\r\n")}, {"NUL", true, []byte("old\x00text")}, {"UTF8", true, []byte("한글\r\n")}, {"invalidUTF8", true, []byte{0xff}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := NewProjectStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
			if tc.present {
				if err := s.Save(projectTestUID, tc.content); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := s.ReadFile(ctx, projectTestUID)
			if tc.name == "invalidUTF8" {
				projectEditCode(t, err, "unreadable")
			} else {
				effective, e := s.Load(projectTestUID)
				if e != nil || err != nil || raw.Present != tc.present || raw.Text != string(tc.content) || raw.Bytes != len(tc.content) || raw.Digest != Digest(tc.content) || raw.Source != string(effective.Source) || raw.EffectiveText != string(effective.Text) || raw.EffectiveDigest != effective.Digest {
					t.Fatalf("raw %+v load %+v errors %v %v", raw, effective, err, e)
				}
			}
			v, err := s.ValidateFile(ctx, projectTestUID, []byte("valid"))
			if err != nil {
				t.Fatal(err)
			}
			saved, err := s.WriteFile(ctx, projectTestUID, []byte(v.Text), v.Revision)
			if err != nil || saved.Text != "valid" || !saved.Present || saved.Source != string(SourceFile) {
				t.Fatalf("save %+v %v", saved, err)
			}
			removed, err := s.DeleteFile(ctx, projectTestUID, saved.Revision)
			if err != nil || removed.Present || removed.Source != string(SourceOff) {
				t.Fatalf("delete %+v %v", removed, err)
			}
			if _, err = s.DeleteFile(ctx, projectTestUID, removed.Revision); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestProjectEditingRevisionAndPreservation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := filepath.Join(root, "config")
	s := NewProjectStore(config, filepath.Join(root, "state"))
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	sentinels := map[string]string{FileName: "global", SettingsFileName: `{"disabled":true}`, filepath.Join(FragmentsDirName, "kept.md"): "fragment"}
	for name, b := range sentinels {
		p := filepath.Join(config, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.ReadFile(ctx, projectTestUID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.ReadFile(ctx, "proj-aaaaaaaaaaaaaaaaaaaaaaaaaq")
	if err != nil || a.Revision == other.Revision {
		t.Fatal("UID not bound", err)
	}
	if err = s.Save(projectTestUID, nil); err != nil {
		t.Fatal(err)
	}
	empty, err := s.ReadFile(ctx, projectTestUID)
	if err != nil || a.Revision == empty.Revision {
		t.Fatal("presence not bound", err)
	}
	_, err = s.WriteFile(ctx, projectTestUID, []byte("new"), a.Revision)
	projectEditCode(t, err, "stale")
	_, err = s.DeleteFile(ctx, projectTestUID, a.Revision)
	projectEditCode(t, err, "stale")
	for _, b := range [][]byte{[]byte("bad\x00"), {0xff}, bytes.Repeat([]byte("x"), MaxSize+1)} {
		code := "invalid"
		if len(b) > MaxSize {
			code = "too-large"
		}
		_, err = s.ValidateFile(ctx, projectTestUID, b)
		projectEditCode(t, err, code)
		_, err = s.WriteFile(ctx, projectTestUID, b, empty.Revision)
		projectEditCode(t, err, code)
	}
	if err = s.Save(projectTestUID, []byte("old")); err != nil {
		t.Fatal(err)
	}
	path, _ := s.Path(projectTestUID)
	if err = os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	current, err := s.ReadFile(ctx, projectTestUID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.WriteFile(ctx, projectTestUID, bytes.Repeat([]byte("x"), MaxSize), current.Revision)
	if err != nil || saved.Bytes != MaxSize {
		t.Fatal("limit", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("mode", err)
	}
	for name, want := range sentinels {
		got, err := os.ReadFile(filepath.Join(config, name))
		if err != nil || string(got) != want {
			t.Fatalf("sentinel %s changed %v", name, err)
		}
	}
}
func TestProjectEditingRejectsEscapesAndNonRegular(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			s := NewProjectStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
			p, _ := s.Path(projectTestUID)
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(root, "outside")
				if err := os.WriteFile(outside, []byte("preserved"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, p); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if _, err := s.ReadFile(ctx, projectTestUID); err == nil {
				t.Fatal("read accepted")
			}
			if _, err := s.ValidateFile(ctx, projectTestUID, []byte("candidate")); err == nil {
				t.Fatal("validate accepted")
			}
			if _, err := s.WriteFile(ctx, projectTestUID, []byte("candidate"), "revision"); err == nil {
				t.Fatal("write accepted")
			}
			if _, err := s.DeleteFile(ctx, projectTestUID, "revision"); err == nil {
				t.Fatal("delete accepted")
			}
		})
	}
}

func TestProjectEditingAtomicSaveFailurePreservesInputs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure fixture requires an unprivileged user")
	}
	root := t.TempDir()
	s := NewProjectStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
	ctx := context.Background()
	if err := s.Save(projectTestUID, []byte("original")); err != nil {
		t.Fatal(err)
	}
	actual := s.configDir
	link := filepath.Join(filepath.Dir(actual), "linked-project-guidance")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	s.configDir = link
	path, _ := s.Path(projectTestUID)
	parent := actual
	current, err := s.ReadFile(ctx, projectTestUID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	_, err = s.WriteFile(ctx, projectTestUID, []byte("candidate"), current.Revision)
	projectEditCode(t, err, "unreadable")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("atomic failure changed original %q %v", got, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("atomic failure left partial file %v %v", entries, err)
	}
}

// Existing oversize files must yield a revision usable for public recovery.
func TestProjectEditingOversizeRecovery(t *testing.T) {
	ctx := context.Background()
	for _, size := range []int{MaxSize + 1, MaxSize * 16} {
		for _, action := range []string{"write", "delete"} {
			t.Run(fmt.Sprintf("%d/%s", size, action), func(t *testing.T) {
				root := t.TempDir()
				s := NewProjectStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
				path, _ := s.Path(projectTestUID)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0640); err != nil {
					t.Fatal(err)
				}
				current, err := s.ReadFile(ctx, projectTestUID)
				if err != nil {
					t.Fatalf("oversize metadata read must permit recovery: %v", err)
				}
				if !current.Oversize || current.PrefixBytes != MaxSize+1 || current.PrefixDigest != Digest(bytes.Repeat([]byte("x"), MaxSize+1)) {
					t.Fatalf("prefix: %+v", current)
				}
				if !current.Present || current.Bytes != size || current.Revision == "" || current.Text != "" || current.Digest != "" || current.Source != "" || current.EffectiveText != "" || current.EffectiveDigest != "" {
					t.Fatalf("metadata: %+v", current)
				}
				validated, err := s.ValidateFile(ctx, projectTestUID, nil)
				if err != nil || validated.Revision != current.Revision {
					t.Fatalf("validate: %+v %v", validated, err)
				}
				var out ProjectFile
				if action == "write" {
					out, err = s.WriteFile(ctx, projectTestUID, []byte("recovered"), current.Revision)
				} else {
					out, err = s.DeleteFile(ctx, projectTestUID, current.Revision)
				}
				if err != nil {
					t.Fatal(err)
				}
				after, err := s.ReadFile(ctx, projectTestUID)
				if out.Oversize || out.PrefixDigest != "" || out.PrefixBytes != 0 {
					t.Fatalf("recovery retained prefix: %+v", out)
				}
				if err != nil || out != after || out.Revision == current.Revision {
					t.Fatalf("result %+v read %+v %v", out, after, err)
				}
				b, err := os.ReadFile(path)
				if action == "write" {
					if err != nil || string(b) != "recovered" {
						t.Fatalf("stored %q %v", b, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("delete: %v", err)
				}
				_, err = s.DeleteFile(ctx, projectTestUID, current.Revision)
				projectEditCode(t, err, "stale")
			})
		}
	}
}

func TestProjectEditingOversizeStaleAndCandidateRefusals(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"prefix", "tail", "truncate", "replace", "wrong-revision", "wrong-uid"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			s := NewProjectStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
			path, _ := s.Path(projectTestUID)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			original := bytes.Repeat([]byte("x"), MaxSize*4)
			if err := os.WriteFile(path, original, 0640); err != nil {
				t.Fatal(err)
			}
			before, err := s.ReadFile(ctx, projectTestUID)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range [][]byte{bytes.Repeat([]byte("x"), MaxSize+1), {0xff}, []byte("bad\x00")} {
				code := "invalid"
				if len(candidate) > MaxSize {
					code = "too-large"
				}
				_, err = s.ValidateFile(ctx, projectTestUID, candidate)
				projectEditCode(t, err, code)
				_, err = s.WriteFile(ctx, projectTestUID, candidate, before.Revision)
				projectEditCode(t, err, code)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			revision := before.Revision
			target := projectTestUID
			switch change {
			case "prefix", "tail":
				f, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				offset := int64(0)
				if change == "tail" {
					offset = MaxSize + 2
				}
				_, err = f.WriteAt([]byte("y"), offset)
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("edit %v %v", err, closeErr)
				}
				// Explicit mtime advance avoids filesystem clock-resolution assumptions.
				if err := os.Chtimes(path, info.ModTime(), info.ModTime().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			case "truncate":
				if err := os.Truncate(path, MaxSize*2); err != nil {
					t.Fatal(err)
				}
			case "replace":
				next := path + ".next"
				if err := os.WriteFile(next, original, 0640); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(next, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(next, path); err != nil {
					t.Fatal(err)
				}
			case "wrong-revision":
				revision = "wrong"
			case "wrong-uid":
				target = "proj-aaaaaaaaaaaaaaaaaaaaaaaaaq"
			}
			preserved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.WriteFile(ctx, target, []byte("candidate"), revision)
			projectEditCode(t, err, "stale")
			_, err = s.DeleteFile(ctx, target, revision)
			projectEditCode(t, err, "stale")
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(preserved, after) {
				t.Fatal("refusal changed file", err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatal("refusal left residue", err)
			}
		})
	}
}

type projectCountingReader struct{ read int }

func (r *projectCountingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0xff
	}
	r.read += len(p)
	return len(p), nil
}
func TestProjectEditingReadBudget(t *testing.T) {
	r := &projectCountingReader{}
	prefix, err := readProjectPrefix(r)
	if err != nil || len(prefix) != MaxSize+1 || r.read != MaxSize+1 {
		t.Fatalf("read %d bytes, returned %d: %v", r.read, len(prefix), err)
	}
}

func TestProjectEditingNormalRevisionAndGlobalParity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := filepath.Join(root, "config")
	s := NewProjectStore(config, filepath.Join(root, "state"))
	for _, content := range [][]byte{nil, {}, []byte("exact\r\n"), bytes.Repeat([]byte("x"), MaxSize)} {
		if err := s.Save(projectTestUID, content); err != nil {
			t.Fatal(err)
		}
		got, err := s.ReadFile(ctx, projectTestUID)
		legacy, _ := json.Marshal(struct {
			UID     string
			Present bool
			Content []byte
		}{projectTestUID, true, append([]byte{}, content...)})
		if err != nil || got.Revision != Digest(legacy) || got.Oversize || got.PrefixBytes != 0 || got.PrefixDigest != "" {
			t.Fatalf("normal revision %+v %v", got, err)
		}
	}
	// Global user/settings/fragment oversize refusal remains unchanged.
	for _, name := range []string{FileName, SettingsFileName, filepath.Join(FragmentsDirName, "part.md")} {
		t.Run(name, func(t *testing.T) {
			globalConfig := filepath.Join(t.TempDir(), "config")
			p := filepath.Join(globalConfig, name)
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, bytes.Repeat([]byte("x"), MaxSize+1), 0600); err != nil {
				t.Fatal(err)
			}
			global := Store{configDir: globalConfig}
			_, err := global.ReadFile(ctx)
			projectEditCode(t, err, "too-large")
			_, err = global.ValidateFile(ctx, nil)
			projectEditCode(t, err, "too-large")
			_, err = global.WriteFile(ctx, []byte("new"), "revision")
			projectEditCode(t, err, "too-large")
			_, err = global.ResetFile(ctx, "revision")
			projectEditCode(t, err, "too-large")
		})
	}
}
