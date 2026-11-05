package processhost

import (
	"reflect"
	"sort"
	"testing"
)

// Snapshot.Pending is persisted into the Registry on every rewrite and compared
// by order. The same pending-control set must therefore always be recorded in
// the same order, regardless of Go map iteration order.
func TestSnapshotPendingOrderIsStableAcrossSnapshots(t *testing.T) {
	ids := []string{"req-e", "req-b", "req-h", "req-a", "req-g", "req-c", "req-f", "req-d"}
	p := &Handle{requests: map[string]Request{}}
	for _, id := range ids {
		p.requests[id] = Request{ID: id, Kind: "permission"}
	}
	order := func() []string {
		p.mu.Lock()
		defer p.mu.Unlock()
		var got []string
		for _, r := range p.snapshotLocked().Pending {
			got = append(got, r.ID)
		}
		return got
	}
	want := order()
	if len(want) != len(ids) {
		t.Fatalf("pending = %v, want %d entries", want, len(ids))
	}
	for i := range 200 {
		if got := order(); !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot %d pending order = %v, first snapshot = %v", i, got, want)
		}
	}
}

// The recorded order is ID order, not merely a stable one.
func TestSnapshotPendingIsInIDOrder(t *testing.T) {
	ids := []string{"req-e", "req-b", "req-h", "req-a", "req-g", "req-c", "req-f", "req-d"}
	p := &Handle{requests: map[string]Request{}}
	for _, id := range ids {
		p.requests[id] = Request{ID: id, Kind: "permission"}
	}
	want := append([]string(nil), ids...)
	sort.Strings(want)
	for i := range 50 {
		p.mu.Lock()
		var got []string
		for _, r := range p.snapshotLocked().Pending {
			got = append(got, r.ID)
		}
		p.mu.Unlock()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot %d pending order = %v, want ID order %v", i, got, want)
		}
	}
}

// Expiring the pending controls emits control-expired in ID order and leaves
// none pending.
func TestExpireEmitsControlExpiredInIDOrder(t *testing.T) {
	ids := []string{"req-e", "req-b", "req-h", "req-a", "req-g", "req-c", "req-f", "req-d"}
	want := append([]string(nil), ids...)
	sort.Strings(want)
	for i := range 50 {
		p := &Handle{host: &Host{limits: Limits{Events: 64}}, requests: map[string]Request{}}
		for _, id := range ids {
			p.requests[id] = Request{ID: id, Kind: "permission"}
		}
		p.mu.Lock()
		p.expireLocked()
		var got []string
		for _, event := range p.critical {
			if event.Kind == "control-expired" && event.Request != nil {
				got = append(got, event.Request.ID)
			}
		}
		left := len(p.requests)
		p.mu.Unlock()
		if left != 0 {
			t.Fatalf("run %d: %d requests still pending after expiry", i, left)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: control-expired order = %v, want ID order %v", i, got, want)
		}
	}
}
