package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Run only on request: PROJMUX_HISTORY_MEASURE=1 go test ./internal/core/usage
// -run TestHistoryRepresentativeWorkload -v. The fixture is a temporary local
// file and never fsyncs or opens the live state directory.
func TestHistoryRepresentativeWorkload(t *testing.T) {
	if os.Getenv("PROJMUX_HISTORY_MEASURE") != "1" {
		t.Skip("manual workload measurement")
	}
	store := NewStore(t.TempDir())
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(store.baseDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.HistoryPath(), 0700); err != nil {
		t.Fatal(err)
	}
	var f *os.File
	var w *bufio.Writer
	var current string
	var totalBytes int64
	for minute := range 30 * 24 * 60 {
		at := now.Add(-HistoryRetention + time.Duration(minute)*time.Minute)
		name := segmentName(at)
		if name != current {
			if w != nil {
				if err := w.Flush(); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			f, err = os.Create(filepath.Join(store.HistoryPath(), name))
			if err != nil {
				t.Fatal(err)
			}
			w = bufio.NewWriterSize(f, 1<<20)
			current = name
		}
		for series := range 6 {
			p := MetricPoint{Name: "usage.percent", Value: float64(series), ObservedAt: at, Provider: "codex", Window: fmt.Sprintf("window-%d", series)}
			b, _ := json.Marshal(p)
			if _, err := w.Write(append(b, '\n')); err != nil {
				t.Fatal(err)
			}
			totalBytes += int64(len(b) + 1)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	metric := MetricPoint{Name: "usage.percent", Value: 3, ObservedAt: now, Provider: "codex", Window: "window-1"}
	measure := func(name string, n int, fn func(int) error) {
		var times []time.Duration
		for i := range n {
			start := time.Now()
			if err := fn(i); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			times = append(times, time.Since(start))
		}
		slices.Sort(times)
		t.Logf("%s n=%d median=%s max=%s", name, n, times[n/2], times[n-1])
	}
	t.Logf("workload rows=%d bytes=%d segments=30", 30*24*60*6, totalBytes)
	measure("append", 5, func(i int) error {
		p := metric
		p.ObservedAt = now.Add(time.Duration(i) * time.Minute)
		return store.AppendHistory([]MetricPoint{p}, p.ObservedAt)
	})
	measure("read", 5, func(i int) error {
		_, err := store.ReadHistory(HistoryFilter{Now: now.Add(5 * time.Minute)})
		return err
	})
	latest := metric
	latest.ObservedAt = now.Add(4 * time.Minute)
	measure("dedup-lock", 5, func(i int) error { return store.AppendHistory([]MetricPoint{latest}, now.Add(5*time.Minute)) })
	measure("out-of-order-dedup", 5, func(i int) error { return store.AppendHistory([]MetricPoint{metric}, now.Add(5*time.Minute)) })
	measure("prune", 3, func(i int) error { return store.AppendHistory(nil, now.Add(time.Duration(i+6)*time.Minute)) })
}
