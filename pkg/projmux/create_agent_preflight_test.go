package projmux

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// preflightRunner fails the test's count for every tmux command it is asked
// to run; a preflight runs none.
type preflightRunner struct{ calls int }

func (r *preflightRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls++
	return nil, errors.New("preflight ran a tmux command")
}

// preflightClient is one Project named sample with stored instructions
// reviewer and the profiles claude, coder (codex), other (antigravity) and
// reviewing (claude with instructions reviewer). The socket is absent, so
// any tmux use would fail.
func preflightClient(t *testing.T) (*Client, string, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state, config := t.TempDir(), t.TempDir()
	c, err := NewClient(Options{StateDir: state, ConfigDir: config, HomeDir: t.TempDir(), ProjmuxExecutable: executable, SocketPath: filepath.Join(t.TempDir(), "absent")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = c.CreateProject(ctx, core.CreateProjectRequest{Root: t.TempDir(), Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	// A profile's instructions must exist when it is written.
	if _, err = c.WriteInstructions(ctx, "reviewer", []byte("Review carefully.\n")); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"claude": "provider = \"claude\"\n", "coder": "provider = \"codex\"\n", "other": "provider = \"antigravity\"\n", "reviewing": "provider = \"claude\"\ninstructions = \"reviewer\"\n"} {
		if _, err = c.WriteProfile(ctx, name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	return c, state, config
}

// preflightTree is every path below the roots with its mode, mtime and bytes.
func preflightTree(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			entry := info.Mode().String() + " " + info.ModTime().String()
			if info.Mode().IsRegular() {
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				entry += " " + string(raw)
			}
			tree[path] = entry
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return tree
}

// TestPreflightCreateAgentLeavesNoEffects requires a refused and an accepted
// preflight, tmux and process, to leave the state and config trees as they
// were, byte and mtime, to run no tmux command and to add no Window.
func TestPreflightCreateAgentLeavesNoEffects(t *testing.T) {
	c, state, config := preflightClient(t)
	windows := func() int {
		reg, err := store.NewStore(store.PathFor(state)).LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		return len(reg.Windows)
	}
	before, count := preflightTree(t, state, config), windows()
	for _, tc := range []struct {
		name    string
		req     core.CreateAgentRequest
		refused bool
	}{
		{"accepted", core.CreateAgentRequest{Project: "sample", Provider: "claude", Instructions: "reviewer", Profile: "claude"}, false},
		{"accepted-window", core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude"}, false},
		{"accepted-process", core.CreateAgentRequest{Project: "sample", Host: "process", Detach: true, Profile: "reviewing"}, false},
		{"missing-instructions", core.CreateAgentRequest{Project: "sample", Provider: "claude", Instructions: "absent"}, true},
		{"missing-profile", core.CreateAgentRequest{Project: "sample", Provider: "claude", Profile: "absent"}, true},
		{"effort", core.CreateAgentRequest{Project: "sample", Provider: "claude", Effort: "bad"}, true},
		{"process-provider", core.CreateAgentRequest{Project: "sample", Host: "process", Detach: true, Profile: "other"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &preflightRunner{}
			_, err := c.preflightCreateAgent(context.Background(), tc.req, runner)
			if (err != nil) != tc.refused {
				t.Fatalf("refused %v: %v", tc.refused, err)
			}
			if runner.calls != 0 {
				t.Fatalf("ran %d tmux commands", runner.calls)
			}
			after := preflightTree(t, state, config)
			for path, entry := range after {
				if before[path] != entry {
					t.Fatalf("changed %s", path)
				}
			}
			if len(after) != len(before) || windows() != count {
				t.Fatalf("paths %d->%d, Windows %d->%d", len(before), len(after), count, windows())
			}
		})
	}
}

// TestPreflightCreateAgentResolvesProfileProvider requires the provider a
// create will run: the request's, or the profile's when it names none.
func TestPreflightCreateAgentResolvesProfileProvider(t *testing.T) {
	c, _, _ := preflightClient(t)
	for _, tc := range []struct {
		req  core.CreateAgentRequest
		want string
	}{
		{core.CreateAgentRequest{Project: "sample", Provider: "claude"}, "claude"},
		{core.CreateAgentRequest{Project: "sample", Profile: "coder"}, "codex"},
		{core.CreateAgentRequest{Project: "sample", Profile: "reviewing"}, "claude"},
		{core.CreateAgentRequest{Project: "sample", Host: "process", Detach: true, Profile: "coder"}, "codex"},
	} {
		got, err := c.PreflightCreateAgent(context.Background(), tc.req)
		if err != nil || got.Provider != tc.want {
			t.Fatalf("%+v: %+v %v, want %s", tc.req, got, err, tc.want)
		}
	}
	// No provider and no profile is refused as CreateAgent refuses it.
	req := core.CreateAgentRequest{Project: "sample", Window: "extra"}
	_, preErr := c.PreflightCreateAgent(context.Background(), req)
	_, createErr := c.CreateAgent(context.Background(), req)
	if preErr == nil || createErr == nil || preErr.Error() != createErr.Error() {
		t.Fatalf("no provider: preflight %v, CreateAgent %v", preErr, createErr)
	}
}

// TestPreflightCreateAgentProcessShape requires a process request to be
// refused first as CreateAgent refuses its shape, and a provider its profile
// chooses to be one the process creator hosts, refused with the words the
// detached process creator uses.
func TestPreflightCreateAgentProcessShape(t *testing.T) {
	c, _, _ := preflightClient(t)
	process := func(r core.CreateAgentRequest) core.CreateAgentRequest {
		r.Project, r.Window, r.Host = "sample", "uid:win-test", "process"
		return r
	}
	for _, req := range []core.CreateAgentRequest{
		process(core.CreateAgentRequest{Provider: "claude"}),
		process(core.CreateAgentRequest{Detach: true, Provider: "claude", Pane: "uid:pane-test"}),
		process(core.CreateAgentRequest{Detach: true, Provider: "claude", Placement: "down"}),
		process(core.CreateAgentRequest{Detach: true, Provider: "claude", CwdFrom: "pane"}),
		process(core.CreateAgentRequest{Detach: true, Provider: "claude", InteractiveOnly: true}),
		process(core.CreateAgentRequest{Detach: true, Provider: "claude", DialogueReplyOnly: true}),
		process(core.CreateAgentRequest{Detach: true, Provider: "antigravity"}),
		// The shape is refused before the settings.
		process(core.CreateAgentRequest{Provider: "claude", Profile: "absent"}),
	} {
		_, preErr := c.PreflightCreateAgent(context.Background(), req)
		_, createErr := c.CreateAgent(context.Background(), req)
		var pre, created *Refusal
		if !errors.As(preErr, &pre) || !errors.As(createErr, &created) || pre.Code != CodeCreateRequestInvalid || pre.Code != created.Code || preErr.Error() != createErr.Error() {
			t.Fatalf("%+v: preflight %v, CreateAgent %v", req, preErr, createErr)
		}
	}
	_, err := c.PreflightCreateAgent(context.Background(), process(core.CreateAgentRequest{Detach: true, Profile: "other"}))
	var refusal *Refusal
	want := mutationruntime.RequireProcessProvider("antigravity")
	if !errors.As(err, &refusal) || refusal.Code != CodeCreateExecutionFailed || want == nil || err.Error() != want.Error() || !strings.HasPrefix(err.Error(), "process-provider-unsupported:") {
		t.Fatalf("profile provider: %v", err)
	}
	// tmux hosts the same profile.
	if got, err := c.PreflightCreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Profile: "other"}); err != nil || got.Provider != "antigravity" {
		t.Fatalf("tmux profile provider: %+v %v", got, err)
	}
}
