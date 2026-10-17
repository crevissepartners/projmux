package projmux

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	guidance "github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/state"
)

// Done is consulted by the contended lock select. Observing it lets the test
// cancel an actual waiter without guessing how long the SDK needs to start.
type guidanceWaitingContext struct {
	context.Context
	entered, waiting    chan struct{}
	entryOnce, waitOnce sync.Once
}

func (c *guidanceWaitingContext) Err() error {
	err := c.Context.Err()
	c.entryOnce.Do(func() { close(c.entered) })
	return err
}

func (c *guidanceWaitingContext) Done() <-chan struct{} {
	c.waitOnce.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func guidanceSDKTree(t *testing.T, root string) map[string]struct {
	Mode fs.FileMode
	Text string
} {
	t.Helper()
	out := map[string]struct {
		Mode fs.FileMode
		Text string
	}{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var text []byte
		if !entry.IsDir() {
			text, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		name, err := filepath.Rel(root, path)
		out[name] = struct {
			Mode fs.FileMode
			Text string
		}{info.Mode(), string(text)}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAgentGuidanceSDKCancellationWhileWaiting(t *testing.T) {
	legacy, err := os.ReadFile("../../internal/core/agentguidance/legacy/c4fc721e.md")
	if err != nil {
		t.Fatal(err)
	}
	user := []byte("\n# User decisions\r\nKeep exact bytes.\n")
	original := append(bytes.Clone(legacy), user...)
	for _, method := range []string{"inspect", "set-enabled"} {
		for _, kind := range []string{"cancel", "deadline"} {
			t.Run(method+"/"+kind, func(t *testing.T) {
				root := t.TempDir()
				configDir, stateDir := filepath.Join(root, "config"), filepath.Join(root, "state")
				for _, dir := range []string{configDir, stateDir} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				for path, content := range map[string][]byte{
					filepath.Join(configDir, guidance.FileName):         original,
					filepath.Join(configDir, guidance.SettingsFileName): []byte(`{"disabled":false,"disabledFragments":["masked.md"]}`),
					filepath.Join(stateDir, "preserved.snapshot"):       []byte("snapshot sentinel"),
				} {
					if err := os.WriteFile(path, content, 0600); err != nil {
						t.Fatal(err)
					}
				}
				client, err := NewClient(Options{ConfigDir: configDir, StateDir: stateDir})
				if err != nil {
					t.Fatal(err)
				}
				held, err := state.AcquireFileLock(filepath.Join(configDir, ".agent-guidance.lock"), time.Second, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Close()
				before := guidanceSDKTree(t, root)
				base, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if kind == "deadline" {
					cancel()
					base, cancel = context.WithTimeout(context.Background(), 2*time.Second)
					want = context.DeadlineExceeded
				}
				defer cancel()
				ctx := &guidanceWaitingContext{Context: base, entered: make(chan struct{}), waiting: make(chan struct{})}
				call := func(ctx context.Context) error {
					if method == "inspect" {
						_, err := client.InspectAgentGuidance(ctx, true)
						return err
					}
					return client.SetAgentGuidanceEnabled(ctx, "", false)
				}
				done := make(chan error, 1)
				go func() { done <- call(ctx) }()
				select {
				case <-ctx.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("SDK did not enter context admission")
				}
				select {
				case <-ctx.waiting:
				case <-time.After(500 * time.Millisecond):
					t.Error("SDK did not pass caller context to the contended policy lock")
				}
				if kind == "cancel" {
					cancel()
				}
				<-base.Done()
				var result error
				returned := false
				select {
				case result = <-done:
					returned = true
				case <-time.After(500 * time.Millisecond):
					t.Error("SDK did not return caller cancellation while lock remained held")
				}
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
				if !returned {
					select {
					case result = <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("SDK did not finish after lock release")
					}
				}
				if !errors.Is(result, want) {
					t.Errorf("want cancellation cause %v, got %v", want, result)
				}
				if after := guidanceSDKTree(t, root); !reflect.DeepEqual(before, after) {
					t.Error("unadmitted SDK request changed policy/user/backup/receipt/snapshot bytes or file list")
				}
				// A late kernel grant must release its descriptor so the next SDK
				// call can acquire the lock and perform the previously canceled work.
				next, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				if err := call(next); err != nil {
					t.Fatal("normal SDK call after cancellation:", err)
				}
				if method == "inspect" {
					got, err := os.ReadFile(filepath.Join(configDir, guidance.FileName))
					if err != nil || !bytes.Equal(got, user) {
						t.Fatalf("fixture did not exercise legacy migration: %q, %v", got, err)
					}
					backup, err := os.ReadFile(filepath.Join(configDir, guidance.FileName+".bak"))
					if err != nil || !bytes.Equal(backup, original) {
						t.Fatal("normal migration did not preserve original backup", err)
					}
				} else {
					settings, _, err := guidance.NewStore(configDir, stateDir).LoadSettings()
					if err != nil || !settings.Disabled {
						t.Fatal("normal policy update did not complete", err)
					}
				}
			})
		}
	}
}
