// Package claude implements a best-effort v0 usage Adapter for Claude Code.
//
// Source of truth: Claude Code persists per-session JSONL transcripts under
// ~/.claude/projects/<flat-cwd>/<sessionID>.jsonl. Each assistant message
// emits a `usage` block with input/cache/output token counters. This
// adapter scans those files and emits a TokenEvent per assistant message
// using the message's top-level `timestamp`.
//
// Caveats:
//
//   - The same logical assistant turn often shows up multiple times across
//     adjacent records (the transcript records partial states). We dedupe
//     on `requestId`, falling back to `message.id` if requestId is missing.
//   - "Tokens" here = input + output + cache_creation_input. cache_read is
//     deliberately excluded so the rolling sum reflects the user's billable
//     consumption rather than re-served context.
//   - We only inspect files modified within the last weekly retention
//     window so cold-start cost stays bounded.
//
// TODO(usage): when Claude Code exposes a stable `claude usage` command or
// HTTP endpoint with authoritative limits and consumption, prefer that over
// JSONL scraping. Until then this adapter is intentionally tolerant of
// schema drift: any malformed record is skipped silently.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

// Name is the adapter's registered identifier. Aligns with the model-key
// used in the limits table.
const Name = "claude"

// scanWindow caps how far back the adapter walks transcripts. Anything
// older than this is skipped entirely; the cache layer handles longer-term
// retention so the adapter can stay cheap on every Collect.
const scanWindow = 8 * 24 * time.Hour

// Adapter is the Claude implementation of usage.Adapter.
type Adapter struct {
	homeDir func() (string, error)
	now     func() time.Time
	// rootOverride lets tests point at a fixture tree without juggling HOME.
	rootOverride string
}

// New returns an Adapter that reads from $HOME/.claude/projects.
func New() *Adapter {
	return &Adapter{homeDir: os.UserHomeDir, now: time.Now}
}

// NewWithRoot is intended for tests: it reads JSONL transcripts from the
// supplied directory tree instead of $HOME/.claude/projects.
func NewWithRoot(root string) *Adapter {
	return &Adapter{homeDir: os.UserHomeDir, now: time.Now, rootOverride: root}
}

// Name implements usage.Adapter.
func (a *Adapter) Name() string { return Name }

// Collect walks the transcript tree and emits TokenEvents. Best-effort: if
// the directory doesn't exist or any single file fails to parse we skip it
// silently rather than error out — the status segment must keep working
// with partial data.
func (a *Adapter) Collect(ctx context.Context) ([]usage.TokenEvent, error) {
	root, err := a.resolveRoot()
	if err != nil {
		return nil, nil // best-effort: no home dir => no events.
	}
	if _, err := os.Stat(root); err != nil {
		return nil, nil
	}

	now := a.now().UTC()
	cutoff := now.Add(-scanWindow)

	var events []usage.TokenEvent
	seen := make(map[string]struct{})

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A single unreadable subdir mustn't take down the whole walk.
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fileEvents := scanTranscript(path, cutoff, seen)
		events = append(events, fileEvents...)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, ctx.Err()) {
		return nil, nil
	}
	return events, nil
}

func (a *Adapter) resolveRoot() (string, error) {
	if a.rootOverride != "" {
		return a.rootOverride, nil
	}
	home, err := a.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// transcriptRecord is the subset of the Claude Code JSONL schema we care
// about. Unknown fields are intentionally ignored so future schema bumps
// don't break the adapter.
type transcriptRecord struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"requestId"`
	Message   *struct {
		ID    string `json:"id"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// scanTranscript reads a single JSONL file and returns the deduped events
// that fall inside the cutoff window. seen is shared across files so we
// dedupe across the entire collect call.
func scanTranscript(path string, cutoff time.Time, seen map[string]struct{}) []usage.TokenEvent {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Claude transcripts contain very long single-line records (full
	// assistant messages with attachments). Bump the buffer so we don't
	// truncate them mid-line.
	const maxLine = 16 * 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	var out []usage.TokenEvent
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec transcriptRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Type != "assistant" {
			continue
		}
		if rec.Message == nil || rec.Message.Usage == nil {
			continue
		}
		if rec.Timestamp.Before(cutoff) {
			continue
		}
		key := rec.RequestID
		if key == "" {
			key = rec.Message.ID
		}
		if key == "" {
			// No stable identity; conservatively skip to avoid double-counting.
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		u := rec.Message.Usage
		// Cache reads are excluded — see package doc.
		tokens := u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens
		if tokens <= 0 {
			continue
		}
		out = append(out, usage.TokenEvent{At: rec.Timestamp.UTC(), Tokens: tokens})
	}
	return out
}
