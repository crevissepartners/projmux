package usage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestHistoryMinuteBoundaryAndThirtyDayPhysicalPrune(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	old := MetricPoint{Name: "usage.percent", Provider: "claude", Window: "quota", Bucket: "a", Value: 7, ObservedAt: now.Add(-HistoryRetention - time.Nanosecond)}
	boundary := old
	boundary.ObservedAt = now.Add(-HistoryRetention)
	boundary.Bucket = "b"
	if err := store.AppendHistory([]MetricPoint{old, boundary}, now); err != nil {
		t.Fatal(err)
	}
	points, err := store.ReadHistory(HistoryFilter{Now: now})
	if err != nil || len(points) != 1 || points[0].Bucket != "b" {
		t.Fatalf("boundary read: %v %v", points, err)
	}
	first := MetricPoint{Name: "usage.percent", Provider: "claude", Window: "quota", Bucket: "a", Value: 10, ObservedAt: now}
	if err := store.AppendHistory([]MetricPoint{first}, now); err != nil {
		t.Fatal(err)
	}
	tooSoon := first
	tooSoon.Value = 20
	tooSoon.ObservedAt = now.Add(time.Minute - time.Nanosecond)
	if err := store.AppendHistory([]MetricPoint{tooSoon}, tooSoon.ObservedAt); err != nil {
		t.Fatal(err)
	}
	exact := first
	exact.Value = 10
	exact.ObservedAt = now.Add(time.Minute)
	if err := store.AppendHistory([]MetricPoint{exact}, exact.ObservedAt); err != nil {
		t.Fatal(err)
	}
	points, err = store.ReadHistory(HistoryFilter{Now: exact.ObservedAt, Provider: "claude", Window: "quota", Metric: "usage.percent"})
	if err != nil || len(points) != 2 || !points[1].ObservedAt.Equal(exact.ObservedAt) {
		t.Fatalf("minute limiter: %v %v", points, err)
	}
	// An idle read hides expired rows; the next write removes their bytes.
	later := now.Add(HistoryRetention + time.Minute)
	points, err = store.ReadHistory(HistoryFilter{Now: later})
	if err != nil || len(points) != 1 {
		t.Fatalf("idle expiration: %v %v", points, err)
	}
	if err := store.AppendHistory(nil, later); err != nil {
		t.Fatal(err)
	}
	physical, err := readHistoryFile(filepath.Join(store.HistoryPath(), segmentName(exact.ObservedAt)))
	if err != nil || len(physical) != 1 {
		t.Fatalf("physical prune: %v %v", physical, err)
	}
}

func TestHistoryConcurrentWritersAndReadersKeepCompleteRows(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Minute)
	var wg sync.WaitGroup
	errCh := make(chan error, 200)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := NewStore(dir)
			for minute := range 8 {
				p := MetricPoint{Name: "agent.live.count", Provider: string(rune('a' + i)), Value: float64(i), ObservedAt: now.Add(time.Duration(minute) * time.Minute)}
				if err := store.AppendHistory([]MetricPoint{p}, p.ObservedAt); err != nil {
					errCh <- err
					return
				}
				if _, err := store.ReadHistory(HistoryFilter{Now: p.ObservedAt}); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	points, err := NewStore(dir).ReadHistory(HistoryFilter{Now: now.Add(8 * time.Minute)})
	if err != nil || len(points) != 64 {
		t.Fatalf("concurrent rows=%d err=%v", len(points), err)
	}
}

func TestHistoryCrossProcessWritersPruneAndReader(t *testing.T) {
	if os.Getenv("PROJMUX_HISTORY_TEST_CHILD") != "" {
		dir := os.Getenv("PROJMUX_HISTORY_TEST_CHILD")
		provider := os.Getenv("PROJMUX_HISTORY_TEST_PROVIDER")
		at := time.Now().UTC().Truncate(time.Minute)
		for minute := range 6 {
			p := MetricPoint{Name: "agent.live.count", Provider: provider, Value: float64(minute), ObservedAt: at.Add(time.Duration(minute) * time.Minute)}
			if err := NewStore(dir).AppendHistory([]MetricPoint{p}, p.ObservedAt); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	dir := t.TempDir()
	store := NewStore(dir)
	oldAt := time.Now().UTC().Add(-HistoryRetention - time.Minute)
	if err := store.AppendHistory([]MetricPoint{{Name: "agent.live.count", Provider: "expired", Value: 1, ObservedAt: oldAt}}, oldAt); err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	for _, provider := range []string{"claude", "codex", "antigravity"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHistoryCrossProcessWritersPruneAndReader$") // #nosec G204 -- current test executable with fixed args
		cmd.Env = append(os.Environ(), "PROJMUX_HISTORY_TEST_CHILD="+dir, "PROJMUX_HISTORY_TEST_PROVIDER="+provider)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if _, err := store.ReadHistory(HistoryFilter{Now: time.Now().UTC().Add(6 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendHistory(nil, time.Now().UTC().Add(6*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child writer: %v", err)
		}
	}
	points, err := store.ReadHistory(HistoryFilter{Now: time.Now().UTC().Add(6 * time.Minute)})
	if err != nil || len(points) != 18 {
		t.Fatalf("cross-process rows=%d err=%v", len(points), err)
	}
	files, err := listSegments(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := files[segmentName(oldAt)]; exists {
		t.Fatal("expired segment retained after concurrent prune")
	}
}

func TestHistoryDamageAndLockFailureRemainErrors(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	p := MetricPoint{Name: "usage.percent", Value: 3, ObservedAt: now}
	if err := store.AppendHistory([]MetricPoint{p}, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.HistoryPath(), segmentName(now))
	if err := os.WriteFile(path, []byte(`{"name":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadHistory(HistoryFilter{Now: now}); err == nil {
		t.Fatal("malformed history read succeeded")
	}
	if err := store.AppendHistory([]MetricPoint{p}, now); err == nil {
		t.Fatal("malformed history append succeeded")
	}
	// A separate process can take the same persistent flock; a blocked writer
	// times out without touching the snapshot cache.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(store.HistoryLockPath(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	started := time.Now()
	if wrote, err := store.TryAppendHistory([]MetricPoint{p}, now); err != nil || wrote {
		t.Fatalf("busy try append wrote=%t err=%v", wrote, err)
	}
	if elapsed := time.Since(started); elapsed >= HistoryLockWaitLimit/4 {
		t.Fatalf("busy try append waited %s", elapsed)
	}
	if err := store.AppendHistory([]MetricPoint{p}, now); !errors.Is(err, ErrHistoryLockTimeout) {
		t.Fatalf("lock error = %v", err)
	}
	if err := store.SaveState(State{Snapshots: []Snapshot{{Model: "claude", Window: Window5h, Pct: 7}}}); err != nil {
		t.Fatalf("snapshot blocked by history lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.baseDir, snapshotFileName)); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryIgnoresNonsegmentsButRejectsDamagedSegments(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	point := MetricPoint{Name: "usage.percent", Value: 3, ObservedAt: now}
	if err := store.AppendHistory([]MetricPoint{point}, now); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes.txt", "notes.jsonl", ".history.jsonl.swp", segmentName(now) + ".bak"} {
		if err := os.WriteFile(filepath.Join(store.HistoryPath(), name), []byte("ignored"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if points, err := store.ReadHistory(HistoryFilter{Now: now}); err != nil || len(points) != 1 {
		t.Fatalf("nonsegment read: %v %v", points, err)
	}
	if err := store.AppendHistory([]MetricPoint{point}, now); err != nil {
		t.Fatalf("nonsegment append: %v", err)
	}
	badDate := filepath.Join(store.HistoryPath(), "2026-13-40.jsonl")
	if err := os.WriteFile(badDate, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadHistory(HistoryFilter{Now: now}); err == nil {
		t.Fatal("invalid segment date read succeeded")
	}
	if err := store.AppendHistory([]MetricPoint{point}, now); err == nil {
		t.Fatal("invalid segment date append succeeded")
	}
	if processed, err := store.TryAppendHistory([]MetricPoint{point}, now); err == nil || processed {
		t.Fatalf("damaged segment try append processed=%t err=%v", processed, err)
	}
	if err := os.Remove(badDate); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.HistoryPath(), segmentName(now))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadHistory(HistoryFilter{Now: now}); err == nil {
		t.Fatal("nonregular segment read succeeded")
	}
	if err := store.AppendHistory([]MetricPoint{point}, now); err == nil {
		t.Fatal("nonregular segment append succeeded")
	}
}

func TestHistorySegmentsDedupAcrossMidnightAndOutOfOrder(t *testing.T) {
	store := NewStore(t.TempDir())
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	point := func(at time.Time) MetricPoint {
		return MetricPoint{Name: "agent.live.count", Provider: "codex", Value: 1, ObservedAt: at}
	}
	first := day.Add(-30 * time.Second)
	if err := store.AppendHistory([]MetricPoint{point(first)}, day); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendHistory([]MetricPoint{point(day.Add(20 * time.Second))}, day.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendHistory([]MetricPoint{point(day.Add(30 * time.Second))}, day.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A delayed sample is accepted when it is a full minute away from its
	// neighbours, even though the latest sample belongs to the next day.
	older := day.Add(-3 * time.Minute)
	if err := store.AppendHistory([]MetricPoint{point(older)}, day.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendHistory([]MetricPoint{point(older.Add(45 * time.Second))}, day.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	points, err := store.ReadHistory(HistoryFilter{Now: day.Add(time.Minute)})
	if err != nil || len(points) != 3 {
		t.Fatalf("midnight/out-of-order points=%v err=%v", points, err)
	}
	files, err := listSegments(store.HistoryPath())
	if err != nil || len(files) != 2 {
		t.Fatalf("daily segments=%v err=%v", files, err)
	}
}

func TestHistoryEmptyReadDoesNotCreateStateAndPruneTempIsIgnored(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new-state")
	store := NewStore(root)
	points, err := store.ReadHistory(HistoryFilter{})
	if err != nil || len(points) != 0 {
		t.Fatalf("empty read: %v %v", points, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created state: %v", err)
	}
	now := time.Now().UTC()
	if err := store.AppendHistory([]MetricPoint{{Name: "usage.percent", Value: 1, ObservedAt: now}}, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.HistoryPath(), ".history.tmp-crashed"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	points, err = store.ReadHistory(HistoryFilter{Now: now})
	if err != nil || len(points) != 1 {
		t.Fatalf("crash temp disrupted reader: %v %v", points, err)
	}
	if err := store.AppendHistory(nil, now.Add(time.Minute)); err != nil {
		t.Fatalf("crash temp disrupted writer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.HistoryPath(), ".history.tmp-crashed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash temp retained after write: %v", err)
	}
}
