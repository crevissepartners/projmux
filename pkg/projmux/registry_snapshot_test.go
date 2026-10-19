package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func snapshotRaw(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("../../internal/services/resources/testdata/registry_snapshot.json")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func snapshotClient(t *testing.T, raw []byte) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	path := store.PathFor(state)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := NewClient(Options{ConfigDir: filepath.Join(root, "config"), StateDir: state, SocketPath: filepath.Join(root, "absent-socket")})
	if err != nil {
		t.Fatal(err)
	}
	return c, root
}

type snapshotFile struct {
	Bytes string
	Mode  fs.FileMode
	Mtime time.Time
}

func snapshotTree(t *testing.T, root string) map[string]snapshotFile {
	t.Helper()
	out := map[string]snapshotFile{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		var b []byte
		if !d.IsDir() {
			b, e = os.ReadFile(path)
			if e != nil {
				return e
			}
		}
		out[path] = snapshotFile{string(b), info.Mode(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The reference loop matches the old private display algorithm: first Project
// and Window matches, followed by the Agent rows themselves (never a UID map).
func snapshotOldUsers(r metadata.Registry, project string) ([]string, bool) {
	out := []string{}
	if project == "" {
		return out, true
	}
	if _, ok := r.Project(project); !ok {
		return out, false
	}
	for _, a := range r.Agents {
		owner := ""
		if a.Metadata.OwnerRef != nil {
			if a.Metadata.OwnerRef.Kind == metadata.KindProject {
				owner = a.Metadata.OwnerUID()
			} else if w, ok := r.Window(a.Metadata.OwnerUID()); ok {
				owner = w.Metadata.OwnerUID()
			}
		}
		if a.Metadata.Annotations["projmux.io/profile"] == "shared" && owner != project {
			out = append(out, a.Metadata.Name)
		}
	}
	return out, true
}
func snapshotPublicUsers(r core.RegistrySnapshot, project string) ([]string, bool) {
	out := []string{}
	if project == "" {
		return out, true
	}
	found := false
	for _, p := range r.Projects {
		if p.Metadata.UID == project {
			found = true
			break
		}
	}
	if !found {
		return out, false
	}
	for _, a := range r.Agents {
		owner := ""
		if a.Metadata.OwnerRef != nil {
			if a.Metadata.OwnerRef.Kind == core.KindProject {
				owner = a.Metadata.OwnerRef.UID
			} else {
				for _, w := range r.Windows {
					if w.Metadata.UID == a.Metadata.OwnerRef.UID {
						if w.Metadata.OwnerRef != nil {
							owner = w.Metadata.OwnerRef.UID
						}
						break
					}
				}
			}
		}
		if a.Metadata.Annotations["projmux.io/profile"] == "shared" && owner != project {
			out = append(out, a.Metadata.Name)
		}
	}
	return out, true
}
func TestRegistrySnapshotRowsParityAndOwnership(t *testing.T) {
	c, _ := snapshotClient(t, snapshotRaw(t))
	r, e := resources.Load(c.paths)
	if e != nil {
		t.Fatal(e)
	}
	got, e := registrySnapshotRows(r)
	if e != nil {
		t.Fatal(e)
	}
	for _, pair := range []struct{ a, b any }{{r.Projects, got.Projects}, {r.Windows, got.Windows}, {r.Agents, got.Agents}} {
		a, _ := json.Marshal(pair.a)
		b, _ := json.Marshal(pair.b)
		if string(a) != string(b) {
			t.Fatalf("stored JSON drift:\n%s\n%s", a, b)
		}
	}
	for _, project := range []string{"", "proj-a", "proj-b", "missing"} {
		a, ok := snapshotOldUsers(r, project)
		b, bok := snapshotPublicUsers(got, project)
		if ok != bok || !reflect.DeepEqual(a, b) {
			t.Fatalf("users %s: %v/%v vs %v/%v", project, a, ok, b, bok)
		}
	}
	if users, _ := snapshotPublicUsers(got, "proj-a"); !reflect.DeepEqual(users, []string{"direct-b", "orphan"}) {
		t.Fatal(users)
	}
	got.Projects[0].Metadata.Labels["nested"] = "changed"
	got.Projects[0].Status.Session.Name = "changed"
	got.Projects[0].Status.Conditions[0].Type = "changed"
	got.Windows[0].Metadata.OwnerRef.UID = "changed"
	got.Windows[0].Status.Conditions[0].Type = "changed"
	got.Agents[0].Metadata.Annotations["projmux.io/profile"] = "changed"
	got.Agents[0].Metadata.OwnerRef.UID = "changed"
	got.Agents[0].Status.Conditions[0].Type = "changed"
	got.Agents[0].Status.SessionRef.Codex.Endpoint.StateDomainID = "changed"
	fresh, e := registrySnapshotRows(r)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(r.Agents)
	b, _ := json.Marshal(fresh.Agents)
	if string(a) != string(b) {
		t.Fatal("source aliases returned data")
	}
	if r.Projects[0].Metadata.Labels["nested"] != "original" || r.Projects[0].Status.Session.Name != "first-a" || r.Projects[0].Status.Conditions[0].Type != "fixture" || r.Windows[0].Metadata.OwnerUID() != "proj-a" || r.Windows[0].Status.Conditions[0].Type != "fixture" {
		t.Fatal("project/window nested aliases")
	}
	// Projection failures remain ordinary errors, rather than Registry refusals.
	r.Projects[0].Metadata.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := registrySnapshotRows(r); err == nil {
		t.Fatal("invalid JSON time accepted")
	}
}
func TestReadRegistrySnapshotEffectFreeAndEmpty(t *testing.T) {
	// Poison executable discovery. Any provider/tmux process would fail and leave
	// a marker; the transport is nil so accidental Observe cannot succeed.
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	for _, name := range []string{"tmux", "codex", "claude", "projmux"} {
		if e := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf called > '"+marker+"'\nexit 91\n"), 0755); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", bin)
	for _, raw := range [][]byte{nil, {}, []byte(`{"apiVersion":"projmux.io/v1alpha1","schemaVersion":5}`), snapshotRaw(t)} {
		c, root := snapshotClient(t, raw)
		c.transport = nil
		before := snapshotTree(t, root)
		got, err := c.ReadRegistrySnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.Projects == nil || got.Windows == nil || got.Agents == nil {
			t.Fatal("nil success slice")
		}
		if !reflect.DeepEqual(before, snapshotTree(t, root)) {
			t.Fatal("read changed bytes/mode/mtime/entries")
		}
		if raw == nil {
			b, _ := json.Marshal(got)
			if string(b) != `{"projects":[],"windows":[],"agents":[]}` {
				t.Fatal(string(b))
			}
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external command invoked: %v", err)
	}
}

// Err changes only at the SDK's two cancellation checkpoints, letting the
// test exercise cancellation after loading without a timing race or I/O hook.
type snapshotCancelContext struct {
	context.Context
	calls int
}

func (c *snapshotCancelContext) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}
func TestReadRegistrySnapshotErrorOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       []byte
		marker    bool
		directory bool
		want      string
	}{
		{name: "first-use", want: ""},
		{name: "initialized-missing", marker: true, want: "missing"},
		{name: "initialized-empty", raw: []byte{}, marker: true, want: "empty"},
		{name: "malformed", raw: []byte("{"), want: "malformed"},
		{name: "unsupported", raw: []byte(`{"schemaVersion":999}`), want: "schema"},
		{name: "read-directory", directory: true, want: "read registry"},
		{name: "invalid-graph", raw: snapshotRaw(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, root := snapshotClient(t, tc.raw)
			path := store.PathFor(c.paths.StateDir)
			if tc.marker {
				if e := os.WriteFile(filepath.Join(filepath.Dir(path), "registry.initialized"), []byte("marker"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			if tc.directory {
				if e := os.Mkdir(path, 0755); e != nil {
					t.Fatal(e)
				}
			}
			before := snapshotTree(t, root)
			_, oldErr := resources.Load(c.paths)
			_, err := c.ReadRegistrySnapshot(context.Background())
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Code != CodeRegistryUnavailable || oldErr == nil || err.Error() != oldErr.Error() || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
					t.Fatalf("error parity: %v / %v", err, oldErr)
				}
			}
			ctx := &snapshotCancelContext{Context: context.Background()}
			_, cancelErr := c.ReadRegistrySnapshot(ctx)
			if oldErr != nil {
				if cancelErr.Error() != oldErr.Error() || ctx.calls != 1 {
					t.Fatalf("loader lost precedence: %v calls %d", cancelErr, ctx.calls)
				}
			} else if !errors.Is(cancelErr, context.Canceled) || ctx.calls != 2 {
				t.Fatalf("return cancellation: %v calls %d", cancelErr, ctx.calls)
			}
			if !reflect.DeepEqual(before, snapshotTree(t, root)) {
				t.Fatal("error path changed filesystem")
			}
		})
	}
	c, _ := snapshotClient(t, nil)
	sentinel := errors.New("captured paths")
	c.pathsErr = sentinel
	if _, e := c.ReadRegistrySnapshot(context.Background()); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.ReadRegistrySnapshot(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	c.paths = config.Paths{StateDir: "\x00"}
	c.pathsErr = nil
	if _, e := c.ReadRegistrySnapshot(context.Background()); e == nil {
		t.Fatal("invalid path accepted")
	}
}
