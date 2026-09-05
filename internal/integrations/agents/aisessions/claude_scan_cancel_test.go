package aisessions

import (
	"context"
	"path/filepath"
	"testing"
)

// cancelClaudeReadContext cancels on the parser's post-Read context check after
// an observer arms it for one file. This deterministically interrupts a real
// file read without timers, sleeps, or changes to the shared JSONL parser.
type cancelClaudeReadContext struct {
	context.Context
	cancel context.CancelFunc
	armed  bool
	checks int
}

func (c *cancelClaudeReadContext) Err() error {
	if c.armed {
		c.checks++
		if c.checks == 2 {
			c.cancel()
		}
	}
	return c.Context.Err()
}

func TestClaudeCanceledScanReturnsOnlyFullyParsedRows(t *testing.T) {
	for _, when := range []string{"before_scan", "during_second_file", "complete"} {
		t.Run(when, func(t *testing.T) {
			fixture := buildClaudeFixture(t, claudeFixtureSpec{matching: 4, cwd: "/workspace/app"})
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &cancelClaudeReadContext{Context: parent, cancel: cancel}
			opened := 0
			observer := func(path string) {
				opened++
				if when == "during_second_file" && filepath.Base(path) == "session-0001.jsonl" {
					ctx.armed = true
				}
			}
			claudeScanObserver.Store(&observer)
			t.Cleanup(func() { claudeScanObserver.Store(nil) })
			if when == "before_scan" {
				cancel()
			}
			result, err := DiscoverResumeSummariesContext(ctx, AgentClaude, fixture.cwd,
				ResumeSummaryOptions{DiscoverOptions: DiscoverOptions{ClaudeProjectsDir: fixture.projectsDir}}, 10)
			if err != nil {
				t.Fatal(err)
			}
			wantRows, wantOpened, wantMore := 4, 4, false
			switch when {
			case "before_scan":
				wantRows, wantOpened, wantMore = 0, 0, true
			case "during_second_file":
				wantRows, wantOpened, wantMore = 1, 2, true
			}
			if len(result.Summaries) != wantRows || opened != wantOpened || result.MoreNotLoaded != wantMore {
				t.Fatalf("rows/opened/more = %d/%d/%t, want %d/%d/%t", len(result.Summaries), opened, result.MoreNotLoaded, wantRows, wantOpened, wantMore)
			}
			if wantRows == 1 && (result.Summaries[0].ResumeID != "11111111-2222-4333-8444-000000000000" || result.Summaries[0].Branch != "main" || result.Summaries[0].Source != SourceClaudeTranscript) {
				t.Fatalf("partial returned an incomplete or wrong row: %#v", result.Summaries)
			}
		})
	}
}
