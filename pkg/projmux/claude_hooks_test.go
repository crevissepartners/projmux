package projmux_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func claudeHooksClient(t *testing.T) (*projmux.Client, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{HomeDir: home, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	return c, root, filepath.Join(home, ".claude", "settings.json")
}

func writeClaudeHooksSettings(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type treeEntry struct {
	mode    fs.FileMode
	modTime time.Time
	data    string
}

func snapshotTree(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	out := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := treeEntry{mode: info.Mode(), modTime: info.ModTime()}
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.data = string(b)
		}
		out[path] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func requireClaudeHooksRefusal(t *testing.T, err error, code projmux.Code) {
	t.Helper()
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("error %v, want %s refusal", err, code)
	}
}

// A preview reads the settings file and the hook catalog and leaves the
// filesystem exactly as it was: no settings, directory or temporary file is
// created, and an existing file keeps its bytes, mode and time.
func TestPreviewClaudeHooksWritesNothing(t *testing.T) {
	for _, fixture := range []struct {
		name, content string
		create        bool
	}{
		{"none", "", false},
		{"empty", "", true},
		{"settings", `{"theme":"dark"}` + "\n", true},
		{"invalid", "{", true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			c, root, path := claudeHooksClient(t)
			if fixture.create {
				writeClaudeHooksSettings(t, path, fixture.content)
			}
			before := snapshotTree(t, root)
			for _, remove := range []bool{false, true} {
				plan, err := c.PreviewClaudeHooks(context.Background(), core.ClaudeHooksRequest{Remove: remove})
				if fixture.name == "invalid" {
					requireClaudeHooksRefusal(t, err, projmux.CodeClaudeSettingsUnreadable)
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if plan.Path != path || plan.Exists != fixture.create || plan.Current != fixture.content {
					t.Fatalf("plan %+v", plan)
				}
				wantBase := ""
				if fixture.create {
					sum := sha256.Sum256([]byte(fixture.content))
					wantBase = hex.EncodeToString(sum[:])
				}
				if plan.Base != wantBase {
					t.Fatalf("base %q, want %q", plan.Base, wantBase)
				}
				if !remove && (!plan.Changed || !strings.Contains(plan.Next, "projmux-managed:claude-hook:v1")) {
					t.Fatalf("install plan %+v", plan)
				}
			}
			after := snapshotTree(t, root)
			if len(after) != len(before) {
				t.Fatalf("preview changed the tree: %v -> %v", before, after)
			}
			for p, entry := range before {
				if after[p] != entry {
					t.Fatalf("preview changed %s: %+v -> %+v", p, entry, after[p])
				}
			}
		})
	}
}

// An apply carries out only the plan it was previewed from: a Base the file
// no longer holds is refused and nothing is written.
func TestApplyClaudeHooksRefusesStaleBase(t *testing.T) {
	ctx := context.Background()
	c, _, path := claudeHooksClient(t)
	missing, err := c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{})
	if err != nil || missing.Exists || missing.Base != "" {
		t.Fatalf("%+v %v", missing, err)
	}
	writeClaudeHooksSettings(t, path, `{"theme":"dark"}`+"\n")
	_, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: missing.Base})
	requireClaudeHooksRefusal(t, err, projmux.CodeClaudeSettingsChanged)

	plan, err := c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{})
	if err != nil {
		t.Fatal(err)
	}
	writeClaudeHooksSettings(t, path, `{"theme":"light"}`+"\n")
	_, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: plan.Base})
	requireClaudeHooksRefusal(t, err, projmux.CodeClaudeSettingsChanged)
	if b, _ := os.ReadFile(path); string(b) != `{"theme":"light"}`+"\n" {
		t.Fatalf("stale apply wrote %q", b)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: plan.Base})
	requireClaudeHooksRefusal(t, err, projmux.CodeClaudeSettingsChanged)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale apply created the file: %v", err)
	}

	current, err := c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: current.Base})
	if err != nil || !applied.Changed || applied.Path != path || applied.Action != current.Action {
		t.Fatalf("%+v %v", applied, err)
	}
	if b, _ := os.ReadFile(path); string(b) != current.Next {
		t.Fatalf("applied %q, want the previewed Next", b)
	}
}

// The other two refusals, and an apply with nothing to change, write nothing.
func TestApplyClaudeHooksRefusesConflictAndUnreadable(t *testing.T) {
	ctx := context.Background()
	c, root, path := claudeHooksClient(t)
	conflict := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"exec projmux internal claude-message-wait"}]}]}}` + "\n"
	writeClaudeHooksSettings(t, path, conflict)
	plan, err := c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{})
	if err != nil || plan.Conflict == "" || plan.Changed || plan.Next != conflict {
		t.Fatalf("%+v %v", plan, err)
	}
	before := snapshotTree(t, root)
	_, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: plan.Base})
	requireClaudeHooksRefusal(t, err, projmux.CodeClaudeHooksConflict)
	if !strings.Contains(err.Error(), plan.Conflict) {
		t.Fatalf("conflict refusal %q", err)
	}
	if after := snapshotTree(t, root); after[path] != before[path] {
		t.Fatalf("conflict apply wrote %+v", after[path])
	}

	writeClaudeHooksSettings(t, path, "[]")
	_, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{})
	requireClaudeHooksRefusal(t, err, projmux.CodeClaudeSettingsUnreadable)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	removal, err := c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{Remove: true})
	if err != nil || removal.Changed {
		t.Fatalf("%+v %v", removal, err)
	}
	before = snapshotTree(t, root)
	result, err := c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Remove: true, Base: removal.Base})
	if err != nil || result.Changed || result.Action != removal.Action {
		t.Fatalf("%+v %v", result, err)
	}
	if after := snapshotTree(t, root); len(after) != len(before) {
		t.Fatalf("no-change apply wrote: %v", after)
	}
}

// A removal plan is the command's own: with a managed and an unmanaged
// projmux entry it keeps the cleaned Next and Changed alongside the Conflict,
// which the apply refuses first; with nothing managed to remove from compact
// settings Next is re-encoded but Changed stays false.
func TestPreviewClaudeHooksRemovePlanValues(t *testing.T) {
	ctx := context.Background()
	c, _, path := claudeHooksClient(t)
	mixed := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"projmux ai ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"}]},{"hooks":[{"type":"command","command":"projmux ai ingest claude-hook >/dev/null 2>&1 || true"}]}]}}` + "\n"
	writeClaudeHooksSettings(t, path, mixed)
	plan, err := c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{Remove: true})
	if err != nil || plan.Conflict == "" || !plan.Changed || plan.Next == plan.Current || strings.Contains(plan.Next, "projmux-managed:claude-hook:v1") {
		t.Fatalf("mixed removal plan %+v %v", plan, err)
	}
	_, err = c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Remove: true, Base: plan.Base})
	requireClaudeHooksRefusal(t, err, projmux.CodeClaudeHooksConflict)
	if b, _ := os.ReadFile(path); string(b) != mixed {
		t.Fatalf("refused removal wrote %q", b)
	}

	compact := `{"theme":"dark","hooks":{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"echo keep"}]}]}}`
	writeClaudeHooksSettings(t, path, compact)
	plan, err = c.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{Remove: true})
	if err != nil || plan.Conflict != "" || plan.Changed || plan.Next == plan.Current {
		t.Fatalf("compact removal plan %+v %v", plan, err)
	}
	result, err := c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Remove: true, Base: plan.Base})
	if err != nil || result.Changed {
		t.Fatalf("%+v %v", result, err)
	}
	if b, _ := os.ReadFile(path); string(b) != compact {
		t.Fatalf("no-op removal wrote %q", b)
	}
}
