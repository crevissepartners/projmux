package runtime

import (
	"errors"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"path/filepath"
	"strings"
)

// PlanNativeCodexAttachment never carries the initial prompt: turn/start is its
// sole writer. The adapter supplies only shell/environment wrapping.
func PlanNativeCodexAttachment(route NativeCodexRoute, w metadata.AgentWorkspace, id, model, effort string, plan func(string, []string, string) (string, []string, error)) (string, []string, error) {
	id = strings.TrimSpace(id)
	if id == "" || !route.Valid() {
		return "", nil, errors.New("native Codex thread or generation route is incomplete")
	}
	args, err := ProviderLaunchArgs("codex", w, nil)
	if err != nil {
		return "", nil, err
	}
	binary := filepath.Clean(route.TUIExecutable)
	argv := append([]string{binary}, CodexLaunchOptionArgs(model, effort)...)
	argv = append(argv, args...)
	remote := "unix://"
	if !route.Default {
		remote += filepath.Clean(route.SocketPath)
	}
	argv = append(argv, "resume", "--remote", remote, id)
	return plan(w.CWD, argv, filepath.Dir(binary))
}
