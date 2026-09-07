package app

import "testing"

func TestObsoleteAndCurrentClaudeCoordinationHooksNeverRunAutomaticMigration(t *testing.T) {
	for _, route := range []string{"claude-message-wait", "claude-message-reply", "claude-message-boundary"} {
		if shouldRunLegacyHookMigrations([]string{"internal", route}) {
			t.Fatalf("internal %s attempted automatic settings migration", route)
		}
	}
}
