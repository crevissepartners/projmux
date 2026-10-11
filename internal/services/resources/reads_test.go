package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestClassifyOnlySelectorFailures(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, errors.New("reader unavailable"), fmt.Errorf("operation: %w", context.Canceled)} {
		if got := Classify(err); got != err {
			t.Fatalf("%v changed to %v", err, got)
		}
	}
	for _, tc := range []struct {
		err  *selector.SelectorError
		code string
	}{
		{&selector.SelectorError{}, "selector-invalid"},
		{&selector.SelectorError{Want: "exactly one", Got: 0}, "resource-not-found"},
		{&selector.SelectorError{Want: "exactly one", Got: 2}, "selector-ambiguous"},
	} {
		err := fmt.Errorf("select: %w", tc.err)
		got := Classify(err)
		var f *Failure
		if !errors.As(got, &f) || f.Code != tc.code || !errors.Is(got, tc.err) || got.Error() != err.Error() {
			t.Fatalf("classified %v", got)
		}
		if Classify(got) != got {
			t.Fatal("failure classified twice")
		}
	}
	_, err := Resolve(metadata.NewRegistry(), metadata.RuntimeObservation{}, nil, "unsupported", selector.Query{})
	var f *Failure
	if err == nil || errors.As(err, &f) {
		t.Fatalf("unexpected resolution classification: %v", err)
	}
}

func TestLoadClassifiesRegistryFailure(t *testing.T) {
	paths := config.Paths{StateDir: t.TempDir()}
	path := store.PathFor(paths.StateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid registry"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(paths)
	var f *Failure
	if !errors.As(err, &f) || f.Code != "registry-unavailable" || f.Cause == nil {
		t.Fatalf("registry error %v", err)
	}
}
