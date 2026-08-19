package metadata

import (
	"fmt"
	"slices"
	"time"
)

// createdRef is one entry of the created-resource ledger.
type createdRef struct {
	Kind Kind
	UID  string
}

// Transaction records the resources one operation created so a post-create
// failure can be rolled back in reverse order. It never touches resources that
// existed before the operation started or that another operation created.
//
// A pre-create failure records nothing, so rolling back is a no-op and the
// registry is left with zero mutations.
type Transaction struct {
	id       string
	registry *Registry
	now      func() time.Time
	ledger   []createdRef
	done     bool
}

// Begin opens a transaction against reg with the supplied operation id.
func (m Mutator) Begin(reg *Registry, operationID string) *Transaction {
	return &Transaction{id: operationID, registry: reg, now: m.clock()}
}

// ID returns the operation id recorded for this transaction.
func (t *Transaction) ID() string { return t.id }

// Created returns the ledger entries in creation order.
func (t *Transaction) Created() []string {
	out := make([]string, 0, len(t.ledger))
	for _, ref := range t.ledger {
		out = append(out, fmt.Sprintf("%s/%s", ref.Kind, ref.UID))
	}
	return out
}

func (t *Transaction) record(kind Kind, uid string) {
	t.ledger = append(t.ledger, createdRef{Kind: kind, UID: uid})
}

// Commit closes the transaction successfully and clears the ledger.
func (t *Transaction) Commit() {
	t.done = true
	t.ledger = nil
}

// Rollback removes, in reverse creation order, only the resources this
// operation created that still carry the same uid. Pre-existing resources and
// resources created by another operation are never touched.
func (t *Transaction) Rollback() {
	if t.done {
		return
	}
	for i := len(t.ledger) - 1; i >= 0; i-- {
		ref := t.ledger[i]
		t.registry.removeCreated(ref)
	}
	t.ledger = nil
	t.done = true
}

func (r *Registry) removeCreated(ref createdRef) {
	switch ref.Kind {
	case KindProject:
		for i := range r.Projects {
			if r.Projects[i].Metadata.UID == ref.UID {
				r.Projects = slices.Delete(r.Projects, i, i+1)
				r.releaseNames(ref.UID)
				return
			}
		}
	case KindControlSession:
		for i := range r.ControlSessions {
			if r.ControlSessions[i].Metadata.UID == ref.UID {
				r.ControlSessions = slices.Delete(r.ControlSessions, i, i+1)
				r.releaseNames(ref.UID)
				return
			}
		}
	case KindWindow:
		for i := range r.Windows {
			if r.Windows[i].Metadata.UID == ref.UID {
				r.Windows = slices.Delete(r.Windows, i, i+1)
				r.releaseNames(ref.UID)
				return
			}
		}
	case KindPane:
		for i := range r.Panes {
			if r.Panes[i].Metadata.UID == ref.UID {
				r.Panes = slices.Delete(r.Panes, i, i+1)
				r.releaseNames(ref.UID)
				return
			}
		}
	case KindAgent:
		for i := range r.Agents {
			if r.Agents[i].Metadata.UID == ref.UID {
				r.Agents = slices.Delete(r.Agents, i, i+1)
				r.releaseNames(ref.UID)
				return
			}
		}
	}
}
