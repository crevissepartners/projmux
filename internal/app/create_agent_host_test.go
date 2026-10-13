package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

func TestNewAgentHostPrecedenceAndPresence(t *testing.T) {
	for _, node := range append([]string{"agent"}, cli.ProviderCreateShortcuts()...) {
		for _, stored := range []string{"", "tmux", "invalid", "process"} {
			for _, explicit := range []string{"", "tmux", "process"} {
				for _, detach := range []string{"", "true", "false"} {
					t.Run(strings.Join([]string{node, stored, explicit, detach}, "/"), func(t *testing.T) {
						home := t.TempDir()
						c := &createCommand{homeDir: func() (string, error) { return home, nil }}
						path := filepath.Join(home, ".config", "projmux", "agent-default-host")
						if stored != "" {
							if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(path, []byte(stored), 0600); err != nil {
								t.Fatal(err)
							}
						}
						args := []string{}
						if explicit != "" {
							args = append(args, "--host", explicit)
						}
						if detach != "" {
							args = append(args, "--detach="+detach)
						}
						flags, err := parseResourceCreateFlags("create "+node, args, &bytes.Buffer{}, resourceCreateShape{host: true, provider: true, split: true})
						host := "tmux"
						if stored == "process" {
							host = "process"
						}
						if explicit != "" {
							host = explicit
						}
						wantDetach := host == "process" && explicit == ""
						if detach != "" {
							wantDetach = detach == "true"
						}
						if err == nil {
							if flags.hostSet != (explicit != "") || flags.detachSet != (detach != "") {
								t.Fatal("presence lost")
							}
							err = c.resolveNewAgentHost("create "+node, &flags)
						}
						if wantDetach && host != "process" {
							if err == nil {
								t.Fatal("tmux detach accepted")
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						if flags.host != host || flags.detach != wantDetach {
							t.Fatalf("got host=%s detach=%v want %s %v", flags.host, flags.detach, host, wantDetach)
						}
					})
				}
			}
		}
	}
}

func TestDefaultHostTmuxEntrypointParity(t *testing.T) {
	routes := [][]string{{"agent", "--provider", "claude"}, {"window", "--provider", "claude"}, {"window"}, {"pane"}}
	for _, provider := range cli.ProviderCreateShortcuts() {
		routes = append(routes, []string{provider})
	}
	for _, route := range routes {
		t.Run(strings.Join(route, "-"), func(t *testing.T) {
			var baseline string
			for _, stored := range []string{"", "tmux", "invalid", "process"} {
				if stored == "process" && !(len(route) == 1 && (route[0] == "window" || route[0] == "pane")) {
					continue
				}
				store := newFakeResourceStore(t)
				cmd, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				home := t.TempDir()
				cmd.homeDir = func() (string, error) { return home, nil }
				if stored != "" {
					path := filepath.Join(home, ".config", "projmux", "agent-default-host")
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(stored), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := append(append([]string{}, route...), "--project", "alpha")
				if route[0] != "window" {
					args = append(args, "--window", "review")
				}
				if route[0] == "codex" {
					args = append(args, "--interactive-only")
				}
				out, _, err := runRoute(t, cmd, args...)
				if err != nil {
					t.Fatal(err)
				}
				if stored == "" {
					baseline = out + store.snapshot()
				} else if !reflect.DeepEqual(out+store.snapshot(), baseline) {
					t.Fatalf("output parity: %q vs %q", out, baseline)
				}
			}
		})
	}
}

func TestDefaultHostReadFailureAndExplicitTmuxOverride(t *testing.T) {
	routes := [][]string{{"agent", "--provider", "claude"}, {"window", "--provider", "claude"}}
	for _, provider := range cli.ProviderCreateShortcuts() {
		routes = append(routes, []string{provider})
	}
	for _, route := range routes {
		for _, explicit := range []bool{false, true} {
			t.Run(strings.Join(route, "-")+"/"+fmt.Sprint(explicit), func(t *testing.T) {
				store := newFakeResourceStore(t)
				tmux := newFakeTmux()
				cmd, _ := newTestAgentCreateCommand(t, store, tmux)
				home := t.TempDir()
				cmd.homeDir = func() (string, error) { return home, nil }
				// Reading a directory fails rather than silently reverting to tmux.
				path := filepath.Join(home, ".config", "projmux", "agent-default-host")
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				args := append(append([]string{}, route...), "--project", "alpha")
				if route[0] != "window" {
					args = append(args, "--window", "review")
				}
				if explicit {
					args = append(args, "--host", "tmux")
				}
				if route[0] == "codex" {
					args = append(args, "--interactive-only")
				}
				before := store.snapshot()
				out, _, err := runRoute(t, cmd, args...)
				if explicit {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil || out != "" || store.snapshot() != before {
					t.Fatalf("read failure effects err=%v out=%q", err, out)
				}
			})
		}
	}
}

func TestExplicitTmuxOverridesCentralProcessForEveryAgentEntrypoint(t *testing.T) {
	routes := [][]string{{"agent", "--provider", "claude"}, {"window", "--provider", "claude"}}
	for _, provider := range cli.ProviderCreateShortcuts() {
		routes = append(routes, []string{provider})
	}
	for _, route := range routes {
		t.Run(strings.Join(route, "-"), func(t *testing.T) {
			var baseline string
			for _, host := range []string{"tmux", "process"} {
				store := newFakeResourceStore(t)
				cmd, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				home := t.TempDir()
				cmd.homeDir = func() (string, error) { return home, nil }
				path := filepath.Join(home, ".config", "projmux", "agent-default-host")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(host), 0600); err != nil {
					t.Fatal(err)
				}
				args := append(append([]string{}, route...), "--host", "tmux", "--project", "alpha")
				if route[0] != "window" {
					args = append(args, "--window", "review")
				}
				if route[0] == "codex" {
					args = append(args, "--interactive-only")
				}
				out, _, err := runRoute(t, cmd, args...)
				if err != nil {
					t.Fatal(err)
				}
				got := out + store.snapshot()
				if host == "tmux" {
					baseline = got
				} else if got != baseline {
					t.Fatalf("explicit host override changed result %q vs %q", got, baseline)
				}
			}
		})
	}
}

func TestNewAgentHostHelpUsesCentralDefault(t *testing.T) {
	for _, node := range append([]string{"agent"}, cli.ProviderCreateShortcuts()...) {
		var help bytes.Buffer
		if err := cli.WriteRouteHelp(&help, "create "+node); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"config agent-host", "--host tmux|process", "foreground", "detached"} {
			if !strings.Contains(help.String(), want) {
				t.Fatalf("help omits %q: %s", want, help.String())
			}
		}
	}
}
