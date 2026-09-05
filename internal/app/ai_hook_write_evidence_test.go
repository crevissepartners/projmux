package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A discarded write error is the strongest form of a surface lying.
//
// Everything else this track chased at least reported something. `disconnected`
// meant nothing was recorded, `no matching pane` named the wrong step — both
// wrong, both visible. A hook reflection path that runs a tmux write and throws
// the error away reports **success**: the ingest log records `result:"state"`
// for an event whose Pane never moved, and no diagnosis built on that log can
// see it, because the log is the thing that is lying.
//
// It is why one hook event appeared to work and another appeared to fail. They
// failed alike; only one of them said so.

// aiHookDiscardedWritePattern matches a tmux invocation whose error is thrown
// away at the call site.
var aiHookDiscardedWritePattern = regexp.MustCompile(`_,? ?_? ?= c\.run\("tmux"`)

// The gate is zero, and it carries no baseline.
//
// Recording the current count as an allowance was considered and rejected.
// Pinning today's state as the passing condition is the exact shape of the E2E
// assertion that made a silent control plane the condition for green, and
// building that trap a second time inside the gate written to stop it is not a
// tradeoff worth making. The count stays a failure until the writes check their
// errors.

// TestHookReflectionWritesNeverDiscardTheirErrorSilently holds the ratchet.
//
// The scan is over the whole package rather than a named list of files, so a
// discarded write appearing somewhere new is caught rather than skipped for
// being somewhere nobody thought to look.
func TestHookReflectionWritesNeverDiscardTheirErrorSilently(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	found := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		payload, readErr := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- package source under test.
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if count := len(aiHookDiscardedWritePattern.FindAllIndex(payload, -1)); count > 0 {
			found[name] = count
		}
	}
	var discarded []string
	total := 0
	for name, count := range found {
		discarded = append(discarded, name+": "+strconv.Itoa(count))
		total += count
	}
	sort.Strings(discarded)
	if total > 0 {
		t.Fatalf("%d tmux write(s) throw their error away:\n  %s\n\n"+
			"Each one lets the ingest log record success for an event whose Pane never moved, so no diagnosis "+
			"built on that log can detect it. Check the error and report a bounded reason instead.",
			total, strings.Join(discarded, "\n  "))
	}
}
