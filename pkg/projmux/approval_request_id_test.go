package projmux_test

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestIsClaudeApprovalRequestIDEqualsInternal(t *testing.T) {
	type row struct {
		id     string
		minted bool
	}
	var rows []row
	for i := 0; i < 8; i++ {
		id, err := agentapproval.NewID()
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row{id, true})
	}
	for _, id := range []string{
		"permission-0123456789abcde", "permission-0123456789abcdef0",
		"permission-0123456789ABCDEF", "permission-0123456789abcdeg", "permission-0123456789abcde-",
		"Permission-0123456789abcdef", "0123456789abcdef", "permission0123456789abcdef", "permission-",
		" permission-0123456789abcdef", "permission-0123456789abcdef ", "permission-0123456789abcdef\n", "\npermission-0123456789abcdef",
		"", "0", "7",
	} {
		rows = append(rows, row{id, false})
	}
	for _, r := range rows {
		got, want := projmux.IsClaudeApprovalRequestID(r.id), agentapproval.ValidID(r.id)
		if got != want {
			t.Fatalf("%q: public %v internal %v", r.id, got, want)
		}
		if got != r.minted {
			t.Fatalf("%q: shape %v, want %v", r.id, got, r.minted)
		}
	}
}
