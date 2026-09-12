package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type nativeWatchProbeClient struct {
	mu                        sync.Mutex
	response                  func(context.Context, int) (json.RawMessage, error)
	events                    chan codexappserver.Notification
	reads                     []time.Time
	budgets                   []time.Duration
	active, maxActive, closes int
}

func (c *nativeWatchProbeClient) Request(ctx context.Context, method string, params, result any) error {
	if method != methodRateLimitsRead || string(params.(json.RawMessage)) != "null" {
		return errors.New("unexpected watcher request")
	}
	c.mu.Lock()
	c.reads = append(c.reads, time.Now())
	deadline, _ := ctx.Deadline()
	c.budgets = append(c.budgets, time.Until(deadline))
	c.active++
	c.maxActive = max(c.maxActive, c.active)
	call := len(c.reads)
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.active--; c.mu.Unlock() }()
	raw, err := c.response(ctx, call)
	if err == nil {
		*result.(*json.RawMessage) = raw
	}
	return err
}
func (c *nativeWatchProbeClient) Notifications() <-chan codexappserver.Notification { return c.events }
func (c *nativeWatchProbeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return nil
}

type nativeWatchProbeStats struct {
	reads                     []time.Time
	budgets                   []time.Duration
	active, maxActive, closes int
}

func (c *nativeWatchProbeClient) stats() nativeWatchProbeStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return nativeWatchProbeStats{reads: append([]time.Time(nil), c.reads...), budgets: append([]time.Duration(nil), c.budgets...), active: c.active, maxActive: c.maxActive, closes: c.closes}
}

type nativeWatchBatches struct {
	mu   sync.Mutex
	rows []usage.Snapshot
}

func (b *nativeWatchBatches) snapshots() []usage.Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]usage.Snapshot(nil), b.rows...)
}

func nativeWatchResponse(pct int, label string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"rateLimits":{"limitId":"codex","limitName":%q,"primary":{"usedPercent":%d,"windowDurationMins":300,"resetsAt":1787380200}}}`, label, pct))
}

func startNativeClockWatch(t *testing.T, client *nativeWatchProbeClient) (context.CancelFunc, <-chan error, *nativeWatchBatches, *int) {
	t.Helper()
	adapter := NewWithRoot(t.TempDir())
	adapter.native = availableNative(client)
	opens := new(int)
	adapter.native.open = func(context.Context) (nativeClient, error) { *opens++; return client, nil }
	ctx, cancel := context.WithCancel(context.Background())
	rows := &nativeWatchBatches{}
	done := make(chan error, 1)
	go func() {
		done <- adapter.WatchNativeRateLimits(ctx, func(next []usage.Snapshot) error {
			rows.mu.Lock()
			rows.rows = append(rows.rows, next...)
			rows.mu.Unlock()
			return nil
		})
	}()
	synctest.Wait()
	return cancel, done, rows, opens
}

func TestWatchNativeRateLimitsPeriodicReadKeepsQuietConnection(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%t", busy), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				client := &nativeWatchProbeClient{events: make(chan codexappserver.Notification, 64)}
				client.response = func(context.Context, int) (json.RawMessage, error) { return nativeWatchResponse(11, "General"), nil }
				cancel, done, rows, opens := startNativeClockWatch(t, client)
				defer cancel()
				for second := 1; second <= 60; second++ {
					if busy {
						// A permanently queued burst at each second must not reset or
						// defer the read cadence, including exactly on the tick.
						for range cap(client.events) {
							client.events <- codexappserver.Notification{Method: methodRateLimitsUpdated, Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":12}}}`)}
						}
					}
					time.Sleep(time.Second)
					synctest.Wait()
					if got, want := len(client.stats().reads), 1+second/30; got != want {
						t.Fatalf("at %ds reads=%d, want %d", second, got, want)
					}
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if *opens != 1 || client.stats().closes != 1 || client.stats().maxActive != 1 {
					t.Fatalf("opens=%d closes=%d max reads=%d", *opens, client.stats().closes, client.stats().maxActive)
				}
				for i, at := range client.stats().reads {
					if at.Sub(start) != time.Duration(i)*30*time.Second || client.stats().budgets[i] != 2*time.Second {
						t.Fatalf("read %d at=%v budget=%v", i, at.Sub(start), client.stats().budgets[i])
					}
				}
				for _, row := range rows.snapshots() {
					if row.Source != usage.SourceAppServer || row.FallbackReason != "" || row.StaleReason != "" {
						t.Fatalf("bad provenance: %#v", row)
					}
				}
			})
		})
	}
}

func TestWatchNativeRateLimitsNoEOFFailureIsBounded(t *testing.T) {
	for _, lateSuccess := range []bool{false, true} {
		t.Run(fmt.Sprintf("late-success=%t", lateSuccess), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				client := &nativeWatchProbeClient{events: make(chan codexappserver.Notification, 2)}
				client.response = func(ctx context.Context, call int) (json.RawMessage, error) {
					if call == 1 {
						return nativeWatchResponse(11, "General"), nil
					}
					<-ctx.Done() // No EOF and no response until the two-second deadline.
					if lateSuccess {
						return nativeWatchResponse(99, "Late"), nil
					}
					return nil, ctx.Err()
				}
				cancel, done, rows, opens := startNativeClockWatch(t, client)
				defer cancel()
				time.Sleep(31 * time.Second)
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("early failure: %v", err)
				default:
				}
				if len(client.stats().reads) != 2 || client.stats().active != 1 {
					t.Fatalf("reads=%d active=%d", len(client.stats().reads), client.stats().active)
				}
				client.events <- codexappserver.Notification{Method: methodRateLimitsUpdated, Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":33}}}`)}
				time.Sleep(time.Second)
				err := <-done
				var stale *usage.StaleReasonError
				if !errors.As(err, &stale) || stale.Reason != usage.ReasonAppServerTimeout {
					t.Fatalf("failure=%v", err)
				}
				if time.Since(start) != 32*time.Second || *opens != 1 || client.stats().closes != 1 || client.stats().active != 0 {
					t.Fatalf("elapsed=%v opens=%d closes=%d active=%d", time.Since(start), *opens, client.stats().closes, client.stats().active)
				}
				client.events <- codexappserver.Notification{Method: methodRateLimitsUpdated, Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":99}}}`)}
				time.Sleep(30 * time.Second)
				synctest.Wait()
				if len(rows.snapshots()) != 1 || rows.snapshots()[0].Pct != 11 {
					t.Fatalf("last good overwritten: %#v", rows.snapshots())
				}
			})
		})
	}
}

func TestWatchNativeRateLimitsPeriodicReadRefreshesSparseBase(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprintf("multi=%t", multi), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := &nativeWatchProbeClient{events: make(chan codexappserver.Notification, 1)}
				client.response = func(_ context.Context, call int) (json.RawMessage, error) {
					var raw json.RawMessage
					switch call {
					case 1:
						raw = nativeWatchResponse(11, "Old")
					case 2:
						raw = json.RawMessage(`{"rateLimits":{"limitId":"codex","limitName":"New","primary":{"usedPercent":22,"windowDurationMins":10080,"resetsAt":1787380999},"secondary":{"usedPercent":"bad","windowDurationMins":300}}}`)
					default:
						raw = json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":"bad"}}}`)
					}
					if multi {
						root, _ := rawObject(raw)
						root["rateLimitsByLimitId"], _ = json.Marshal(map[string]json.RawMessage{
							"codex":   root["rateLimits"],
							"invalid": json.RawMessage(`{"limitId":123,"primary":{"usedPercent":99}}`),
						})
						raw, _ = json.Marshal(root)
					}
					return raw, nil
				}
				cancel, done, batches, _ := startNativeClockWatch(t, client)
				defer cancel()
				time.Sleep(30 * time.Second)
				synctest.Wait()
				rows := batches.snapshots()
				if len(rows) != 2 || rows[1].Pct != 22 {
					t.Fatalf("periodic batch=%#v", rows)
				}
				client.events <- codexappserver.Notification{Method: methodRateLimitsUpdated, Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":33},"secondary":{"usedPercent":44}}}`)}
				synctest.Wait()
				rows = batches.snapshots()
				if len(rows) != 4 {
					t.Fatalf("sparse batch=%#v", rows)
				}
				row := rows[2]
				if row.Pct != 33 || row.Window != usage.WindowWeekly || *row.RateLimit.Label != "New" || row.ResetsAt.Unix() != 1787380999 || row.Source != usage.SourceAppServer || !row.UpdatedAt.Equal(rows[1].UpdatedAt) {
					t.Fatalf("sparse row did not inherit periodic base: %#v", row)
				}
				if row := rows[3]; row.Pct != 44 || row.Window != usage.WindowQuota || row.RateLimit.CadenceMinutes != nil {
					t.Fatalf("sparse row inherited cadence from a rejected row: %#v", row)
				}
				time.Sleep(30 * time.Second)
				err := <-done
				var stale *usage.StaleReasonError
				if !errors.As(err, &stale) || stale.Reason != usage.ReasonAppServerProtocol {
					t.Fatalf("malformed result=%v", err)
				}
				if len(batches.snapshots()) != 4 {
					t.Fatalf("malformed result replaced last good: %#v", batches.snapshots())
				}
			})
		})
	}
}

func TestWatchNativeRateLimitsParentCancelRejectsLateProbeAndEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &nativeWatchProbeClient{events: make(chan codexappserver.Notification, 1)}
		client.response = func(ctx context.Context, call int) (json.RawMessage, error) {
			if call > 1 {
				<-ctx.Done()
			}
			return nativeWatchResponse(call*11, "General"), nil
		}
		cancel, done, rows, _ := startNativeClockWatch(t, client)
		time.Sleep(30 * time.Second)
		synctest.Wait()
		cancel()
		client.events <- codexappserver.Notification{Method: methodRateLimitsUpdated, Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":99}}}`)}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("stop=%v", err)
		}
		if len(rows.snapshots()) != 1 || client.stats().active != 0 || client.stats().closes != 1 {
			t.Fatalf("rows=%#v active=%d closes=%d", rows.snapshots(), client.stats().active, client.stats().closes)
		}
	})
}
