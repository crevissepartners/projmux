// Package processattention reads the durable process attention store and the
// current process declarations without any owner socket access. Writers stay
// with the foreground owner.
package processattention

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Record holds only routing and lifecycle metadata. Provider frames, question
// text, tool input, answers and diagnostics remain outside the store.
type Record struct {
	Binding        processhost.Binding
	Provider       string
	Sequence       uint64
	Badge          string
	AckSequence    uint64
	Terminal       bool
	PendingEnded   bool
	Pending        map[string]Pending
	NoticeSequence uint64
	NoticeKind     string
}

// Pending is one open question or approval of a Record.
type Pending struct {
	Kind     string
	Sequence uint64
}

// ErrDamaged reports a store file that is present but not a record map.
var ErrDamaged = errors.New("damaged process attention store")

// StorePath is the one durable store file under the state directory.
func StorePath(stateDir string) string {
	return filepath.Join(stateDir, "process-attention.json")
}

// ReadStore reads the store file without creating, locking or repairing it. A
// missing file is an empty store; an undecodable one wraps ErrDamaged.
func ReadStore(stateDir string) (map[string]Record, error) {
	return ReadFile(StorePath(stateDir))
}

// ReadFile is ReadStore for an explicit store path.
func ReadFile(path string) (map[string]Record, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- explicit per-user state path, never provider input.
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records map[string]Record
	if err = json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDamaged, err)
	}
	if records == nil {
		return nil, ErrDamaged
	}
	return records, nil
}

// Effective is the stored badge aggregated with every pending kind, in sorted
// request order.
func (r Record) Effective() string {
	badge := r.Badge
	ids := make([]string, 0, len(r.Pending))
	for id := range r.Pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		badge = aibadge.Aggregate(badge, r.Pending[id].Kind)
	}
	return badge
}
