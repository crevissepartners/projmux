package mutations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestAcceptedRegistrationCompletesAfterCancellation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "metadata", "registry.json")
	writer := NewStore(path)
	update := writer.UpdateConvergent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer.UpdateConvergent = func(fn func(*metadata.Registry) error) (metadata.Registry, bool, error) {
		// The service admitted the request before calling the writer. Cancellation
		// while waiting for its transaction cannot turn a committed write into failure.
		cancel()
		return update(fn)
	}
	result, err := RegisterProject(ctx, writer, metadata.RegisterProjectOptions{Root: root, DefaultShell: "/bin/sh"})
	if err != nil || result.Reused || ctx.Err() == nil {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
	// Reusing a root whose directory disappeared must bypass directory validation.
	if err = os.Remove(root); err != nil {
		t.Fatal(err)
	}
	again, err := RegisterProject(context.Background(), writer, metadata.RegisterProjectOptions{Root: root})
	if err != nil || !again.Reused || again.Project.Metadata.UID != result.Project.Metadata.UID {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestCancellationIsNotARegistryRefusal(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if Classify(err) != err {
			t.Fatalf("cancellation reclassified: %v", Classify(err))
		}
	}
}
