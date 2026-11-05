package processhost

import (
	"reflect"
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
	for i := 0; i < 200; i++ {
		if got := order(); !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot %d pending order = %v, first snapshot = %v", i, got, want)
		}
	}
}
