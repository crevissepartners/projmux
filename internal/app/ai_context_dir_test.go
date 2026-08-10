package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// stubbedContextDirCommand builds an aiCommand whose tmux reads are answered
// from the supplied pane cwd / session name / anchor, so resolveContextDir can
// be exercised without a live tmux. env holds extra environment overrides
// (e.g. TMUX_SPLIT_CONTEXT_DIR); TMUX is always set so the in-tmux branch runs.
func stubbedContextDirCommand(paneCWD, sessionName, anchor string, env map[string]string) *aiCommand {
	return &aiCommand{
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux-1234/default,1,0"
			}
			if v, ok := env[name]; ok {
				return v
			}
			return ""
		},
		readCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "tmux" {
				return nil, nil
			}
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "display-message") && strings.Contains(joined, "#{pane_current_path}"):
				return []byte(paneCWD + "\n"), nil
			case strings.HasPrefix(joined, "display-message") && strings.Contains(joined, "#{session_name}"):
				return []byte(sessionName + "\n"), nil
			case strings.HasPrefix(joined, "show-options") && strings.Contains(joined, inttmux.ProjectPathSessionOption):
				if anchor == "" {
					return nil, nil
				}
				return []byte(anchor + "\n"), nil
			default:
				return nil, nil
			}
		},
	}
}

func TestResolveContextDirEnvOverrideWins(t *testing.T) {
	t.Parallel()
	anchor := t.TempDir()
	override := t.TempDir()
	cmd := stubbedContextDirCommand(anchor, "proj", anchor, map[string]string{"TMUX_SPLIT_CONTEXT_DIR": override})
	if got := cmd.resolveContextDir(); got != override {
		t.Fatalf("resolveContextDir() = %q, want env override %q", got, override)
	}
}

func TestResolveContextDirPaneEqualsAnchor(t *testing.T) {
	t.Parallel()
	anchor := t.TempDir()
	cmd := stubbedContextDirCommand(anchor, "proj", anchor, nil)
	if got := cmd.resolveContextDir(); got != anchor {
		t.Fatalf("resolveContextDir() = %q, want pane cwd %q", got, anchor)
	}
}

func TestResolveContextDirPaneDescendantKept(t *testing.T) {
	t.Parallel()
	anchor := t.TempDir()
	sub := filepath.Join(anchor, "web", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cmd := stubbedContextDirCommand(sub, "proj", anchor, nil)
	if got := cmd.resolveContextDir(); got != sub {
		t.Fatalf("resolveContextDir() = %q, want descendant pane cwd %q", got, sub)
	}
}

func TestResolveContextDirPaneOutsideDriftsToAnchor(t *testing.T) {
	t.Parallel()
	anchor := t.TempDir()
	outside := t.TempDir()
	cmd := stubbedContextDirCommand(outside, "proj", anchor, nil)
	if got := cmd.resolveContextDir(); got != anchor {
		t.Fatalf("resolveContextDir() = %q, want anchor %q for out-of-tree pane", got, anchor)
	}
}

func TestResolveContextDirNoAnchorFallsBackToPane(t *testing.T) {
	t.Parallel()
	// No anchor option (pre-anchor / external session): keep current behaviour.
	pane := t.TempDir()
	cmd := stubbedContextDirCommand(pane, "proj", "", nil)
	if got := cmd.resolveContextDir(); got != pane {
		t.Fatalf("resolveContextDir() = %q, want live pane cwd %q", got, pane)
	}
}

func TestResolveSessionProjectPath(t *testing.T) {
	t.Parallel()
	anchor := t.TempDir()

	cmd := stubbedContextDirCommand(anchor, "proj", anchor, nil)
	if got := cmd.resolveSessionProjectPath("proj"); got != anchor {
		t.Fatalf("resolveSessionProjectPath() = %q, want %q", got, anchor)
	}
	if got := cmd.resolveSessionProjectPath(""); got != "" {
		t.Fatalf("resolveSessionProjectPath(\"\") = %q, want empty", got)
	}

	// Anchor points at a path that no longer exists → treated as absent.
	stale := stubbedContextDirCommand(anchor, "proj", filepath.Join(anchor, "gone"), nil)
	if got := stale.resolveSessionProjectPath("proj"); got != "" {
		t.Fatalf("resolveSessionProjectPath() with stale anchor = %q, want empty", got)
	}
}

func TestPathWithinTree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		anchor string
		path   string
		want   bool
	}{
		{"exact", "/proj", "/proj", true},
		{"descendant", "/proj", "/proj/web", true},
		{"deep descendant", "/proj", "/proj/web/api", true},
		{"sibling", "/proj", "/other", false},
		{"parent", "/proj/web", "/proj", false},
		{"prefix not boundary", "/proj", "/project", false},
		{"empty anchor", "", "/proj", false},
		{"empty path", "/proj", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := pathWithinTree(tc.anchor, tc.path); got != tc.want {
				t.Fatalf("pathWithinTree(%q, %q) = %v, want %v", tc.anchor, tc.path, got, tc.want)
			}
		})
	}
}
