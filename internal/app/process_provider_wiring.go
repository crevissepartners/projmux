package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Store identity follows the host's turn-scoped request fence. Providers may
// reuse a request ID in a later turn without reopening an earlier answer.
func processControlID(b processhost.Binding, r processhost.Request) string {
	raw, _ := json.Marshal(struct {
		Binding                   processhost.Binding
		Connection, Turn, Request string
	}{b, r.Connection, r.Turn, r.ID})
	sum := sha256.Sum256(raw)
	prefix := "permission-"
	if r.Kind == "question" {
		prefix = "question-"
	}
	return prefix + hex.EncodeToString(sum[:8])
}

// Both providers discard inherited runtime authority before adding the exact
// launch binding. Provider-specific hooks may append their own private fields.
func processProviderLaunchEnv(launch processhost.Launch, bindingKey, hostKey, socket string) []string {
	raw, _ := json.Marshal(launch.Binding)
	env := make([]string, 0, len(launch.Command.Env)+2)
	for _, value := range launch.Command.Env {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "PMX_INTERNAL_") || key == "TMUX" || key == "TMUX_PANE" || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" {
			continue
		}
		env = append(env, value)
	}
	return append(env, bindingKey+"="+string(raw), hostKey+"="+socket)
}
