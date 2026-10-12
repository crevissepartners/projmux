package projectcandidates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
)

func TestCandidatesOrderRegistrationAndReadOnly(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "typed", true: "legacy"}[legacy], func(t *testing.T) {
			home := t.TempDir()
			repo := filepath.Join(home, "repos")
			extra := filepath.Join(home, "extra")
			candidate := filepath.Join(home, "pinned")
			for _, p := range []string{candidate, filepath.Join(repo, "a"), filepath.Join(repo, "z"), filepath.Join(extra, "b")} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			paths := config.Paths{ConfigDir: filepath.Join(home, "config"), StateDir: filepath.Join(home, "state")}
			reg := metadata.Registry{Projects: []metadata.Project{{Metadata: metadata.ObjectMeta{UID: "proj-sample", Name: "sample"}, Spec: metadata.ProjectSpec{Root: filepath.Join(repo, "z")}}}}
			if err := os.MkdirAll(paths.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if err := os.WriteFile(paths.PinFile(), []byte(candidate+"\n"+filepath.Join(repo, "z")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := pins.NewDefaultStore(paths).Save(pins.Set{Pins: []pins.Pin{{Kind: pins.KindCandidate, Value: candidate}, {Kind: pins.KindProject, Value: "proj-sample"}}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := config.SaveProjdir(home, "old-saved-root"); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveWorkdirs(home, []string{filepath.Join(home, "ignored")}); err != nil {
				t.Fatal(err)
			}
			watched := []string{paths.PinFile(), config.ProjdirFile(home), config.WorkdirsFile(home)}
			before := map[string][]byte{}
			times := map[string]int64{}
			for _, p := range watched {
				b, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				before[p] = b
				i, e := os.Stat(p)
				if e != nil {
					t.Fatal(e)
				}
				times[p] = i.ModTime().UnixNano()
			}
			s := Service{Paths: func() (config.Paths, error) { return paths, nil }, HomeDir: func() (string, error) { return home, nil }, LookupEnv: func(k string) string {
				if k == "PROJMUX_PROJDIR" {
					return repo + string(os.PathListSeparator) + extra
				}
				return ""
			}, LoadRegistry: func() (metadata.Registry, error) { return reg, nil }, TmuxProjdir: func() string { t.Fatal("env primary must win"); return "" }}
			got, err := s.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := Result{Candidates: []Candidate{{Path: candidate, Pinned: true}, {Path: filepath.Join(repo, "z"), Pinned: true, ProjectUID: "proj-sample"}, {Path: filepath.Join(repo, "a")}, {Path: filepath.Join(extra, "b")}}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v want %+v", got, want)
			}
			for _, p := range watched {
				b, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				i, e := os.Stat(p)
				if e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(before[p], b) || times[p] != i.ModTime().UnixNano() {
					t.Fatalf("read changed %s", p)
				}
			}
			if _, e := os.Stat(paths.StateDir); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("state created: %v", e)
			}
		})
	}
}

func TestCandidatesEmptyAndCancellation(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(home, "config"), StateDir: filepath.Join(home, "state")}
	s := Service{Paths: func() (config.Paths, error) { return paths, nil }, HomeDir: func() (string, error) { return home, nil }}
	got, err := s.List(context.Background())
	if err != nil || got.Candidates == nil || len(got.Candidates) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(paths.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config created: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
