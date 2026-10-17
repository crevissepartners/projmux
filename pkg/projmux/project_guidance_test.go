package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	guidance "github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/state"
)

func projectGuidanceClient(t *testing.T) (*Client, config.Paths, string) {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
	var uid string
	_, err := storemetadata.NewDefaultStore(paths).Update(func(reg *metadata.Registry) error {
		result, err := (metadata.Mutator{DirExists: func(string) (bool, error) { return true, nil }}).RegisterProject(reg, metadata.RegisterProjectOptions{Root: root, Name: "sample"})
		uid = result.Project.Metadata.UID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	return client, paths, uid
}
func TestProjectGuidanceSDKCancellationWhileWaiting(t *testing.T) {
	for _, method := range []string{"write", "delete"} {
		for _, kind := range []string{"cancel", "deadline"} {
			t.Run(method+"/"+kind, func(t *testing.T) {
				client, paths, uid := projectGuidanceClient(t)
				ctx := context.Background()
				store := guidance.NewDefaultProjectStore(paths)
				if err := store.Save(uid, []byte("original")); err != nil {
					t.Fatal(err)
				}
				current, err := client.ReadProjectGuidance(ctx, "sample")
				if err != nil {
					t.Fatal(err)
				}
				held, err := state.AcquireFileLock(filepath.Join(paths.ConfigDir, ".agent-guidance.lock"), time.Second, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Close()
				before := guidanceSDKTree(t, filepath.Dir(paths.ConfigDir))
				base, cancel := context.WithCancel(ctx)
				want := context.Canceled
				if kind == "deadline" {
					cancel()
					base, cancel = context.WithTimeout(ctx, 2*time.Second)
					want = context.DeadlineExceeded
				}
				defer cancel()
				waiting := &guidanceWaitingContext{Context: base, entered: make(chan struct{}), waiting: make(chan struct{})}
				call := func(ctx context.Context) error {
					if method == "write" {
						_, err := client.WriteProjectGuidance(ctx, "sample", []byte("replacement"), current.Revision)
						return err
					}
					_, err := client.DeleteProjectGuidance(ctx, "sample", current.Revision)
					return err
				}
				done := make(chan error, 1)
				go func() { done <- call(waiting) }()
				select {
				case <-waiting.waiting:
				case <-time.After(time.Second):
					t.Fatal("SDK did not reach contended lock")
				}
				if kind == "cancel" {
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("cause %v want %v", err, want)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("SDK failed to return while lock remained held")
				}
				if after := guidanceSDKTree(t, filepath.Dir(paths.ConfigDir)); !reflect.DeepEqual(before, after) {
					t.Fatal("canceled request changed tree")
				}
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
				next, stop := context.WithTimeout(ctx, time.Second)
				defer stop()
				if err := call(next); err != nil {
					t.Fatal("normal call after cancellation", err)
				}
				path, _ := store.Path(uid)
				b, err := os.ReadFile(path)
				if method == "delete" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatal("not deleted", err)
					}
				} else if err != nil || string(b) != "replacement" {
					t.Fatal("not written", err)
				}
			})
		}
	}
}
