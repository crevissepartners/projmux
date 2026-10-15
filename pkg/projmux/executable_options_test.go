package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestRelaunchInvalidExecutableBeforeStop(t *testing.T) {
	for _, kind := range []string{"empty", "relative", "missing", "directory", "not-executable"} {
		t.Run(kind, func(t *testing.T) {
			c, _, plan, runner := relaunchFailureFixture(t)
			switch kind {
			case "empty":
				c.projmuxExecutable = ""
			case "relative":
				c.projmuxExecutable = "relative"
			case "missing":
				c.projmuxExecutable = filepath.Join(t.TempDir(), "missing")
			case "directory":
				c.projmuxExecutable = t.TempDir()
			case "not-executable":
				c.projmuxExecutable = filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(c.projmuxExecutable, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := store.PathFor(c.paths.StateDir)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.relaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + plan.Target.Metadata.UID, Model: "sonnet", Confirm: true}, runner)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeExecutableInvalid {
				t.Fatalf("want executable refusal, got %v", err)
			}
			if runner.stops != 0 || runner.resumes != 0 || out.Stop.Committed || out.Resume.PaneID != "" {
				t.Fatalf("effects before refusal: stops=%d resumes=%d result=%+v", runner.stops, runner.resumes, out)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatalf("registry changed: %v", err)
			}
		})
	}
}
