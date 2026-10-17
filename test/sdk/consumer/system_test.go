package consumer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalSystemSampleConsumer(t *testing.T) {
	root := t.TempDir()
	client, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.SampleSystem(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled call touched state: %v", err)
	}
	var sample core.SystemSample
	sample, err = client.SampleSystem(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" && !sample.Supported {
		t.Fatal("Linux host sampling unsupported")
	}
	for _, value := range []*int{sample.CPUPercent, sample.MemoryPercent} {
		if value != nil && (*value < 0 || *value > 100) {
			t.Fatalf("percentage outside 0..100: %d", *value)
		}
	}
	if !sample.Supported && (sample.CPUPercent != nil || sample.MemoryPercent != nil) {
		t.Fatal("unsupported platform has metrics")
	}
	// The public DTO is independently mutable without internal imports.
	if sample.MemoryPercent != nil {
		*sample.MemoryPercent = -1
	}
	next, err := client.SampleSystem(context.Background())
	if err != nil || (next.MemoryPercent != nil && *next.MemoryPercent == -1) {
		t.Fatalf("caller mutation leaked: %+v %v", next, err)
	}
	if _, err := os.Stat(filepath.Join(root, "config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config touched: %v", err)
	}
}
