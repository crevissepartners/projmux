package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	"github.com/crevissepartners/projmux/internal/core/lifecycle"
)

// flagValueUsageCounters counts every lookup a refused flag value must not
// reach: session identity, candidate discovery, the open-route validator, the
// home directory, and the ephemeral inventory.
type flagValueUsageCounters struct {
	identity, discover, openRoute, homeDir, inventory int
}

func (c *flagValueUsageCounters) total() int {
	return c.identity + c.discover + c.openRoute + c.homeDir + c.inventory
}

func (c *flagValueUsageCounters) app() *App {
	return &App{
		switcher: &switchCommand{
			identity: switchIdentityResolverFunc(func(string) (string, error) {
				c.identity++
				return "session", nil
			}),
			discover: func(candidates.Inputs) ([]string, error) {
				c.discover++
				return nil, nil
			},
			validateProjectOpenRoute: func(context.Context, string) error {
				c.openRoute++
				return nil
			},
		},
		attach: &attachCommand{
			homeDir: func() (string, error) {
				c.homeDir++
				return "/home/tester", nil
			},
			inventory: attachInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
				c.inventory++
				return nil, nil
			}),
		},
		prune: &pruneCommand{
			inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
				c.inventory++
				return nil, nil
			}),
		},
	}
}

// TestFlagValueRefusalsAreUsageErrorsBeforeAnyLookup pins the flag values the
// catalog synopsis does not spell as a closed set (the form V guard in
// public_route_argv_guard_test.go covers those that it does). Each refusal is
// a usage error (exit 2) with its reason text unchanged, prints its route's
// Usage block exactly once, writes nothing to stdout, and happens before any
// lookup or effect.
func TestFlagValueRefusalsAreUsageErrorsBeforeAnyLookup(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		argv   []string
		reason string
		usage  string
	}{
		{
			name:   "switch --anchor",
			argv:   []string{"switch", "--anchor", "bogus"},
			reason: "switch --anchor requires an exact %N Pane handle",
			usage:  "  projmux switch [--ui popup|sidebar] [--anchor <pane>]",
		},
		{
			name:   "switch sidebar-open --anchor",
			argv:   []string{"switch", "sidebar-open", "--path", "/work/alpha", "--anchor", "bogus"},
			reason: "switch sidebar-open --anchor requires an exact %N Pane handle",
			usage:  "  projmux switch sidebar-open --path <path> --anchor <pane>",
		},
		{
			name:   "switch sidebar-open --mode",
			argv:   []string{"switch", "sidebar-open", "--path", "/work/alpha", "--anchor", "%1", "--client", "/dev/pts/9", "--mode", "bogus"},
			reason: `switch sidebar-open: unknown startup mode "bogus"`,
			usage:  "  projmux switch sidebar-open --path <path> --anchor <pane>",
		},
		{
			name:   "runtime attach --keep",
			argv:   []string{"runtime", "attach", "--keep", "-1"},
			reason: "plan auto attach: ephemeral keep count must be non-negative",
			usage:  "  projmux runtime attach [--keep <n>] [--fallback home|ephemeral]",
		},
		{
			name:   "runtime prune --keep",
			argv:   []string{"runtime", "prune", "--keep", "-1"},
			reason: "plan ephemeral prune: ephemeral keep count must be non-negative",
			usage:  "  projmux runtime prune",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			counters := &flagValueUsageCounters{}
			var stdout, stderr bytes.Buffer
			err := counters.app().Run(test.argv, &stdout, &stderr)
			if err == nil {
				t.Fatal("Run() error = nil, want a usage error")
			}
			if !IsUsageError(err) {
				t.Fatalf("Run() error = %v (%T), want a usage error (exit 2)", err, err)
			}
			if err.Error() != test.reason {
				t.Fatalf("reason = %q, want %q unchanged", err.Error(), test.reason)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want 0 bytes", stdout.String())
			}
			if got := strings.Count(stderr.String(), "Usage:"); got != 1 {
				t.Fatalf("stderr has %d Usage blocks, want 1:\n%s", got, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.usage) {
				t.Fatalf("stderr does not carry the route's own synopsis %q:\n%s", test.usage, stderr.String())
			}
			// The entrypoint prints the reason; the handler must not.
			if strings.Contains(stderr.String(), test.reason) {
				t.Fatalf("handler stderr already carries the reason, so it would print twice:\n%s", stderr.String())
			}
			if counters.total() != 0 {
				t.Fatalf("a refused flag value reached a lookup: %+v", *counters)
			}
		})
	}
}

// TestKeepZeroStillReachesTheInventory is the boundary control for the
// negative --keep refusal: 0 is a valid keep count and is not refused early.
func TestKeepZeroStillReachesTheInventory(t *testing.T) {
	t.Parallel()
	for _, argv := range [][]string{
		{"runtime", "prune", "--keep", "0"},
		{"runtime", "attach", "--keep", "0"},
	} {
		counters := &flagValueUsageCounters{}
		var stderr bytes.Buffer
		err := counters.app().Run(argv, &bytes.Buffer{}, &stderr)
		if IsUsageError(err) {
			t.Fatalf("%q: Run() error = %v, a valid keep count must not be a usage error", argv, err)
		}
		if counters.inventory != 1 {
			t.Fatalf("%q: inventory reads = %d, want 1 (err %v)", argv, counters.inventory, err)
		}
	}
}
