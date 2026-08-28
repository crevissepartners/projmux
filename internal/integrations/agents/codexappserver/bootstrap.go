package codexappserver

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ThreadSnapshot is the content-free pre-turn view of one exact thread. It
// deliberately omits the conversation name, every turn, and every item body,
// so a binding can be bootstrapped before the thread's first user message
// exists and nothing in it can carry prompt or response text.
type ThreadSnapshot struct {
	ThreadID      string
	CWD           string
	RuntimeStatus string
	ActiveFlags   []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BootstrapThread opens one pre-turn binding for an existing thread.
//
// Upstream refuses thread/read with includeTurns=true for a thread whose first
// turn has not materialized, so this path never asks for turns and never
// requires one to exist. It also never relies on a mutation implicitly
// subscribing the connection: the explicit thread/resume subscription is sent
// first and the includeTurns=false snapshot second, so no lifecycle event can
// fall into a gap between the snapshot and the subscription. Ordering the two
// requests this way is what a later reconnect barrier needs; this seam owns
// only the order, not the barrier.
//
// It sends exactly one thread/resume and exactly one thread/read, creates no
// thread, and starts no turn. Because thread/resume always excludes turns, it
// requires a connection that negotiated the experimental API capability.
//
// Observed upstream limit on installed Codex 0.150.1: thread/resume answers
// only for a thread whose rollout already exists, so a thread whose first turn
// never ran refuses the subscription leg while its includeTurns=false snapshot
// stays readable. The installed smoke records that as typed evidence.
func (c *Client) BootstrapThread(ctx context.Context, threadID, cwd string, roots []string) (ThreadSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ThreadSnapshot{}, fmt.Errorf("%w: bootstrap requires thread id", ErrProtocol)
	}
	binding, err := c.ResumeThread(ctx, threadID, cwd, roots)
	if err != nil {
		return ThreadSnapshot{}, err
	}
	thread, err := c.ReadCatalogThread(ctx, binding.ThreadID)
	if err != nil {
		return ThreadSnapshot{}, err
	}
	return newThreadSnapshot(thread), nil
}

// newThreadSnapshot projects the catalog metadata onto the pre-turn snapshot.
// The conversation name is dropped here on purpose: identity, location, and
// runtime status are all a binding needs, and the title is provider content.
func newThreadSnapshot(thread CatalogThread) ThreadSnapshot {
	snapshot := ThreadSnapshot{
		ThreadID:      thread.ID,
		CWD:           thread.CWD,
		RuntimeStatus: thread.RuntimeStatus,
		CreatedAt:     thread.CreatedAt,
		UpdatedAt:     thread.UpdatedAt,
	}
	if len(thread.ActiveFlags) > 0 {
		snapshot.ActiveFlags = append([]string(nil), thread.ActiveFlags...)
	}
	return snapshot
}
