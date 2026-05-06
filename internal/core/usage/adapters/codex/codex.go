// Package codex implements a best-effort v0 usage Adapter for the Codex CLI.
//
// Source of truth: the Codex CLI persists per-session "rollout" JSONL files
// under ~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-*.jsonl. These contain
// `event_msg` records of `type: "token_count"` whose payload reports the
// last assistant turn's usage:
//
//	{
//	  "timestamp": "2026-04-08T08:14:25.958Z",
//	  "type": "event_msg",
//	  "payload": {
//	    "type": "token_count",
//	    "info": {
//	      "last_token_usage": {"total_tokens": 14476, ...},
//	      "total_token_usage": {"total_tokens": 82491, ...},
//	      ...
//	    },
//	    "rate_limits": {...}
//	  }
//	}
//
// We emit one TokenEvent per `last_token_usage.total_tokens`, since
// `total_token_usage` is a running session sum that would double-count if we
// summed it.
//
// Caveats:
//
//   - Codex's `rate_limits.primary/secondary` payload also exposes the
//     official 5h/weekly used_percent. A future PR should prefer that
//     directly because it sidesteps any local-counting drift; for now we
//     only consume `last_token_usage` so the adapter has a single shape.
//   - Old session directories outside the weekly retention window are
//     skipped to keep cold-start cheap.
//
// TODO(usage): switch to `rate_limits.primary.used_percent` for the 5h
// window once we plumb a "report percent directly" hook through the
// aggregator.
package codex

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

// Name is the adapter's registered identifier.
const Name = "codex"

// scanWindow caps how far back the adapter walks rollout transcripts.
const scanWindow = 8 * 24 * time.Hour

// Adapter is the Codex implementation of usage.Adapter.
type Adapter struct {
	homeDir      func() (string, error)
	now          func() time.Time
	rootOverride string
}

// New returns an Adapter that reads from $HOME/.codex/sessions.
func New() *Adapter {
	return &Adapter{homeDir: os.UserHomeDir, now: time.Now}
}

// NewWithRoot is intended for tests.
func NewWithRoot(root string) *Adapter {
	return &Adapter{homeDir: os.UserHomeDir, now: time.Now, rootOverride: root}
}

// Name implements usage.Adapter.
func (a *Adapter) Name() string { return Name }

// Collect walks the rollout tree and emits one TokenEvent per token_count
// record. Best-effort: missing tree => nil events, malformed lines skipped
// silently.
func (a *Adapter) Collect(ctx context.Context) ([]usage.TokenEvent, error) {
	root, err := a.resolveRoot()
	if err != nil {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		return nil, nil
	}

	now := a.now().UTC()
	cutoff := now.Add(-scanWindow)

	var events []usage.TokenEvent

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
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
		events = append(events, scanRollout(path, cutoff)...)
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
	return filepath.Join(home, ".codex", "sessions"), nil
}

// rolloutRecord captures the bits of the codex rollout schema we need.
type rolloutRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   *struct {
		Type string `json:"type"`
		Info *struct {
			LastTokenUsage *struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func scanRollout(path string, cutoff time.Time) []usage.TokenEvent {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	const maxLine = 16 * 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	var out []usage.TokenEvent
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec rolloutRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Type != "event_msg" || rec.Payload == nil {
			continue
		}
		if rec.Payload.Type != "token_count" {
			continue
		}
		if rec.Payload.Info == nil || rec.Payload.Info.LastTokenUsage == nil {
			continue
		}
		if rec.Timestamp.Before(cutoff) {
			continue
		}
		tokens := rec.Payload.Info.LastTokenUsage.TotalTokens
		if tokens <= 0 {
			continue
		}
		out = append(out, usage.TokenEvent{At: rec.Timestamp.UTC(), Tokens: tokens})
	}
	return out
}
