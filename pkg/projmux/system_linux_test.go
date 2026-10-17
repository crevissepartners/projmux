//go:build linux

package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSampleSystemProducerParity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	c, err := NewClient(Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "sdk")})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	stat := "cpu 100 0 0 100 0 0 0 0\n"
	mem := "MemTotal: 1000 kB\nMemAvailable: 600 kB\n"
	read := func(path string) ([]byte, error) {
		switch path {
		case systemstatus.DefaultProcStatPath:
			return []byte(stat), nil
		case systemstatus.DefaultProcMemInfoPath:
			return []byte(mem), nil
		default:
			t.Fatalf("unexpected read %q", path)
			return nil, os.ErrNotExist
		}
	}
	sampler := systemstatus.Sampler{ReadFile: read, Now: func() time.Time { return now }, CachePath: filepath.Join(root, "producer", "cpu.json")}
	for _, step := range []struct {
		advance     time.Duration
		stat, mem   string
		cpu, memory int
	}{
		{0, stat, mem, -1, 40},
		{time.Second, "cpu 160 0 0 140 0 0 0 0\n", mem, 60, 40},
		{30 * time.Second, "cpu 170 0 0 230 0 0 0 0\n", mem, 10, 40},
		{30*time.Second + time.Nanosecond, "cpu 180 0 0 320 0 0 0 0\n", mem, -1, 40},
		{time.Second, "cpu 1 0 0 1 0 0 0 0\n", mem, -1, 40},
		{time.Second, "invalid", "invalid", -1, -1},
	} {
		now = now.Add(step.advance)
		stat, mem = step.stat, step.mem
		want := sampler.Sample()
		got, err := c.sampleSystem(context.Background(), sampler)
		if err != nil || !got.Supported || !reflect.DeepEqual(got.CPUPercent, want.CPUPercent) || !reflect.DeepEqual(got.MemoryPercent, want.MemoryPercent) {
			t.Fatalf("SDK=%+v producer=%+v err=%v", got, want, err)
		}
		value := func(p *int) int {
			if p == nil {
				return -1
			}
			return *p
		}
		if value(got.CPUPercent) != step.cpu || value(got.MemoryPercent) != step.memory {
			t.Fatalf("unexpected percentages %+v", got)
		}
		a, err := os.ReadFile(c.paths.LiveResourcesSampleFile())
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(sampler.CachePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("cache parity: %s != %s", a, b)
		}
	}
	// Sampling creates only the existing CPU cache, no config or usage history.
	var files []string
	err = filepath.WalkDir(c.paths.StateDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil || !reflect.DeepEqual(files, []string{c.paths.LiveResourcesSampleFile()}) {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if _, err := os.Stat(c.paths.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config touched: %v", err)
	}
}

func TestSampleSystemPathsCancellationAndOwnership(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config-parent"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "captured"))
	captured, err := NewClient(Options{})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := NewClient(Options{StateDir: filepath.Join(root, "explicit")})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "later"))
	now := time.Unix(100, 0)
	sampler := systemstatus.Sampler{Now: func() time.Time { return now }, ReadFile: func(path string) ([]byte, error) {
		if path == systemstatus.DefaultProcStatPath {
			return []byte("cpu 150 0 0 150 0 0 0 0\n"), nil
		}
		return []byte("MemTotal: 1000 kB\nMemAvailable: 600 kB\n"), nil
	}}
	for _, c := range []*Client{captured, explicit} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.SampleSystem(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := os.Stat(c.paths.StateDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled call wrote state: %v", err)
		}
		if _, err := c.sampleSystem(context.Background(), sampler); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(c.paths.LiveResourcesSampleFile())
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(c.paths.LiveResourcesSampleFile())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.SampleSystem(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		after, err := os.ReadFile(c.paths.LiveResourcesSampleFile())
		if err != nil {
			t.Fatal(err)
		}
		infoAfter, err := os.Stat(c.paths.LiveResourcesSampleFile())
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) || !info.ModTime().Equal(infoAfter.ModTime()) {
			t.Fatal("canceled call changed cache")
		}
	}
	if captured.paths.StateDir != filepath.Join(root, "captured", "projmux") {
		t.Fatal(captured.paths.StateDir)
	}
	if _, err := os.Stat(filepath.Join(root, "later")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("later environment used: %v", err)
	}
	// Give just one client a different CPU reference; the other's result stays nil.
	seed := []byte(`{"total":200,"idle":100,"at_unix_nano":100000000000}`)
	if err := os.WriteFile(explicit.paths.LiveResourcesSampleFile(), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	first, err := explicit.sampleSystem(context.Background(), sampler)
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUPercent == nil || *first.CPUPercent != 50 {
		t.Fatalf("first=%+v", first)
	}
	*first.CPUPercent, *first.MemoryPercent = 999, 999
	other, err := captured.sampleSystem(context.Background(), sampler)
	if err != nil {
		t.Fatal(err)
	}
	if other.CPUPercent != nil || other.MemoryPercent == nil || *other.MemoryPercent != 40 {
		t.Fatalf("other=%+v", other)
	}
	if err := os.WriteFile(explicit.paths.LiveResourcesSampleFile(), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := explicit.sampleSystem(context.Background(), sampler)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, core.SystemSample{Supported: true, CPUPercent: intPointer(50), MemoryPercent: intPointer(40)}) {
		t.Fatalf("caller mutation leaked: %+v", again)
	}
}

func intPointer(v int) *int { return &v }
