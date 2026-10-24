package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

// Exercise the generated option commands on tmux, rather than emulating its
// array formatting, matching, or append semantics in Go.
func TestGeneratedTmuxOptionUpdatesAreIdempotent(t *testing.T) {
	liveguard.RequireActive(t)
	binary := requireRealTmux(t)
	for name, generated := range map[string]string{
		"app":        tmuxAppConfig("/bin/true", "/bin/sh", config.StatusbarDecorationOff),
		"standalone": tmuxStandaloneConfig("/bin/true", config.StatusbarDecorationOff),
	} {
		t.Run(name, func(t *testing.T) {
			// CI deliberately lengthens TMPDIR. Keep the Unix socket short,
			// and canonicalize /tmp on macOS for exact socket_path cleanup.
			root, err := filepath.EvalSymlinks(shortTempDomain(t))
			if err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(root, "s")
			run := func(args ...string) string {
				t.Helper()
				out, err := exec.Command(binary, append([]string{"-S", socket}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("tmux %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("-f", "/dev/null", "new-session", "-d", "-s", "options")
			defer func() {
				actual := run("display-message", "-p", "#{socket_path}")
				if actual != socket {
					t.Errorf("cleanup refused: socket %q != %q", actual, socket)
					return
				}
				run("kill-server")
			}()
			// Preserve tmux defaults, user entries, similar-but-not-equal entries,
			// and pre-existing duplicates. None of these may be replaced or removed.
			run("set", "-as", "terminal-features", ",user*:RGB,xterm*:RGB-extra,xterm*:RGB,xterm*:RGB")
			run("set", "-ga", "update-environment", "USER_KEEP WSL_DISTRO_NAME_EXTRA TERM_PROGRAM TERM_PROGRAM")
			read := func(scope, option string) []string {
				return strings.Split(run("show-options", scope, "-v", option), "\n")
			}
			beforeFeatures := read("-s", "terminal-features")
			beforeEnv := read("-g", "update-environment")
			// A session override must not be mistaken for the global option we set.
			run("set", "-t", "options", "update-environment", "WSL_DISTRO_NAME")
			var lines []string
			for line := range strings.SplitSeq(generated, "\n") {
				if strings.Contains(line, "terminal-features") || strings.Contains(line, "update-environment") {
					lines = append(lines, line)
				}
			}
			if len(lines) == 0 {
				t.Fatal("no generated option commands")
			}
			path := filepath.Join(root, "options.conf")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			run("source-file", path)
			firstFeatures, firstEnv := read("-s", "terminal-features"), read("-g", "update-environment")
			run("source-file", path)
			run("source-file", path)
			if got := read("-s", "terminal-features"); !slices.Equal(got, firstFeatures) {
				t.Errorf("terminal-features accumulated: first %q, third %q", firstFeatures, got)
			}
			if got := read("-g", "update-environment"); !slices.Equal(got, firstEnv) {
				t.Errorf("update-environment accumulated: first %q, third %q", firstEnv, got)
			}
			if !slices.Equal(firstFeatures, beforeFeatures) {
				t.Errorf("existing feature entries changed: before %q, after %q", beforeFeatures, firstFeatures)
			}
			if len(firstEnv) < len(beforeEnv) || !slices.Equal(firstEnv[:len(beforeEnv)], beforeEnv) {
				t.Errorf("default/user env entries changed: before %q, after %q", beforeEnv, firstEnv)
			}
			if name == "app" {
				for _, entry := range []string{"WSL_DISTRO_NAME", "WSL_INTEROP", "WSLENV", "VSCODE_IPC_HOOK_CLI", "VSCODE_GIT_IPC_HANDLE", "VSCODE_GIT_ASKPASS_NODE", "VSCODE_GIT_ASKPASS_MAIN", "VSCODE_GIT_ASKPASS_EXTRA_ARGS", "VSCODE_INJECTION", "TERM_PROGRAM_VERSION", "PROJMUX_WELCOME"} {
					count := 0
					for _, got := range firstEnv {
						if got == entry {
							count++
						}
					}
					if count != 1 {
						t.Errorf("exact entry %s count = %d, want 1", entry, count)
					}
				}
			}
			// A similar feature pattern is not an exact entry. A missing RGB entry
			// must be appended once, without replacing the similar/user entries.
			run("set", "-s", "terminal-features", "user*:RGB,xterm*:RGB-extra,prefix-xterm*:RGB")
			run("source-file", path)
			missingFeatures := read("-s", "terminal-features")
			wantFeatures := []string{"user*:RGB", "xterm*:RGB-extra", "prefix-xterm*:RGB", "xterm*:RGB"}
			if !slices.Equal(missingFeatures, wantFeatures) {
				t.Errorf("missing exact feature: got %q, want %q", missingFeatures, wantFeatures)
			}
			run("source-file", path)
			if got := read("-s", "terminal-features"); !slices.Equal(got, missingFeatures) {
				t.Errorf("new feature accumulated: first %q, second %q", missingFeatures, got)
			}
		})
	}
}
