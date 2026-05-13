package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

func TestBuildHookTrustPopupArgsTargetsWidePopup(t *testing.T) {
	args, err := buildHookTrustPopupArgs(
		"/tmp/proj mux/bin/projmux",
		"/tmp/request file.json",
		"/tmp/decision file.txt",
		hookTrustPopupTarget{client: "/dev/pts/7"},
	)
	if err != nil {
		t.Fatalf("buildHookTrustPopupArgs() error = %v", err)
	}

	wantPrefix := []string{
		"display-popup",
		"-c", "/dev/pts/7",
		"-E",
		"-w", hookTrustPopupWidth,
		"-h", hookTrustPopupHeight,
		"-T", hookTrustPopupTitle,
	}
	if len(args) != len(wantPrefix)+1 || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("popup args = %#v, want prefix %#v", args, wantPrefix)
	}

	command := args[len(args)-1]
	for _, want := range []string{
		"'/tmp/proj mux/bin/projmux' tmux hook-trust-prompt",
		"--request '/tmp/request file.json'",
		"--decision '/tmp/decision file.txt'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
		}
	}
}

func TestHookTrustPromptWritesDecision(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	decisionPath := filepath.Join(dir, "decision.txt")
	request := `{"RepoPath":"/workspace/repo","RelativePath":".projmux/config.toml","SHA256":"abc123","Preview":"[startup]\nrun = \"agent\""}`
	if err := os.WriteFile(requestPath, []byte(request), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &tmuxCommand{}
	var stdout, stderr bytes.Buffer
	err := cmd.runHookTrustPromptWithReader(
		[]string{"--request", requestPath, "--decision", decisionPath},
		strings.NewReader("a\n"),
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("runHookTrustPromptWithReader() error = %v, stderr = %q", err, stderr.String())
	}

	rawDecision, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(rawDecision)), string(hooks.ProjectHookAllowAlways); got != want {
		t.Fatalf("decision = %q, want %q", got, want)
	}
	for _, want := range []string{
		"Trust project hooks",
		projmuxpicker.MutedStart,
		"[a] Allow always",
		".projmux/config.toml",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want substring %q", stdout.String(), want)
		}
	}
}

func TestTmuxProjectHookPromptUsesDisplayPopupDecision(t *testing.T) {
	dir := t.TempDir()
	runner := &hookTrustPopupRecordingRunner{}
	prompt := tmuxProjectHookPrompt(
		func(name string) string {
			switch name {
			case "TMUX":
				return "/tmp/tmux/default,1,0"
			case hookTrustPopupTargetClientEnv:
				return "/dev/pts/9"
			case "TMUX_SESSIONIZER_CONTEXT_PANE":
				return "%hidden-behind-parent-popup"
			default:
				return ""
			}
		},
		func() (string, error) { return filepath.Join(dir, "projmux"), nil },
		runner,
	)

	decision := prompt(hooks.ProjectHookPromptRequest{
		RepoPath:     "/repo",
		RelativePath: ".projmux/config.toml",
		SHA256:       "abc123",
	})

	if decision != hooks.ProjectHookAllowOnce {
		t.Fatalf("decision = %q, want allow-once", decision)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("tmux calls = %#v, want one display-popup call", runner.calls)
	}
	call := runner.calls[0]
	if call.name != "tmux" || len(call.args) == 0 || call.args[0] != "display-popup" {
		t.Fatalf("tmux call = %#v, want display-popup", call)
	}
	command := call.args[len(call.args)-1]
	if !strings.Contains(command, "tmux hook-trust-prompt") {
		t.Fatalf("popup command = %q, want hook trust prompt", command)
	}
	if !containsTmuxArgPair(call.args, "-c", "/dev/pts/9") {
		t.Fatalf("tmux call args = %#v, want target client", call.args)
	}
	if containsTmuxArgPair(call.args, "-t", "%hidden-behind-parent-popup") {
		t.Fatalf("tmux call args = %#v, want no fallback target pane from sessionizer context", call.args)
	}
	if containsTmuxArg(call.args, "-t") {
		t.Fatalf("tmux call args = %#v, want client-scoped popup without target pane", call.args)
	}
}

type hookTrustPopupRecordingRunner struct {
	calls []recordedTmuxCall
}

func (r *hookTrustPopupRecordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: append([]string(nil), args...)})
	if name != "tmux" || len(args) == 0 {
		return nil, nil
	}
	command := args[len(args)-1]
	decisionPath := singleQuotedFlagValue(command, "--decision")
	if decisionPath != "" {
		_ = os.WriteFile(decisionPath, []byte(string(hooks.ProjectHookAllowOnce)+"\n"), 0o600)
	}
	return nil, nil
}

func singleQuotedFlagValue(command, flag string) string {
	_, after, ok := strings.Cut(command, flag+" '")
	if !ok {
		return ""
	}
	value, _, ok := strings.Cut(after, "'")
	if !ok {
		return ""
	}
	return value
}

func containsTmuxArg(args []string, key string) bool {
	return slices.Contains(args, key)
}
