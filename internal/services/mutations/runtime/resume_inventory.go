package runtime

import (
	"context"
	"fmt"
	"slices"
	"strings"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// topologyOwnerGuard is one execute pass's exact parent and uid-claim proof.
//
// UID strings on the planned handles are not enough. A live Window can be
// relinked to another session and a live Pane can be join-paned into another
// Window without either uid changing, so a split could otherwise land in the
// wrong parent. Worse, a Window relinked out of the selected session is invisible
// to the selected-session plan, which would happily create a second live Window
// carrying the same Registry uid. Both are proven here against one server-wide
// inventory, before the first mutation.
type TopologyOwnerGuard struct {
	WindowOwner map[string]string
	PaneOwner   map[string]RuntimeOwner
	WindowUIDs  map[string][]string
	PaneUIDs    map[string][]string
}

func NewTopologyOwnerGuard(ctx context.Context, runtime *Materializer) (*TopologyOwnerGuard, error) {
	windowsOut, err := runtime.ReadTmux(ctx, "list-windows", "-a", "-F",
		TmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
	if err != nil {
		// No server is the ordinary state of an offline Project on a fresh
		// socket, and it is also the strongest possible answer to "is this uid
		// claimed anywhere": nothing is live, so the inventory is empty.
		if IsMissingTmuxServer(err) {
			return EmptyTopologyOwnerGuard(), nil
		}
		return nil, fmt.Errorf("inventory tmux Window owners: %w", err)
	}
	panesOut, err := runtime.ReadTmux(ctx, "list-panes", "-a", "-F",
		TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
	if err != nil {
		if IsMissingTmuxServer(err) {
			return EmptyTopologyOwnerGuard(), nil
		}
		return nil, fmt.Errorf("inventory tmux Pane owners: %w", err)
	}
	guard := &TopologyOwnerGuard{
		WindowOwner: map[string]string{},
		PaneOwner:   map[string]RuntimeOwner{},
		WindowUIDs:  map[string][]string{},
		PaneUIDs:    map[string][]string{},
	}
	windowRows, err := StrictTmuxRows(windowsOut, 3)
	if err != nil {
		return nil, fmt.Errorf("malformed tmux Window owner inventory: %w", err)
	}
	for _, row := range windowRows {
		if ExactTmuxHandle(row[0], "$") == "" || ExactTmuxHandle(row[1], "@") == "" {
			return nil, fmt.Errorf("malformed tmux Window owner inventory")
		}
		if existing, seen := guard.WindowOwner[row[1]]; seen && existing != row[0] {
			return nil, fmt.Errorf("tmux window %s reports two owning sessions", row[1])
		}
		guard.WindowOwner[row[1]] = row[0]
		if uid := strings.TrimSpace(row[2]); uid != "" && !slices.Contains(guard.WindowUIDs[uid], row[1]) {
			guard.WindowUIDs[uid] = append(guard.WindowUIDs[uid], row[1])
		}
	}
	paneRows, err := StrictTmuxRows(panesOut, 4)
	if err != nil {
		return nil, fmt.Errorf("malformed tmux Pane owner inventory: %w", err)
	}
	for _, row := range paneRows {
		if ExactTmuxHandle(row[0], "$") == "" || ExactTmuxHandle(row[1], "@") == "" || ExactTmuxHandle(row[2], "%") == "" {
			return nil, fmt.Errorf("malformed tmux Pane owner inventory")
		}
		owner := RuntimeOwner{SessionID: row[0], WindowID: row[1]}
		if existing, seen := guard.PaneOwner[row[2]]; seen && existing != owner {
			return nil, fmt.Errorf("tmux pane %s reports two owning windows", row[2])
		}
		guard.PaneOwner[row[2]] = owner
		if uid := strings.TrimSpace(row[3]); uid != "" && !slices.Contains(guard.PaneUIDs[uid], row[2]) {
			guard.PaneUIDs[uid] = append(guard.PaneUIDs[uid], row[2])
		}
	}
	return guard, nil
}

func EmptyTopologyOwnerGuard() *TopologyOwnerGuard {
	return &TopologyOwnerGuard{
		WindowOwner: map[string]string{},
		PaneOwner:   map[string]RuntimeOwner{},
		WindowUIDs:  map[string][]string{},
		PaneUIDs:    map[string][]string{},
	}
}

func RequireSoleUIDClaim(kind, label, uid, handle string, claims []string) error {
	if handle == "" {
		if len(claims) != 0 {
			return fmt.Errorf("%s %s uid %s is already live on %v elsewhere on this socket", kind, label, uid, claims)
		}
		return nil
	}
	if len(claims) != 1 || claims[0] != handle {
		return fmt.Errorf("%s %s uid %s is claimed by %v, want exactly %s", kind, label, uid, claims, handle)
	}
	return nil
}

func IsMissingTmuxServer(err error) bool {
	if inttmux.IsNoServerFailure(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no server running on ") ||
		strings.Contains(message, "failed to connect to server: connection refused")
}
