package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Normalize only minted UIDs (including references and generated names),
// operation IDs and these named clock fields. All other bytes remain comparable.
func normalizeProjectJSON(t *testing.T, raw string, reg metadata.Registry) string {
	t.Helper()
	for i, p := range reg.Projects {
		raw = strings.ReplaceAll(raw, p.Metadata.UID, "PROJECT-"+string(rune('A'+i)))
	}
	for i, w := range reg.Windows {
		raw = strings.ReplaceAll(raw, w.Metadata.UID, "WINDOW-"+string(rune('A'+i)))
	}
	for i, p := range reg.Panes {
		raw = strings.ReplaceAll(raw, p.Metadata.UID, "PANE-"+string(rune('A'+i)))
	}
	// Replace only values of named non-deterministic fields, retaining key order,
	// whitespace, escaping and every deterministic byte from the real encoders.
	clocks := regexp.MustCompile(`("(?:createdAt|updatedAt|lastTransitionAt|observedAt|operationId|operationID)"\s*:\s*)"[^"]*"`)
	return clocks.ReplaceAllString(raw, `${1}"NORMALIZED"`)

}
func projectRegistry(t *testing.T, state string) metadata.Registry {
	t.Helper()
	r, e := store.NewStore(store.PathFor(state)).LoadReadOnly()
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func projectClient(t *testing.T, state string) *projmux.Client {
	t.Helper()
	c, e := projmux.NewClient(projmux.Options{StateDir: state})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func projectCLI(t *testing.T, state string, args ...string) (string, error) {
	t.Helper()
	cmd := newCreateCommand()
	s := store.NewStore(store.PathFor(state))
	cmd.store = &resourceStore{load: s.LoadReadOnly, updateConvergent: s.UpdateConvergent, mutator: store.DefaultMutator}
	var out, errout bytes.Buffer
	err := cmd.Run(append([]string{"project"}, args...), &out, &errout)
	return out.String(), err
}
func TestCreateProjectSDKCLIParity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("TMUX", "/a/poison,99,99")
	t.Setenv("TMUX_PANE", "%999999")
	for _, explicit := range []bool{false, true} {
		root := filepath.Join(home, "roots", map[bool]string{false: "implicit", true: "explicit"}[explicit])
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		req := core.CreateProjectRequest{Root: root, Labels: []string{"team=sdk", "team=last", "empty="}}
		if explicit {
			req.Name = "chosen"
		}
		sdkState := filepath.Join(t.TempDir(), "sdk")
		c := projectClient(t, sdkState)
		cliState := filepath.Join(t.TempDir(), "cli-json")
		receiptState := filepath.Join(t.TempDir(), "cli-receipt")
		args := []string{"--root", root}
		if explicit {
			args = append(args, "--name", req.Name)
		}
		for _, label := range req.Labels {
			args = append(args, "--label", label)
		}
		for attempt := range 2 {
			result, err := c.CreateProject(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Created != (attempt == 0) || result.Reused != (attempt == 1) || len(result.Windows) != 1 || len(result.Panes) != 1 {
				t.Fatalf("result %+v", result)
			}
			out, err := projectCLI(t, cliState, append(args, "-o", "json")...)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := projectCLI(t, receiptState, append(args, "-o", "receipt")...)
			if err != nil {
				t.Fatal(err)
			}
			sr, cr, rr := projectRegistry(t, sdkState), projectRegistry(t, cliState), projectRegistry(t, receiptState)
			if a, b := normalizeProjectJSON(t, sdkJSON(t, result.Projection), sr), normalizeProjectJSON(t, out, cr); a != b {
				t.Fatalf("projection differs\n%s\n%s", a, b)
			}
			if a, b := normalizeProjectJSON(t, sdkJSON(t, result.Receipt), sr), normalizeProjectJSON(t, receipt, rr); a != b {
				t.Fatalf("receipt differs\n%s\n%s", a, b)
			}
			if a, b := normalizeProjectJSON(t, projectRegistryBytes(t, sdkState), sr), normalizeProjectJSON(t, projectRegistryBytes(t, cliState), cr); a != b {
				t.Fatalf("registry differs\n%s\n%s", a, b)
			}
			// Each caller independently reuses with byte and mtime preservation.
			for _, state := range []string{sdkState, cliState, receiptState} {
				p := store.PathFor(state)
				before, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				info, e := os.Stat(p)
				if e != nil {
					t.Fatal(e)
				}
				if state == sdkState {
					_, e = c.CreateProject(context.Background(), req)
				} else {
					_, e = projectCLI(t, state, append(args, "-o", "receipt")...)
				}
				if e != nil {
					t.Fatal(e)
				}
				after, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				next, e := os.Stat(p)
				if e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(before, after) || !info.ModTime().Equal(next.ModTime()) {
					t.Fatal("reuse wrote registry")
				}
			}
		}
	}
}
func TestCreateProjectSDKRefusalParity(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	c := projectClient(t, state)
	_, err := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		req   core.CreateProjectRequest
		code  projmux.Code
		field string
		args  []string
		usage bool
	}{
		{core.CreateProjectRequest{}, projmux.CodeProjectRootInvalid, "Root", nil, true},
		{core.CreateProjectRequest{Root: "relative"}, projmux.CodeProjectRootInvalid, "Root", []string{"--root", "relative"}, true},
		{core.CreateProjectRequest{Root: filepath.Join(root, "missing")}, projmux.CodeProjectRootInvalid, "Root", []string{"--root", filepath.Join(root, "missing")}, true},
		{core.CreateProjectRequest{Root: other, Name: "bad name"}, projmux.CodeResourceNameInvalid, "Name", []string{"--root", other, "--name", "bad name"}, true},
		{core.CreateProjectRequest{Root: other, Name: "existing"}, projmux.CodeResourceNameConflict, "Name", []string{"--root", other, "--name", "existing"}, true},
		{core.CreateProjectRequest{Root: root, Name: "different"}, projmux.CodeResourceNameConflict, "Name", []string{"--root", root, "--name", "different"}, true},
		{core.CreateProjectRequest{Root: root, Labels: []string{"bad"}}, projmux.CodeResourceLabelInvalid, "Labels", []string{"--root", root, "--label", "bad"}, true},
		{core.CreateProjectRequest{Root: root, Labels: []string{"=x"}}, projmux.CodeResourceLabelInvalid, "Labels", []string{"--root", root, "--label", "=x"}, true},
	} {
		before, _ := os.ReadFile(store.PathFor(state))
		_, se := c.CreateProject(context.Background(), tc.req)
		_, ce := projectCLI(t, state, tc.args...)
		var refusal *projmux.Refusal
		if !errors.As(se, &refusal) || refusal.Code != tc.code {
			t.Fatalf("SDK: %T %v want %s", se, se, tc.code)
		}
		var usage *UsageError
		if ce == nil || errors.As(ce, &usage) != tc.usage {
			t.Fatalf("CLI failure %T %v", ce, ce)
		}
		if (tc.field == "Name" && se.Error() != ce.Error()) || (tc.field != "Name" && (!strings.Contains(se.Error(), tc.field) || strings.Contains(se.Error(), "--"))) {
			t.Fatalf("SDK syntax %v", se)
		}
		after, _ := os.ReadFile(store.PathFor(state))
		if !bytes.Equal(before, after) {
			t.Fatal("refusal wrote registry")
		}
	}
	// Broken Registry is runtime failure, never input failure.
	if err := os.WriteFile(store.PathFor(state), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, se := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: other})
	_, ce := projectCLI(t, state, "--root", other)
	var refusal *projmux.Refusal
	var usage *UsageError
	if !errors.As(se, &refusal) || refusal.Code != projmux.CodeRegistryUnavailable || ce == nil || errors.As(ce, &usage) {
		t.Fatalf("registry errors %v / %v", se, ce)
	}
}
func TestCreateProjectSDKCancellationAndIsolation(t *testing.T) {
	stateA := filepath.Join(t.TempDir(), "a")
	stateB := filepath.Join(t.TempDir(), "b")
	a, b := projectClient(t, stateA), projectClient(t, stateB)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	if _, err := a.CreateProject(ctx, core.CreateProjectRequest{Root: root}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled call touched state: %v", err)
	}
	before := os.Environ()
	for _, c := range []*projmux.Client{a, b} {
		if _, err := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(before, "\n") != strings.Join(os.Environ(), "\n") {
		t.Fatal("SDK changed environment")
	}
	if projectRegistry(t, stateA).Projects[0].Metadata.UID == projectRegistry(t, stateB).Projects[0].Metadata.UID {
		t.Fatal("clients shared registry")
	}
}

func projectRegistryBytes(t *testing.T, state string) string {
	t.Helper()
	b, e := os.ReadFile(store.PathFor(state))
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestCreateProjectSDKCapturesEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	state := filepath.Join(t.TempDir(), "state")
	c := projectClient(t, state)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/changed-after-construction")
	result, err := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: home})
	if err != nil || result.Project.Status.Session == nil || result.Project.Status.Session.Name != "home" {
		t.Fatalf("captured HOME: %+v %v", result, err)
	}
}
