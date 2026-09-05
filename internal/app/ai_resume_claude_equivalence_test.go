package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
)

// C-3's oracle is the full pre-optimization row set declared by each fixture,
// settled by the real global dedupe/sort/cap. It is independent of the new scan
// and includes cases that necessarily require O(M) reads to preserve the rows.
func TestClaudeLimitedScanPreservesFullSettledRows(t *testing.T) {
	const cwd = "/workspace/app"
	const limit = 3
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	type record struct {
		id, cwd, project, title string
		mtime                   time.Time
	}
	for _, name := range []string{"large_terminal_tie", "many_newest_duplicates", "many_newest_nonmatches", "depth_and_equal_id_ties"} {
		t.Run(name, func(t *testing.T) {
			var records []record
			depth := 0
			add := func(id, recordedCWD string, at time.Time) {
				records = append(records, record{id: id, cwd: recordedCWD, project: cwd, title: fmt.Sprintf("Title %d", len(records)), mtime: at})
			}
			switch name {
			case "large_terminal_tie":
				add("newest-a", cwd, base.Add(time.Second))
				add("newest-b", cwd, base.Add(time.Second))
				for i := range 512 {
					// Filename order opposes content ResumeID order. The final
					// file in this large tie must survive the visible cap.
					add(fmt.Sprintf("tie-%04d", 512-i), cwd, base)
				}
				add("older", cwd, base.Add(-time.Second))
			case "many_newest_duplicates":
				for i := range 256 {
					add("same-id", cwd, base.Add(-time.Duration(i)*time.Second))
				}
				for i := range 8 {
					add(fmt.Sprintf("unique-%d", i), cwd, base.Add(-time.Duration(256+i)*time.Second))
				}
			case "many_newest_nonmatches":
				for i := range 256 {
					add(fmt.Sprintf("nonmatch-%d", i), cwd+"-other", base.Add(-time.Duration(i)*time.Second))
				}
				for i := range 8 {
					add(fmt.Sprintf("match-%d", i), cwd, base.Add(-time.Duration(256+i)*time.Second))
				}
			case "depth_and_equal_id_ties":
				depth = 1
				add("duplicate", cwd, base)
				add("older", cwd, base.Add(-time.Hour))
				add("duplicate", cwd+"/child", base)
				records[len(records)-1].project = cwd + "/child"
				add("child-newest", cwd+"/child", base.Add(time.Second))
				records[len(records)-1].project = cwd + "/child"
				add("child-tie", cwd+"/child", base)
				records[len(records)-1].project = cwd + "/child"
				add("outside-tree", cwd+"-other", base.Add(time.Second))
				records[len(records)-1].project = cwd + "-other"
			}

			projectsDir := t.TempDir()
			var fullRows []aisessions.ResumeSummary
			for i, record := range records {
				dir := filepath.Join(projectsDir, aisessions.EncodeClaudeProjectPath(record.project))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, fmt.Sprintf("file-%04d.jsonl", i))
				content, err := json.Marshal(map[string]string{"type": "ai-title", "sessionId": record.id, "cwd": record.cwd, "gitBranch": "main", "aiTitle": record.title})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, record.mtime, record.mtime); err != nil {
					t.Fatal(err)
				}
				if record.cwd != cwd && !(depth == 1 && record.cwd == cwd+"/child") {
					continue
				}
				relative := ""
				if depth > 0 {
					relative = "./"
					if record.cwd != cwd {
						relative = "./child"
					}
				}
				fullRows = append(fullRows, aisessions.ResumeSummary{Provider: aiModeClaude, ResumeID: record.id, LastModified: record.mtime.Local(),
					Label: record.title, TitleProvenance: aisessions.TitleExplicitProvider, Branch: "main", RelativeCWD: relative, Source: aisessions.SourceClaudeTranscript})
			}
			opts := aisessions.ResumeSummaryOptions{DiscoverOptions: aisessions.DiscoverOptions{ClaudeProjectsDir: projectsDir, Depth: depth}}
			full, err := aisessions.DiscoverResumeSummariesContext(context.Background(), aiModeClaude, cwd, opts, 0)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := settleAIResumeSummaries(fullRows, limit)
			baseline, _ := settleAIResumeSummaries(full.Summaries, limit)
			if !reflect.DeepEqual(baseline, want) {
				t.Fatalf("full scan differs from declared pre-optimization rows:\ngot %#v\nwant %#v", baseline, want)
			}
			limited, err := aisessions.DiscoverResumeSummariesContext(context.Background(), aiModeClaude, cwd, opts, limit)
			if err != nil {
				t.Fatal(err)
			}
			got, capped := settleAIResumeSummaries(limited.Summaries, limit)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("limited settled row set/order/content changed:\ngot %#v\nwant %#v", got, want)
			}
			if !limited.MoreNotLoaded {
				t.Fatal("older unscanned transcripts must report MoreNotLoaded")
			}
			wantCount := limit
			if name == "large_terminal_tie" {
				wantCount = 514
				if !capped {
					t.Fatal("terminal tie must be capped globally")
				}
			}
			projection := resumeProviderProjection(aiResumeProviderResult{provider: aiModeClaude, discovery: limited}, true)
			if projection.state != aiResumeProviderCount || projection.count != wantCount {
				t.Fatalf("confirmed invocation count = %+v, want count/%d", projection, wantCount)
			}
		})
	}
}
