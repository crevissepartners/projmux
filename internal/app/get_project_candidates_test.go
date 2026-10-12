package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/pincmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	service "github.com/crevissepartners/projmux/internal/services/projectcandidates"
)

// The independent projection retains the original discovery composition to
// compare configured order, registration and pin state against the CLI service.
func TestProjectCandidatesPickerProjectionParity(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repos")
	a := filepath.Join(repo, "a")
	z := filepath.Join(repo, "z")
	pinned := filepath.Join(home, "pinned")
	for _, p := range []string{a, z, pinned} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	paths := config.Paths{ConfigDir: filepath.Join(home, "config"), StateDir: filepath.Join(home, "state")}
	if err := os.MkdirAll(paths.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.PinFile(), []byte(z+"\n"+pinned+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg := metadata.Registry{Projects: []metadata.Project{{Metadata: metadata.ObjectMeta{UID: "proj-sample"}, Spec: metadata.ProjectSpec{Root: z}}}}
	lookup := func(k string) string {
		if k == "PROJMUX_PROJDIR" {
			return repo
		}
		return ""
	}
	switcher := newSwitchCommand()
	switcher.homeDir = func() (string, error) { return home, nil }
	switcher.lookupEnv = lookup
	switcher.pinStore = func() (switchPinStore, error) { return pins.NewDefaultStore(paths), nil }
	switcher.pinProjects = func() ([]pins.ProjectRef, error) { return pincmd.ProjectRefsOf(reg), nil }
	switcher.saveProjdir = func(string, string) error { t.Fatal("saved discovery memory"); return nil }
	inputs, err := switcher.projectDiscoveryInputs(false)
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := candidates.DiscoverProjectRoots(inputs)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := switcher.loadPinSelection()
	if err != nil {
		t.Fatal(err)
	}
	want := service.Result{Candidates: make([]service.Candidate, 0, len(discovered))}
	for _, path := range discovered {
		c := service.Candidate{Path: path, Pinned: selected.PinnedPath(path)}
		if p, ok := reg.ProjectByRoot(cleanOptionalPath(path)); ok {
			c.ProjectUID = p.Metadata.UID
		}
		want.Candidates = append(want.Candidates, c)
	}
	s := service.Service{Paths: func() (config.Paths, error) { return paths, nil }, HomeDir: switcher.homeDir, LookupEnv: lookup, LoadRegistry: func() (metadata.Registry, error) { return reg, nil }}
	got, err := s.List(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v; want %+v", got, err, want)
	}
	cmd := &getCommand{projectCandidates: &s}
	out, stderr, err := runGet(t, cmd, "project-candidates", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("%s %v", stderr, err)
	}
	var result service.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil || !reflect.DeepEqual(result, want) {
		t.Fatalf("%s %v", out, err)
	}
	alias, _, err := runGet(t, cmd, "project-candidate", "-o", "json")
	if err != nil || alias != out {
		t.Fatalf("alias %s %v", alias, err)
	}
	for _, args := range [][]string{{"project-candidates", "x"}, {"project-candidates", "-o", "wide"}, {"project-candidates", "--", "-o", "json"}, {"project-candidates", "--unknown"}} {
		out, _, err := runGet(t, cmd, args...)
		if err == nil || out != "" {
			t.Fatalf("%v %q %v", args, out, err)
		}
	}
}
